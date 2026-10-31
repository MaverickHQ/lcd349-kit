/* SPDX-License-Identifier: Apache-2.0
 *
 * Touch: the AXS15231B's report read over its own I2C bus and decoded by
 * axs_touch_decode (kit ADR 0002), polled (kit ADR 0003). The board tests
 * read the backend's read counts through a hook at the end of this file.
 */
#include <string.h>

#include "esp_log.h"
#include "freertos/FreeRTOS.h"

#include "axs_touch.h"
#include "hw.h"

#define TOUCH_HZ 300000 /* requirement 2.1 */
#define TOUCH_TIMEOUT_MS 20

static const char *TAG = "lcd349_touch";
static i2c_master_dev_handle_t s_dev;
/* The decoder's state, on lcd349_ui only. */
static axs_touch_state_t s_state;
static bool s_read_failing;
/* The read counts, written on lcd349_ui and read by the test hook. */
static portMUX_TYPE s_counts_lock = portMUX_INITIALIZER_UNLOCKED;
static uint32_t s_reports, s_pressed;

/* The read command asks for a report of AXS_TOUCH_REPORT_LEN bytes (its byte
 * 7), then the report is read (spikes/hw/RESULTS.md §Touch). */
static esp_err_t read_report(uint8_t report[AXS_TOUCH_REPORT_LEN])
{
    static const uint8_t cmd[11] = {0xb5, 0xab, 0xa5, 0x5a, 0, 0, 0, AXS_TOUCH_REPORT_LEN, 0, 0, 0};
    esp_err_t err = i2c_master_transmit(s_dev, cmd, sizeof cmd, TOUCH_TIMEOUT_MS);
    if (err == ESP_OK) {
        err = i2c_master_receive(s_dev, report, AXS_TOUCH_REPORT_LEN, TOUCH_TIMEOUT_MS);
    }
    return err;
}

/* The controller must answer a read: a report or the no-report pattern. */
lcd349_err_t be_touch_init(void)
{
    i2c_master_bus_handle_t bus = hw_i2c_touch();
    if (!bus) {
        return LCD349_ERR_IO;
    }
    i2c_device_config_t dc = {
        .dev_addr_length = I2C_ADDR_BIT_LEN_7,
        .device_address = LCD349_TOUCH_I2C_ADDR,
        .scl_speed_hz = TOUCH_HZ,
    };
    esp_err_t err = i2c_master_bus_add_device(bus, &dc, &s_dev);
    uint8_t report[AXS_TOUCH_REPORT_LEN];
    if (err == ESP_OK) {
        err = read_report(report);
    }
    if (err != ESP_OK) {
        ESP_LOGE(TAG, "AXS15231B touch at 0x%02x: %s", LCD349_TOUCH_I2C_ADDR, esp_err_to_name(err));
        return LCD349_ERR_IO;
    }
    return LCD349_OK;
}

/* A read that fails returns no report, so it counts towards the three silent
 * reads that release (requirement 2.4): a dead bus can't hold a touch down. */
bool be_touch_read(uint16_t *px, uint16_t *py)
{
    uint8_t report[AXS_TOUCH_REPORT_LEN];
    esp_err_t err = read_report(report);
    if (err != ESP_OK) {
        if (!s_read_failing) {
            ESP_LOGW(TAG, "read: %s", esp_err_to_name(err));
        }
        memset(report, 0x30, sizeof report);
    }
    s_read_failing = err != ESP_OK;
    axs_touch_point_t p = axs_touch_decode(report, &s_state);
    taskENTER_CRITICAL(&s_counts_lock);
    s_reports += err == ESP_OK;
    s_pressed += err == ESP_OK && p.pressed;
    taskEXIT_CRITICAL(&s_counts_lock);
    *px = p.px;
    *py = p.py;
    return p.pressed;
}

/* A test hook (test_apps/board/main/test_touch.c): the reports read, and how
 * many decoded as pressed. */
void hw_touch_counts(uint32_t *reports, uint32_t *pressed);

void hw_touch_counts(uint32_t *reports, uint32_t *pressed)
{
    taskENTER_CRITICAL(&s_counts_lock);
    *reports = s_reports;
    *pressed = s_pressed;
    taskEXIT_CRITICAL(&s_counts_lock);
}
