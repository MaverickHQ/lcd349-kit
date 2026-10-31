/* SPDX-License-Identifier: Apache-2.0
 * Tests for the strip transform against the whole-frame transform (seam
 * S1-2). The display lane adds them (task 3.1).
 */
#include <stdio.h>
#include <string.h>

#include "lcd349_board.h"
#include "rotate.h"
#include "tests.h"
#include "unity.h"

#define FRAME_PX (LCD349_LCD_W * LCD349_LCD_H)
#define STRIP_ROWS 64 /* panel rows per strip, as the board sends them (task 3.1) */
#define STRIP_PX (STRIP_ROWS * LCD349_PANEL_W)
#define GUARD 0x5A5A

_Static_assert(LCD349_PANEL_H % STRIP_ROWS == 0, "whole strips cover the panel");

static uint16_t land[FRAME_PX];
static uint16_t whole[FRAME_PX];
static uint16_t assembled[FRAME_PX];
/* One strip with a strip-sized guard on each side, to catch writes outside it. */
static uint16_t band[3 * STRIP_PX];

static void test_rotate_band_matches_whole_frame(void)
{
    /* Every pixel differs from its neighbours across and down, and nearly
     * every pixel's two bytes differ (pixel 0's don't), so a misplaced pixel
     * or a missed swap changes the bytes. */
    for (int i = 0; i < FRAME_PX; i++) {
        land[i] = (uint16_t)(i * 2654435761u >> 16);
    }
    for (int cw = 0; cw <= 1; cw++) {
        for (int swap = 0; swap <= 1; swap++) {
            char msg[32];
            snprintf(msg, sizeof msg, "cw=%d swap=%d", cw, swap);
            lcd349__rotate_frame(land, whole, cw, swap);
            memset(assembled, 0, sizeof assembled);
            for (int r = 0; r < LCD349_PANEL_H; r += STRIP_ROWS) {
                for (size_t i = 0; i < sizeof band / sizeof band[0]; i++) {
                    band[i] = GUARD;
                }
                lcd349__rotate_band(land, band + STRIP_PX, r, STRIP_ROWS, cw, swap);
                for (int i = 0; i < STRIP_PX; i++) {
                    TEST_ASSERT_EQUAL_HEX16_MESSAGE(GUARD, band[i], msg);
                    TEST_ASSERT_EQUAL_HEX16_MESSAGE(GUARD, band[2 * STRIP_PX + i], msg);
                }
                memcpy(assembled + r * LCD349_PANEL_W, band + STRIP_PX, STRIP_PX * sizeof band[0]);
            }
            TEST_ASSERT_EQUAL_MEMORY_MESSAGE(whole, assembled, sizeof whole, msg);
        }
    }
}

void run_rotate_tests(void)
{
    RUN_TEST(test_rotate_band_matches_whole_frame);
}
