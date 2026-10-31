/* SPDX-License-Identifier: Apache-2.0
 *
 * hello: the smallest lcd349 product. It greets, and shows and logs every
 * touch and button edge; a button press beeps. Nothing on screen is a
 * control, so the whole screen is a touch pad.
 */
#include <stdio.h>

#include "esp_log.h"
#include "lcd349.h"

static const char *TAG = "hello";

static lv_obj_t *s_event;

static lv_obj_t *label(lv_obj_t *parent, const char *text, const lv_font_t *font, uint32_t color)
{
    lv_obj_t *l = lv_label_create(parent);
    lv_obj_set_style_text_font(l, font, 0);
    lv_obj_set_style_text_color(l, lv_color_hex(color), 0);
    lv_label_set_text(l, text);
    return l;
}

/* Kit callbacks run on the kit's event task: take the LVGL lock to update. */
static void show(const char *text)
{
    lcd349_ui_lock();
    lv_label_set_text(s_event, text);
    lcd349_ui_unlock();
}

static void on_touch(const lcd349_touch_sample_t *t, void *ctx)
{
    (void)ctx;
    static bool was_down;
    if (t->pressed == was_down) {
        return; /* log edges, not every sample */
    }
    was_down = t->pressed;
    char text[48];
    snprintf(text, sizeof text, "touch %s at %u, %u", t->pressed ? "down" : "up", t->x, t->y);
    ESP_LOGI(TAG, "%s", text);
    show(text);
}

static void on_button(const lcd349_btn_event_t *ev, void *ctx)
{
    (void)ctx;
    char text[48];
    snprintf(text, sizeof text, "button %s %s", ev->btn == LCD349_BTN_BOOT ? "BOOT" : "PWR", ev->down ? "down" : "up");
    ESP_LOGI(TAG, "%s", text);
    show(text);
    if (ev->down) {
        lcd349_tone(ev->btn == LCD349_BTN_BOOT ? 880 : 660, 40, 60);
    }
}

void app_main(void)
{
    lcd349_err_t err = lcd349_init(NULL);
    ESP_LOGI(TAG, "lcd349_init: %s, status 0x%03x", lcd349_err_str(err), (unsigned)lcd349_board_status());
    if (!lcd349_display()) {
        return;
    }
    lcd349_ui_lock();
    lv_obj_t *scr = lv_screen_active();
    lv_obj_set_style_bg_color(scr, lv_color_hex(0x0B1622), 0);
    lv_obj_set_style_bg_opa(scr, LV_OPA_COVER, 0);
    lv_obj_set_scrollable(scr, false);
    lv_obj_t *title = label(scr, "Hello from the bar", &lv_font_montserrat_20, 0xFFFFFF);
    lv_obj_align(title, LV_ALIGN_TOP_LEFT, 16, 24);
    lv_obj_t *hint = label(scr, "Touch anywhere, or press BOOT or PWR.", &lv_font_montserrat_14, 0xB8C4D0);
    lv_obj_align(hint, LV_ALIGN_TOP_LEFT, 16, 60);
    s_event = label(scr, "waiting", &lv_font_montserrat_20, 0x7FD1FF);
    lv_obj_set_width(s_event, LCD349_LCD_W - 32);
    lv_label_set_long_mode(s_event, LV_LABEL_LONG_MODE_DOTS);
    lv_obj_align(s_event, LV_ALIGN_BOTTOM_LEFT, 16, -24);
    lcd349_ui_unlock();

    lcd349_touch_subscribe(on_touch, NULL);
    lcd349_buttons_subscribe(on_button, NULL);
    ESP_LOGI(TAG, "ready");
}
