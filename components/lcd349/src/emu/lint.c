/* SPDX-License-Identifier: Apache-2.0
 *
 * ui.lint (requirement 6.2): checks the active screen against the design
 * rules in device.json (generated into design_rules.h). Each finding has a
 * severity, the rule id, the object's path and a fix. Objects tagged
 * "ignore:<rule-id>" are skipped for that rule; rules list their own exempt
 * tags (touch-target exempts "list-row").
 */
#include <math.h>
#include <stdarg.h>
#include <stdio.h>
#include <string.h>

#include "cJSON.h"
#include "design_rules.h"
#include "emu.h"
#include "lvgl.h"
#include "ui_common.h"

#define MAX_INTERACTIVE 256

typedef struct {
    cJSON *arr;
    int errors, warnings;
    lv_obj_t *interactive[MAX_INTERACTIVE];
    int n_interactive;
} lint_t;

static const lcd349_rule_t *rule(const char *id)
{
    for (size_t i = 0; i < LCD349_RULES_LEN; i++) {
        if (strcmp(LCD349_RULES[i].id, id) == 0) {
            return &LCD349_RULES[i];
        }
    }
    return NULL;
}

static double threshold(const lcd349_rule_t *r)
{
    return r->threshold_x100 / 100.0;
}

/* Whether obj is exempt from r: an ignore tag, or one of the rule's exempt tags. */
static bool exempt(const lv_obj_t *obj, const lcd349_rule_t *r)
{
    char tag[48];
    snprintf(tag, sizeof tag, "ignore:%s", r->id);
    if (lcd349_ui_has_tag(obj, tag)) {
        return true;
    }
    char list[96];
    snprintf(list, sizeof list, "%s", r->exempt_tags);
    for (char *save = NULL, *t = strtok_r(list, ",", &save); t; t = strtok_r(NULL, ",", &save)) {
        if (lcd349_ui_has_tag(obj, t)) {
            return true;
        }
    }
    return false;
}

static bool active(const lv_obj_t *obj, const lcd349_rule_t *r)
{
    return r && r->severity != LCD349_SEV_OFF && !exempt(obj, r);
}

__attribute__((format(printf, 4, 5))) static void finding(lint_t *L, const lcd349_rule_t *r, const lv_obj_t *obj, const char *fmt, ...)
{
    char msg[256], path[512];
    va_list ap;
    va_start(ap, fmt);
    vsnprintf(msg, sizeof msg, fmt, ap);
    va_end(ap);
    cJSON *f = cJSON_CreateObject();
    cJSON_AddStringToObject(f, "rule", r->id);
    cJSON_AddStringToObject(f, "severity", r->severity == LCD349_SEV_ERROR ? "error" : "warning");
    cJSON_AddStringToObject(f, "path", emu_ui_path(obj, path, sizeof path));
    cJSON_AddStringToObject(f, "class", emu_ui_class(obj));
    if (emu_ui_is_label(obj)) {
        cJSON_AddStringToObject(f, "text", lv_label_get_text(obj));
    }
    cJSON_AddStringToObject(f, "message", msg);
    cJSON_AddStringToObject(f, "fix", r->fix);
    cJSON_AddItemToArray(L->arr, f);
    if (r->severity == LCD349_SEV_ERROR) {
        L->errors++;
    } else {
        L->warnings++;
    }
}

/* ---- Colour: WCAG 2 contrast on RGB565-quantised colours ---- */

static double channel(double c)
{
    c /= 255.0;
    return c <= 0.04045 ? c / 12.92 : pow((c + 0.055) / 1.055, 2.4);
}

static double luminance(lv_color_t c)
{
    uint16_t q = lv_color_to_u16(c); /* what the panel can actually show */
    double r = (((q >> 11) & 0x1F) * 527 + 23) >> 6;
    double g = (((q >> 5) & 0x3F) * 259 + 33) >> 6;
    double b = ((q & 0x1F) * 527 + 23) >> 6;
    return 0.2126 * channel(r) + 0.7152 * channel(g) + 0.0722 * channel(b);
}

static double contrast(lv_color_t a, lv_color_t b)
{
    double la = luminance(a), lb = luminance(b);
    return (fmax(la, lb) + 0.05) / (fmin(la, lb) + 0.05);
}

/* ---- Helpers ---- */

static bool has_scroll_range(const lv_obj_t *o)
{
    lv_obj_t *m = (lv_obj_t *)o;
    return lv_obj_get_scroll_top(m) > 0 || lv_obj_get_scroll_bottom(m) > 0 || lv_obj_get_scroll_left(m) > 0 ||
           lv_obj_get_scroll_right(m) > 0;
}

/* A drag can scroll an object only if the press lands on it or inside it:
 * LVGL starts the scroll chain at the pressed object, which must be
 * clickable. A truncated label has a scroll range, but nothing to press. */
static bool pressable_within(const lv_obj_t *obj)
{
    if (lv_obj_is_clickable(obj)) {
        return true;
    }
    for (uint32_t i = 0; i < lv_obj_get_child_count(obj); i++) {
        if (pressable_within(lv_obj_get_child(obj, (int32_t)i))) {
            return true;
        }
    }
    return false;
}

static bool in_scrolling_ancestor(const lv_obj_t *obj)
{
    for (const lv_obj_t *p = lv_obj_get_parent(obj); p; p = lv_obj_get_parent(p)) {
        if (lv_obj_is_scrollable(p) && has_scroll_range(p)) {
            return true;
        }
    }
    return false;
}

/* LVGL 9.6 keeps its area helpers private. */
static bool area_in(const lv_area_t *in, const lv_area_t *out)
{
    return in->x1 >= out->x1 && in->y1 >= out->y1 && in->x2 <= out->x2 && in->y2 <= out->y2;
}

static bool area_intersect(lv_area_t *res, const lv_area_t *a, const lv_area_t *b)
{
    res->x1 = a->x1 > b->x1 ? a->x1 : b->x1;
    res->y1 = a->y1 > b->y1 ? a->y1 : b->y1;
    res->x2 = a->x2 < b->x2 ? a->x2 : b->x2;
    res->y2 = a->y2 < b->y2 ? a->y2 : b->y2;
    return res->x1 <= res->x2 && res->y1 <= res->y2;
}

static bool on_screen(const lv_area_t *a)
{
    return a->x1 >= 0 && a->y1 >= 0 && a->x2 < LCD349_LCD_W && a->y2 < LCD349_LCD_H;
}

static uint32_t utf8_next(const char *s, size_t *i)
{
    const unsigned char *u = (const unsigned char *)s;
    uint32_t c = u[*i];
    int extra = c < 0x80 ? 0 : (c & 0xE0) == 0xC0 ? 1 : (c & 0xF0) == 0xE0 ? 2 : (c & 0xF8) == 0xF0 ? 3 : -1;
    if (extra < 0) {
        (*i)++;
        return 0xFFFD;
    }
    c &= extra == 0 ? 0x7F : extra == 1 ? 0x1F : extra == 2 ? 0x0F : 0x07;
    (*i)++;
    for (int k = 0; k < extra; k++) {
        if ((u[*i] & 0xC0) != 0x80) {
            return 0xFFFD;
        }
        c = (c << 6) | (u[(*i)++] & 0x3F);
    }
    return c;
}

/* ---- Per-object checks ---- */

static void check_label(lint_t *L, lv_obj_t *obj)
{
    const char *txt = lv_label_get_text(obj);
    if (!txt || !*txt) {
        return;
    }
    const lv_font_t *font = lv_obj_get_style_text_font(obj, LV_PART_MAIN);
    int32_t lh = lv_font_get_line_height(font);

    const lcd349_rule_t *r = rule(LCD349_RULE_FONT_MIN);
    if (active(obj, r) && lh < threshold(r)) {
        finding(L, r, obj, "text line height %d px (%.1f mm); minimum %.0f px", (int)lh, lh / (double)LCD349_PX_PER_MM_X10 * 10,
                threshold(r));
    }

    r = rule(LCD349_RULE_CONTRAST);
    lv_color_t fg = emu_ui_text_color(obj), bg = emu_ui_bg(obj);
    double cr = contrast(fg, bg);
    if (active(obj, r) && cr < threshold(r)) {
        finding(L, r, obj, "contrast %.2f:1 (text #%02X%02X%02X on #%02X%02X%02X); minimum %.1f:1", cr, fg.red, fg.green, fg.blue, bg.red,
                bg.green, bg.blue, threshold(r));
    }

    r = rule(LCD349_RULE_CLIPPED_TEXT);
    if (active(obj, r)) {
        lv_label_long_mode_t mode = lv_label_get_long_mode(obj);
        int32_t cw = lv_obj_get_content_width(obj), ch = lv_obj_get_content_height(obj);
        lv_point_t sz;
        lv_text_get_size(&sz, txt, font, lv_obj_get_style_text_letter_space(obj, LV_PART_MAIN),
                         lv_obj_get_style_text_line_space(obj, LV_PART_MAIN), mode == LV_LABEL_LONG_MODE_WRAP ? cw : LV_COORD_MAX,
                         LV_TEXT_FLAG_NONE);
        bool clipped = (mode == LV_LABEL_LONG_MODE_WRAP && sz.y > ch) || (mode == LV_LABEL_LONG_MODE_CLIP && (sz.x > cw || sz.y > ch));
        if (clipped) {
            finding(L, r, obj, "text needs %d x %d px but the label's content area is %d x %d px", (int)sz.x, (int)sz.y, (int)cw, (int)ch);
        } else {
            lv_obj_t *p = lv_obj_get_parent(obj);
            lv_area_t a, pa;
            lv_obj_get_coords(obj, &a);
            /* A parent clips its children unless it shows overflow or can actually scroll to them. */
            if (p && !lv_obj_is_overflow_visible(p) && !(lv_obj_is_scrollable(p) && has_scroll_range(p))) {
                lv_obj_get_coords(p, &pa);
                if (!area_in(&a, &pa)) {
                    finding(L, r, obj, "the label runs outside its parent, which cuts it off");
                }
            }
        }
    }

    r = rule(LCD349_RULE_MISSING_GLYPH);
    if (active(obj, r)) {
        char missing[64] = {0};
        size_t mlen = 0;
        int count = 0;
        for (size_t i = 0; txt[i];) {
            uint32_t cp = utf8_next(txt, &i);
            if (cp == '\n' || cp == '\r') {
                continue;
            }
            lv_font_glyph_dsc_t dsc;
            if (!lv_font_get_glyph_dsc(font, &dsc, cp, 0)) {
                if (count < 6) {
                    mlen += (size_t)snprintf(missing + mlen, sizeof missing - mlen, "%sU+%04X", count ? " " : "", (unsigned)cp);
                }
                count++;
            }
        }
        if (count) {
            finding(L, r, obj, "%d character(s) aren't in font %s: %s", count, emu_ui_font_name(font), missing);
        }
    }
}

static void check_object(lv_obj_t *obj, void *ctx)
{
    lint_t *L = ctx;
    if (!lv_obj_get_parent(obj) || !emu_ui_visible(obj)) {
        return; /* the screen itself, or hidden */
    }
    lv_area_t a;
    lv_obj_get_coords(obj, &a);

    const lcd349_rule_t *r = rule(LCD349_RULE_OFF_SCREEN);
    if (active(obj, r) && lv_area_get_width(&a) > 0 && lv_area_get_height(&a) > 0 && !on_screen(&a) && !in_scrolling_ancestor(obj)) {
        lv_area_t pa;
        lv_obj_t *p = lv_obj_get_parent(obj);
        lv_obj_get_coords(p, &pa);
        if (!lv_obj_get_parent(p) || on_screen(&pa)) { /* report the outermost object only */
            finding(L, r, obj, "spans x %d..%d, y %d..%d; the screen is 0..%d, 0..%d", (int)a.x1, (int)a.x2, (int)a.y1, (int)a.y2,
                    LCD349_LCD_W - 1, LCD349_LCD_H - 1);
        }
    }

    if (emu_ui_interactive(obj)) {
        lv_area_t ca;
        lv_obj_get_click_area(obj, &ca);
        int32_t w = lv_area_get_width(&ca), h = lv_area_get_height(&ca);
        r = rule(LCD349_RULE_TOUCH_TARGET);
        if (active(obj, r) && (w < threshold(r) || h < threshold(r))) {
            finding(L, r, obj, "click area %d x %d px (%.1f x %.1f mm); minimum %.0f x %.0f px", (int)w, (int)h,
                    w * 10.0 / LCD349_PX_PER_MM_X10, h * 10.0 / LCD349_PX_PER_MM_X10, threshold(r), threshold(r));
        }
        if (L->n_interactive < MAX_INTERACTIVE) {
            L->interactive[L->n_interactive++] = obj;
        }
    }

    r = rule(LCD349_RULE_NO_MOMENTUM);
    if (active(obj, r) && lv_obj_is_scrollable(obj) && lv_obj_is_scroll_momentum(obj) &&
        has_scroll_range(obj) && pressable_within(obj)) {
        finding(L, r, obj, "this object scrolls and has LV_OBJ_FLAG_SCROLL_MOMENTUM set");
    }

    r = rule(LCD349_RULE_GRADIENT);
    if (active(obj, r) && lv_obj_get_style_bg_opa(obj, LV_PART_MAIN) > LV_OPA_TRANSP &&
        lv_obj_get_style_bg_grad_dir(obj, LV_PART_MAIN) != LV_GRAD_DIR_NONE) {
        lv_color_t c1 = lv_obj_get_style_bg_color(obj, LV_PART_MAIN), c2 = lv_obj_get_style_bg_grad_color(obj, LV_PART_MAIN);
        if (lv_color_to_u16(c1) != lv_color_to_u16(c2)) {
            finding(L, r, obj, "background gradient from #%02X%02X%02X to #%02X%02X%02X", c1.red, c1.green, c1.blue, c2.red, c2.green, c2.blue);
        }
    }

    if (emu_ui_is_label(obj)) {
        check_label(L, obj);
    }
}

static bool related(const lv_obj_t *a, const lv_obj_t *b)
{
    for (const lv_obj_t *p = a; p; p = lv_obj_get_parent(p)) {
        if (p == b) {
            return true;
        }
    }
    for (const lv_obj_t *p = b; p; p = lv_obj_get_parent(p)) {
        if (p == a) {
            return true;
        }
    }
    return false;
}

static bool on_top_layer(const lv_obj_t *obj)
{
    for (const lv_obj_t *p = obj; p; p = lv_obj_get_parent(p)) {
        if (p == lv_layer_top()) {
            return true;
        }
    }
    return false;
}

/* A modal is shown when the top layer has a visible, clickable object that
 * covers the whole screen (a backdrop, as lv_msgbox makes): taps can't reach
 * the screen below, so its objects can't clash with the modal's. */
static bool modal_shown(void)
{
    lv_obj_t *top = lv_layer_top();
    for (uint32_t i = 0; i < lv_obj_get_child_count(top); i++) {
        lv_obj_t *c = lv_obj_get_child(top, (int32_t)i);
        lv_area_t a;
        lv_obj_get_coords(c, &a);
        if (!lv_obj_is_hidden(c) && lv_obj_is_clickable(c) && a.x1 <= 0 && a.y1 <= 0 &&
            a.x2 >= LCD349_LCD_W - 1 && a.y2 >= LCD349_LCD_H - 1) {
            return true;
        }
    }
    return false;
}

static void check_overlaps(lint_t *L)
{
    const lcd349_rule_t *r = rule(LCD349_RULE_OVERLAP);
    bool modal = modal_shown();
    for (int i = 0; i < L->n_interactive; i++) {
        for (int j = i + 1; j < L->n_interactive; j++) {
            lv_obj_t *a = L->interactive[i], *b = L->interactive[j];
            if (!active(a, r) || !active(b, r) || related(a, b)) {
                continue;
            }
            if (modal && on_top_layer(a) != on_top_layer(b)) {
                continue; /* one is under the modal's backdrop */
            }
            lv_area_t aa, ba, out;
            lv_obj_get_click_area(a, &aa);
            lv_obj_get_click_area(b, &ba);
            if (area_intersect(&out, &aa, &ba)) {
                char other[512];
                finding(L, r, b, "click area overlaps %s by %d x %d px", emu_ui_path(a, other, sizeof other), (int)lv_area_get_width(&out),
                        (int)lv_area_get_height(&out));
            }
        }
    }
}

cJSON *emu_ui_lint(int *errors, int *warnings)
{
    lint_t L = {.arr = cJSON_CreateArray()};
    lv_obj_update_layout(lv_screen_active());
    lv_obj_update_layout(lv_layer_top()); /* a dialog or toast made since the last refresh */
    emu_ui_walk(lv_screen_active(), check_object, &L);
    emu_ui_walk(lv_layer_top(), check_object, &L);
    check_overlaps(&L);
    *errors = L.errors;
    *warnings = L.warnings;
    return L.arr;
}
