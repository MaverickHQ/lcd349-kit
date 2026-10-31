/* SPDX-License-Identifier: Apache-2.0
 * The RTC on the board (seam S1-4). The io lane adds its tests here, each
 * TEST_CASE tagged "[rtc]".
 */
#include <stdio.h>

#include "freertos/FreeRTOS.h"
#include "freertos/task.h"
#include "lcd349.h"
#include "unity.h"
#include "unity_test_runner.h"

#define SET_S 1800000000LL /* 2027-01-15 08:00:00 UTC */

/* Seconds from SET_S, clamped so a wild reading still fails as itself. */
static int32_t since_set(int64_t t)
{
    int64_t d = t - SET_S;
    return d > INT32_MAX ? INT32_MAX : d < INT32_MIN ? INT32_MIN : (int32_t)d;
}

/* The clock keeps the time it was set to, and runs at a second a second. */
TEST_CASE("test_rtc_round_trip", "[rtc]")
{
    int64_t t = 0;
    TEST_ASSERT_EQUAL_STRING(lcd349_err_str(LCD349_OK), lcd349_err_str(lcd349_rtc_set(SET_S)));
    TEST_ASSERT_EQUAL_STRING(lcd349_err_str(LCD349_OK), lcd349_err_str(lcd349_rtc_get(&t)));
    printf("rtc: set %lld, read %lld\n", SET_S, (long long)t);
    TEST_ASSERT_EQUAL_INT32(0, since_set(t));

    vTaskDelay(pdMS_TO_TICKS(2000));
    TEST_ASSERT_EQUAL_STRING(lcd349_err_str(LCD349_OK), lcd349_err_str(lcd349_rtc_get(&t)));
    printf("rtc: 2 s later %lld\n", (long long)t);
    TEST_ASSERT_INT32_WITHIN(1, 2, since_set(t));
}
