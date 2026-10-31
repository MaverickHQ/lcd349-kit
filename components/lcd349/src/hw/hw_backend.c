/* SPDX-License-Identifier: Apache-2.0
 *
 * Hardware backend (esp32s3): what isn't one subsystem's. Each subsystem
 * has its own file with its be_* functions (power.c, display.c, touch.c,
 * buttons.c, battery.c, audio.c, rtc.c, kv.c, console.c, wifi.c, sleep.c);
 * until its lane implements it, a subsystem reports LCD349_ERR_UNAVAILABLE,
 * which the SDK handles like any failed subsystem (lcd349_board_status()).
 * This file keeps the start-up hooks, the boot reason, the clock and the two
 * I2C buses.
 */
#include "esp_log.h"
#include "esp_system.h"
#include "esp_timer.h"

#include "hw.h"

static const char *TAG = "lcd349_hw";

/* ---- I2C buses ---- */

static i2c_master_bus_handle_t s_main_bus, s_touch_bus;
static bool s_buses_tried;

static i2c_master_bus_handle_t new_bus(i2c_port_num_t port, int sda, int scl, const char *name)
{
    i2c_master_bus_config_t bc = {
        .i2c_port = port,
        .sda_io_num = sda,
        .scl_io_num = scl,
        .clk_source = I2C_CLK_SRC_DEFAULT,
        .glitch_ignore_cnt = 7,
        .flags.enable_internal_pullup = true,
    };
    i2c_master_bus_handle_t bus = NULL;
    esp_err_t err = i2c_new_master_bus(&bc, &bus);
    if (err != ESP_OK) {
        ESP_LOGE(TAG, "%s I2C bus: %s", name, esp_err_to_name(err));
        return NULL;
    }
    return bus;
}

/* Creating a bus doesn't touch any device on it, so the expander (and with
 * it the power latch) is left as it is (kit ADR 0001). */
static void buses_up(void)
{
    if (s_buses_tried) {
        return;
    }
    s_buses_tried = true;
    s_main_bus = new_bus(I2C_NUM_0, LCD349_PIN_I2C_SDA, LCD349_PIN_I2C_SCL, "main");
    s_touch_bus = new_bus(I2C_NUM_1, LCD349_PIN_TOUCH_SDA, LCD349_PIN_TOUCH_SCL, "touch");
}

i2c_master_bus_handle_t hw_i2c_main(void)
{
    buses_up();
    return s_main_bus;
}

i2c_master_bus_handle_t hw_i2c_touch(void)
{
    buses_up();
    return s_touch_bus;
}

/* ---- Start-up, boot reason, time ---- */

void be_started(void) {}

void be_ui_poll(void) {}

lcd349_boot_reason_t be_boot_reason(void)
{
    switch (esp_reset_reason()) {
    case ESP_RST_POWERON: return LCD349_BOOT_POWER_ON;
    case ESP_RST_SW: return LCD349_BOOT_SOFTWARE;
    case ESP_RST_PANIC:
    case ESP_RST_INT_WDT:
    case ESP_RST_TASK_WDT:
    case ESP_RST_WDT: return LCD349_BOOT_CRASH;
    case ESP_RST_BROWNOUT: return LCD349_BOOT_BROWNOUT;
    default: return LCD349_BOOT_OTHER;
    }
}

uint64_t be_time_us(void)
{
    return (uint64_t)esp_timer_get_time();
}
