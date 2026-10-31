/* SPDX-License-Identifier: Apache-2.0
 * The harness's own check (seam S1-4): a test runs on the board after
 * lcd349_init, and its result reaches the host.
 */
#include "esp_attr.h"
#include "esp_system.h"
#include "freertos/FreeRTOS.h"
#include "freertos/task.h"
#include "lcd349.h"
#include "unity.h"
#include "unity_test_runner.h"

TEST_CASE("test_board_clock_advances", "[harness]")
{
    uint64_t us0 = lcd349_time_us();
    uint32_t ms0 = lcd349_time_ms();
    vTaskDelay(pdMS_TO_TICKS(100));
    uint32_t us = (uint32_t)(lcd349_time_us() - us0);
    uint32_t ms = lcd349_time_ms() - ms0;
    /* A 100 ms delay at a 1 kHz tick (99-100 ms: it ends on the 100th tick
     * boundary): the kit's clock moves with it. */
    TEST_ASSERT_UINT32_WITHIN(5, 100, ms);
    TEST_ASSERT_UINT32_WITHIN(5000, 100000, us);
}

/* A multi-stage test runs across a reboot: the harness sends the second
 * stage after the restart (tasks 5.4 and 7.3 rely on it). RTC memory keeps
 * the mark through the restart, and through the reset reopening the port
 * may cause. */
#define RESTART_MARK 0x4C333439u

static RTC_NOINIT_ATTR uint32_t s_restart_mark;

static void mark_and_restart(void)
{
    s_restart_mark = RESTART_MARK;
    esp_restart();
}

static void mark_survived(void)
{
    uint32_t mark = s_restart_mark;
    s_restart_mark = 0;
    TEST_ASSERT_EQUAL_HEX32(RESTART_MARK, mark);
}

TEST_CASE_MULTIPLE_STAGES("test_board_resumes_after_restart", "[harness]", mark_and_restart, mark_survived);
