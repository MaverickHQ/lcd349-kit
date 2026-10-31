/* SPDX-License-Identifier: Apache-2.0 */
#include "recipes.h"

/* recipe:start confirm */
/* A modal question on the top layer. A full-screen backdrop takes every tap
 * (and dims the screen), so nothing below can be pressed by accident. Cancel
 * has the focus first: a stray BOOT hold must never confirm. BOOT steps
 * between the two buttons and a hold chooses; a tap chooses directly. */
typedef struct {
    lv_obj_t *backdrop, *buttons[2];
    int focus; /* 0 = cancel, 1 = the action */
    confirm_cb_t done;
    void *ctx;
} confirm_t;

static void confirm_finish(confirm_t *c, bool confirmed)
{
    confirm_cb_t done = c->done;
    void *ctx = c->ctx;
    lv_obj_delete(c->backdrop); /* frees c */
    done(confirmed, ctx);
}

static void confirm_clicked(lv_event_t *e)
{
    confirm_t *c = lv_event_get_user_data(e);
    confirm_finish(c, lv_event_get_current_target_obj(e) == c->buttons[1]);
}

static void confirm_free(lv_event_t *e)
{
    lv_free(lv_event_get_user_data(e));
}

lv_obj_t *confirm_show(const char *question, const char *action, confirm_cb_t done, void *ctx)
{
    confirm_t *c = lv_malloc_zeroed(sizeof *c);
    c->done = done;
    c->ctx = ctx;
    c->backdrop = ui_panel(lv_layer_top(), LCD349_LCD_W, LCD349_LCD_H, 0x000000);
    lv_obj_set_style_bg_opa(c->backdrop, LV_OPA_70, 0);
    lv_obj_set_clickable(c->backdrop, true); /* swallows taps outside the dialog */
    lv_obj_add_event_cb(c->backdrop, confirm_free, LV_EVENT_DELETE, c);

    lv_obj_t *box = ui_panel(c->backdrop, LCD349_LCD_W - 4 * UI_PAD, LCD349_LCD_H - 4 * UI_PAD, UI_SURFACE);
    lv_obj_set_style_radius(box, 8, 0);
    lv_obj_center(box);
    lv_obj_t *q = ui_label(box, question, UI_FONT_LARGE, UI_TEXT);
    lv_obj_set_width(q, LCD349_LCD_W - 6 * UI_PAD);
    lv_label_set_long_mode(q, LV_LABEL_LONG_MODE_DOTS);
    lv_obj_align(q, LV_ALIGN_TOP_LEFT, UI_PAD, UI_PAD);

    const int bw = 180;
    c->buttons[0] = ui_button(box, "Cancel", bw, UI_TARGET, confirm_clicked, c);
    lv_obj_align(c->buttons[0], LV_ALIGN_BOTTOM_LEFT, UI_PAD, -UI_PAD);
    c->buttons[1] = ui_button(box, action, bw, UI_TARGET, confirm_clicked, c);
    lv_obj_set_style_bg_color(c->buttons[1], lv_color_hex(UI_DANGER), 0);
    lv_obj_align(c->buttons[1], LV_ALIGN_BOTTOM_RIGHT, -UI_PAD, -UI_PAD);
    ui_set_focus(c->buttons[0], true);
    lv_obj_set_user_data(c->backdrop, c);
    return c->backdrop;
}

void confirm_step(lv_obj_t *dialog)
{
    confirm_t *c = lv_obj_get_user_data(dialog);
    ui_set_focus(c->buttons[c->focus], false);
    c->focus ^= 1;
    ui_set_focus(c->buttons[c->focus], true);
}

void confirm_choose(lv_obj_t *dialog)
{
    confirm_t *c = lv_obj_get_user_data(dialog);
    confirm_finish(c, c->focus == 1);
}
/* recipe:end */
