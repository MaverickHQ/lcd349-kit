/* SPDX-License-Identifier: Apache-2.0
 * Power on the board (seam S1-4). The power lane adds its tests here, each
 * TEST_CASE tagged "[power]".
 */
#include "expander.h"
#include "freertos/FreeRTOS.h"
#include "freertos/task.h"
#include "lcd349.h"
#include "unity.h"
#include "unity_test_runner.h"

/* lcd349_init (the harness runs it before the tests) holds power on P6 and
 * enables the backlight's boost on P1, by register (kit ADR 0001). */
TEST_CASE("test_init_reports_power_ok", "[power]")
{
    TEST_ASSERT_EQUAL_HEX32(0, lcd349_board_status() & LCD349_SUB_POWER);

    const uint8_t p6_p1 = (1u << LCD349_EXPANDER_PIN_SYS_EN) | (1u << LCD349_EXPANDER_PIN_BL_EN);
    uint8_t out = 0, cfg = 0xFF;
    TEST_ASSERT_EQUAL(LCD349_OK, hw_expander_read(HW_EXPANDER_REG_OUTPUT, &out));
    TEST_ASSERT_EQUAL(LCD349_OK, hw_expander_read(HW_EXPANDER_REG_CONFIG, &cfg));
    TEST_ASSERT_EQUAL_HEX8(p6_p1, out & p6_p1); /* driven high */
    TEST_ASSERT_EQUAL_HEX8(0, cfg & p6_p1);     /* as outputs */
}

/* The harness runs on the desk's USB, which keeps the board running when the
 * latch is released (requirement 1.7). */
TEST_CASE("test_power_off_on_usb_returns_on_usb", "[power]")
{
    uint32_t t0 = lcd349_time_ms();
    TEST_ASSERT_EQUAL(LCD349_ERR_ON_USB, lcd349_power_off());
    uint32_t ms = lcd349_time_ms() - t0;
    TEST_ASSERT_GREATER_OR_EQUAL_UINT32(LCD349_POWER_OFF_USB_CHECK_MS, ms); /* still running 1 s later */
    TEST_ASSERT_LESS_OR_EQUAL_UINT32(1500, ms);

    /* The hold is asserted again. */
    uint8_t out = 0;
    TEST_ASSERT_EQUAL(LCD349_OK, hw_expander_read(HW_EXPANDER_REG_OUTPUT, &out));
    TEST_ASSERT_BITS_HIGH(1u << LCD349_EXPANDER_PIN_SYS_EN, out);
}

/* On the desk's USB the board keeps running with the hold released, so the
 * latch reads back both ways. Released is P6 driven low, still an output,
 * with every other pin as it was; P7 is left out, because a tone task drives
 * it on its own. (Added in the lane's review: the power-off test passes even
 * if releasing does nothing.) */
TEST_CASE("test_power_hold_drives_p6", "[power]")
{
    const uint8_t p6 = 1u << LCD349_EXPANDER_PIN_SYS_EN;
    const uint8_t others = (uint8_t)~(p6 | (1u << LCD349_EXPANDER_PIN_NS_MODE));
    uint8_t out0 = 0, cfg0 = 0, out = 0, cfg = 0;
    TEST_ASSERT_EQUAL(LCD349_OK, hw_expander_read(HW_EXPANDER_REG_OUTPUT, &out0));
    TEST_ASSERT_EQUAL(LCD349_OK, hw_expander_read(HW_EXPANDER_REG_CONFIG, &cfg0));

    TEST_ASSERT_EQUAL(LCD349_OK, lcd349_power_hold(false));
    lcd349_err_t read_out = hw_expander_read(HW_EXPANDER_REG_OUTPUT, &out);
    lcd349_err_t read_cfg = hw_expander_read(HW_EXPANDER_REG_CONFIG, &cfg);
    TEST_ASSERT_EQUAL(LCD349_OK, lcd349_power_hold(true)); /* held again before any check */
    TEST_ASSERT_EQUAL(LCD349_OK, read_out);
    TEST_ASSERT_EQUAL(LCD349_OK, read_cfg);
    TEST_ASSERT_BITS_LOW(p6, out); /* released */
    TEST_ASSERT_BITS_LOW(p6, cfg); /* still an output */
    TEST_ASSERT_EQUAL_HEX8(out0 & others, out & others);
    TEST_ASSERT_EQUAL_HEX8(cfg0 & others, cfg & others);

    TEST_ASSERT_EQUAL(LCD349_OK, hw_expander_read(HW_EXPANDER_REG_OUTPUT, &out));
    TEST_ASSERT_BITS_HIGH(p6, out); /* held */
}

/* The desk's USB is a host (requirement 1.8, D1-10). The kit asks the backend
 * once a second, so 2.5 s of calls span at least two of its checks. */
TEST_CASE("test_power_source_on_usb_is_usb", "[power]")
{
    for (int i = 0; i < 6; i++) {
        TEST_ASSERT_EQUAL(LCD349_POWER_SOURCE_USB, lcd349_power_source());
        vTaskDelay(pdMS_TO_TICKS(500));
    }
}
