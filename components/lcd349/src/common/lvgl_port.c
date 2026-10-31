/* SPDX-License-Identifier: Apache-2.0
 *
 * The LVGL port shared by both backends: a landscape full-frame display
 * (each backend rotates the frame into the panel's orientation), the touch
 * input device, and the lcd349_ui task that owns LVGL.
 */
#include <string.h>

#include "esp_log.h"
#include "freertos/FreeRTOS.h"
#include "freertos/queue.h"
#include "freertos/semphr.h"
#include "freertos/task.h"

#include "kit.h"
#include "rotate.h"

#if LV_COLOR_DEPTH != 16
#error "lcd349 needs LVGL with 16-bit colour: set CONFIG_LV_COLOR_DEPTH_16=y"
#endif

#define FRAME_PX (LCD349_LCD_W * LCD349_LCD_H)
#define FRAME_BYTES (FRAME_PX * 2)
#define UI_CALL_QUEUE_LEN 16
#define UI_MAX_WAIT_MS 10

typedef struct {
    void (*fn)(void *);
    void *ctx;
} ui_call_t;

static const char *TAG = "lcd349_ui";
static SemaphoreHandle_t s_lock;
static QueueHandle_t s_calls;
static lv_display_t *s_disp;
static lv_indev_t *s_indev;
static uint16_t *s_draw; /* landscape, LVGL renders here */
static uint8_t s_backlight;
static bool s_asleep;
static bool s_was_pressed;
static uint16_t s_last_x, s_last_y;

/* ---- Lock and calls ---- */

void lcd349_ui_lock(void)
{
    if (s_lock) {
        xSemaphoreTakeRecursive(s_lock, portMAX_DELAY);
    }
}

void lcd349_ui_unlock(void)
{
    if (s_lock) {
        xSemaphoreGiveRecursive(s_lock);
    }
}

lcd349_err_t lcd349_ui_call(void (*fn)(void *ctx), void *ctx)
{
    if (!fn) {
        return LCD349_ERR_ARG;
    }
    if (!s_calls) {
        return LCD349_ERR_STATE;
    }
    ui_call_t c = {fn, ctx};
    return xQueueSend(s_calls, &c, 0) == pdTRUE ? LCD349_OK : LCD349_ERR_NO_MEM;
}

/* ---- Display ---- */

lv_display_t *lcd349_display(void)
{
    return s_disp;
}

static uint32_t tick_cb(void)
{
    return lcd349_time_ms();
}

static void flush_cb(lv_display_t *d, const lv_area_t *area, uint8_t *px)
{
    (void)area;
    if (s_asleep) {
        lv_display_flush_ready(d);
        return;
    }
    be_panel_flush((const uint16_t *)px);
    lv_display_flush_ready(d);
}

lcd349_err_t lcd349_backlight_set(uint8_t pct)
{
    if (pct > 100) {
        return LCD349_ERR_ARG;
    }
    if (!lcd349__initialised()) {
        return LCD349_ERR_STATE;
    }
    if (!lcd349__ok(LCD349_SUB_DISPLAY)) {
        return LCD349_ERR_UNAVAILABLE;
    }
    lcd349_ui_lock();
    lcd349_err_t err = s_asleep ? LCD349_OK : be_backlight_set(pct);
    if (err == LCD349_OK) {
        s_backlight = pct;
    }
    lcd349_ui_unlock();
    return err;
}

uint8_t lcd349_backlight_get(void)
{
    return s_backlight;
}

lcd349_err_t lcd349_display_sleep(bool sleep)
{
    if (!lcd349__initialised()) {
        return LCD349_ERR_STATE;
    }
    if (!lcd349__ok(LCD349_SUB_DISPLAY)) {
        return LCD349_ERR_UNAVAILABLE;
    }
    lcd349_ui_lock();
    lcd349_err_t err = LCD349_OK;
    if (sleep != s_asleep) {
        /* Asleep, no new areas are invalidated and flush_cb sends nothing to
         * the panel (a redraw already pending is dropped there). The whole
         * screen is redrawn on wake. */
        if (sleep) {
            err = be_backlight_set(0);
            lv_display_enable_invalidation(s_disp, false);
        } else {
            lv_display_enable_invalidation(s_disp, true);
            lv_obj_invalidate(lv_screen_active());
            err = be_backlight_set(s_backlight);
        }
        s_asleep = sleep;
    }
    lcd349_ui_unlock();
    return err;
}

bool lcd349_display_is_asleep(void)
{
    return s_asleep;
}

/* ---- Touch ---- */

lv_indev_t *lcd349_touch_indev(void)
{
    return s_indev;
}

/* Touch is read only while the display is awake and lit (requirement 1.5). */
static void touch_read_cb(lv_indev_t *indev, lv_indev_data_t *data)
{
    (void)indev;
    /* Hit-test against the layout about to be drawn: LVGL lays out only when
     * it refreshes, so a touch just after the UI changed would find objects
     * where they were (or nowhere), and then see the finger "dragged" onto
     * the right one, which never clicks. Free when nothing changed. */
    lv_obj_update_layout(lv_display_get_screen_active(s_disp));
    lv_obj_update_layout(lv_display_get_layer_top(s_disp));
    lv_obj_update_layout(lv_display_get_layer_sys(s_disp));
    uint16_t px, py, x = s_last_x, y = s_last_y;
    bool pressed = false;
    if (!s_asleep && s_backlight > 0 && be_touch_read(&px, &py)) {
        lcd349__panel_to_landscape(px, py, LCD349_ROTATION_CW90, &x, &y);
        if (x < LCD349_LCD_W && y < LCD349_LCD_H) {
            pressed = true;
        }
    }
    if (pressed || s_was_pressed) {
        if (pressed) {
            s_last_x = x;
            s_last_y = y;
        }
        lcd349_touch_sample_t s = {.x = s_last_x, .y = s_last_y, .pressed = pressed, .t_ms = lcd349_time_ms()};
        lcd349__evt_touch(&s);
    }
    s_was_pressed = pressed;
    data->point.x = s_last_x;
    data->point.y = s_last_y;
    data->state = pressed ? LV_INDEV_STATE_PRESSED : LV_INDEV_STATE_RELEASED;
}

/* ---- The UI task ---- */

static void ui_task(void *arg)
{
    (void)arg;
    for (;;) {
        lcd349_ui_lock();
        uint32_t next = lv_timer_handler();
        be_ui_poll();
        lcd349_ui_unlock();

        if (next > UI_MAX_WAIT_MS) {
            next = UI_MAX_WAIT_MS;
        }
        TickType_t wait = pdMS_TO_TICKS(next);
        ui_call_t c;
        /* Sleep until the next LVGL timer, waking early for queued calls. */
        if (xQueueReceive(s_calls, &c, wait ? wait : 1) == pdTRUE) {
            do {
                lcd349_ui_lock();
                c.fn(c.ctx);
                lcd349_ui_unlock();
            } while (xQueueReceive(s_calls, &c, 0) == pdTRUE);
        }
    }
}

lcd349_err_t lcd349__lvgl_start(const lcd349_config_t *cfg, uint32_t *failed)
{
    s_lock = xSemaphoreCreateRecursiveMutex();
    s_calls = xQueueCreate(UI_CALL_QUEUE_LEN, sizeof(ui_call_t));
    if (!s_lock || !s_calls) {
        *failed |= LCD349_SUB_DISPLAY | LCD349_SUB_TOUCH;
        return LCD349_ERR_NO_MEM;
    }

    lv_init();
    lv_tick_set_cb(tick_cb);

    lcd349_err_t err = be_display_init();
    if (err == LCD349_OK) {
        s_draw = lcd349_alloc(FRAME_BYTES, LCD349_MEM_ANY);
        if (!s_draw) {
            err = LCD349_ERR_NO_MEM;
        }
    }
    if (err == LCD349_OK) {
        s_disp = lv_display_create(LCD349_LCD_W, LCD349_LCD_H);
        lv_display_set_color_format(s_disp, LV_COLOR_FORMAT_RGB565);
        /* Full-frame rendering: the panel only takes whole frames. */
        lv_display_set_buffers(s_disp, s_draw, NULL, FRAME_BYTES, LV_DISPLAY_RENDER_MODE_FULL);
        lv_display_set_flush_cb(s_disp, flush_cb);
        lv_display_set_dpi(s_disp, LCD349_PPI);
        s_backlight = cfg->backlight_pct;
        if (be_backlight_set(s_backlight) != LCD349_OK) {
            ESP_LOGW(TAG, "backlight unavailable");
        }
    } else {
        *failed |= LCD349_SUB_DISPLAY;
        ESP_LOGE(TAG, "display unavailable: %s", lcd349_err_str(err));
    }

    if (s_disp && be_touch_init() == LCD349_OK) {
        s_indev = lv_indev_create();
        lv_indev_set_type(s_indev, LV_INDEV_TYPE_POINTER);
        lv_indev_set_read_cb(s_indev, touch_read_cb);
        lv_indev_set_display(s_indev, s_disp);
        lv_timer_set_period(lv_indev_get_read_timer(s_indev), cfg->touch_period_ms);
    } else {
        *failed |= LCD349_SUB_TOUCH;
    }

    if (xTaskCreate(ui_task, "lcd349_ui", 16384, NULL, LCD349_PRIO_UI, NULL) != pdPASS) {
        *failed |= LCD349_SUB_DISPLAY | LCD349_SUB_TOUCH;
        return LCD349_ERR_NO_MEM;
    }
    return err;
}
