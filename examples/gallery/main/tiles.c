/* SPDX-License-Identifier: Apache-2.0 */
#include "recipes.h"

/* recipe:start tiles */
/* A horizontal menu: big tiles along the screen's long axis, which a swipe
 * scrolls a tile at a time (snapping, no momentum). BOOT steps the focus and
 * a hold opens; a tap opens the tile tapped. */
#define TILE_W 140
#define TILE_H (UI_BODY_H - 2 * UI_PAD)

typedef struct {
    lv_obj_t *tiles[12];
    int n, selected;
    recipe_pick_cb_t on_open;
    void *ctx;
} tiles_t;

static void tiles_free(lv_event_t *e)
{
    lv_free(lv_event_get_user_data(e));
}

static void tile_clicked(lv_event_t *e)
{
    tiles_t *t = lv_event_get_user_data(e);
    int i = (int)lv_obj_get_index(lv_event_get_current_target_obj(e));
    ui_set_focus(t->tiles[t->selected], false); /* the tapped tile becomes the selection */
    t->selected = i;
    ui_set_focus(t->tiles[i], true);
    t->on_open(i, t->ctx);
}

lv_obj_t *tiles_create(lv_obj_t *parent, const tile_t *items, int n, recipe_pick_cb_t on_open, void *ctx)
{
    lv_obj_t *row = ui_panel(parent, LCD349_LCD_W, UI_BODY_H, UI_BG);
    lv_obj_set_scrollable(row, true);
    lv_obj_set_scroll_dir(row, LV_DIR_HOR);
    lv_obj_set_scroll_momentum(row, false);
    lv_obj_set_scroll_snap_x(row, LV_SCROLL_SNAP_CENTER);
    lv_obj_set_scrollbar_mode(row, LV_SCROLLBAR_MODE_OFF);
    lv_obj_set_flex_flow(row, LV_FLEX_FLOW_ROW);
    lv_obj_set_flex_align(row, LV_FLEX_ALIGN_START, LV_FLEX_ALIGN_CENTER, LV_FLEX_ALIGN_CENTER);
    lv_obj_set_style_pad_column(row, UI_PAD, 0);
    lv_obj_set_style_pad_hor(row, UI_PAD, 0);
    tiles_t *t = lv_malloc_zeroed(sizeof *t);
    t->n = LV_MIN(n, 12);
    t->on_open = on_open;
    t->ctx = ctx;
    for (int i = 0; i < t->n; i++) {
        lv_obj_t *tile = lv_button_create(row);
        lv_obj_set_size(tile, TILE_W, TILE_H);
        lv_obj_set_style_bg_color(tile, lv_color_hex(UI_SURFACE), 0);
        lv_obj_set_style_shadow_width(tile, 0, 0);
        lv_obj_set_snappable(tile, true);
        lv_obj_set_scroll_on_focus(tile, false); /* LVGL's default: a tap would move the row */
        ui_no_press_animation(tile);
        lv_obj_add_event_cb(tile, tile_clicked, LV_EVENT_CLICKED, t);
        lv_obj_t *icon = ui_label(tile, items[i].symbol, UI_FONT_LARGE, UI_TEXT);
        lv_obj_align(icon, LV_ALIGN_CENTER, 0, -16);
        lv_obj_t *label = ui_label(tile, items[i].label, UI_FONT, UI_TEXT);
        lv_obj_align(label, LV_ALIGN_CENTER, 0, 20);
        t->tiles[i] = tile;
    }
    lv_obj_set_user_data(row, t);
    lv_obj_add_event_cb(row, tiles_free, LV_EVENT_DELETE, t);
    ui_set_focus(t->tiles[0], true);
    return row;
}

void tiles_step(lv_obj_t *row, int delta)
{
    tiles_t *t = lv_obj_get_user_data(row);
    ui_set_focus(t->tiles[t->selected], false);
    t->selected = (t->selected + delta + t->n) % t->n;
    ui_set_focus(t->tiles[t->selected], true);
    lv_obj_scroll_to_view(t->tiles[t->selected], LV_ANIM_OFF);
}

void tiles_open_selected(lv_obj_t *row)
{
    tiles_t *t = lv_obj_get_user_data(row);
    t->on_open(t->selected, t->ctx);
}
/* recipe:end */
