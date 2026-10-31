/* SPDX-License-Identifier: Apache-2.0
 * Tests for the touch-report decoder against the frames captured on the
 * board (seam S1-3). The touch lane adds them (tasks 4.2-4.7, and its review).
 */
#include <string.h>

#include "axs_touch.h"
#include "fixtures/axs_touch.h"
#include "tests.h"
#include "unity.h"

static void test_touch_down_maps_to_panel(void)
{
    /* The owner's top-left touch: raw (639, 123), landscape (0, 48) under cw90. */
    axs_touch_state_t st = {0};
    axs_touch_point_t p = axs_touch_decode(HOLD_TL_DOWN, &st);
    TEST_ASSERT_TRUE(p.pressed);
    TEST_ASSERT_EQUAL_UINT16(123, p.px);
    TEST_ASSERT_EQUAL_UINT16(0, p.py);
}

static void test_no_report_keeps_state(void)
{
    axs_touch_state_t st = {0};
    axs_touch_decode(HOLD_TL_DOWN, &st);
    axs_touch_point_t p = axs_touch_decode(NO_REPORT, &st);
    TEST_ASSERT_TRUE(p.pressed);
    TEST_ASSERT_EQUAL_UINT16(123, p.px);
    TEST_ASSERT_EQUAL_UINT16(0, p.py);

    axs_touch_state_t idle = {0};
    p = axs_touch_decode(NO_REPORT, &idle);
    TEST_ASSERT_FALSE(p.pressed);
}

static void test_malformed_frame_keeps_state(void)
{
    axs_touch_state_t st = {0};
    axs_touch_decode(HOLD_TL_DOWN, &st);
    axs_touch_point_t p = axs_touch_decode(SHIFTED, &st);
    TEST_ASSERT_TRUE(p.pressed);
    TEST_ASSERT_EQUAL_UINT16(123, p.px);
    TEST_ASSERT_EQUAL_UINT16(0, p.py);

    axs_touch_state_t idle = {0};
    p = axs_touch_decode(SHIFTED, &idle);
    TEST_ASSERT_FALSE(p.pressed);
}

static void test_malformed_header_keeps_state(void)
{
    /* R2.3's other arms, which no captured frame shows: a gesture byte that
     * isn't 0, and a point count above 2. Each is a sound report (the
     * top-left hold, panel (123, 0)) with that one byte changed. */
    uint8_t gesture[AXS_TOUCH_REPORT_LEN], three[AXS_TOUCH_REPORT_LEN];
    memcpy(gesture, HOLD_TL_CONTACT, sizeof gesture);
    gesture[0] = 0x01;
    memcpy(three, HOLD_TL_CONTACT, sizeof three);
    three[1] = 3;

    /* Held elsewhere: the two-finger report's first point, panel (91, 496). */
    axs_touch_state_t st = {0};
    axs_touch_decode(TWO_POINTS, &st);
    axs_touch_point_t p = axs_touch_decode(gesture, &st);
    TEST_ASSERT_TRUE(p.pressed);
    TEST_ASSERT_EQUAL_UINT16(91, p.px);
    TEST_ASSERT_EQUAL_UINT16(496, p.py);
    p = axs_touch_decode(three, &st);
    TEST_ASSERT_TRUE(p.pressed);
    TEST_ASSERT_EQUAL_UINT16(91, p.px);
    TEST_ASSERT_EQUAL_UINT16(496, p.py);

    axs_touch_state_t idle = {0};
    TEST_ASSERT_FALSE(axs_touch_decode(gesture, &idle).pressed);
    TEST_ASSERT_FALSE(axs_touch_decode(three, &idle).pressed);
}

static void test_lift_releases(void)
{
    axs_touch_state_t st = {0};
    axs_touch_decode(HOLD_TL_DOWN, &st);
    TEST_ASSERT_FALSE(axs_touch_decode(LIFT, &st).pressed);
}

static void test_three_silent_reads_release(void)
{
    /* The top-left hold ended like this on the board: no lift event, only
     * no-report frames. */
    axs_touch_state_t st = {0};
    axs_touch_decode(HOLD_TL_DOWN, &st);
    TEST_ASSERT_TRUE(axs_touch_decode(NO_REPORT, &st).pressed);
    TEST_ASSERT_TRUE(axs_touch_decode(NO_REPORT, &st).pressed);
    TEST_ASSERT_FALSE(axs_touch_decode(NO_REPORT, &st).pressed);

    /* In a row: a report between them starts the count again. */
    axs_touch_state_t held = {0};
    axs_touch_decode(HOLD_TL_DOWN, &held);
    axs_touch_decode(NO_REPORT, &held);
    axs_touch_decode(NO_REPORT, &held);
    axs_touch_decode(HOLD_TL_CONTACT, &held);
    axs_touch_decode(NO_REPORT, &held);
    TEST_ASSERT_TRUE(axs_touch_decode(NO_REPORT, &held).pressed);
}

static void test_two_points_keep_first(void)
{
    /* A finger on each half: the first point is raw (143, 91). */
    axs_touch_state_t st = {0};
    axs_touch_point_t p = axs_touch_decode(TWO_POINTS, &st);
    TEST_ASSERT_TRUE(p.pressed);
    TEST_ASSERT_EQUAL_UINT16(91, p.px);
    TEST_ASSERT_EQUAL_UINT16(496, p.py);
}

static void test_echo_tail_reads_first_point(void)
{
    /* Real reports whose bytes after the counted points aren't ff. They pass
     * R2.3, so the first point is used: raw (11, 6) and raw (147, 85). */
    axs_touch_state_t st = {0};
    axs_touch_point_t p = axs_touch_decode(ONE_POINT_ECHO_TAIL, &st);
    TEST_ASSERT_TRUE(p.pressed);
    TEST_ASSERT_EQUAL_UINT16(6, p.px);
    TEST_ASSERT_EQUAL_UINT16(628, p.py);

    axs_touch_state_t two = {0};
    p = axs_touch_decode(TWO_POINTS_ECHO_SECOND, &two);
    TEST_ASSERT_TRUE(p.pressed);
    TEST_ASSERT_EQUAL_UINT16(85, p.px);
    TEST_ASSERT_EQUAL_UINT16(492, p.py);
}

void run_touch_tests(void)
{
    RUN_TEST(test_touch_down_maps_to_panel);
    RUN_TEST(test_no_report_keeps_state);
    RUN_TEST(test_malformed_frame_keeps_state);
    RUN_TEST(test_malformed_header_keeps_state);
    RUN_TEST(test_lift_releases);
    RUN_TEST(test_three_silent_reads_release);
    RUN_TEST(test_two_points_keep_first);
    RUN_TEST(test_echo_tail_reads_first_point);
}
