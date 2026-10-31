/* SPDX-License-Identifier: Apache-2.0
 *
 * The lcd349_evt task: debounces raw button levels and runs button, touch
 * and Wi-Fi subscriber callbacks, in order, off the UI task. It also asks
 * the backend for the power source once a second and reports changes.
 */
#include <string.h>

#include "esp_log.h"
#include "freertos/FreeRTOS.h"
#include "freertos/queue.h"
#include "freertos/task.h"

#include "debounce.h"
#include "kit.h"

#define MAX_SUBS 4
#define QUEUE_LEN 32
#define POWER_POLL_MS 1000

typedef enum { EV_BTN_RAW, EV_TOUCH, EV_WIFI } ev_kind_t;

typedef struct {
    ev_kind_t kind;
    union {
        struct {
            uint8_t btn;
            bool pressed;
            uint32_t t_ms;
        } raw;
        lcd349_touch_sample_t touch;
        lcd349_wifi_status_t wifi;
    };
} ev_t;

typedef struct {
    void *cb;
    void *ctx;
} sub_t;

static const char *TAG = "lcd349_evt";
static QueueHandle_t s_q;
static lcd349_debounce_t s_deb[LCD349_BTN_COUNT];
static bool s_down[LCD349_BTN_COUNT];
static sub_t s_btn_subs[MAX_SUBS], s_touch_subs[MAX_SUBS], s_wifi_subs[MAX_SUBS];
static sub_t s_power_sub;
static portMUX_TYPE s_subs_mux = portMUX_INITIALIZER_UNLOCKED;
static lcd349_power_source_t s_source; /* UNKNOWN until lcd349_init */
static uint32_t s_power_due;

static lcd349_err_t add_sub(sub_t *subs, void *cb, void *ctx)
{
    if (!cb) {
        return LCD349_ERR_ARG;
    }
    lcd349_err_t err = LCD349_ERR_NO_MEM;
    portENTER_CRITICAL(&s_subs_mux);
    for (int i = 0; i < MAX_SUBS; i++) {
        if (!subs[i].cb) {
            subs[i] = (sub_t){cb, ctx};
            err = LCD349_OK;
            break;
        }
    }
    portEXIT_CRITICAL(&s_subs_mux);
    return err;
}

lcd349_err_t lcd349_buttons_subscribe(lcd349_btn_cb_t cb, void *ctx)
{
    return add_sub(s_btn_subs, (void *)cb, ctx);
}

lcd349_err_t lcd349_touch_subscribe(lcd349_touch_cb_t cb, void *ctx)
{
    return add_sub(s_touch_subs, (void *)cb, ctx);
}

lcd349_err_t lcd349_wifi_subscribe(lcd349_wifi_cb_t cb, void *ctx)
{
    return add_sub(s_wifi_subs, (void *)cb, ctx);
}

lcd349_err_t lcd349_power_on_source_change(lcd349_power_source_cb_t cb, void *ctx)
{
    portENTER_CRITICAL(&s_subs_mux);
    s_power_sub = (sub_t){(void *)cb, ctx};
    portEXIT_CRITICAL(&s_subs_mux);
    return LCD349_OK;
}

lcd349_power_source_t lcd349_power_source(void)
{
    return __atomic_load_n(&s_source, __ATOMIC_RELAXED);
}

bool lcd349_button_is_down(lcd349_btn_t btn)
{
    return (unsigned)btn < LCD349_BTN_COUNT && __atomic_load_n(&s_down[btn], __ATOMIC_RELAXED);
}

static void post(const ev_t *e)
{
    if (!s_q || xQueueSend(s_q, e, pdMS_TO_TICKS(50)) != pdTRUE) {
        ESP_LOGW(TAG, "event queue full; dropped kind %d", e->kind);
    }
}

void lcd349__button_raw(lcd349_btn_t btn, bool pressed, uint32_t t_ms)
{
    if ((unsigned)btn >= LCD349_BTN_COUNT) {
        return;
    }
    ev_t e = {.kind = EV_BTN_RAW, .raw = {(uint8_t)btn, pressed, t_ms}};
    post(&e);
}

void lcd349__evt_touch(const lcd349_touch_sample_t *s)
{
    ev_t e = {.kind = EV_TOUCH, .touch = *s};
    post(&e);
}

void lcd349__wifi_changed(const lcd349_wifi_status_t *status)
{
    ev_t e = {.kind = EV_WIFI, .wifi = *status};
    post(&e);
}

static void snapshot(const sub_t *subs, sub_t *out)
{
    portENTER_CRITICAL(&s_subs_mux);
    memcpy(out, subs, sizeof(sub_t) * MAX_SUBS);
    portEXIT_CRITICAL(&s_subs_mux);
}

static void deliver_button(lcd349_btn_t btn, bool down, uint32_t t_ms)
{
    __atomic_store_n(&s_down[btn], down, __ATOMIC_RELAXED);
    lcd349_btn_event_t ev = {.btn = btn, .down = down, .t_ms = t_ms};
    sub_t subs[MAX_SUBS];
    snapshot(s_btn_subs, subs);
    for (int i = 0; i < MAX_SUBS && subs[i].cb; i++) {
        ((lcd349_btn_cb_t)subs[i].cb)(&ev, subs[i].ctx);
    }
}

/* Reports every button whose raw level has now been stable long enough. */
static void settle(uint32_t now)
{
    for (int b = 0; b < LCD349_BTN_COUNT; b++) {
        uint32_t edge;
        if (lcd349__debounce_settle(&s_deb[b], now, LCD349_BUTTON_DEBOUNCE_MS, &edge)) {
            deliver_button((lcd349_btn_t)b, s_deb[b].stable, edge);
        }
    }
}

/* Asks the backend for the power source when the poll is due, and tells the
 * callback about a change. */
static void poll_power(uint32_t now)
{
    if ((int32_t)(now - s_power_due) < 0) {
        return;
    }
    s_power_due = now + POWER_POLL_MS;
    lcd349_power_source_t source = be_power_source();
    if (source == __atomic_load_n(&s_source, __ATOMIC_RELAXED)) {
        return;
    }
    __atomic_store_n(&s_source, source, __ATOMIC_RELAXED);
    portENTER_CRITICAL(&s_subs_mux);
    sub_t sub = s_power_sub;
    portEXIT_CRITICAL(&s_subs_mux);
    if (sub.cb) {
        ((lcd349_power_source_cb_t)sub.cb)(source, sub.ctx);
    }
}

static TickType_t next_wait(uint32_t now)
{
    uint32_t due = s_power_due - now;
    if ((int32_t)due < 0) {
        due = 0;
    }
    for (int b = 0; b < LCD349_BTN_COUNT; b++) {
        uint32_t d = lcd349__debounce_due_in(&s_deb[b], now, LCD349_BUTTON_DEBOUNCE_MS);
        if (d < due) {
            due = d;
        }
    }
    TickType_t t = pdMS_TO_TICKS(due);
    return t ? t : 1;
}

static void evt_task(void *arg)
{
    (void)arg;
    ev_t e;
    for (;;) {
        bool got = xQueueReceive(s_q, &e, next_wait(lcd349_time_ms())) == pdTRUE;
        if (got) {
            sub_t subs[MAX_SUBS];
            switch (e.kind) {
            case EV_BTN_RAW:
                lcd349__debounce_raw(&s_deb[e.raw.btn], e.raw.pressed, e.raw.t_ms);
                break;
            case EV_TOUCH:
                snapshot(s_touch_subs, subs);
                for (int i = 0; i < MAX_SUBS && subs[i].cb; i++) {
                    ((lcd349_touch_cb_t)subs[i].cb)(&e.touch, subs[i].ctx);
                }
                break;
            case EV_WIFI:
                snapshot(s_wifi_subs, subs);
                for (int i = 0; i < MAX_SUBS && subs[i].cb; i++) {
                    ((lcd349_wifi_cb_t)subs[i].cb)(&e.wifi, subs[i].ctx);
                }
                break;
            }
        }
        settle(lcd349_time_ms());
        poll_power(lcd349_time_ms());
    }
}

lcd349_err_t lcd349__evt_start(void)
{
    uint32_t now = lcd349_time_ms();
    for (int b = 0; b < LCD349_BTN_COUNT; b++) {
        lcd349__debounce_init(&s_deb[b], false, now);
    }
    /* The source at start-up is no change: the callback hears only changes. */
    __atomic_store_n(&s_source, be_power_source(), __ATOMIC_RELAXED);
    s_power_due = now + POWER_POLL_MS;
    s_q = xQueueCreate(QUEUE_LEN, sizeof(ev_t));
    if (!s_q) {
        return LCD349_ERR_NO_MEM;
    }
    if (xTaskCreate(evt_task, "lcd349_evt", 6144, NULL, LCD349_PRIO_EVT, NULL) != pdPASS) {
        return LCD349_ERR_NO_MEM;
    }
    return LCD349_OK;
}
