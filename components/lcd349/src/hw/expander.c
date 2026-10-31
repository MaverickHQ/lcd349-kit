/* SPDX-License-Identifier: Apache-2.0
 *
 * The TCA9554 register helper (expander.h). One mutex covers each
 * read-modify-write, so the power latch (P6), BL_EN (P1) and the amplifier
 * (P7), driven from different tasks, never undo each other's bits.
 */
#include "esp_log.h"
#include "freertos/FreeRTOS.h"
#include "freertos/semphr.h"

#include "expander.h"

static const char *TAG = "lcd349_exp";

#define EXPANDER_I2C_HZ 400000
#define EXPANDER_TIMEOUT_MS 50

static i2c_master_dev_handle_t s_dev;
static SemaphoreHandle_t s_lock;
static StaticSemaphore_t s_lock_buf;
static bool s_tried;

/* be_power_init makes the first call, on lcd349_init's thread, before
 * anything else uses the expander. Adding the device doesn't touch the chip. */
static bool expander_up(void)
{
    if (s_tried) {
        return s_dev != NULL;
    }
    s_tried = true;
    s_lock = xSemaphoreCreateMutexStatic(&s_lock_buf);
    i2c_master_bus_handle_t bus = hw_i2c_main();
    if (!bus) {
        return false;
    }
    i2c_device_config_t dc = {
        .dev_addr_length = I2C_ADDR_BIT_LEN_7,
        .device_address = LCD349_EXPANDER_I2C_ADDR,
        .scl_speed_hz = EXPANDER_I2C_HZ,
    };
    esp_err_t err = i2c_master_bus_add_device(bus, &dc, &s_dev);
    if (err != ESP_OK) {
        ESP_LOGE(TAG, "add device: %s", esp_err_to_name(err));
        s_dev = NULL;
    }
    return s_dev != NULL;
}

static lcd349_err_t reg_read(uint8_t reg, uint8_t *val)
{
    esp_err_t err = i2c_master_transmit_receive(s_dev, &reg, 1, val, 1, EXPANDER_TIMEOUT_MS);
    if (err != ESP_OK) {
        ESP_LOGE(TAG, "read 0x%02x: %s", reg, esp_err_to_name(err));
        return LCD349_ERR_IO;
    }
    return LCD349_OK;
}

static lcd349_err_t reg_write(uint8_t reg, uint8_t val)
{
    uint8_t b[2] = {reg, val};
    esp_err_t err = i2c_master_transmit(s_dev, b, sizeof b, EXPANDER_TIMEOUT_MS);
    if (err != ESP_OK) {
        ESP_LOGE(TAG, "write 0x%02x: %s", reg, esp_err_to_name(err));
        return LCD349_ERR_IO;
    }
    return LCD349_OK;
}

lcd349_err_t hw_expander_read(uint8_t reg, uint8_t *val)
{
    if (!expander_up()) {
        return LCD349_ERR_UNAVAILABLE;
    }
    xSemaphoreTake(s_lock, portMAX_DELAY);
    lcd349_err_t err = reg_read(reg, val);
    xSemaphoreGive(s_lock);
    return err;
}

lcd349_err_t hw_expander_out(uint8_t pin, bool high)
{
    if (pin > 7) {
        return LCD349_ERR_ARG;
    }
    if (!expander_up()) {
        return LCD349_ERR_UNAVAILABLE;
    }
    const uint8_t bit = (uint8_t)(1u << pin);
    uint8_t out, cfg;
    xSemaphoreTake(s_lock, portMAX_DELAY);
    /* The level first: a pin that is still an input then starts driving at
     * the right level (P6 never drops on the way). */
    lcd349_err_t err = reg_read(HW_EXPANDER_REG_OUTPUT, &out);
    if (err == LCD349_OK) {
        err = reg_write(HW_EXPANDER_REG_OUTPUT, high ? out | bit : out & ~bit);
    }
    if (err == LCD349_OK) {
        err = reg_read(HW_EXPANDER_REG_CONFIG, &cfg);
    }
    if (err == LCD349_OK && (cfg & bit)) {
        err = reg_write(HW_EXPANDER_REG_CONFIG, cfg & ~bit);
    }
    xSemaphoreGive(s_lock);
    return err;
}
