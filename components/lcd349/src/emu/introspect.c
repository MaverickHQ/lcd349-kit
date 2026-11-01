/* SPDX-License-Identifier: Apache-2.0
 *
 * ui.tree (requirement 6.1): the active screen as structured data, so an
 * agent can see sizes, fonts and colours instead of guessing from pixels.
 * Each object has its class, absolute coordinates, flags, text, font,
 * resolved text and background colours, tags and a stable path.
 */
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

#include "cJSON.h"
#include "emu.h"
#include "lvgl.h"
#include "lvgl_private.h"
#include "ui_common.h"

#define TREE_MAX_OBJECTS 2000

/* ---- Shared helpers ---- */

const char *emu_ui_class(const lv_obj_t *obj)
{
    const lv_obj_class_t *cls = lv_obj_get_class(obj);
    const char *n = cls && cls->name ? cls->name : "lv_obj";
    return strncmp(n, "lv_", 3) == 0 ? n + 3 : n;
}

bool emu_ui_is_label(const lv_obj_t *obj)
{
    return lv_obj_check_type(obj, &lv_label_class);
}

bool emu_ui_visible(const lv_obj_t *obj)
{
    for (const lv_obj_t *o = obj; o; o = lv_obj_get_parent(o)) {
        if (lv_obj_is_hidden(o) || lv_obj_get_style_opa(o, LV_PART_MAIN) == LV_OPA_TRANSP) {
            return false;
        }
    }
    return true;
}

bool emu_ui_interactive(const lv_obj_t *obj)
{
    if (!lv_obj_is_clickable(obj) || !emu_ui_visible(obj) || lv_obj_get_parent(obj) == NULL) {
        return false;
    }
    /* Plain containers are clickable by default in LVGL; count them only when
     * something listens. Widgets made to be pressed always count. */
    static const lv_obj_class_t *const pressable[] = {
        &lv_button_class,
#if LV_USE_CHECKBOX
        &lv_checkbox_class,
#endif
#if LV_USE_SWITCH
        &lv_switch_class,
#endif
#if LV_USE_SLIDER
        &lv_slider_class,
#endif
#if LV_USE_DROPDOWN
        &lv_dropdown_class,
#endif
#if LV_USE_ROLLER
        &lv_roller_class,
#endif
#if LV_USE_TEXTAREA
        &lv_textarea_class,
#endif
#if LV_USE_BUTTONMATRIX
        &lv_buttonmatrix_class,
#endif
    };
    for (size_t i = 0; i < sizeof pressable / sizeof pressable[0]; i++) {
        if (lv_obj_check_type(obj, pressable[i])) {
            return true;
        }
    }
    /* Or a plain object that listens for input. Other handlers (a DELETE
     * handler that frees memory, a DRAW handler) don't make it pressable. */
    static const lv_event_code_t input[] = {
        LV_EVENT_ALL,           LV_EVENT_PRESSED,           LV_EVENT_PRESSING,      LV_EVENT_PRESS_LOST,
        LV_EVENT_SHORT_CLICKED, LV_EVENT_LONG_PRESSED,      LV_EVENT_LONG_PRESSED_REPEAT,
        LV_EVENT_CLICKED,       LV_EVENT_RELEASED,          LV_EVENT_GESTURE,
    };
    uint32_t n = lv_obj_get_event_count((lv_obj_t *)obj);
    for (uint32_t i = 0; i < n; i++) {
        const lv_event_dsc_t *d = lv_obj_get_event_dsc((lv_obj_t *)obj, i);
        lv_event_code_t code = (lv_event_code_t)(d->filter & ~LV_EVENT_PREPROCESS);
        for (size_t j = 0; j < sizeof input / sizeof input[0]; j++) {
            if (code == input[j]) {
                return true;
            }
        }
    }
    return false;
}

static lv_color_t mix(lv_color_t fg, lv_color_t bg, lv_opa_t opa)
{
    return lv_color_mix(fg, bg, opa);
}

lv_color_t emu_ui_bg(const lv_obj_t *obj)
{
    /* Composite every ancestor's background, screen first, over black (an unlit panel). */
    const lv_obj_t *chain[64];
    int n = 0;
    for (const lv_obj_t *o = obj; o && n < 64; o = lv_obj_get_parent(o)) {
        chain[n++] = o;
    }
    lv_color_t c = lv_color_black();
    for (int i = n - 1; i >= 0; i--) {
        lv_opa_t opa = lv_obj_get_style_bg_opa(chain[i], LV_PART_MAIN);
        if (opa > LV_OPA_TRANSP) {
            c = mix(lv_obj_get_style_bg_color(chain[i], LV_PART_MAIN), c, opa);
        }
    }
    return c;
}

lv_color_t emu_ui_text_color(const lv_obj_t *obj)
{
    lv_opa_t opa = lv_obj_get_style_text_opa(obj, LV_PART_MAIN);
    return mix(lv_obj_get_style_text_color(obj, LV_PART_MAIN), emu_ui_bg(obj), opa);
}

#define FONT_ENTRY(n) {&lv_font_montserrat_##n, "montserrat_" #n},
static const struct {
    const lv_font_t *font;
    const char *name;
} s_fonts[] = {
#if LV_FONT_MONTSERRAT_8
    FONT_ENTRY(8)
#endif
#if LV_FONT_MONTSERRAT_10
    FONT_ENTRY(10)
#endif
#if LV_FONT_MONTSERRAT_12
    FONT_ENTRY(12)
#endif
#if LV_FONT_MONTSERRAT_14
    FONT_ENTRY(14)
#endif
#if LV_FONT_MONTSERRAT_16
    FONT_ENTRY(16)
#endif
#if LV_FONT_MONTSERRAT_18
    FONT_ENTRY(18)
#endif
#if LV_FONT_MONTSERRAT_20
    FONT_ENTRY(20)
#endif
#if LV_FONT_MONTSERRAT_22
    FONT_ENTRY(22)
#endif
#if LV_FONT_MONTSERRAT_24
    FONT_ENTRY(24)
#endif
#if LV_FONT_MONTSERRAT_26
    FONT_ENTRY(26)
#endif
#if LV_FONT_MONTSERRAT_28
    FONT_ENTRY(28)
#endif
#if LV_FONT_MONTSERRAT_30
    FONT_ENTRY(30)
#endif
#if LV_FONT_MONTSERRAT_32
    FONT_ENTRY(32)
#endif
#if LV_FONT_MONTSERRAT_34
    FONT_ENTRY(34)
#endif
#if LV_FONT_MONTSERRAT_36
    FONT_ENTRY(36)
#endif
#if LV_FONT_MONTSERRAT_38
    FONT_ENTRY(38)
#endif
#if LV_FONT_MONTSERRAT_40
    FONT_ENTRY(40)
#endif
#if LV_FONT_MONTSERRAT_42
    FONT_ENTRY(42)
#endif
#if LV_FONT_MONTSERRAT_44
    FONT_ENTRY(44)
#endif
#if LV_FONT_MONTSERRAT_46
    FONT_ENTRY(46)
#endif
#if LV_FONT_MONTSERRAT_48
    FONT_ENTRY(48)
#endif
    {NULL, NULL},
};

const char *emu_ui_font_name(const lv_font_t *font)
{
    for (size_t i = 0; s_fonts[i].font; i++) {
        if (s_fonts[i].font == font) {
            return s_fonts[i].name;
        }
    }
    return "custom";
}

static const lv_obj_t *root_of(const lv_obj_t *obj)
{
    while (lv_obj_get_parent(obj)) {
        obj = lv_obj_get_parent(obj);
    }
    return obj;
}

char *emu_ui_path(const lv_obj_t *obj, char *out, size_t n)
{
    const lv_obj_t *chain[64];
    int depth = 0;
    for (const lv_obj_t *o = obj; lv_obj_get_parent(o) && depth < 64; o = lv_obj_get_parent(o)) {
        chain[depth++] = o;
    }
    const lv_obj_t *root = root_of(obj);
    size_t len = (size_t)snprintf(out, n, "%s", root == lv_layer_top() ? "top" : root == lv_layer_sys() ? "sys" : "screen");
    for (int i = depth - 1; i >= 0 && len < n; i--) {
        len += (size_t)snprintf(out + len, n - len, "/%d:%s", (int)lv_obj_get_index(chain[i]), emu_ui_class(chain[i]));
    }
    return out;
}

void emu_ui_walk(lv_obj_t *obj, void (*fn)(lv_obj_t *o, void *ctx), void *ctx)
{
    fn(obj, ctx);
    uint32_t n = lv_obj_get_child_count(obj);
    for (uint32_t i = 0; i < n; i++) {
        emu_ui_walk(lv_obj_get_child(obj, (int32_t)i), fn, ctx);
    }
}

/* ---- ui.tree ---- */

static void add_color(cJSON *o, const char *name, lv_color_t c)
{
    char hex[8];
    snprintf(hex, sizeof hex, "#%02X%02X%02X", c.red, c.green, c.blue);
    cJSON_AddStringToObject(o, name, hex);
}

static cJSON *node(lv_obj_t *obj, int depth, int *count, bool *truncated)
{
    if (*count >= TREE_MAX_OBJECTS) {
        *truncated = true;
        return NULL;
    }
    (*count)++;
    char path[512];
    lv_area_t a;
    lv_obj_get_coords(obj, &a);
    cJSON *o = cJSON_CreateObject();
    cJSON_AddStringToObject(o, "path", emu_ui_path(obj, path, sizeof path));
    cJSON_AddStringToObject(o, "class", emu_ui_class(obj));
    cJSON_AddNumberToObject(o, "x", a.x1);
    cJSON_AddNumberToObject(o, "y", a.y1);
    cJSON_AddNumberToObject(o, "w", lv_area_get_width(&a));
    cJSON_AddNumberToObject(o, "h", lv_area_get_height(&a));
    cJSON_AddBoolToObject(o, "visible", emu_ui_visible(obj));
    cJSON_AddBoolToObject(o, "clickable", lv_obj_is_clickable(obj));
    cJSON_AddBoolToObject(o, "interactive", emu_ui_interactive(obj));
    cJSON_AddBoolToObject(o, "scrollable", lv_obj_is_scrollable(obj));
    if (lv_obj_is_checked(obj)) {
        cJSON_AddBoolToObject(o, "checked", true);
    }
    if (lv_obj_is_disabled(obj)) {
        cJSON_AddBoolToObject(o, "disabled", true);
    }
    if (emu_ui_is_label(obj)) {
        const char *txt = lv_label_get_text(obj);
        cJSON_AddStringToObject(o, "text", txt ? txt : "");
        const lv_font_t *f = lv_obj_get_style_text_font(obj, LV_PART_MAIN);
        cJSON *font = cJSON_AddObjectToObject(o, "font");
        cJSON_AddStringToObject(font, "name", emu_ui_font_name(f));
        cJSON_AddNumberToObject(font, "line_height", lv_font_get_line_height(f));
        add_color(o, "text_color", emu_ui_text_color(obj));
    }
    add_color(o, "bg_color", emu_ui_bg(obj));
    char tags[64];
    if (lcd349_ui_tags(obj, tags, sizeof tags)) {
        cJSON *t = cJSON_AddArrayToObject(o, "tags");
        for (char *s = strtok(tags, ","); s; s = strtok(NULL, ",")) {
            cJSON_AddItemToArray(t, cJSON_CreateString(s));
        }
    }
    uint32_t n = lv_obj_get_child_count(obj);
    if (n && depth > 1) {
        cJSON *kids = cJSON_AddArrayToObject(o, "children");
        for (uint32_t i = 0; i < n; i++) {
            cJSON *k = node(lv_obj_get_child(obj, (int32_t)i), depth - 1, count, truncated);
            if (!k) {
                break;
            }
            cJSON_AddItemToArray(kids, k);
        }
    } else if (n) {
        cJSON_AddNumberToObject(o, "children_not_shown", n);
    }
    return o;
}

/* Resolves "screen/0:obj/2:button" (class names optional) to an object. An
 * empty path, or one of slashes only, is the active screen; a segment is a
 * child index in digits, then optionally ':' and its class. */
static lv_obj_t *resolve(const char *path)
{
    if (!path || !*path || !strcmp(path, "screen")) {
        return lv_screen_active();
    }
    char buf[512];
    snprintf(buf, sizeof buf, "%s", path);
    char *save = NULL;
    char *seg = strtok_r(buf, "/", &save);
    if (!seg) {
        return lv_screen_active();
    }
    lv_obj_t *o = !strcmp(seg, "screen") ? lv_screen_active() : !strcmp(seg, "top") ? lv_layer_top() : NULL;
    while (o && (seg = strtok_r(NULL, "/", &save))) {
        if (*seg < '0' || *seg > '9') {
            return NULL;
        }
        char *end = NULL;
        long idx = strtol(seg, &end, 10);
        if ((*end != '\0' && *end != ':') || idx < 0 || (uint32_t)idx >= lv_obj_get_child_count(o)) {
            return NULL;
        }
        o = lv_obj_get_child(o, (int32_t)idx);
        if (*end == ':' && strcmp(end + 1, emu_ui_class(o)) != 0) {
            return NULL;
        }
    }
    return o;
}

cJSON *emu_ui_tree(const char *path, int depth, int *count, bool *truncated)
{
    lv_obj_update_layout(lv_screen_active());
    lv_obj_update_layout(lv_layer_top());
    lv_obj_t *root = resolve(path);
    if (!root) {
        return NULL;
    }
    cJSON *t = node(root, depth, count, truncated);
    /* Pop-ups usually live on the top layer: include it when it has content. */
    if (t && (!path || !strcmp(path, "screen")) && lv_obj_get_child_count(lv_layer_top())) {
        cJSON *top = node(lv_layer_top(), depth, count, truncated);
        if (top) {
            cJSON_AddItemToObject(t, "top_layer", top);
        }
    }
    return t;
}

/* ---- Text search for wait ---- */

typedef struct {
    const char *needle;
    char *found;
    size_t found_len;
    bool hit;
} find_ctx_t;

static void find_cb(lv_obj_t *o, void *ctx)
{
    find_ctx_t *f = ctx;
    if (f->hit || !emu_ui_is_label(o) || !emu_ui_visible(o)) {
        return;
    }
    const char *txt = lv_label_get_text(o);
    if (txt && strstr(txt, f->needle)) {
        snprintf(f->found, f->found_len, "%s", txt);
        f->hit = true;
    }
}

bool emu_ui_draw_pending(void)
{
    lv_display_t *d = lv_display_get_default();
    return d && d->inv_p > 0;
}

bool emu_ui_find_text(const char *needle, char *found, size_t found_len)
{
    if (!lv_display_get_default()) {
        return false;
    }
    find_ctx_t f = {needle, found, found_len, false};
    emu_ui_walk(lv_screen_active(), find_cb, &f);
    if (!f.hit) {
        emu_ui_walk(lv_layer_top(), find_cb, &f);
    }
    return f.hit;
}
