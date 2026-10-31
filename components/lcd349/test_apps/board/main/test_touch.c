/* SPDX-License-Identifier: Apache-2.0
 * Touch on the board (seam S1-4). The touch lane adds its tests here, each
 * TEST_CASE tagged "[touch]".
 */
#include <stdio.h>

#include "freertos/FreeRTOS.h"
#include "freertos/task.h"
#include "lcd349.h"
#include "unity.h"
#include "unity_test_runner.h"

/* The backend's read counts, a test hook in src/hw/touch.c: the reports read
 * from the controller (the I2C transfer worked), and how many of them decoded
 * as pressed. Both are written on lcd349_ui. */
void hw_touch_counts(uint32_t *reports, uint32_t *pressed);

#define IDLE_MS 2000

static volatile int s_samples;

static void count_sample(const lcd349_touch_sample_t *s, void *ctx)
{
    (void)s;
    (void)ctx;
    s_samples++;
}

/* Nobody touches the board during a run. The core polls touch every touch
 * period while the display is lit (requirement 2.7), so 2 s of idle reads
 * must all come back released: no pressed report and no touch sample
 * (requirements 2.1, 2.2). */
TEST_CASE("test_touch_idle_is_released", "[touch]")
{
    TEST_ASSERT_EQUAL_HEX32_MESSAGE(0, lcd349_board_status() & LCD349_SUB_TOUCH, "touch came up");
    TEST_ASSERT_NOT_NULL(lcd349_touch_indev());
    TEST_ASSERT_FALSE_MESSAGE(lcd349_display_is_asleep(), "touch is read only while the display is awake");
    TEST_ASSERT_NOT_EQUAL_MESSAGE(0, lcd349_backlight_get(), "touch is read only while the display is lit");

    s_samples = 0;
    TEST_ASSERT_EQUAL(LCD349_OK, lcd349_touch_subscribe(count_sample, NULL));
    uint32_t reports0 = 0, pressed0 = 0, reports = 0, pressed = 0;
    hw_touch_counts(&reports0, &pressed0);
    vTaskDelay(pdMS_TO_TICKS(IDLE_MS));
    hw_touch_counts(&reports, &pressed);
    printf("@R {\"touch_idle\":{\"ms\":%d,\"reports\":%lu,\"pressed\":%lu,\"samples\":%d}}\n", IDLE_MS,
           (unsigned long)(reports - reports0), (unsigned long)(pressed - pressed0), s_samples);

    /* One read every touch period. LVGL starts each period when the read
     * runs, so a read can slip by a tick: 90 % of them is the floor. */
    TEST_ASSERT_GREATER_OR_EQUAL_UINT32_MESSAGE(IDLE_MS / LCD349_TOUCH_PERIOD_MS * 9 / 10, reports - reports0,
                                                "reports read in 2 s");
    TEST_ASSERT_EQUAL_UINT32_MESSAGE(0, pressed - pressed0, "reports that decoded as pressed");
    TEST_ASSERT_EQUAL_MESSAGE(0, s_samples, "touch samples");
}
