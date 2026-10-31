/* SPDX-License-Identifier: Apache-2.0
 *
 * Unit tests for the viewer's pure parts (core.c). The rotation test runs
 * the kit's own lcd349__rotate_frame, so the viewer can't drift from it.
 */
#include <math.h>
#include <stdio.h>
#include <string.h>

#include "core.h"
#include "lcd349_board.h"
#include "rotate.h"

static int s_checks, s_fails;

#define CHECK(c)                                                                \
    do {                                                                        \
        s_checks++;                                                             \
        if (!(c)) {                                                             \
            s_fails++;                                                          \
            fprintf(stderr, "%s:%d: CHECK(%s) failed\n", __FILE__, __LINE__, #c); \
        }                                                                       \
    } while (0)

static bool near(float a, float b, float eps)
{
    return fabsf(a - b) <= eps;
}

/* ---- Device facts and fb.bin ---- */

static void test_facts_match_the_kit(void)
{
    CHECK(VIEW_LCD_W == LCD349_LCD_W && VIEW_LCD_H == LCD349_LCD_H);
    CHECK(VIEW_PANEL_W == LCD349_PANEL_W && VIEW_PANEL_H == LCD349_PANEL_H);
    CHECK(VIEW_ROTATION_CW90 == LCD349_ROTATION_CW90 && VIEW_BYTE_SWAP == LCD349_BYTE_SWAP);
    CHECK((int)lroundf(VIEW_PX_PER_MM * 10) == LCD349_PX_PER_MM_X10);
}

static void test_rotation_inverts_the_kit(void)
{
    static uint16_t land[VIEW_LCD_W * VIEW_LCD_H], panel[VIEW_PANEL_W * VIEW_PANEL_H], back[VIEW_LCD_W * VIEW_LCD_H];
    for (int i = 0; i < VIEW_LCD_W * VIEW_LCD_H; i++) {
        land[i] = (uint16_t)((uint32_t)i * 2654435761u >> 16);
    }
    for (int cw = 0; cw < 2; cw++) {
        for (int swap = 0; swap < 2; swap++) {
            lcd349__rotate_frame(land, panel, cw, swap);
            memset(back, 0, sizeof back);
            view_panel_to_landscape(panel, back, cw, swap);
            CHECK(memcmp(land, back, sizeof land) == 0);
        }
    }
}

static void put32(uint8_t *p, uint32_t v)
{
    memcpy(p, &v, 4);
}

static void put16(uint8_t *p, uint16_t v)
{
    memcpy(p, &v, 2);
}

static void test_header(void)
{
    uint8_t h[VIEW_FB_HDR] = {0};
    view_hdr_t o;
    CHECK(!view_hdr_parse(h, &o)); /* starting: no magic yet */
    memcpy(h, "L349FB01", 8);
    put32(h + VIEW_OFF_VERSION, 1);
    put16(h + VIEW_OFF_WIDTH, 172);
    put16(h + VIEW_OFF_HEIGHT, 640);
    put32(h + VIEW_OFF_SEQ, 6);
    put32(h + VIEW_OFF_FRAMES, 3);
    put32(h + VIEW_OFF_PID, 4242);
    h[VIEW_OFF_BACKLIGHT] = 80;
    h[VIEW_OFF_POWER] = VIEW_POWER_LIGHT_SLEEP;
    h[VIEW_OFF_ASLEEP] = 1;
    h[VIEW_OFF_ON_BATTERY] = 1;
    put16(h + VIEW_OFF_BATTERY_MV, 3900);
    h[VIEW_OFF_BATTERY_PCT] = 70;
    h[VIEW_OFF_WIFI] = 3;
    put32(h + VIEW_OFF_STATUS_SEQ, 9);
    h[VIEW_OFF_ROTATION] = 2;
    h[VIEW_OFF_BYTE_SWAP] = 2;
    CHECK(view_hdr_parse(h, &o));
    CHECK(o.seq == 6 && o.frames == 3 && o.pid == 4242 && o.status_seq == 9);
    CHECK(o.backlight == 80 && o.power == VIEW_POWER_LIGHT_SLEEP && o.asleep == 1 && o.on_battery == 1);
    CHECK(o.battery_mv == 3900 && o.battery_pct == 70 && o.wifi == 3);
    CHECK(!o.cw && !o.swap);

    h[VIEW_OFF_ROTATION] = 0; /* unknown: the generated default */
    h[VIEW_OFF_BYTE_SWAP] = 0;
    CHECK(view_hdr_parse(h, &o) && o.cw == VIEW_ROTATION_CW90 && o.swap == VIEW_BYTE_SWAP);
    h[VIEW_OFF_ROTATION] = 1;
    h[VIEW_OFF_BYTE_SWAP] = 1;
    CHECK(view_hdr_parse(h, &o) && o.cw && o.swap);

    put16(h + VIEW_OFF_WIDTH, 240); /* another board */
    CHECK(!view_hdr_parse(h, &o));
    put16(h + VIEW_OFF_WIDTH, 172);
    put32(h + VIEW_OFF_VERSION, 2);
    CHECK(!view_hdr_parse(h, &o));
}

/* ---- JSON ---- */

static jtok_t t[256];

static int parse(const char *s)
{
    return json_parse(s, strlen(s), t, 256);
}

static void test_json_answers(void)
{
    const char *s = "{\"jsonrpc\":\"2.0\",\"id\":7,\"result\":{\"events\":[{\"seq\":1,\"t_ms\":120,\"hz\":880,\"ms\":40,\"volume\":60},"
                    "{\"seq\":2,\"t_ms\":160,\"stop\":true}],\"next\":2}}";
    CHECK(parse(s) > 0);
    CHECK(json_num(s, t, json_get(s, t, 0, "id"), -1) == 7);
    int res = json_get(s, t, 0, "result");
    int evs = json_get(s, t, res, "events");
    CHECK(evs >= 0 && t[evs].type == JT_ARR && t[evs].size == 2);
    int e0 = json_item(t, evs, 0), e1 = json_item(t, evs, 1);
    CHECK(json_num(s, t, json_get(s, t, e0, "hz"), 0) == 880);
    CHECK(json_num(s, t, json_get(s, t, e0, "volume"), 0) == 60);
    CHECK(json_get(s, t, e0, "stop") < 0 && json_true(t, json_get(s, t, e1, "stop")));
    CHECK(json_item(t, evs, 2) < 0 && json_item(t, res, 0) < 0);
    CHECK(json_num(s, t, json_get(s, t, res, "next"), 0) == 2);
    CHECK(json_get(s, t, 0, "error") < 0 && json_get(s, t, evs, "seq") < 0);
    CHECK(json_num(s, t, json_get(s, t, 0, "jsonrpc"), -1) == -1); /* a string isn't a number */

    const char *w = "  {\"a\" : [ true , false , null , -1.5e3 ] }  ";
    CHECK(parse(w) == 7);
    int a = json_get(w, t, 0, "a");
    CHECK(json_true(t, json_item(t, a, 0)) && !json_true(t, json_item(t, a, 1)) && t[json_item(t, a, 2)].type == JT_NULL);
    CHECK(json_num(w, t, json_item(t, a, 3), 0) == -1500);
}

static void test_json_strings(void)
{
    const char *s = "{\"error\":{\"code\":1,\"message\":\"a \\\"q\\\" \\u00e9 \\\\ \\/ \\ud83d\\ude00 \\ud800x\\n\"}}";
    CHECK(parse(s) > 0);
    char out[64];
    CHECK(json_str(s, t, json_get(s, t, json_get(s, t, 0, "error"), "message"), out, sizeof out));
    CHECK(strcmp(out, "a \"q\" \xc3\xa9 \\ / \xf0\x9f\x98\x80 ?x\n") == 0);
    char small[4];
    CHECK(json_str(s, t, json_get(s, t, json_get(s, t, 0, "error"), "message"), small, sizeof small) && strcmp(small, "a \"") == 0);
    CHECK(!json_str(s, t, json_get(s, t, json_get(s, t, 0, "error"), "code"), out, sizeof out) && out[0] == '\0');
}

static void test_json_rejects(void)
{
    const char *bad[] = {
        "", " ", "{", "}", "{\"a\":}", "{\"a\" 1}", "{a:1}", "[1,]", "[1 2]", "01", "1.", "-", "1e", "+1", "\"abc", "tru", "nul",
        "{} x", "\"\x01\"", "\"\\x\"", "\"\\u12g4\"", "{\"a\":1,}", "[\"a\":1]",
    };
    for (size_t i = 0; i < sizeof bad / sizeof bad[0]; i++) {
        if (parse(bad[i]) >= 0) {
            fprintf(stderr, "accepted invalid JSON: [%s]\n", bad[i]);
            CHECK(!"invalid JSON accepted");
        }
    }
    char deep[80] = "";
    for (int i = 0; i < 34; i++) {
        strcat(deep, "[");
    }
    for (int i = 0; i < 34; i++) {
        strcat(deep, "]");
    }
    CHECK(parse(deep) < 0); /* deeper than 32 */
    CHECK(json_parse("[1,2,3]", 7, t, 3) < 0); /* needs 4 tokens */
    CHECK(json_parse("[1,2,3]", 7, t, 4) == 4);
    CHECK(parse("0") == 1 && parse("-0.5E+2") == 1 && parse("\"\"") == 1 && parse("{}") == 1 && parse("[]") == 1);
}

/* ---- Layout and scale ---- */

static void test_layout(void)
{
    view_layout_t l;
    view_layout(&l, 1.0f);
    CHECK(l.screen.w == 640 && l.screen.h == 172);
    CHECK(near(l.case_r.w, VIEW_CASE_MM_W * VIEW_PX_PER_MM, 0.01f));
    CHECK(l.screen.x >= l.case_r.x && l.screen.x + l.screen.w <= l.case_r.x + l.case_r.w);
    CHECK(l.screen.y >= l.case_r.y && l.screen.y + l.screen.h <= l.case_r.y + l.case_r.h);
    CHECK(l.badges.y + l.badges.h <= l.case_r.y && l.case_r.y + l.case_r.h <= l.keys[0].y);
    CHECK(l.keys[0].y + l.keys[0].h <= l.caption.y && l.caption.y + l.caption.h <= l.hint.y && l.hint.y + l.hint.h <= l.win_h);
    CHECK(l.win_w >= VIEW_MIN_W && l.case_r.x + l.case_r.w <= l.win_w);

    int dx, dy;
    CHECK(view_to_device(&l, l.screen.x + 0.5f, l.screen.y + 0.5f, &dx, &dy) && dx == 0 && dy == 0);
    CHECK(view_to_device(&l, l.screen.x + 639.9f, l.screen.y + 171.9f, &dx, &dy) && dx == 639 && dy == 171);
    CHECK(!view_to_device(&l, l.screen.x - 1, l.screen.y + 10, &dx, &dy) && dx == 0 && dy == 10);
    CHECK(!view_to_device(&l, l.screen.x + 700, l.screen.y + 500, &dx, &dy) && dx == 639 && dy == 171);

    view_layout(&l, 0.5f);
    CHECK(l.screen.w == 320 && l.screen.h == 86);
    CHECK(view_to_device(&l, l.screen.x + 100, l.screen.y + 50, &dx, &dy) && dx == 200 && dy == 100);

    for (int i = 0; i < VIEW_N_BUTTONS; i++) {
        float cx = l.keys[i].x + l.keys[i].w / 2, cy = l.keys[i].y + l.keys[i].h / 2;
        CHECK(view_hit_button(&l, cx, cy) == i);
        CHECK(VIEW_BUTTONS[i].has_pos || l.on_case[i].w == 0);
    }
    CHECK(view_hit_button(&l, l.screen.x + 10, l.screen.y + 10) == -1);
    CHECK(VIEW_BUTTONS[0].key == 'b' && VIEW_BUTTONS[1].key == 'p');
}

static void test_scale(void)
{
    /* The development Mac's built-in panel: 1440 points across 286.9 mm
     * (CoreGraphics), while sdl2-compat reports a nominal 192 dpi. */
    float s = view_scale_from_mm(1440, 286.9f);
    CHECK(near(s, 0.6692f, 0.0005f));
    float host_mm_per_point = 286.9f / 1440;
    CHECK(near(640 * s * host_mm_per_point, 640 / VIEW_PX_PER_MM, 0.01f)); /* the screen is its real width */
    CHECK(near(view_scale_from_ppi(254, 2), 0.6667f, 0.0005f));            /* 10 px/mm at 2 px per point */
    CHECK(near(view_scale_from_ppi(96, 1), 0.5039f, 0.0005f));
    CHECK(view_scale_from_mm(0, 10) == 0 && view_scale_from_ppi(-1, 1) == 0 && view_scale_from_ppi(100, 0) == 0);
    CHECK(view_dpi_is_nominal(96) && view_dpi_is_nominal(192) && view_dpi_is_nominal(144) && view_dpi_is_nominal(72) &&
          view_dpi_is_nominal(120));
    CHECK(!view_dpi_is_nominal(109.0f) && !view_dpi_is_nominal(157.3f) && !view_dpi_is_nominal(254));
}

/* ---- Touch queue ---- */

static void test_touch_queue(void)
{
    view_touchq_t q = {0};
    view_sample_t s[VIEW_TOUCHQ_MAX];

    /* A tap: press and release, both kept. */
    view_touch_push(&q, 10, 20, true);
    view_touch_push(&q, 10, 20, false);
    CHECK(view_touch_take(&q, s, VIEW_TOUCHQ_MAX) == 2);
    CHECK(s[0].down && s[0].start && s[0].x == 10 && s[0].y == 20 && !s[1].down);

    /* A drag: moves coalesce into the newest position. */
    for (int x = 0; x < 4; x++) {
        view_touch_push(&q, x, 0, true);
    }
    CHECK(q.n == 2 && q.q[0].start && q.q[1].x == 3 && !q.q[1].start);
    CHECK(view_touch_take(&q, s, 1) == 1 && s[0].start && q.n == 1);
    view_touch_push(&q, 4, 0, true);
    CHECK(q.n == 1 && q.q[0].x == 4);
    CHECK(view_touch_take(&q, s, VIEW_TOUCHQ_MAX) == 1);
    view_touch_push(&q, 5, 0, true); /* sent already: a new move is queued */
    view_touch_push(&q, 5, 0, true); /* no movement */
    view_touch_push(&q, 5, 0, false);
    CHECK(q.n == 2 && q.q[0].down && !q.q[0].start && !q.q[1].down);

    /* A release without a press is ignored; positions are clamped. */
    view_touch_clear(&q);
    view_touch_push(&q, 1, 1, false);
    CHECK(q.n == 0);
    view_touch_push(&q, -5, 999, true);
    CHECK(q.n == 1 && q.q[0].x == 0 && q.q[0].y == VIEW_LCD_H - 1);

    /* A full queue drops whole touches, never just a release. */
    view_touch_clear(&q);
    for (int i = 0; i < 40; i++) {
        view_touch_push(&q, i, 0, true);
        view_touch_push(&q, i, 5, true);
        view_touch_push(&q, i, 0, false);
    }
    CHECK(q.n % 3 == 0 && q.n <= VIEW_TOUCHQ_MAX);
    bool paired = true;
    for (int i = 0; i < q.n; i += 3) {
        paired &= q.q[i].start && q.q[i + 1].down && !q.q[i + 1].start && !q.q[i + 2].down && q.q[i].x == q.q[i + 2].x;
    }
    CHECK(paired);
    CHECK(!q.down && !q.dropping);
}

/* ---- Font ---- */

static void test_font(void)
{
    const char *ui[] = {
        "NO INSTANCE", "OFF", "NOT RUNNING", "DOWNLOAD MODE", "SLEEP", "SCREEN OFF", "ON", "BAT 100%", "USB", "WIFI OFF", "WIFI SCAN",
        "WIFI JOIN", "WIFI", "NO WIFI", "WIFI ?", "LIGHT 80%", "BOOT (b)", "PWR (p)",
        "Not yet measured: button positions on the case, and the screen's place in it",
        "Drag on the screen to touch. Hold B or P, or a key above, to press.",
        "the display is dark (asleep, off or backlight 0): touch is never a wake source; press a button",
        "this client isn't holding boot; hold it first with state \"down\"",
    };
    for (size_t i = 0; i < sizeof ui / sizeof ui[0]; i++) {
        for (const char *c = ui[i]; *c; c++) {
            if (!view_font_has(*c)) {
                fprintf(stderr, "no glyph for '%c' in \"%s\"\n", *c, ui[i]);
                CHECK(!"missing glyph");
            }
        }
    }
    CHECK(view_text_width("AB", 2) == 22 && view_text_width("", 2) == 0);
    view_rect_t r[256], r2[256];
    int n = view_text_rects("I", 0, 0, 1, r, 256);
    CHECK(n == 7 && r[0].x == 1 && r[0].w == 3 && r[1].x == 2 && r[1].w == 1); /* runs merge into one rect per row segment */
    CHECK(view_text_rects("I", 10, 20, 2, r, 256) == 7 && r[0].x == 12 && r[0].y == 20 && r[0].w == 6 && r[0].h == 2);
    /* Unknown and UTF-8 characters draw as one '?'; lowercase as uppercase. */
    int q = view_text_rects("?", 0, 0, 1, r, 256);
    CHECK(view_text_rects("\xc3\x97", 0, 0, 1, r2, 256) == q && memcmp(r, r2, sizeof r[0] * (size_t)q) == 0);
    CHECK(view_text_width("\xc3\x97", 1) == 5 && view_text_rects("~", 0, 0, 1, r2, 256) == q);
    n = view_text_rects("wifi", 0, 0, 1, r, 256);
    CHECK(view_text_rects("WIFI", 0, 0, 1, r2, 256) == n && memcmp(r, r2, sizeof r[0] * (size_t)n) == 0);
    CHECK(view_text_rects("WWWW", 0, 0, 1, r, 3) == 3); /* never more than max */
}

int main(void)
{
    test_facts_match_the_kit();
    test_rotation_inverts_the_kit();
    test_header();
    test_json_answers();
    test_json_strings();
    test_json_rejects();
    test_layout();
    test_scale();
    test_touch_queue();
    test_font();
    printf("%d checks, %d failures\n", s_checks, s_fails);
    return s_fails ? 1 : 0;
}
