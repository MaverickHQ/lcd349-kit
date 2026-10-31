/* SPDX-License-Identifier: Apache-2.0 */
#include "core.h"

#include <math.h>
#include <stdlib.h>
#include <string.h>

#define LW VIEW_LCD_W
#define LH VIEW_LCD_H
#define PW VIEW_PANEL_W

_Static_assert(VIEW_PANEL_W == VIEW_LCD_H && VIEW_PANEL_H == VIEW_LCD_W, "panel is the landscape frame rotated");

/* ---- fb.bin ---- */

static uint32_t rd32(const uint8_t *p)
{
    uint32_t v;
    memcpy(&v, p, 4);
    return v;
}

static uint16_t rd16(const uint8_t *p)
{
    uint16_t v;
    memcpy(&v, p, 2);
    return v;
}

bool view_hdr_parse(const uint8_t *h, view_hdr_t *o)
{
    if (memcmp(h, VIEW_FB_MAGIC, 8) != 0 || rd16(h + VIEW_OFF_WIDTH) != VIEW_PANEL_W || rd16(h + VIEW_OFF_HEIGHT) != VIEW_PANEL_H) {
        return false;
    }
    o->version = rd32(h + VIEW_OFF_VERSION);
    if (o->version != 1) {
        return false;
    }
    o->seq = rd32(h + VIEW_OFF_SEQ);
    o->frames = rd32(h + VIEW_OFF_FRAMES);
    o->pid = rd32(h + VIEW_OFF_PID);
    o->status_seq = rd32(h + VIEW_OFF_STATUS_SEQ);
    o->backlight = h[VIEW_OFF_BACKLIGHT];
    o->power = h[VIEW_OFF_POWER];
    o->asleep = h[VIEW_OFF_ASLEEP];
    o->on_battery = h[VIEW_OFF_ON_BATTERY];
    o->battery_mv = rd16(h + VIEW_OFF_BATTERY_MV);
    o->battery_pct = h[VIEW_OFF_BATTERY_PCT];
    o->wifi = h[VIEW_OFF_WIFI];
    uint8_t rot = h[VIEW_OFF_ROTATION], swap = h[VIEW_OFF_BYTE_SWAP];
    o->cw = rot == 1 ? true : rot == 2 ? false : VIEW_ROTATION_CW90;
    o->swap = swap == 1 ? true : swap == 2 ? false : VIEW_BYTE_SWAP;
    return true;
}

void view_panel_to_landscape(const uint16_t *panel, uint16_t *land, bool cw, bool swap)
{
    /* The kit puts landscape (x, y) on panel row x (cw) or LW-1-x (ccw), at
     * column LH-1-y (cw) or y (ccw). Reads run along panel rows. */
    for (int x = 0; x < LW; x++) {
        const uint16_t *row = panel + (cw ? x : LW - 1 - x) * PW;
        for (int y = 0; y < LH; y++) {
            uint16_t c = row[cw ? LH - 1 - y : y];
            land[y * LW + x] = swap ? (uint16_t)((c >> 8) | (c << 8)) : c;
        }
    }
}

/* ---- JSON ---- */

#define JSON_MAX_DEPTH 32

typedef struct {
    const char *s;
    size_t len, pos;
    jtok_t *t;
    int n, max;
} jp_t;

static void skip_ws(jp_t *p)
{
    while (p->pos < p->len && (p->s[p->pos] == ' ' || p->s[p->pos] == '\t' || p->s[p->pos] == '\n' || p->s[p->pos] == '\r')) {
        p->pos++;
    }
}

static int new_tok(jp_t *p, jtype_t type)
{
    if (p->n >= p->max) {
        return -1;
    }
    p->t[p->n] = (jtok_t){type, (int)p->pos, (int)p->pos, 0, 0};
    return p->n++;
}

static bool is_digit(char c)
{
    return c >= '0' && c <= '9';
}

static bool is_hex(char c)
{
    return is_digit(c) || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F');
}

/* At the opening quote: records the contents' range and moves past the closing quote. */
static bool parse_string(jp_t *p, int k)
{
    p->pos++;
    p->t[k].start = (int)p->pos;
    while (p->pos < p->len) {
        unsigned char c = (unsigned char)p->s[p->pos];
        if (c == '"') {
            p->t[k].end = (int)p->pos++;
            p->t[k].next = k + 1;
            return true;
        }
        if (c < 0x20) {
            return false;
        }
        if (c == '\\') {
            if (++p->pos >= p->len) {
                return false;
            }
            char e = p->s[p->pos];
            if (e == 'u') {
                for (int i = 1; i <= 4; i++) {
                    if (p->pos + i >= p->len || !is_hex(p->s[p->pos + i])) {
                        return false;
                    }
                }
                p->pos += 4;
            } else if (!strchr("\"\\/bfnrt", e)) {
                return false;
            }
        }
        p->pos++;
    }
    return false;
}

static bool parse_number(jp_t *p)
{
    const char *s = p->s;
    size_t i = p->pos, n = p->len;
    if (i < n && s[i] == '-') {
        i++;
    }
    if (i < n && s[i] == '0') {
        i++;
    } else if (i < n && s[i] >= '1' && s[i] <= '9') {
        while (i < n && is_digit(s[i])) {
            i++;
        }
    } else {
        return false;
    }
    if (i < n && s[i] == '.') {
        if (++i >= n || !is_digit(s[i])) {
            return false;
        }
        while (i < n && is_digit(s[i])) {
            i++;
        }
    }
    if (i < n && (s[i] == 'e' || s[i] == 'E')) {
        if (++i < n && (s[i] == '+' || s[i] == '-')) {
            i++;
        }
        if (i >= n || !is_digit(s[i])) {
            return false;
        }
        while (i < n && is_digit(s[i])) {
            i++;
        }
    }
    p->pos = i;
    return true;
}

static bool parse_value(jp_t *p, int depth);

static bool parse_container(jp_t *p, int depth, bool obj)
{
    int k = new_tok(p, obj ? JT_OBJ : JT_ARR);
    if (k < 0) {
        return false;
    }
    char close = obj ? '}' : ']';
    p->pos++;
    skip_ws(p);
    if (p->pos < p->len && p->s[p->pos] == close) {
        p->pos++;
    } else {
        for (;;) {
            skip_ws(p);
            if (obj) {
                if (p->pos >= p->len || p->s[p->pos] != '"') {
                    return false;
                }
                int key = new_tok(p, JT_STR);
                if (key < 0 || !parse_string(p, key)) {
                    return false;
                }
                skip_ws(p);
                if (p->pos >= p->len || p->s[p->pos] != ':') {
                    return false;
                }
                p->pos++;
            }
            if (!parse_value(p, depth + 1)) {
                return false;
            }
            p->t[k].size++;
            skip_ws(p);
            if (p->pos < p->len && p->s[p->pos] == ',') {
                p->pos++;
                continue;
            }
            if (p->pos < p->len && p->s[p->pos] == close) {
                p->pos++;
                break;
            }
            return false;
        }
    }
    p->t[k].end = (int)p->pos;
    p->t[k].next = p->n;
    return true;
}

static bool parse_literal(jp_t *p, const char *word, jtype_t type)
{
    size_t n = strlen(word);
    if (p->len - p->pos < n || memcmp(p->s + p->pos, word, n) != 0) {
        return false;
    }
    int k = new_tok(p, type);
    if (k < 0) {
        return false;
    }
    p->pos += n;
    p->t[k].end = (int)p->pos;
    p->t[k].next = k + 1;
    return true;
}

static bool parse_value(jp_t *p, int depth)
{
    if (depth > JSON_MAX_DEPTH) {
        return false;
    }
    skip_ws(p);
    if (p->pos >= p->len) {
        return false;
    }
    char c = p->s[p->pos];
    switch (c) {
    case '{':
        return parse_container(p, depth, true);
    case '[':
        return parse_container(p, depth, false);
    case '"': {
        int k = new_tok(p, JT_STR);
        return k >= 0 && parse_string(p, k);
    }
    case 't':
        return parse_literal(p, "true", JT_TRUE);
    case 'f':
        return parse_literal(p, "false", JT_FALSE);
    case 'n':
        return parse_literal(p, "null", JT_NULL);
    default: {
        int k = new_tok(p, JT_NUM);
        if (k < 0 || !parse_number(p)) {
            return false;
        }
        p->t[k].end = (int)p->pos;
        p->t[k].next = k + 1;
        return true;
    }
    }
}

int json_parse(const char *s, size_t len, jtok_t *t, int max)
{
    jp_t p = {s, len, 0, t, 0, max};
    if (!parse_value(&p, 0)) {
        return -1;
    }
    skip_ws(&p);
    return p.pos == len ? p.n : -1;
}

int json_get(const char *s, const jtok_t *t, int obj, const char *key)
{
    if (obj < 0 || t[obj].type != JT_OBJ) {
        return -1;
    }
    size_t kl = strlen(key);
    int k = obj + 1;
    for (int i = 0; i < t[obj].size; i++) {
        int v = k + 1;
        if ((size_t)(t[k].end - t[k].start) == kl && memcmp(s + t[k].start, key, kl) == 0) {
            return v;
        }
        k = t[v].next;
    }
    return -1;
}

int json_item(const jtok_t *t, int arr, int i)
{
    if (arr < 0 || t[arr].type != JT_ARR || i < 0 || i >= t[arr].size) {
        return -1;
    }
    int k = arr + 1;
    while (i-- > 0) {
        k = t[k].next;
    }
    return k;
}

double json_num(const char *s, const jtok_t *t, int i, double def)
{
    if (i < 0 || t[i].type != JT_NUM) {
        return def;
    }
    char buf[64];
    int n = t[i].end - t[i].start;
    if (n <= 0 || n >= (int)sizeof buf) {
        return def;
    }
    memcpy(buf, s + t[i].start, (size_t)n);
    buf[n] = '\0';
    return strtod(buf, NULL);
}

bool json_true(const jtok_t *t, int i)
{
    return i >= 0 && t[i].type == JT_TRUE;
}

static int hexval(char c)
{
    return is_digit(c) ? c - '0' : (c | 0x20) - 'a' + 10;
}

static size_t put_utf8(char *out, size_t room, uint32_t cp)
{
    char b[4];
    size_t n;
    if (cp < 0x80) {
        b[0] = (char)cp;
        n = 1;
    } else if (cp < 0x800) {
        b[0] = (char)(0xC0 | (cp >> 6));
        b[1] = (char)(0x80 | (cp & 0x3F));
        n = 2;
    } else if (cp < 0x10000) {
        b[0] = (char)(0xE0 | (cp >> 12));
        b[1] = (char)(0x80 | ((cp >> 6) & 0x3F));
        b[2] = (char)(0x80 | (cp & 0x3F));
        n = 3;
    } else {
        b[0] = (char)(0xF0 | (cp >> 18));
        b[1] = (char)(0x80 | ((cp >> 12) & 0x3F));
        b[2] = (char)(0x80 | ((cp >> 6) & 0x3F));
        b[3] = (char)(0x80 | (cp & 0x3F));
        n = 4;
    }
    if (n > room) {
        return 0;
    }
    memcpy(out, b, n);
    return n;
}

bool json_str(const char *s, const jtok_t *t, int i, char *out, size_t cap)
{
    if (cap == 0) {
        return false;
    }
    out[0] = '\0';
    if (i < 0 || t[i].type != JT_STR) {
        return false;
    }
    size_t o = 0;
    for (int p = t[i].start; p < t[i].end && o + 1 < cap;) {
        char c = s[p++];
        if (c != '\\') {
            out[o++] = c;
            continue;
        }
        char e = s[p++];
        uint32_t cp;
        switch (e) {
        case 'b': cp = '\b'; break;
        case 'f': cp = '\f'; break;
        case 'n': cp = '\n'; break;
        case 'r': cp = '\r'; break;
        case 't': cp = '\t'; break;
        case 'u':
            cp = (uint32_t)(hexval(s[p]) << 12 | hexval(s[p + 1]) << 8 | hexval(s[p + 2]) << 4 | hexval(s[p + 3]));
            p += 4;
            if (cp >= 0xD800 && cp < 0xDC00 && p + 6 <= t[i].end && s[p] == '\\' && s[p + 1] == 'u') {
                uint32_t lo = (uint32_t)(hexval(s[p + 2]) << 12 | hexval(s[p + 3]) << 8 | hexval(s[p + 4]) << 4 | hexval(s[p + 5]));
                if (lo >= 0xDC00 && lo < 0xE000) {
                    cp = 0x10000 + ((cp - 0xD800) << 10) + (lo - 0xDC00);
                    p += 6;
                }
            }
            if (cp >= 0xD800 && cp < 0xE000) {
                cp = '?'; /* an unpaired surrogate */
            }
            break;
        default: cp = (uint32_t)(unsigned char)e; break; /* \" \\ \/ */
        }
        size_t n = put_utf8(out + o, cap - 1 - o, cp);
        if (n == 0) {
            break;
        }
        o += n;
    }
    out[o] = '\0';
    return true;
}

/* ---- Layout ---- */

#define CAPTION_H 7.0f /* text at 1 point per dot */
#define HINT_H 14.0f   /* text at 2 points per dot */

void view_layout(view_layout_t *l, float scale)
{
    memset(l, 0, sizeof *l);
    l->scale = scale;
    const float mm = VIEW_PX_PER_MM * scale; /* points per millimetre on the device */
    const float m = VIEW_MARGIN;
    float case_w = VIEW_CASE_MM_W * mm, case_h = VIEW_CASE_MM_H * mm;
    float keys_w = VIEW_N_BUTTONS * VIEW_KEY_W + (VIEW_N_BUTTONS - 1) * m;
    float content_w = fmaxf(case_w, fmaxf(keys_w, VIEW_MIN_W - 2 * m));
    l->win_w = ceilf(content_w + 2 * m);

    float y = m;
    l->badges = (view_rect_t){m, y, content_w, VIEW_BADGE_H};
    y += VIEW_BADGE_H + m;
    l->case_r = (view_rect_t){(l->win_w - case_w) / 2, y, case_w, case_h};
    l->screen = (view_rect_t){l->case_r.x + VIEW_SCREEN_OFFSET_MM_X * mm, y + VIEW_SCREEN_OFFSET_MM_Y * mm, VIEW_LCD_W * scale,
                              VIEW_LCD_H * scale};
    y += case_h + m;

    float kx = (l->win_w - keys_w) / 2;
    for (int i = 0; i < VIEW_N_BUTTONS; i++) {
        l->keys[i] = (view_rect_t){kx + i * (VIEW_KEY_W + m), y, VIEW_KEY_W, VIEW_KEY_H};
        if (VIEW_BUTTONS[i].has_pos) {
            float w = 7.0f * mm, h = 3.5f * mm; /* a cap drawn at the measured centre */
            l->on_case[i] = (view_rect_t){l->screen.x + VIEW_BUTTONS[i].x_mm * mm - w / 2, l->screen.y + VIEW_BUTTONS[i].y_mm * mm - h / 2, w, h};
        }
    }
    y += VIEW_KEY_H + 6;
    l->caption = (view_rect_t){m, y, content_w, CAPTION_H};
    y += CAPTION_H + 8;
    l->hint = (view_rect_t){m, y, content_w, HINT_H};
    y += HINT_H + m;
    l->win_h = ceilf(y);
}

static bool in_rect(const view_rect_t *r, float x, float y)
{
    return r->w > 0 && x >= r->x && x < r->x + r->w && y >= r->y && y < r->y + r->h;
}

int view_hit_button(const view_layout_t *l, float x, float y)
{
    for (int i = 0; i < VIEW_N_BUTTONS; i++) {
        if (in_rect(&l->keys[i], x, y) || in_rect(&l->on_case[i], x, y)) {
            return i;
        }
    }
    return -1;
}

bool view_to_device(const view_layout_t *l, float x, float y, int *dx, int *dy)
{
    int px = (int)floorf((x - l->screen.x) / l->scale);
    int py = (int)floorf((y - l->screen.y) / l->scale);
    bool inside = px >= 0 && px < VIEW_LCD_W && py >= 0 && py < VIEW_LCD_H;
    *dx = px < 0 ? 0 : px >= VIEW_LCD_W ? VIEW_LCD_W - 1 : px;
    *dy = py < 0 ? 0 : py >= VIEW_LCD_H ? VIEW_LCD_H - 1 : py;
    return inside;
}

/* ---- Physical scale ---- */

float view_scale_from_mm(float display_points_w, float display_mm_w)
{
    if (display_points_w <= 0 || display_mm_w <= 0) {
        return 0;
    }
    return display_points_w / display_mm_w / VIEW_PX_PER_MM;
}

float view_scale_from_ppi(float host_ppi, float pixels_per_point)
{
    if (host_ppi <= 0 || pixels_per_point <= 0) {
        return 0;
    }
    return host_ppi / 25.4f / pixels_per_point / VIEW_PX_PER_MM;
}

bool view_dpi_is_nominal(float dpi)
{
    static const float base[] = {96.0f, 72.0f};
    static const float factor[] = {1.0f, 1.25f, 1.5f, 1.75f, 2.0f, 2.25f, 2.5f, 3.0f, 4.0f};
    for (size_t b = 0; b < sizeof base / sizeof base[0]; b++) {
        for (size_t f = 0; f < sizeof factor / sizeof factor[0]; f++) {
            if (fabsf(dpi - base[b] * factor[f]) < 0.05f) {
                return true;
            }
        }
    }
    return false;
}

/* ---- Touch queue ---- */

static void touch_append(view_touchq_t *t, uint16_t x, uint16_t y, bool down, bool start)
{
    if (t->n < VIEW_TOUCHQ_MAX) {
        t->q[t->n++] = (view_sample_t){x, y, down, start};
    }
}

void view_touch_push(view_touchq_t *t, int x, int y, bool down)
{
    uint16_t ux = (uint16_t)(x < 0 ? 0 : x >= VIEW_LCD_W ? VIEW_LCD_W - 1 : x);
    uint16_t uy = (uint16_t)(y < 0 ? 0 : y >= VIEW_LCD_H ? VIEW_LCD_H - 1 : y);
    if (!down) {
        if (!t->down) {
            return; /* no touch to end */
        }
        t->down = false;
        if (t->dropping) {
            t->dropping = false;
            return;
        }
        touch_append(t, ux, uy, false, false); /* a press reserved room for this */
        return;
    }
    if (!t->down) {
        t->down = true;
        /* Each touch needs at most three slots (press, newest move, release):
         * drop a whole touch rather than leave a press without its release. */
        t->dropping = t->n > VIEW_TOUCHQ_MAX - 3;
        if (!t->dropping) {
            touch_append(t, ux, uy, true, true);
        }
    } else if (t->dropping || (ux == t->last_x && uy == t->last_y)) {
        return;
    } else if (t->n > 0 && t->q[t->n - 1].down && !t->q[t->n - 1].start) {
        t->q[t->n - 1].x = ux; /* coalesce with the waiting move */
        t->q[t->n - 1].y = uy;
    } else {
        touch_append(t, ux, uy, true, false);
    }
    t->last_x = ux;
    t->last_y = uy;
}

int view_touch_take(view_touchq_t *t, view_sample_t *out, int max)
{
    int n = t->n < max ? t->n : max;
    memcpy(out, t->q, (size_t)n * sizeof *out);
    memmove(t->q, t->q + n, (size_t)(t->n - n) * sizeof *out);
    t->n -= n;
    return n;
}

void view_touch_clear(view_touchq_t *t)
{
    memset(t, 0, sizeof *t);
}

/* ---- Font: 5x7, drawn for this viewer ---- */

#define G(a, b, c, d, e, f, g) {a, b, c, d, e, f, g}

static const char *const FONT[64][7] = {
    [' ' - 32] = G(".....", ".....", ".....", ".....", ".....", ".....", "....."),
    ['!' - 32] = G("..#..", "..#..", "..#..", "..#..", "..#..", ".....", "..#.."),
    ['"' - 32] = G(".#.#.", ".#.#.", ".....", ".....", ".....", ".....", "....."),
    ['#' - 32] = G(".#.#.", ".#.#.", "#####", ".#.#.", "#####", ".#.#.", ".#.#."),
    ['%' - 32] = G("##...", "##..#", "...#.", "..#..", ".#...", "#..##", "...##"),
    ['\'' - 32] = G("..#..", "..#..", ".....", ".....", ".....", ".....", "....."),
    ['(' - 32] = G("...#.", "..#..", ".#...", ".#...", ".#...", "..#..", "...#."),
    [')' - 32] = G(".#...", "..#..", "...#.", "...#.", "...#.", "..#..", ".#..."),
    ['*' - 32] = G(".....", "..#..", "#.#.#", ".###.", "#.#.#", "..#..", "....."),
    ['+' - 32] = G(".....", "..#..", "..#..", "#####", "..#..", "..#..", "....."),
    [',' - 32] = G(".....", ".....", ".....", ".....", ".##..", "..#..", ".#..."),
    ['-' - 32] = G(".....", ".....", ".....", "#####", ".....", ".....", "....."),
    ['.' - 32] = G(".....", ".....", ".....", ".....", ".....", ".##..", ".##.."),
    ['/' - 32] = G(".....", "....#", "...#.", "..#..", ".#...", "#....", "....."),
    ['0' - 32] = G(".###.", "#...#", "#..##", "#.#.#", "##..#", "#...#", ".###."),
    ['1' - 32] = G("..#..", ".##..", "..#..", "..#..", "..#..", "..#..", ".###."),
    ['2' - 32] = G(".###.", "#...#", "....#", "...#.", "..#..", ".#...", "#####"),
    ['3' - 32] = G("#####", "...#.", "..#..", "...#.", "....#", "#...#", ".###."),
    ['4' - 32] = G("...#.", "..##.", ".#.#.", "#..#.", "#####", "...#.", "...#."),
    ['5' - 32] = G("#####", "#....", "####.", "....#", "....#", "#...#", ".###."),
    ['6' - 32] = G("..##.", ".#...", "#....", "####.", "#...#", "#...#", ".###."),
    ['7' - 32] = G("#####", "....#", "...#.", "..#..", ".#...", ".#...", ".#..."),
    ['8' - 32] = G(".###.", "#...#", "#...#", ".###.", "#...#", "#...#", ".###."),
    ['9' - 32] = G(".###.", "#...#", "#...#", ".####", "....#", "...#.", ".##.."),
    [':' - 32] = G(".....", ".##..", ".##..", ".....", ".##..", ".##..", "....."),
    [';' - 32] = G(".....", ".##..", ".##..", ".....", ".##..", "..#..", ".#..."),
    ['<' - 32] = G("...#.", "..#..", ".#...", "#....", ".#...", "..#..", "...#."),
    ['=' - 32] = G(".....", ".....", "#####", ".....", "#####", ".....", "....."),
    ['>' - 32] = G(".#...", "..#..", "...#.", "....#", "...#.", "..#..", ".#..."),
    ['?' - 32] = G(".###.", "#...#", "....#", "...#.", "..#..", ".....", "..#.."),
    ['@' - 32] = G(".###.", "#...#", "....#", ".##.#", "#.#.#", "#.#.#", ".###."),
    ['A' - 32] = G(".###.", "#...#", "#...#", "#####", "#...#", "#...#", "#...#"),
    ['B' - 32] = G("####.", "#...#", "#...#", "####.", "#...#", "#...#", "####."),
    ['C' - 32] = G(".###.", "#...#", "#....", "#....", "#....", "#...#", ".###."),
    ['D' - 32] = G("###..", "#..#.", "#...#", "#...#", "#...#", "#..#.", "###.."),
    ['E' - 32] = G("#####", "#....", "#....", "####.", "#....", "#....", "#####"),
    ['F' - 32] = G("#####", "#....", "#....", "####.", "#....", "#....", "#...."),
    ['G' - 32] = G(".###.", "#...#", "#....", "#.###", "#...#", "#...#", ".####"),
    ['H' - 32] = G("#...#", "#...#", "#...#", "#####", "#...#", "#...#", "#...#"),
    ['I' - 32] = G(".###.", "..#..", "..#..", "..#..", "..#..", "..#..", ".###."),
    ['J' - 32] = G("..###", "...#.", "...#.", "...#.", "...#.", "#..#.", ".##.."),
    ['K' - 32] = G("#...#", "#..#.", "#.#..", "##...", "#.#..", "#..#.", "#...#"),
    ['L' - 32] = G("#....", "#....", "#....", "#....", "#....", "#....", "#####"),
    ['M' - 32] = G("#...#", "##.##", "#.#.#", "#.#.#", "#...#", "#...#", "#...#"),
    ['N' - 32] = G("#...#", "#...#", "##..#", "#.#.#", "#..##", "#...#", "#...#"),
    ['O' - 32] = G(".###.", "#...#", "#...#", "#...#", "#...#", "#...#", ".###."),
    ['P' - 32] = G("####.", "#...#", "#...#", "####.", "#....", "#....", "#...."),
    ['Q' - 32] = G(".###.", "#...#", "#...#", "#...#", "#.#.#", "#..#.", ".##.#"),
    ['R' - 32] = G("####.", "#...#", "#...#", "####.", "#.#..", "#..#.", "#...#"),
    ['S' - 32] = G(".####", "#....", "#....", ".###.", "....#", "....#", "####."),
    ['T' - 32] = G("#####", "..#..", "..#..", "..#..", "..#..", "..#..", "..#.."),
    ['U' - 32] = G("#...#", "#...#", "#...#", "#...#", "#...#", "#...#", ".###."),
    ['V' - 32] = G("#...#", "#...#", "#...#", "#...#", "#...#", ".#.#.", "..#.."),
    ['W' - 32] = G("#...#", "#...#", "#...#", "#.#.#", "#.#.#", "#.#.#", ".#.#."),
    ['X' - 32] = G("#...#", "#...#", ".#.#.", "..#..", ".#.#.", "#...#", "#...#"),
    ['Y' - 32] = G("#...#", "#...#", ".#.#.", "..#..", "..#..", "..#..", "..#.."),
    ['Z' - 32] = G("#####", "....#", "...#.", "..#..", ".#...", "#....", "#####"),
    ['[' - 32] = G(".###.", ".#...", ".#...", ".#...", ".#...", ".#...", ".###."),
    [']' - 32] = G(".###.", "...#.", "...#.", "...#.", "...#.", "...#.", ".###."),
    ['_' - 32] = G(".....", ".....", ".....", ".....", ".....", ".....", "#####"),
};

static const char *const *glyph(unsigned char c)
{
    if (c >= 'a' && c <= 'z') {
        c = (unsigned char)(c - 'a' + 'A');
    }
    if (c < 32 || c > 95 || !FONT[c - 32][0]) {
        c = '?';
    }
    return FONT[c - 32];
}

bool view_font_has(char c)
{
    unsigned char u = (unsigned char)c;
    if (u >= 'a' && u <= 'z') {
        u = (unsigned char)(u - 'a' + 'A');
    }
    return u >= 32 && u <= 95 && FONT[u - 32][0] != NULL;
}

/* Counts drawn characters: a UTF-8 sequence draws as one '?'. */
static size_t text_len(const char *s)
{
    size_t n = 0;
    for (; *s; s++) {
        if (((unsigned char)*s & 0xC0) != 0x80) {
            n++;
        }
    }
    return n;
}

float view_text_width(const char *s, float px)
{
    size_t n = text_len(s);
    return n ? (float)n * 6 * px - px : 0;
}

int view_text_rects(const char *s, float x, float y, float px, view_rect_t *rects, int max)
{
    int n = 0;
    for (; *s; s++) {
        unsigned char c = (unsigned char)*s;
        if ((c & 0xC0) == 0x80) {
            continue; /* the rest of a UTF-8 sequence */
        }
        const char *const *g = glyph(c >= 0x80 ? '?' : c);
        for (int row = 0; row < 7; row++) {
            for (int col = 0; col < 5;) {
                if (g[row][col] != '#') {
                    col++;
                    continue;
                }
                int run = col;
                while (run < 5 && g[row][run] == '#') {
                    run++;
                }
                if (n < max) {
                    rects[n++] = (view_rect_t){x + col * px, y + row * px, (run - col) * px, px};
                }
                col = run;
            }
        }
        x += 6 * px;
    }
    return n;
}
