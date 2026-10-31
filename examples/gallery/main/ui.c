/* SPDX-License-Identifier: Apache-2.0 */
#include "ui.h"

/* recipe:start ui */
lv_obj_t *ui_label(lv_obj_t *parent, const char *text, const lv_font_t *font, uint32_t color)
{
    lv_obj_t *l = lv_label_create(parent);
    lv_obj_set_style_text_font(l, font, 0);
    lv_obj_set_style_text_color(l, lv_color_hex(color), 0);
    lv_label_set_text(l, text);
    return l;
}

lv_obj_t *ui_panel(lv_obj_t *parent, int w, int h, uint32_t bg)
{
    lv_obj_t *p = lv_obj_create(parent);
    lv_obj_set_size(p, w, h);
    lv_obj_set_style_bg_color(p, lv_color_hex(bg), 0);
    lv_obj_set_style_bg_opa(p, LV_OPA_COVER, 0);
    lv_obj_set_style_border_width(p, 0, 0);
    lv_obj_set_style_radius(p, 0, 0);
    lv_obj_set_style_pad_all(p, 0, 0);
    lv_obj_set_scrollable(p, false);
    lv_obj_set_clickable(p, false); /* LVGL makes containers clickable; a panel only holds things */
    return p;
}

void ui_no_press_animation(lv_obj_t *obj)
{
    static const lv_style_prop_t no_props[] = {LV_STYLE_PROP_INV};
    static lv_style_transition_dsc_t none;
    static bool ready;
    if (!ready) {
        lv_style_transition_dsc_init(&none, no_props, lv_anim_path_linear, 0, 0, NULL);
        ready = true;
    }
    lv_obj_set_style_transition(obj, &none, LV_STATE_DEFAULT);
    lv_obj_set_style_transition(obj, &none, LV_STATE_PRESSED);
}

lv_obj_t *ui_button(lv_obj_t *parent, const char *text, int w, int h, lv_event_cb_t on_click, void *ctx)
{
    lv_obj_t *b = lv_button_create(parent);
    lv_obj_set_size(b, LV_MAX(w, UI_TARGET), LV_MAX(h, UI_TARGET));
    lv_obj_set_style_bg_color(b, lv_color_hex(UI_ACCENT), 0);
    lv_obj_set_style_shadow_width(b, 0, 0);
    ui_no_press_animation(b);
    if (on_click) {
        lv_obj_add_event_cb(b, on_click, LV_EVENT_CLICKED, ctx);
    }
    lv_obj_t *l = ui_label(b, text, UI_FONT_LARGE, UI_TEXT);
    lv_obj_center(l);
    return b;
}

void ui_set_focus(lv_obj_t *obj, bool focused)
{
    lv_obj_set_style_outline_width(obj, focused ? 3 : 0, 0);
    lv_obj_set_style_outline_color(obj, lv_color_hex(UI_TEXT), 0);
    lv_obj_set_style_outline_pad(obj, focused ? 2 : 0, 0);
}
/* recipe:end */
