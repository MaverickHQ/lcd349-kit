/* SPDX-License-Identifier: Apache-2.0 */
#include "recipes.h"

/* recipe:start card */
/* A detail card: a large title, a body that wraps over two lines and ends in
 * "..." if it's longer (deliberate truncation passes lint; clipped text
 * doesn't), and a Back button on the right where a thumb reaches it. */
lv_obj_t *card_create(lv_obj_t *parent, const char *title, const char *body, lv_event_cb_t on_back, void *ctx)
{
    const int back_w = 120;
    const int text_w = LCD349_LCD_W - back_w - 3 * UI_PAD;
    lv_obj_t *card = ui_panel(parent, LCD349_LCD_W, UI_BODY_H, UI_BG);

    lv_obj_t *t = ui_label(card, title, UI_FONT_LARGE, UI_TEXT);
    lv_obj_set_width(t, text_w);
    lv_label_set_long_mode(t, LV_LABEL_LONG_MODE_DOTS);
    lv_obj_set_pos(t, UI_PAD, UI_PAD);

    lv_obj_t *b = ui_label(card, body, UI_FONT, UI_MUTED);
    lv_obj_set_size(b, text_w, 2 * lv_font_get_line_height(UI_FONT));
    lv_label_set_long_mode(b, LV_LABEL_LONG_MODE_DOTS);
    lv_obj_set_pos(b, UI_PAD, UI_PAD + lv_font_get_line_height(UI_FONT_LARGE) + 8);

    lv_obj_t *back = ui_button(card, LV_SYMBOL_LEFT " Back", back_w, UI_BODY_H - 2 * UI_PAD, on_back, ctx);
    lv_obj_align(back, LV_ALIGN_RIGHT_MID, -UI_PAD, 0);
    return card;
}
/* recipe:end */
