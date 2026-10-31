/* SPDX-License-Identifier: Apache-2.0
 * The display and backlight on the board (seam S1-4). The display lane adds
 * its tests here, each TEST_CASE tagged "[display]".
 */
#include <stdio.h>
#include <stdlib.h>

#include "freertos/FreeRTOS.h"
#include "freertos/task.h"
#include "lcd349.h"
#include "unity.h"
#include "unity_test_runner.h"

/* The backend's flush timing, a test hook in src/hw/display.c: how many
 * frames be_panel_flush has sent to the panel, and how long the last one
 * took, in microseconds. */
void hw_display_flush_timing(uint32_t *frames, uint32_t *last_us);
/* Another: the backlight PWM's duty now, and the duty that is fully lit. */
void hw_display_backlight_duty(uint32_t *duty, uint32_t *full);

#define FLUSHES 100
#define FRAME_WAIT_MS 200

/* lcd349_init (the harness runs it before the tests) brings up the panel and
 * the backlight, and LVGL gets a display for it. */
TEST_CASE("test_display_ok", "[display]")
{
    TEST_ASSERT_EQUAL_HEX32(0, lcd349_board_status() & LCD349_SUB_DISPLAY);
    TEST_ASSERT_NOT_NULL(lcd349_display());
}

static int cmp_u32(const void *a, const void *b)
{
    uint32_t x = *(const uint32_t *)a, y = *(const uint32_t *)b;
    return x < y ? -1 : x > y;
}

/* Each frame is a new solid colour, rendered and flushed by lcd349_ui as a
 * product's would be (requirements 1.3, 1.4; P1-N1). */
TEST_CASE("test_flush_within_budget", "[display]")
{
    static uint32_t us[FLUSHES];
    lcd349_ui_lock();
    lv_obj_t *prev = lv_screen_active();
    lv_obj_t *scr = lv_obj_create(NULL);
    lv_screen_load(scr);
    lcd349_ui_unlock();

    uint32_t frames = 0, last = 0;
    hw_display_flush_timing(&frames, &last);
    for (int i = 0; i < FLUSHES; i++) {
        lcd349_ui_lock();
        lv_obj_set_style_bg_color(scr, lv_color_hsv_to_rgb((uint16_t)(i * 37 % 360), 100, 100), 0);
        lcd349_ui_unlock();
        uint32_t before = frames;
        for (int ms = 0; ms < FRAME_WAIT_MS && frames == before; ms++) {
            vTaskDelay(pdMS_TO_TICKS(1));
            hw_display_flush_timing(&frames, &last);
        }
        TEST_ASSERT_NOT_EQUAL_MESSAGE(before, frames, "the backend sent no frame within 200 ms");
        us[i] = last;
    }

    lcd349_ui_lock();
    lv_screen_load(prev);
    lv_obj_delete(scr);
    lcd349_ui_unlock();

    uint64_t sum = 0;
    for (int i = 0; i < FLUSHES; i++) {
        sum += us[i];
    }
    qsort(us, FLUSHES, sizeof us[0], cmp_u32);
    uint32_t mean = (uint32_t)(sum / FLUSHES), p95 = us[FLUSHES * 95 / 100];
    printf("@R {\"flush\":{\"n\":%d,\"mean_us\":%lu,\"p95_us\":%lu,\"min_us\":%lu,\"max_us\":%lu}}\n", FLUSHES,
           (unsigned long)mean, (unsigned long)p95, (unsigned long)us[0], (unsigned long)us[FLUSHES - 1]);
    TEST_ASSERT_LESS_OR_EQUAL_UINT32(15000, mean);
    TEST_ASSERT_LESS_OR_EQUAL_UINT32(16000, p95);
}

/* Requirement 1.6. The PWM's duty follows within 1 % of full scale, and 0 is
 * dark. The level the test found is set again at the end: touch is read only
 * while the backlight is on. */
TEST_CASE("test_backlight_round_trip", "[display]")
{
    const uint8_t before = lcd349_backlight_get();
    const uint8_t levels[] = {0, 37, 100};
    for (size_t i = 0; i < sizeof levels / sizeof levels[0]; i++) {
        char msg[16];
        snprintf(msg, sizeof msg, "at %u %%", levels[i]);
        TEST_ASSERT_EQUAL_MESSAGE(LCD349_OK, lcd349_backlight_set(levels[i]), msg);
        TEST_ASSERT_EQUAL_UINT8_MESSAGE(levels[i], lcd349_backlight_get(), msg);
        /* The PWM takes a new duty at its next period. */
        vTaskDelay(pdMS_TO_TICKS(2));
        uint32_t duty = 0, full = 0;
        hw_display_backlight_duty(&duty, &full);
        TEST_ASSERT_UINT32_WITHIN_MESSAGE(full / 100, levels[i] * full / 100, duty, msg);
        if (levels[i] == 0) {
            TEST_ASSERT_EQUAL_UINT32_MESSAGE(0, duty, msg);
        }
    }
    TEST_ASSERT_EQUAL(LCD349_ERR_ARG, lcd349_backlight_set(101));
    TEST_ASSERT_EQUAL(LCD349_ERR_ARG, lcd349_backlight_set(255));
    TEST_ASSERT_EQUAL_UINT8(100, lcd349_backlight_get());
    TEST_ASSERT_EQUAL(LCD349_OK, lcd349_backlight_set(before));
}
