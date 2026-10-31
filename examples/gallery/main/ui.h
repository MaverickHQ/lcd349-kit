/* SPDX-License-Identifier: Apache-2.0
 *
 * The gallery's shared look: colours, the two layout bands, and small helpers
 * every recipe uses. Sizes come from lcd349_board.h, never copied numbers.
 */
#pragma once

#include "lcd349.h"

/* recipe:start ui */
/* Colours: a dark background with text well over the 4.5:1 contrast rule. */
#define UI_BG 0x0B1622
#define UI_SURFACE 0x1B2B40
#define UI_ACCENT 0x1565C0
#define UI_TEXT 0xFFFFFF
#define UI_MUTED 0xB8C4D0
#define UI_DANGER 0xB71C1C
#define UI_GOOD 0x2E7D32

/* Two bands: a status bar on top, the body below it. */
#define UI_STATUS_H 24
#define UI_BODY_H (LCD349_LCD_H - UI_STATUS_H)
#define UI_TARGET LCD349_RULE_TOUCH_TARGET_PX /* the smallest touch target */
#define UI_PAD 12

#define UI_FONT (&lv_font_montserrat_14)
#define UI_FONT_LARGE (&lv_font_montserrat_20)

/* A label in one of the two fonts. */
lv_obj_t *ui_label(lv_obj_t *parent, const char *text, const lv_font_t *font, uint32_t color);

/* A plain container: no border, padding, scrolling or clicks. */
lv_obj_t *ui_panel(lv_obj_t *parent, int w, int h, uint32_t bg);

/* A button with a centred label, at least UI_TARGET in both directions. */
lv_obj_t *ui_button(lv_obj_t *parent, const char *text, int w, int h, lv_event_cb_t on_click, void *ctx);

/* Every frame is a full-panel flush on this board, so presses change state
 * at once instead of animating. (A NULL transition crashes LVGL 9.6.) */
void ui_no_press_animation(lv_obj_t *obj);

/* The ring around the object the buttons have stepped to. */
void ui_set_focus(lv_obj_t *obj, bool focused);
/* recipe:end */
