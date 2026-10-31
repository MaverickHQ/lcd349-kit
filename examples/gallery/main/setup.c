/* SPDX-License-Identifier: Apache-2.0 */
#include <stdio.h>

#include "recipes.h"

/* recipe:start setup */
/* A setup screen: settings in one row (no scrolling), each control a full
 * touch target. Brightness steps in tens with - and +; sound is a switch
 * sized up to the touch target. Changes apply at once and are saved with
 * lcd349_kv, so they survive a reset. */
typedef struct {
    lv_obj_t *value;
    uint8_t brightness;
} setup_t;

static void setup_free(lv_event_t *e)
{
    lv_free(lv_event_get_user_data(e));
}

static void setup_show(setup_t *s)
{
    lv_label_set_text_fmt(s->value, "%u%%", s->brightness);
}

static void brightness_step(lv_event_t *e, int delta)
{
    setup_t *s = lv_event_get_user_data(e);
    int b = LV_CLAMP(10, (int)s->brightness + delta, 100);
    s->brightness = (uint8_t)b;
    lcd349_backlight_set(s->brightness);
    lcd349_kv_set_blob("setup", "brightness", &s->brightness, 1);
    setup_show(s);
}

static void brightness_down(lv_event_t *e) { brightness_step(e, -10); }
static void brightness_up(lv_event_t *e) { brightness_step(e, +10); }

static void sound_changed(lv_event_t *e)
{
    uint8_t on = lv_obj_has_state(lv_event_get_current_target_obj(e), LV_STATE_CHECKED);
    lcd349_kv_set_blob("setup", "sound", &on, 1);
}

lv_obj_t *setup_create(lv_obj_t *parent, lv_event_cb_t on_done, void *ctx)
{
    lv_obj_t *panel = ui_panel(parent, LCD349_LCD_W, UI_BODY_H, UI_BG);
    setup_t *s = lv_malloc_zeroed(sizeof *s);
    s->brightness = lcd349_backlight_get();
    lv_obj_set_user_data(panel, s);
    lv_obj_add_event_cb(panel, setup_free, LV_EVENT_DELETE, s);
    const int top = UI_PAD + lv_font_get_line_height(UI_FONT) + 6;

    lv_obj_t *l = ui_label(panel, "Brightness", UI_FONT, UI_MUTED);
    lv_obj_set_pos(l, UI_PAD, UI_PAD);
    lv_obj_t *minus = ui_button(panel, LV_SYMBOL_MINUS, UI_TARGET + 8, UI_TARGET + 8, brightness_down, s);
    lv_obj_set_pos(minus, UI_PAD, top);
    s->value = ui_label(panel, "", UI_FONT_LARGE, UI_TEXT);
    lv_obj_set_width(s->value, 72);
    lv_obj_set_style_text_align(s->value, LV_TEXT_ALIGN_CENTER, 0);
    lv_obj_align_to(s->value, minus, LV_ALIGN_OUT_RIGHT_MID, 4, 0);
    lv_obj_t *plus = ui_button(panel, LV_SYMBOL_PLUS, UI_TARGET + 8, UI_TARGET + 8, brightness_up, s);
    lv_obj_align_to(plus, s->value, LV_ALIGN_OUT_RIGHT_MID, 4, 0);
    setup_show(s);

    lv_obj_t *sl = ui_label(panel, "Sound", UI_FONT, UI_MUTED);
    lv_obj_set_pos(sl, 300, UI_PAD);
    lv_obj_t *sw = lv_switch_create(panel);
    lv_obj_set_size(sw, 2 * UI_TARGET, UI_TARGET);
    lv_obj_set_pos(sw, 300, top + 4);
    lv_obj_set_style_bg_color(sw, lv_color_hex(UI_GOOD), LV_PART_INDICATOR | LV_STATE_CHECKED);
    uint8_t on = 1;
    size_t len = 1;
    lcd349_kv_get_blob("setup", "sound", &on, &len);
    if (on) {
        lv_obj_add_state(sw, LV_STATE_CHECKED);
    }
    lv_obj_add_event_cb(sw, sound_changed, LV_EVENT_VALUE_CHANGED, NULL);

    lv_obj_t *done = ui_button(panel, LV_SYMBOL_OK " Done", 140, UI_BODY_H - 2 * UI_PAD, on_done, ctx);
    lv_obj_align(done, LV_ALIGN_RIGHT_MID, -UI_PAD, 0);
    return panel;
}
/* recipe:end */
