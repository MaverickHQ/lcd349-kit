/* SPDX-License-Identifier: Apache-2.0
 * Audio on the board (seam S1-4). The audio lane adds its tests here, each
 * TEST_CASE tagged "[audio]".
 */
#include "expander.h"
#include "freertos/FreeRTOS.h"
#include "freertos/task.h"
#include "lcd349.h"
#include "unity.h"
#include "unity_test_runner.h"

/* P7's level on the pin (the input register), whoever drives it: the
 * NS_MODE line has a pull-down, so it reads high only while the expander
 * drives it high. */
static bool amp_pin_high(void)
{
    uint8_t in = 0;
    TEST_ASSERT_EQUAL(LCD349_OK, hw_expander_read(HW_EXPANDER_REG_INPUT, &in));
    return (in >> LCD349_EXPANDER_PIN_NS_MODE) & 1u;
}

static void wait_until(uint32_t t0, uint32_t ms)
{
    while (lcd349_time_ms() - t0 < ms) {
        vTaskDelay(1);
    }
}

/* The NS4150B amplifier (TCA9554 P7) is on only while a tone plays
 * (requirement 3.2, D1-5). */
TEST_CASE("test_tone_gates_the_amplifier", "[audio]")
{
    const uint16_t ms = 300;
    TEST_ASSERT_FALSE(amp_pin_high()); /* off at rest */

    uint32_t t0 = lcd349_time_ms();
    TEST_ASSERT_EQUAL(LCD349_OK, lcd349_tone(880, ms, 30));
    wait_until(t0, ms / 2);
    TEST_ASSERT_TRUE(amp_pin_high()); /* on during the tone */
    wait_until(t0, ms + 100);
    TEST_ASSERT_FALSE(amp_pin_high()); /* off 100 ms after it ends */
}

/* Waits up to 2.5 s for the amplifier to go off, so a test starts at rest
 * even after one that failed mid-tone. */
static void settle(void)
{
    uint32_t t0 = lcd349_time_ms();
    while (amp_pin_high() && lcd349_time_ms() - t0 < 2500) {
        vTaskDelay(pdMS_TO_TICKS(10));
    }
    TEST_ASSERT_FALSE(amp_pin_high()); /* off at rest */
}

/* A stop ends the tone, and the amplifier goes off with it (lcd349_audio.h,
 * requirement 3.2). */
TEST_CASE("test_tone_stop_turns_the_amplifier_off", "[audio]")
{
    settle();
    uint32_t t0 = lcd349_time_ms();
    TEST_ASSERT_EQUAL(LCD349_OK, lcd349_tone(880, 1000, 30));
    wait_until(t0, 150);
    TEST_ASSERT_TRUE(amp_pin_high()); /* on during the tone */

    uint32_t t1 = lcd349_time_ms();
    TEST_ASSERT_EQUAL(LCD349_OK, lcd349_tone_stop());
    wait_until(t1, 100);
    TEST_ASSERT_FALSE(amp_pin_high()); /* off 100 ms after the stop; the tone had 750 ms left */
}

/* A new tone replaces the one playing: the amplifier stays on for the new
 * tone and goes off when it ends, not when the first would have
 * (lcd349_audio.h, requirement 3.2). */
TEST_CASE("test_new_tone_replaces_the_playing_one", "[audio]")
{
    const uint16_t ms = 300;
    settle();
    uint32_t t0 = lcd349_time_ms();
    TEST_ASSERT_EQUAL(LCD349_OK, lcd349_tone(880, 1000, 30));
    wait_until(t0, 150);

    uint32_t t1 = lcd349_time_ms();
    TEST_ASSERT_EQUAL(LCD349_OK, lcd349_tone(660, ms, 30));
    wait_until(t1, ms / 2);
    TEST_ASSERT_TRUE(amp_pin_high()); /* on during the new tone */
    wait_until(t1, ms + 100);
    TEST_ASSERT_FALSE(amp_pin_high()); /* off 100 ms after it ends; the first had 450 ms left */
}
