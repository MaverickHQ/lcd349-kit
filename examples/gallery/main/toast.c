/* SPDX-License-Identifier: Apache-2.0 */
#include "recipes.h"

/* recipe:start toast */
/* A one-line message at the bottom of the top layer that removes itself.
 * It appears and goes at once (no fade: animation costs a flush per frame),
 * takes no input, and a new toast replaces the one showing. */
static lv_obj_t *s_toast;

static void toast_expire(lv_timer_t *t)
{
    lv_obj_t *toast = lv_timer_get_user_data(t);
    if (toast == s_toast) {
        lv_obj_delete(s_toast);
        s_toast = NULL;
    }
}

static void toast_deleted(lv_event_t *e)
{
    lv_timer_delete(lv_event_get_user_data(e));
    if (lv_event_get_current_target_obj(e) == s_toast) {
        s_toast = NULL;
    }
}

void toast_show(const char *text, uint32_t ms)
{
    if (s_toast) {
        lv_obj_delete(s_toast);
    }
    s_toast = ui_panel(lv_layer_top(), LCD349_LCD_W - 8 * UI_PAD, 40, UI_SURFACE);
    lv_obj_set_style_radius(s_toast, 8, 0);
    lv_obj_align(s_toast, LV_ALIGN_BOTTOM_MID, 0, -UI_PAD);
    lv_obj_t *l = ui_label(s_toast, text, UI_FONT, UI_TEXT);
    lv_obj_set_width(l, LCD349_LCD_W - 10 * UI_PAD);
    lv_label_set_long_mode(l, LV_LABEL_LONG_MODE_DOTS);
    lv_obj_center(l);
    lv_timer_t *t = lv_timer_create(toast_expire, ms, s_toast);
    lv_timer_set_repeat_count(t, 1);
    lv_timer_set_auto_delete(t, false);
    lv_obj_add_event_cb(s_toast, toast_deleted, LV_EVENT_DELETE, t);
}
/* recipe:end */
