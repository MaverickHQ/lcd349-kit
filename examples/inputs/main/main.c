/* SPDX-License-Identifier: Apache-2.0
 *
 * inputs: shows and logs every input and state the board offers.
 *  - Touch: the last sample's position; the TAP button counts taps and beeps.
 *  - Buttons: each gesture from the classifier (short, holds at 1 s and 3 s,
 *    and BOOT+PWR together).
 *  - BOOT held 1 s: light sleep until a button press. The waking press only wakes.
 *  - PWR held 3 s: power off (on USB the board says it can't).
 *  - Battery, Wi-Fi and power, refreshed every second.
 *  - Serial console lines starting "@P " are commands, answered on the console
 *    the way a USB provisioning protocol would be: "@P ping" answers
 *    "@P pong", "@P echo <text>" answers "@P echo <text>", and "@P crash"
 *    aborts (the CLI's crash-capture test uses it).
 *  - "@P guide" starts the guided board checkpoint (guide.c), which takes the
 *    screen and the buttons until it's done.
 * Every event is logged too, so scenarios and tests can wait for it.
 */
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

#include "esp_log.h"
#include "freertos/FreeRTOS.h"
#include "freertos/queue.h"
#include "freertos/task.h"
#include "guide.h"
#include "lcd349.h"

static const char *TAG = "inputs";

#define COLOR_BG 0x0B1622
#define COLOR_TEXT 0xFFFFFF
#define COLOR_MUTED 0xB8C4D0
#define COLOR_BUTTON 0x1565C0

static QueueHandle_t s_buttons;
static lv_obj_t *s_status, *s_touch, *s_gesture, *s_tap_label;
static int s_taps;

/* ---- UI updates from other tasks: take the LVGL lock ---- */

static void set_text(lv_obj_t *l, const char *text)
{
    lcd349_ui_lock();
    lv_label_set_text(l, text);
    lcd349_ui_unlock();
}

static void on_tap(lv_event_t *e)
{
    (void)e;
    s_taps++;
    lv_label_set_text_fmt(s_tap_label, "TAP %d", s_taps);
    lcd349_tone(880, 40, 60);
    ESP_LOGI(TAG, "tap count %d", s_taps);
}

static lv_obj_t *label(lv_obj_t *parent, int x, int y, int w, uint32_t color, const lv_font_t *font)
{
    lv_obj_t *l = lv_label_create(parent);
    lv_obj_set_pos(l, x, y);
    lv_obj_set_width(l, w);
    lv_label_set_long_mode(l, LV_LABEL_LONG_MODE_DOTS);
    lv_obj_set_style_text_color(l, lv_color_hex(color), 0);
    lv_obj_set_style_text_font(l, font, 0);
    return l;
}

static void build_ui(void)
{
    lcd349_ui_lock();
    lv_obj_t *scr = lv_screen_active();
    lv_obj_set_style_bg_color(scr, lv_color_hex(COLOR_BG), 0);
    lv_obj_set_style_bg_opa(scr, LV_OPA_COVER, 0);
    lv_obj_set_scrollable(scr, false);

    s_status = label(scr, 12, 8, 616, COLOR_MUTED, &lv_font_montserrat_14);
    lv_label_set_text(s_status, "starting");

    lv_obj_t *tap = lv_button_create(scr);
    lv_obj_set_size(tap, 160, 72);
    lv_obj_align(tap, LV_ALIGN_CENTER, 0, 2);
    lv_obj_set_style_bg_color(tap, lv_color_hex(COLOR_BUTTON), 0);
    /* No press animation: every frame is a full-panel flush on this board.
     * (A NULL transition crashes LVGL 9.6; use a zero-length one.) */
    static const lv_style_prop_t no_props[] = {LV_STYLE_PROP_INV};
    static lv_style_transition_dsc_t no_transition;
    lv_style_transition_dsc_init(&no_transition, no_props, lv_anim_path_linear, 0, 0, NULL);
    lv_obj_set_style_transition(tap, &no_transition, LV_STATE_DEFAULT);
    lv_obj_set_style_transition(tap, &no_transition, LV_STATE_PRESSED);
    lv_obj_add_event_cb(tap, on_tap, LV_EVENT_CLICKED, NULL);
    s_tap_label = lv_label_create(tap);
    lv_obj_set_style_text_font(s_tap_label, &lv_font_montserrat_20, 0);
    lv_obj_set_style_text_color(s_tap_label, lv_color_hex(COLOR_TEXT), 0);
    lv_label_set_text(s_tap_label, "TAP 0");
    lv_obj_center(s_tap_label);

    s_touch = label(scr, 12, 146, 300, COLOR_TEXT, &lv_font_montserrat_14);
    lv_label_set_text(s_touch, "touch: none");
    s_gesture = label(scr, 328, 146, 300, COLOR_TEXT, &lv_font_montserrat_14);
    lv_obj_set_style_text_align(s_gesture, LV_TEXT_ALIGN_RIGHT, 0);
    lv_label_set_text(s_gesture, "button: none");
    lcd349_ui_unlock();
}

/* ---- Events from the kit (lcd349_evt task) ---- */

static void on_touch(const lcd349_touch_sample_t *t, void *ctx)
{
    (void)ctx;
    static bool was_down;
    if (t->pressed != was_down) {
        ESP_LOGI(TAG, "touch %s %u %u", t->pressed ? "down" : "up", t->x, t->y);
        was_down = t->pressed;
    }
    char text[48];
    snprintf(text, sizeof text, "touch: %u, %u %s", t->x, t->y, t->pressed ? "down" : "up");
    set_text(s_touch, text);
}

static void on_button(const lcd349_btn_event_t *ev, void *ctx)
{
    (void)ctx;
    ESP_LOGI(TAG, "button %s %s", ev->btn == LCD349_BTN_BOOT ? "BOOT" : "PWR", ev->down ? "down" : "up");
    xQueueSend(s_buttons, ev, 0);
}

static void on_wifi(const lcd349_wifi_status_t *st, void *ctx)
{
    (void)ctx;
    static const char *states[] = {"idle", "scanning", "connecting", "connected", "disconnected"};
    static const char *reasons[] = {"", "auth", "not_found", "lost", "stopped"};
    ESP_LOGI(TAG, "wifi %s %s %s", states[st->state], reasons[st->reason], st->ssid);
}

/* ---- The app task: gestures, actions, status ---- */

static const char *btn_name(lcd349_btn_t b)
{
    return b == LCD349_BTN_BOOT ? "BOOT" : "PWR";
}

static void refresh_status(void)
{
    uint16_t mv = 0;
    uint8_t pct = 0;
    lcd349_wifi_status_t ws = {0};
    lcd349_battery_read(&mv, &pct);
    lcd349_wifi_status(&ws);
    static const char *states[] = {"idle", "scanning", "connecting", "connected", "disconnected"};
    char text[96];
    snprintf(text, sizeof text, "battery %u mV %u%%   wifi %s %s", mv, pct, states[ws.state], ws.ssid);
    set_text(s_status, text);
}

/* ---- Serial console commands ---- */

static void console_task(void *arg)
{
    (void)arg;
    char line[LCD349_CONSOLE_LINE_MAX + 1];
    for (;;) {
        lcd349_err_t err = lcd349_console_read_line(line, sizeof line, 1000);
        if (err == LCD349_ERR_UNAVAILABLE) {
            break;
        }
        if (err != LCD349_OK) {
            continue;
        }
        if (strncmp(line, "@P ", 3) != 0) {
            ESP_LOGI(TAG, "console line: %s", line);
            continue;
        }
        const char *cmd = line + 3;
        if (!strcmp(cmd, "ping")) {
            printf("@P pong\n");
            guide_ping();
        } else if (!strncmp(cmd, "echo ", 5)) {
            printf("@P echo %s\n", cmd + 5);
        } else if (!strcmp(cmd, "crash")) {
            ESP_LOGE(TAG, "crashing on request");
            abort();
        } else if (!strcmp(cmd, "guide") || !strncmp(cmd, "guide ", 6)) {
            guide_command(cmd[5] ? cmd + 6 : NULL);
        } else if (!strncmp(cmd, "wifi ", 5)) {
            guide_wifi(cmd + 5);
        } else {
            printf("@P error unknown command\n");
        }
        memset(line, 0, sizeof line); /* "@P wifi" lines carry a password */
    }
    vTaskDelete(NULL);
}

void app_main(void)
{
    s_buttons = xQueueCreate(16, sizeof(lcd349_btn_event_t));
    lcd349_err_t err = lcd349_init(NULL);
    ESP_LOGI(TAG, "lcd349_init: %s, status 0x%03x", lcd349_err_str(err), (unsigned)lcd349_board_status());
    if (!lcd349_display()) {
        return;
    }
    build_ui();
    lcd349_touch_subscribe(on_touch, NULL);
    lcd349_buttons_subscribe(on_button, NULL);
    lcd349_wifi_subscribe(on_wifi, NULL);

    /* Storage survives resets (and is cleared by lcd349 run --wipe). */
    uint32_t boots = 0;
    size_t blen = sizeof boots;
    lcd349_kv_get_blob("app", "boots", &boots, &blen);
    boots++;
    lcd349_kv_set_blob("app", "boots", &boots, sizeof boots);
    ESP_LOGI(TAG, "boot count %u, boot reason %d", (unsigned)boots, lcd349_boot_reason());

    /* A saved network from storage, or a test network for the emulator. */
    lcd349_wifi_net_t net = {0};
    size_t len = sizeof net.ssid;
    if (lcd349_kv_get_str("wifi", "ssid", net.ssid, &len) != LCD349_OK) {
        strcpy(net.ssid, "lcd349-test");
        strcpy(net.password, "password123");
    } else {
        len = sizeof net.password;
        lcd349_kv_get_str("wifi", "pass", net.password, &len);
    }
    lcd349_wifi_set_networks(&net, 1);
    lcd349_wifi_start();
    guide_init();
    xTaskCreate(console_task, "console", 4096, NULL, 1, NULL);

    lcd349_classifier_t cls;
    lcd349_classifier_cfg_t cfg = {.hold_ms = {1000, 3000}, .chord_window_ms = 150};
    lcd349_classifier_init(&cls, &cfg);
    bool wake_press = false; /* the press that woke the board only wakes */
    uint32_t next_status = 0;
    ESP_LOGI(TAG, "ready");

    for (;;) {
        lcd349_gesture_t g[LCD349_CLASSIFIER_MAX_OUT];
        size_t n = 0;
        lcd349_btn_event_t ev;
        if (xQueueReceive(s_buttons, &ev, pdMS_TO_TICKS(20)) == pdTRUE) {
            n = lcd349_classifier_feed(&cls, &ev, g, LCD349_CLASSIFIER_MAX_OUT);
        } else {
            n = lcd349_classifier_tick(&cls, lcd349_time_ms(), g, LCD349_CLASSIFIER_MAX_OUT);
        }
        if (wake_press) {
            if (!lcd349_button_is_down(LCD349_BTN_BOOT) && !lcd349_button_is_down(LCD349_BTN_PWR)) {
                wake_press = false;
                lcd349_classifier_init(&cls, &cfg);
            }
            n = 0;
        }
        if (guide_running()) {
            n = 0; /* the guide has the buttons */
        }
        for (size_t i = 0; i < n; i++) {
            char text[64] = "";
            switch (g[i].kind) {
            case LCD349_GESTURE_SHORT:
                ESP_LOGI(TAG, "gesture SHORT %s %u ms", btn_name(g[i].btn), (unsigned)g[i].held_ms);
                snprintf(text, sizeof text, "button: %s short", btn_name(g[i].btn));
                break;
            case LCD349_GESTURE_HOLD:
                ESP_LOGI(TAG, "gesture HOLD %s level %u", btn_name(g[i].btn), g[i].level);
                snprintf(text, sizeof text, "button: %s hold %u", btn_name(g[i].btn), g[i].level);
                break;
            case LCD349_GESTURE_HOLD_END:
                ESP_LOGI(TAG, "gesture HOLD_END %s level %u %u ms", btn_name(g[i].btn), g[i].level, (unsigned)g[i].held_ms);
                if (g[i].btn == LCD349_BTN_BOOT && g[i].level == 1) {
                    ESP_LOGI(TAG, "sleeping until a button");
                    lcd349_btn_t by;
                    if (lcd349_sleep_until_button(0, &by) == LCD349_OK) {
                        ESP_LOGI(TAG, "woke by %s", btn_name(by));
                        wake_press = true;
                    }
                } else if (g[i].btn == LCD349_BTN_PWR && g[i].level == 2) {
                    ESP_LOGI(TAG, "power off requested");
                    ESP_LOGI(TAG, "power_off: %s", lcd349_err_str(lcd349_power_off()));
                }
                break;
            case LCD349_GESTURE_CHORD:
                ESP_LOGI(TAG, "gesture CHORD");
                snprintf(text, sizeof text, "button: BOOT+PWR");
                break;
            case LCD349_GESTURE_CHORD_END:
                ESP_LOGI(TAG, "gesture CHORD_END");
                break;
            }
            if (text[0]) {
                set_text(s_gesture, text);
            }
        }
        uint32_t now = lcd349_time_ms();
        if ((int32_t)(now - next_status) >= 0) {
            refresh_status();
            next_status = now + 1000;
        }
    }
}
