/* SPDX-License-Identifier: Apache-2.0
 * Tests for the pure parts of the common core.
 */
#include <string.h>

#include "control.h"
#include "debounce.h"
#include "lcd349.h"
#include "rotate.h"
#include "tests.h"
#include "unity.h"

/* ---- Classifier ---- */

static lcd349_classifier_t c;
static lcd349_gesture_t g[LCD349_CLASSIFIER_MAX_OUT];

static void setup_classifier(void)
{
    lcd349_classifier_cfg_t cfg = {.hold_ms = {1000, 3000}, .chord_window_ms = 150};
    TEST_ASSERT_EQUAL(LCD349_OK, lcd349_classifier_init(&c, &cfg));
}

static size_t press(lcd349_btn_t b, bool down, uint32_t t)
{
    lcd349_btn_event_t ev = {b, down, t};
    return lcd349_classifier_feed(&c, &ev, g, LCD349_CLASSIFIER_MAX_OUT);
}

static void test_classifier_short(void)
{
    setup_classifier();
    TEST_ASSERT_EQUAL(0, press(LCD349_BTN_BOOT, true, 100));
    TEST_ASSERT_EQUAL(1, press(LCD349_BTN_BOOT, false, 400));
    TEST_ASSERT_EQUAL(LCD349_GESTURE_SHORT, g[0].kind);
    TEST_ASSERT_EQUAL(LCD349_BTN_BOOT, g[0].btn);
    TEST_ASSERT_EQUAL_UINT32(300, g[0].held_ms);
}

static void test_classifier_hold_at_each_threshold(void)
{
    setup_classifier();
    press(LCD349_BTN_PWR, true, 0);
    /* 1 ms short of the first threshold: nothing yet. */
    TEST_ASSERT_EQUAL(0, lcd349_classifier_tick(&c, 999, g, LCD349_CLASSIFIER_MAX_OUT));
    TEST_ASSERT_EQUAL(1, lcd349_classifier_tick(&c, 1000, g, LCD349_CLASSIFIER_MAX_OUT));
    TEST_ASSERT_EQUAL(LCD349_GESTURE_HOLD, g[0].kind);
    TEST_ASSERT_EQUAL(1, g[0].level);
    TEST_ASSERT_EQUAL_UINT32(1000, g[0].t_ms);
    /* Ticking again doesn't repeat it. */
    TEST_ASSERT_EQUAL(0, lcd349_classifier_tick(&c, 1500, g, LCD349_CLASSIFIER_MAX_OUT));
    TEST_ASSERT_EQUAL(1, lcd349_classifier_tick(&c, 3000, g, LCD349_CLASSIFIER_MAX_OUT));
    TEST_ASSERT_EQUAL(2, g[0].level);
    TEST_ASSERT_EQUAL(1, press(LCD349_BTN_PWR, false, 3500));
    TEST_ASSERT_EQUAL(LCD349_GESTURE_HOLD_END, g[0].kind);
    TEST_ASSERT_EQUAL(2, g[0].level);
    TEST_ASSERT_EQUAL_UINT32(3500, g[0].held_ms);
}

static void test_classifier_release_catches_up_missed_holds(void)
{
    /* No ticks at all: the release still reports the holds it passed, in order, then the end. */
    setup_classifier();
    press(LCD349_BTN_BOOT, true, 0);
    TEST_ASSERT_EQUAL(3, press(LCD349_BTN_BOOT, false, 3200));
    TEST_ASSERT_EQUAL(LCD349_GESTURE_HOLD, g[0].kind);
    TEST_ASSERT_EQUAL(1, g[0].level);
    TEST_ASSERT_EQUAL(LCD349_GESTURE_HOLD, g[1].kind);
    TEST_ASSERT_EQUAL(2, g[1].level);
    TEST_ASSERT_EQUAL(LCD349_GESTURE_HOLD_END, g[2].kind);
}

static void test_classifier_release_just_before_threshold_is_short(void)
{
    setup_classifier();
    press(LCD349_BTN_BOOT, true, 0);
    TEST_ASSERT_EQUAL(1, press(LCD349_BTN_BOOT, false, 999));
    TEST_ASSERT_EQUAL(LCD349_GESTURE_SHORT, g[0].kind);
}

static void test_classifier_chord(void)
{
    setup_classifier();
    press(LCD349_BTN_BOOT, true, 0);
    TEST_ASSERT_EQUAL(1, press(LCD349_BTN_PWR, true, 150)); /* exactly at the window */
    TEST_ASSERT_EQUAL(LCD349_GESTURE_CHORD, g[0].kind);
    TEST_ASSERT_EQUAL(LCD349_BTN_BOOT, g[0].btn);
    /* Consumed: no holds while chorded, no SHORT on release. */
    TEST_ASSERT_EQUAL(0, lcd349_classifier_tick(&c, 5000, g, LCD349_CLASSIFIER_MAX_OUT));
    TEST_ASSERT_EQUAL(0, press(LCD349_BTN_BOOT, false, 5100));
    TEST_ASSERT_EQUAL(1, press(LCD349_BTN_PWR, false, 5200));
    TEST_ASSERT_EQUAL(LCD349_GESTURE_CHORD_END, g[0].kind);
    /* Afterwards the buttons work normally again. */
    press(LCD349_BTN_PWR, true, 6000);
    TEST_ASSERT_EQUAL(1, press(LCD349_BTN_PWR, false, 6100));
    TEST_ASSERT_EQUAL(LCD349_GESTURE_SHORT, g[0].kind);
}

static void test_classifier_late_second_button_is_not_a_chord(void)
{
    setup_classifier();
    press(LCD349_BTN_BOOT, true, 0);
    TEST_ASSERT_EQUAL(0, press(LCD349_BTN_PWR, true, 151));
    TEST_ASSERT_EQUAL(1, press(LCD349_BTN_PWR, false, 300));
    TEST_ASSERT_EQUAL(LCD349_GESTURE_SHORT, g[0].kind);
    TEST_ASSERT_EQUAL(LCD349_BTN_PWR, g[0].btn);
    TEST_ASSERT_EQUAL(1, press(LCD349_BTN_BOOT, false, 400));
    TEST_ASSERT_EQUAL(LCD349_GESTURE_SHORT, g[0].kind);
    TEST_ASSERT_EQUAL(LCD349_BTN_BOOT, g[0].btn);
}

static void test_classifier_rejects_bad_config(void)
{
    lcd349_classifier_t x;
    lcd349_classifier_cfg_t descending = {.hold_ms = {2000, 1000}};
    lcd349_classifier_cfg_t gap = {.hold_ms = {1000, 0, 3000}};
    lcd349_classifier_cfg_t wide_chord = {.hold_ms = {500}, .chord_window_ms = 500};
    TEST_ASSERT_EQUAL(LCD349_ERR_ARG, lcd349_classifier_init(&x, &descending));
    TEST_ASSERT_EQUAL(LCD349_ERR_ARG, lcd349_classifier_init(&x, &gap));
    TEST_ASSERT_EQUAL(LCD349_ERR_ARG, lcd349_classifier_init(&x, &wide_chord));
    TEST_ASSERT_EQUAL(LCD349_ERR_ARG, lcd349_classifier_init(&x, NULL));
}

static void test_classifier_progress(void)
{
    setup_classifier();
    TEST_ASSERT_EQUAL(0, lcd349_classifier_hold_permille(&c, LCD349_BTN_BOOT, 0));
    press(LCD349_BTN_BOOT, true, 1000);
    TEST_ASSERT_EQUAL(500, lcd349_classifier_hold_permille(&c, LCD349_BTN_BOOT, 1500));
    lcd349_classifier_tick(&c, 2000, g, LCD349_CLASSIFIER_MAX_OUT);
    /* Between the first (1000) and second (3000) thresholds. */
    TEST_ASSERT_EQUAL(500, lcd349_classifier_hold_permille(&c, LCD349_BTN_BOOT, 3000));
    lcd349_classifier_tick(&c, 4000, g, LCD349_CLASSIFIER_MAX_OUT);
    TEST_ASSERT_EQUAL(1000, lcd349_classifier_hold_permille(&c, LCD349_BTN_BOOT, 4000));
}

/* ---- Debounce ---- */

static void test_debounce_glitch_and_edge_time(void)
{
    lcd349_debounce_t d;
    uint32_t edge;
    lcd349__debounce_init(&d, false, 0);
    /* A 5 ms glitch is dropped. */
    lcd349__debounce_raw(&d, true, 100);
    lcd349__debounce_raw(&d, false, 105);
    TEST_ASSERT_FALSE(lcd349__debounce_settle(&d, 120, 10, &edge));
    TEST_ASSERT_EQUAL_UINT32(UINT32_MAX, lcd349__debounce_due_in(&d, 120, 10));
    /* A real press settles 10 ms later and reports when it started. */
    lcd349__debounce_raw(&d, true, 200);
    TEST_ASSERT_EQUAL_UINT32(4, lcd349__debounce_due_in(&d, 206, 10));
    TEST_ASSERT_FALSE(lcd349__debounce_settle(&d, 209, 10, &edge));
    TEST_ASSERT_TRUE(lcd349__debounce_settle(&d, 210, 10, &edge));
    TEST_ASSERT_EQUAL_UINT32(200, edge);
    TEST_ASSERT_TRUE(d.stable);
    /* Bouncy release: the edge time is the last transition. */
    lcd349__debounce_raw(&d, false, 500);
    lcd349__debounce_raw(&d, true, 502);
    lcd349__debounce_raw(&d, false, 504);
    TEST_ASSERT_TRUE(lcd349__debounce_settle(&d, 520, 10, &edge));
    TEST_ASSERT_EQUAL_UINT32(504, edge);
    TEST_ASSERT_FALSE(d.stable);
}

/* ---- Rotation and byte swap: golden bytes for a pixel in each corner ---- */

static uint16_t land[LCD349_LCD_W * LCD349_LCD_H];
static uint16_t panel[LCD349_PANEL_W * LCD349_PANEL_H];

static void corners(void)
{
    memset(land, 0, sizeof land);
    land[0] = 0x1234;                                             /* top-left */
    land[LCD349_LCD_W - 1] = 0x5678;                              /* top-right */
    land[(LCD349_LCD_H - 1) * LCD349_LCD_W] = 0x9ABC;             /* bottom-left */
    land[(LCD349_LCD_H - 1) * LCD349_LCD_W + LCD349_LCD_W - 1] = 0xDEF0; /* bottom-right */
}

/* Expected panel byte pairs: {panel row, panel column, first byte, second byte}. */
typedef struct {
    int row, col;
    uint8_t b0, b1;
} golden_t;

static void check_golden(const golden_t *gold, int n)
{
    const uint8_t *bytes = (const uint8_t *)panel;
    int nonzero = 0;
    for (size_t i = 0; i < sizeof panel; i++) {
        nonzero += bytes[i] != 0;
    }
    TEST_ASSERT_EQUAL_MESSAGE(8, nonzero, "exactly the four corner pixels are set");
    for (int i = 0; i < n; i++) {
        size_t off = 2u * (size_t)(gold[i].row * LCD349_PANEL_W + gold[i].col);
        TEST_ASSERT_EQUAL_HEX8(gold[i].b0, bytes[off]);
        TEST_ASSERT_EQUAL_HEX8(gold[i].b1, bytes[off + 1]);
    }
}

static void test_rotate_cw_swapped(void)
{
    corners();
    lcd349__rotate_frame(land, panel, true, true);
    /* Clockwise: landscape top-left goes to the panel's top-right; bytes big-endian. */
    golden_t gold[] = {
        {0, 171, 0x12, 0x34},
        {639, 171, 0x56, 0x78},
        {0, 0, 0x9A, 0xBC},
        {639, 0, 0xDE, 0xF0},
    };
    check_golden(gold, 4);
}

static void test_rotate_ccw_swapped(void)
{
    corners();
    lcd349__rotate_frame(land, panel, false, true);
    golden_t gold[] = {
        {639, 0, 0x12, 0x34},
        {0, 0, 0x56, 0x78},
        {639, 171, 0x9A, 0xBC},
        {0, 171, 0xDE, 0xF0},
    };
    check_golden(gold, 4);
}

static void test_rotate_unswapped_keeps_host_order(void)
{
    corners();
    lcd349__rotate_frame(land, panel, true, false);
    TEST_ASSERT_EQUAL_HEX16(0x1234, panel[0 * LCD349_PANEL_W + 171]);
}

static void test_touch_transform_matches_rotation(void)
{
    /* A touch on a panel pixel maps to the landscape pixel drawn there, both ways. */
    for (int cw = 0; cw <= 1; cw++) {
        uint16_t pts[][2] = {{0, 0}, {639, 0}, {0, 171}, {639, 171}, {320, 86}};
        for (size_t i = 0; i < sizeof pts / sizeof pts[0]; i++) {
            corners();
            memset(land, 0, sizeof land);
            land[pts[i][1] * LCD349_LCD_W + pts[i][0]] = 0xFFFF;
            lcd349__rotate_frame(land, panel, cw, false);
            uint16_t px, py, x, y;
            lcd349__landscape_to_panel(pts[i][0], pts[i][1], cw, &px, &py);
            TEST_ASSERT_EQUAL_HEX16(0xFFFF, panel[py * LCD349_PANEL_W + px]);
            lcd349__panel_to_landscape(px, py, cw, &x, &y);
            TEST_ASSERT_EQUAL(pts[i][0], x);
            TEST_ASSERT_EQUAL(pts[i][1], y);
        }
    }
}

/* ---- Battery curve ---- */

static void test_battery_curve(void)
{
    const lcd349_board_info_t *b = lcd349_board_info();
    TEST_ASSERT_TRUE(b->battery_curve_len >= 2);
    const lcd349_battery_point_t *top = &b->battery_curve[0];
    const lcd349_battery_point_t *bot = &b->battery_curve[b->battery_curve_len - 1];
    TEST_ASSERT_EQUAL(100, lcd349_battery_pct_from_mv(top->mv));
    TEST_ASSERT_EQUAL(100, lcd349_battery_pct_from_mv(top->mv + 300));
    TEST_ASSERT_EQUAL(0, lcd349_battery_pct_from_mv(bot->mv));
    TEST_ASSERT_EQUAL(0, lcd349_battery_pct_from_mv(1000));
    /* Every curve point maps to its own percentage, and the mapping never rises as voltage falls. */
    uint8_t prev = 100;
    for (uint16_t mv = top->mv; mv >= bot->mv; mv -= 5) {
        uint8_t p = lcd349_battery_pct_from_mv(mv);
        TEST_ASSERT_TRUE(p <= prev);
        prev = p;
    }
    for (uint8_t i = 0; i < b->battery_curve_len; i++) {
        TEST_ASSERT_EQUAL(b->battery_curve[i].pct, lcd349_battery_pct_from_mv(b->battery_curve[i].mv));
    }
}

/* ---- Error strings ---- */

int lcd349_test_cpp_sizes(void);

static void test_cpp_api(void)
{
    TEST_ASSERT_EQUAL(LCD349_BACKLIGHT_DEFAULT_PCT + 100, lcd349_test_cpp_sizes());
}

static void test_err_str(void)
{
    TEST_ASSERT_EQUAL_STRING("LCD349_OK", lcd349_err_str(LCD349_OK));
    TEST_ASSERT_EQUAL_STRING("LCD349_ERR_ON_USB", lcd349_err_str(LCD349_ERR_ON_USB));
    TEST_ASSERT_EQUAL_STRING("LCD349_ERR_?", lcd349_err_str((lcd349_err_t)99));
}

/* ---- Request nesting (the control task parses requests recursively) ---- */

static void test_json_depth(void)
{
    char deep[64], deeper[64];
    memset(deep, 0, sizeof deep);
    memset(deeper, 0, sizeof deeper);
    for (int i = 0; i < 16; i++) {
        deep[i] = '[';
        deep[16 + i] = ']';
    }
    memcpy(deeper, "[", 1);
    memcpy(deeper + 1, deep, 32);
    deeper[33] = ']';
    TEST_ASSERT_TRUE(emu_json_depth_ok(deep, strlen(deep), RPC_MAX_DEPTH));
    TEST_ASSERT_FALSE(emu_json_depth_ok(deeper, strlen(deeper), RPC_MAX_DEPTH));
    TEST_ASSERT_TRUE(emu_json_depth_ok("{}", 2, 1));
    TEST_ASSERT_FALSE(emu_json_depth_ok("{\"a\":{}}", 8, 1));
    /* Brackets in strings, escaped quotes included, don't count. */
    const char *s = "{\"a\":\"[[{{\\\"]]\"}";
    TEST_ASSERT_TRUE(emu_json_depth_ok(s, strlen(s), 1));
    /* Only len bytes are read. */
    TEST_ASSERT_TRUE(emu_json_depth_ok("[[", 1, 1));
}

void run_pure_tests(void)
{
    RUN_TEST(test_classifier_short);
    RUN_TEST(test_classifier_hold_at_each_threshold);
    RUN_TEST(test_classifier_release_catches_up_missed_holds);
    RUN_TEST(test_classifier_release_just_before_threshold_is_short);
    RUN_TEST(test_classifier_chord);
    RUN_TEST(test_classifier_late_second_button_is_not_a_chord);
    RUN_TEST(test_classifier_rejects_bad_config);
    RUN_TEST(test_classifier_progress);
    RUN_TEST(test_debounce_glitch_and_edge_time);
    RUN_TEST(test_rotate_cw_swapped);
    RUN_TEST(test_rotate_ccw_swapped);
    RUN_TEST(test_rotate_unswapped_keeps_host_order);
    RUN_TEST(test_touch_transform_matches_rotation);
    RUN_TEST(test_battery_curve);
    RUN_TEST(test_err_str);
    RUN_TEST(test_cpp_api);
    RUN_TEST(test_json_depth);
}
