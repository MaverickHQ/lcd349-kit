/* SPDX-License-Identifier: Apache-2.0 */
#include "recipes.h"

/* recipe:start hold */
/* Hold to confirm: for actions that matter, a short press only shows how;
 * holding the button fills the bar, and the action happens when the hold
 * completes (the classifier's HOLD gesture). Touch never confirms.
 *
 * The visibility guard: a hold counts only if the button went down at least
 * 500 ms after the prompt appeared, so a press meant for the previous screen
 * can't approve this one. */
#define HOLD_GUARD_MS 500

typedef struct {
    lv_obj_t *bar;
    lcd349_btn_t btn;
    uint32_t shown_ms;
    uint16_t shown_step;
} hold_t;

static void hold_free(lv_event_t *e)
{
    lv_free(lv_event_get_user_data(e));
}

lv_obj_t *hold_create(lv_obj_t *parent, const char *prompt, lcd349_btn_t btn)
{
    lv_obj_t *panel = ui_panel(parent, LCD349_LCD_W, UI_BODY_H, UI_BG);
    hold_t *h = lv_malloc_zeroed(sizeof *h);
    h->btn = btn;
    h->shown_ms = lcd349_time_ms();
    lv_obj_t *p = ui_label(panel, prompt, UI_FONT_LARGE, UI_TEXT);
    lv_obj_align(p, LV_ALIGN_TOP_LEFT, UI_PAD, UI_PAD);
    h->bar = lv_bar_create(panel);
    lv_obj_set_size(h->bar, LCD349_LCD_W - 2 * UI_PAD, 32);
    lv_obj_align(h->bar, LV_ALIGN_BOTTOM_MID, 0, -UI_PAD - 24);
    lv_bar_set_range(h->bar, 0, 10);
    lv_obj_set_style_bg_color(h->bar, lv_color_hex(0x3A5270), LV_PART_MAIN); /* an empty track that still shows */
    lv_obj_set_style_bg_color(h->bar, lv_color_hex(UI_GOOD), LV_PART_INDICATOR);
    lv_obj_t *hint = ui_label(panel, "Release to cancel", UI_FONT, UI_MUTED);
    lv_obj_align(hint, LV_ALIGN_BOTTOM_LEFT, UI_PAD, -4);
    lv_obj_set_user_data(panel, h);
    lv_obj_add_event_cb(panel, hold_free, LV_EVENT_DELETE, h);
    return panel;
}

/* Call as the hold progresses (lcd349_classifier_hold_permille). The bar
 * moves in tenths, so a 1 s hold costs ten flushes, not fifty. */
void hold_progress(lv_obj_t *hold, uint16_t permille)
{
    hold_t *h = lv_obj_get_user_data(hold);
    uint16_t step = permille / 100;
    if (step != h->shown_step) {
        h->shown_step = step;
        lv_bar_set_value(h->bar, step, LV_ANIM_OFF);
    }
}

/* True when g completes a hold that should count. */
bool hold_accepts(lv_obj_t *hold, const lcd349_gesture_t *g)
{
    hold_t *h = lv_obj_get_user_data(hold);
    uint32_t pressed_at = g->t_ms - g->held_ms;
    return g->kind == LCD349_GESTURE_HOLD && g->btn == h->btn && g->level == 1 &&
           (int32_t)(pressed_at - h->shown_ms) >= HOLD_GUARD_MS;
}
/* recipe:end */
