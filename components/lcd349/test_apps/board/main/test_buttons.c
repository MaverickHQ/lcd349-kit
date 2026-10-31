/* SPDX-License-Identifier: Apache-2.0
 * Buttons on the board (seam S1-4). The io lane adds its tests here, each
 * TEST_CASE tagged "[buttons]".
 */
#include "driver/gpio.h"
#include "freertos/FreeRTOS.h"
#include "freertos/task.h"
#include "kit.h"
#include "lcd349.h"
#include "unity.h"
#include "unity_test_runner.h"

static volatile int s_events;

static void count_event(const lcd349_btn_event_t *ev, void *ctx)
{
    (void)ev;
    (void)ctx;
    s_events++;
}

/* Nobody touches the board during a run: both buttons rest released
 * (requirement 2.8: active low, so the pull-ups hold them high). */
TEST_CASE("test_buttons_released_at_rest", "[buttons]")
{
    TEST_ASSERT_EQUAL_HEX32_MESSAGE(0, lcd349_board_status() & LCD349_SUB_BUTTONS, "buttons came up");
    TEST_ASSERT_FALSE_MESSAGE(be_button_raw(LCD349_BTN_BOOT), "BOOT raw level");
    TEST_ASSERT_FALSE_MESSAGE(be_button_raw(LCD349_BTN_PWR), "PWR raw level");

    s_events = 0;
    TEST_ASSERT_EQUAL(LCD349_OK, lcd349_buttons_subscribe(count_event, NULL));
    vTaskDelay(pdMS_TO_TICKS(1000));
    TEST_ASSERT_EQUAL_MESSAGE(0, s_events, "button events at rest");
    TEST_ASSERT_FALSE(lcd349_button_is_down(LCD349_BTN_BOOT));
    TEST_ASSERT_FALSE(lcd349_button_is_down(LCD349_BTN_PWR));
}

static volatile int s_boot_downs, s_boot_ups;

static void count_boot(const lcd349_btn_event_t *ev, void *ctx)
{
    (void)ctx;
    if (ev->btn == LCD349_BTN_BOOT) {
        if (ev->down) {
            s_boot_downs++;
        } else {
            s_boot_ups++;
        }
    }
}

/* A BOOT press made without a finger: GPIO0's own open-drain output pulls
 * the pin low, as the button does, and lets it go. It comes through the
 * edge interrupt, the debounce and the events as one down and one up.
 * Open drain never drives the pin high, so a real press at the same time
 * does no harm. Only BOOT: PWR's wiring beyond GPIO16 isn't recorded
 * (spikes/hw/RESULTS.md), so the test leaves it alone. The pin is back as an
 * input before anything is asserted. */
TEST_CASE("test_boot_press_reaches_subscribers", "[buttons]")
{
    s_boot_downs = s_boot_ups = 0;
    TEST_ASSERT_EQUAL(LCD349_OK, lcd349_buttons_subscribe(count_boot, NULL));

    gpio_set_level(LCD349_PIN_BOOT, 0);
    gpio_set_direction(LCD349_PIN_BOOT, GPIO_MODE_INPUT_OUTPUT_OD);
    vTaskDelay(pdMS_TO_TICKS(100));
    bool raw_down = be_button_raw(LCD349_BTN_BOOT), down = lcd349_button_is_down(LCD349_BTN_BOOT);
    int downs = s_boot_downs, ups = s_boot_ups;
    gpio_set_level(LCD349_PIN_BOOT, 1);
    gpio_set_direction(LCD349_PIN_BOOT, GPIO_MODE_INPUT);
    vTaskDelay(pdMS_TO_TICKS(100));

    TEST_ASSERT_TRUE_MESSAGE(raw_down, "BOOT raw level while pulled low");
    TEST_ASSERT_TRUE_MESSAGE(down, "BOOT down while pulled low");
    TEST_ASSERT_EQUAL_MESSAGE(1, downs, "down events while pulled low");
    TEST_ASSERT_EQUAL_MESSAGE(0, ups, "up events while pulled low");
    TEST_ASSERT_FALSE_MESSAGE(be_button_raw(LCD349_BTN_BOOT), "BOOT raw level after letting go");
    TEST_ASSERT_FALSE_MESSAGE(lcd349_button_is_down(LCD349_BTN_BOOT), "BOOT down after letting go");
    TEST_ASSERT_EQUAL_MESSAGE(1, s_boot_downs, "down events in all");
    TEST_ASSERT_EQUAL_MESSAGE(1, s_boot_ups, "up events in all");
}
