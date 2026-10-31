/* SPDX-License-Identifier: Apache-2.0 */
#include "recipes.h"

/* recipe:start list */
/* Rows that a tap opens, or that BOOT steps through (short press) and opens
 * (hold). The list scrolls without momentum and snaps to whole rows, because
 * kinetic scrolling judders when every frame is a full-panel flush. Rows are
 * tagged "list-row": dense rows may be a little under the touch target size,
 * since a row is as wide as the screen. The selected row is filled with the
 * accent colour: an outline would be clipped at the screen's edges. */
#define LIST_ROW_H 48

typedef struct {
    lv_obj_t *rows[16];
    int n, selected;
    recipe_pick_cb_t on_open;
    void *ctx;
} list_t;

static void list_free(lv_event_t *e)
{
    lv_free(lv_event_get_user_data(e));
}

static void list_row_clicked(lv_event_t *e)
{
    lv_obj_t *row = lv_event_get_current_target_obj(e);
    list_t *l = lv_event_get_user_data(e);
    l->on_open((int)lv_obj_get_index(row), l->ctx);
}

lv_obj_t *list_create(lv_obj_t *parent, const char *const *items, int n, recipe_pick_cb_t on_open, void *ctx)
{
    lv_obj_t *list = ui_panel(parent, LCD349_LCD_W, UI_BODY_H, UI_BG);
    lv_obj_set_scrollable(list, true);
    lv_obj_set_scroll_dir(list, LV_DIR_VER);
    lv_obj_set_scroll_momentum(list, false);
    lv_obj_set_scroll_snap_y(list, LV_SCROLL_SNAP_START);
    lv_obj_set_scrollbar_mode(list, LV_SCROLLBAR_MODE_OFF);
    lv_obj_set_flex_flow(list, LV_FLEX_FLOW_COLUMN);
    lv_obj_set_style_pad_row(list, 0, 0);
    list_t *l = lv_malloc_zeroed(sizeof *l);
    l->n = LV_MIN(n, 16);
    l->on_open = on_open;
    l->ctx = ctx;
    for (int i = 0; i < l->n; i++) {
        lv_obj_t *row = ui_panel(list, LCD349_LCD_W, LIST_ROW_H, i % 2 ? UI_BG : UI_SURFACE);
        lv_obj_set_clickable(row, true);
        lv_obj_set_snappable(row, true);
        lv_obj_set_scroll_on_focus(row, false); /* LVGL's default: a tap would move the list */
        lcd349_ui_tag(row, "list-row");
        lv_obj_add_event_cb(row, list_row_clicked, LV_EVENT_CLICKED, l);
        lv_obj_t *text = ui_label(row, items[i], UI_FONT_LARGE, UI_TEXT);
        lv_obj_set_width(text, LCD349_LCD_W - 2 * UI_PAD);
        lv_label_set_long_mode(text, LV_LABEL_LONG_MODE_DOTS);
        lv_obj_align(text, LV_ALIGN_LEFT_MID, UI_PAD, 0);
        l->rows[i] = row;
    }
    lv_obj_set_user_data(list, l);
    lv_obj_add_event_cb(list, list_free, LV_EVENT_DELETE, l);
    list_step(list, 0);
    return list;
}

static void list_paint(list_t *l, int i, bool selected)
{
    uint32_t bg = selected ? UI_ACCENT : i % 2 ? UI_BG : UI_SURFACE;
    lv_obj_set_style_bg_color(l->rows[i], lv_color_hex(bg), 0);
}

void list_step(lv_obj_t *list, int delta)
{
    list_t *l = lv_obj_get_user_data(list);
    list_paint(l, l->selected, false);
    l->selected = (l->selected + delta + l->n) % l->n;
    list_paint(l, l->selected, true);
    lv_obj_scroll_to_view(l->rows[l->selected], LV_ANIM_OFF);
}

void list_open_selected(lv_obj_t *list)
{
    list_t *l = lv_obj_get_user_data(list);
    l->on_open(l->selected, l->ctx);
}
/* recipe:end */
