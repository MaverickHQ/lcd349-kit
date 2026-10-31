/* SPDX-License-Identifier: Apache-2.0
 *
 * The recipes. Call every function with the LVGL lock held: from an LVGL
 * callback, or between lcd349_ui_lock() and lcd349_ui_unlock().
 */
#pragma once

#include "ui.h"

typedef void (*recipe_pick_cb_t)(int index, void *ctx);

/* Status bar: title, Wi-Fi, battery and clock across the top band. */
lv_obj_t *status_bar_create(lv_obj_t *parent, const char *title);
void status_bar_refresh(lv_obj_t *bar);

/* List: rows stepped with BOOT and opened by a tap or a hold. */
lv_obj_t *list_create(lv_obj_t *parent, const char *const *items, int n, recipe_pick_cb_t on_open, void *ctx);
void list_step(lv_obj_t *list, int delta);
void list_open_selected(lv_obj_t *list);

/* Detail card: a title, a body that ends in "..." when too long, and Back. */
lv_obj_t *card_create(lv_obj_t *parent, const char *title, const char *body, lv_event_cb_t on_back, void *ctx);

/* Menu tiles: a horizontal row of large tiles that snaps tile by tile. */
typedef struct {
    const char *symbol; /* an LV_SYMBOL_*, which the built-in fonts carry */
    const char *label;
} tile_t;
lv_obj_t *tiles_create(lv_obj_t *parent, const tile_t *tiles, int n, recipe_pick_cb_t on_open, void *ctx);
void tiles_step(lv_obj_t *row, int delta);
void tiles_open_selected(lv_obj_t *row);

/* Confirm dialog: a modal question with Cancel and an action. */
typedef void (*confirm_cb_t)(bool confirmed, void *ctx);
lv_obj_t *confirm_show(const char *question, const char *action, confirm_cb_t done, void *ctx);
void confirm_step(lv_obj_t *dialog);
void confirm_choose(lv_obj_t *dialog);

/* Toast: a short message that clears itself. */
void toast_show(const char *text, uint32_t ms);

/* Hold to confirm: a prompt and a bar that fills while a button is held. */
lv_obj_t *hold_create(lv_obj_t *parent, const char *prompt, lcd349_btn_t btn);
void hold_progress(lv_obj_t *hold, uint16_t permille);
bool hold_accepts(lv_obj_t *hold, const lcd349_gesture_t *g);

/* Setup: brightness steps and a sound switch, saved to storage. */
lv_obj_t *setup_create(lv_obj_t *parent, lv_event_cb_t on_done, void *ctx);
