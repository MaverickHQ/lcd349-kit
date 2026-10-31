/* SPDX-License-Identifier: Apache-2.0
 *
 * lcd349-view's pure parts: no SDL, no sockets, no files. view.c and audio.c
 * use them; test_core.c tests them.
 */
#pragma once

#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>

#include "device_gen.h"

/* ---- fb.bin (the emulator's components/lcd349/src/emu/emu.h; offsets are
 * pinned there with _Static_assert) ---- */

#define VIEW_FB_MAGIC "L349FB01"
#define VIEW_FB_HDR 64
#define VIEW_FB_BYTES (VIEW_FB_HDR + VIEW_PANEL_W * VIEW_PANEL_H * 2)

enum {
    VIEW_OFF_VERSION = 8,
    VIEW_OFF_WIDTH = 12,
    VIEW_OFF_HEIGHT = 14,
    VIEW_OFF_SEQ = 16,
    VIEW_OFF_FRAMES = 20,
    VIEW_OFF_PID = 32,
    VIEW_OFF_BACKLIGHT = 36,
    VIEW_OFF_POWER = 37,
    VIEW_OFF_ASLEEP = 38,
    VIEW_OFF_ON_BATTERY = 39,
    VIEW_OFF_BATTERY_MV = 40,
    VIEW_OFF_BATTERY_PCT = 42,
    VIEW_OFF_WIFI = 43,
    VIEW_OFF_STATUS_SEQ = 44,
    VIEW_OFF_ROTATION = 48,
    VIEW_OFF_BYTE_SWAP = 49,
};

enum { VIEW_POWER_ON = 0, VIEW_POWER_LIGHT_SLEEP = 1, VIEW_POWER_OFF = 2, VIEW_POWER_DOWNLOAD = 3 };

/* The status fields of the header, decoded. */
typedef struct {
    uint32_t version, seq, frames, pid, status_seq;
    uint8_t backlight, power, asleep, on_battery, battery_pct, wifi;
    uint16_t battery_mv;
    bool cw;   /* landscape rotated clockwise into the panel */
    bool swap; /* RGB565 bytes swapped */
} view_hdr_t;

/* Decodes a header. False if the magic or the dimensions are wrong (the
 * instance is starting, or it isn't this board). Unknown rotation or byte
 * order (0) falls back to device_gen.h. */
bool view_hdr_parse(const uint8_t *hdr, view_hdr_t *out);

/* Panel-native pixels to landscape RGB565 in host byte order: the inverse of
 * the kit's lcd349__rotate_frame. */
void view_panel_to_landscape(const uint16_t *panel, uint16_t *land, bool cw, bool swap);

/* ---- JSON: just enough for control-socket answers ---- */

typedef enum { JT_OBJ = 1, JT_ARR, JT_STR, JT_NUM, JT_TRUE, JT_FALSE, JT_NULL } jtype_t;

typedef struct {
    jtype_t type;
    int start, end; /* byte range; strings exclude the quotes and keep escapes */
    int size;       /* members of an object, items of an array */
    int next;       /* index of the token after this one's subtree */
} jtok_t;

/* Parses one JSON value that fills s (surrounding whitespace allowed). Returns
 * the token count, or -1 if s isn't valid JSON or needs more than max tokens. */
int json_parse(const char *s, size_t len, jtok_t *t, int max);
/* Index of key's value in object obj, or -1. */
int json_get(const char *s, const jtok_t *t, int obj, const char *key);
/* Index of item i of array arr, or -1. */
int json_item(const jtok_t *t, int arr, int i);
double json_num(const char *s, const jtok_t *t, int i, double def);
bool json_true(const jtok_t *t, int i);
/* Unescapes string token i into out (always NUL-terminated). False if i isn't a string. */
bool json_str(const char *s, const jtok_t *t, int i, char *out, size_t cap);

/* ---- Layout, in window points ---- */

typedef struct {
    float x, y, w, h;
} view_rect_t;

typedef struct {
    float scale; /* window points per device pixel */
    float win_w, win_h;
    view_rect_t badges;  /* status badges, above the case */
    view_rect_t case_r;  /* the case outline */
    view_rect_t screen;  /* the active area */
    view_rect_t keys[VIEW_N_BUTTONS];    /* BOOT and PWR keys, below the case */
    view_rect_t on_case[VIEW_N_BUTTONS]; /* on the case edge; w = 0 until measured */
    view_rect_t caption; /* a note under the keys */
    view_rect_t hint;    /* the hint line, or a message */
} view_layout_t;

#define VIEW_MARGIN 12.0f
#define VIEW_BADGE_H 22.0f
#define VIEW_KEY_W 112.0f
#define VIEW_KEY_H 30.0f
#define VIEW_MIN_W 400.0f

void view_layout(view_layout_t *l, float scale);
/* The button (index into VIEW_BUTTONS) at a window point, or -1. */
int view_hit_button(const view_layout_t *l, float x, float y);
/* Window point to landscape device pixel. False outside the active area
 * (*dx, *dy are then clamped to its edge). */
bool view_to_device(const view_layout_t *l, float x, float y, int *dx, int *dy);

/* ---- Physical scale ---- */

/* Window points per device pixel from the host display's width in window
 * points and in millimetres (macOS: CoreGraphics). */
float view_scale_from_mm(float display_points_w, float display_mm_w);
/* From a pixel density in pixels per inch and the window's pixels per point. */
float view_scale_from_ppi(float host_ppi, float pixels_per_point);
/* True if dpi looks like a nominal value (96 or 72 times a common scale
 * factor) rather than a measured one. */
bool view_dpi_is_nominal(float dpi);

/* ---- Touch: the mouse as a finger ---- */

typedef struct {
    uint16_t x, y;
    bool down;
    bool start; /* the first down sample of a touch */
} view_sample_t;

#define VIEW_TOUCHQ_MAX 64

typedef struct {
    view_sample_t q[VIEW_TOUCHQ_MAX];
    int n;
    bool down;     /* the finger is down (as queued) */
    bool dropping; /* the queue was full when this touch began: it is dropped whole */
    uint16_t last_x, last_y;
} view_touchq_t;

/* Queues a sample. Moves are coalesced: while the finger stays down, only the
 * newest position waits to be sent. A press and a release are always kept. */
void view_touch_push(view_touchq_t *t, int x, int y, bool down);
/* Takes up to max queued samples, oldest first. */
int view_touch_take(view_touchq_t *t, view_sample_t *out, int max);
void view_touch_clear(view_touchq_t *t);

/* ---- Text: a 5x7 bitmap font for the skin (uppercase) ---- */

/* Fills rects with one rectangle per lit dot of s drawn at (x, y), each dot
 * px points square. Returns how many rects, at most max. Lowercase letters
 * are drawn as uppercase; characters without a glyph as '?'. */
int view_text_rects(const char *s, float x, float y, float px, view_rect_t *rects, int max);
float view_text_width(const char *s, float px);
bool view_font_has(char c);
