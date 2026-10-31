/* SPDX-License-Identifier: Apache-2.0
 * The battery reading on the board (seam S1-4). The io lane adds its tests
 * here, each TEST_CASE tagged "[battery]".
 */
#include <stdio.h>

#include "lcd349.h"
#include "unity.h"
#include "unity_test_runner.h"

/* A connected Li-Po cell, charging or not, reads between flat and full
 * (with room for the ADC's error); the percentage comes with it. */
TEST_CASE("test_battery_plausible", "[battery]")
{
    uint16_t mv = 0;
    uint8_t pct = 0xff;
    TEST_ASSERT_EQUAL_STRING(lcd349_err_str(LCD349_OK), lcd349_err_str(lcd349_battery_read(&mv, &pct)));
    printf("battery: %u mV, %u %%\n", (unsigned)mv, (unsigned)pct);
    TEST_ASSERT_GREATER_OR_EQUAL_UINT16(3000, mv);
    TEST_ASSERT_LESS_OR_EQUAL_UINT16(4400, mv);
    TEST_ASSERT_LESS_OR_EQUAL_UINT8(100, pct);
    TEST_ASSERT_EQUAL_UINT8(lcd349_battery_pct_from_mv(mv), pct);
}
