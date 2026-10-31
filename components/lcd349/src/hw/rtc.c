/* SPDX-License-Identifier: Apache-2.0
 *
 * RTC: a PCF85063 driver written from the datasheet (requirement 3.3). The
 * time is UTC in registers 0x04-0x0A (seconds, minutes, hours, days,
 * weekdays, months, years), BCD, 24-hour mode, two-digit years taken as
 * 2000-2099. Setting it stops the clock (Control_1 STOP) while the time is
 * written, so it can't tick between the registers.
 */
#include <time.h>

#include "esp_log.h"

#include "hw.h"

#define RTC_HZ 400000
#define RTC_TIMEOUT_MS 50

#define REG_CTRL1 0x00
#define CTRL1_STOP 0x20
#define CTRL1_12_24 0x02 /* set: 12-hour mode */
#define REG_TIME 0x04    /* seconds; then 0x05-0x0A */
#define SECONDS_OS 0x80  /* the oscillator stopped: the time isn't valid */

#define UNIX_2000 946684800LL
#define UNIX_2100 4102444800LL

static const char *TAG = "lcd349_rtc";
static i2c_master_dev_handle_t s_dev;
static uint8_t s_ctrl1; /* Control_1 while running: 24-hour mode, not stopped */

static esp_err_t rtc_read(uint8_t reg, uint8_t *buf, size_t n)
{
    return i2c_master_transmit_receive(s_dev, &reg, 1, buf, n, RTC_TIMEOUT_MS);
}

static esp_err_t rtc_write(const uint8_t *reg_then_data, size_t n)
{
    return i2c_master_transmit(s_dev, reg_then_data, n, RTC_TIMEOUT_MS);
}

static esp_err_t write_ctrl1(uint8_t v)
{
    uint8_t w[2] = {REG_CTRL1, v};
    return rtc_write(w, sizeof w);
}

static uint8_t to_bcd(int v) { return (uint8_t)(((v / 10) << 4) | (v % 10)); }
static int from_bcd(uint8_t b) { return (b >> 4) * 10 + (b & 0x0f); }

/* Days from 1970-01-01 to y-m-d in the proleptic Gregorian calendar (the
 * usual era arithmetic: 400-year eras of 146097 days, years from March). */
static int64_t days_from_civil(int y, int m, int d)
{
    y -= m <= 2;
    int era = (y >= 0 ? y : y - 399) / 400;
    int yoe = y - era * 400;
    int doy = (153 * (m + (m > 2 ? -3 : 9)) + 2) / 5 + d - 1;
    int doe = yoe * 365 + yoe / 4 - yoe / 100 + doy;
    return (int64_t)era * 146097 + doe - 719468;
}

lcd349_err_t be_rtc_init(void)
{
    i2c_master_bus_handle_t bus = hw_i2c_main();
    if (!bus) {
        return LCD349_ERR_IO;
    }
    i2c_device_config_t dc = {
        .dev_addr_length = I2C_ADDR_BIT_LEN_7,
        .device_address = LCD349_RTC_I2C_ADDR,
        .scl_speed_hz = RTC_HZ,
    };
    esp_err_t err = i2c_master_bus_add_device(bus, &dc, &s_dev);
    uint8_t regs[5] = {0}; /* Control_1 .. Seconds */
    if (err == ESP_OK) {
        err = rtc_read(REG_CTRL1, regs, sizeof regs);
    }
    if (err == ESP_OK && (regs[0] & (CTRL1_STOP | CTRL1_12_24))) {
        err = write_ctrl1(regs[0] & ~(CTRL1_STOP | CTRL1_12_24));
    }
    if (err != ESP_OK) {
        ESP_LOGE(TAG, "PCF85063 at 0x%02x: %s", LCD349_RTC_I2C_ADDR, esp_err_to_name(err));
        return LCD349_ERR_IO;
    }
    s_ctrl1 = regs[0] & ~(CTRL1_STOP | CTRL1_12_24);
    if (regs[4] & SECONDS_OS) {
        ESP_LOGW(TAG, "the clock stopped (lost power): its time isn't valid until set");
    }
    return LCD349_OK;
}

lcd349_err_t be_rtc_get(int64_t *unix_s)
{
    uint8_t r[7];
    esp_err_t err = rtc_read(REG_TIME, r, sizeof r);
    if (err != ESP_OK) {
        ESP_LOGW(TAG, "read: %s", esp_err_to_name(err));
        return LCD349_ERR_IO;
    }
    int sec = from_bcd(r[0] & 0x7f), min = from_bcd(r[1] & 0x7f), hour = from_bcd(r[2] & 0x3f);
    int day = from_bcd(r[3] & 0x3f), mon = from_bcd(r[5] & 0x1f), year = 2000 + from_bcd(r[6]);
    if (sec > 59 || min > 59 || hour > 23 || day < 1 || day > 31 || mon < 1 || mon > 12) {
        ESP_LOGW(TAG, "read an impossible time %02x %02x %02x %02x %02x %02x", r[6], r[5], r[3], r[2], r[1], r[0]);
        return LCD349_ERR_IO;
    }
    *unix_s = days_from_civil(year, mon, day) * 86400 + hour * 3600 + min * 60 + sec;
    return LCD349_OK;
}

lcd349_err_t be_rtc_set(int64_t unix_s)
{
    if (unix_s < UNIX_2000 || unix_s >= UNIX_2100) {
        return LCD349_ERR_ARG; /* two-digit years */
    }
    time_t t = (time_t)unix_s;
    struct tm tm;
    gmtime_r(&t, &tm);
    uint8_t w[8] = {
        REG_TIME,
        to_bcd(tm.tm_sec), /* OS cleared: the time is valid again */
        to_bcd(tm.tm_min),
        to_bcd(tm.tm_hour),
        to_bcd(tm.tm_mday),
        (uint8_t)tm.tm_wday,
        to_bcd(tm.tm_mon + 1),
        to_bcd(tm.tm_year - 100),
    };
    esp_err_t err = write_ctrl1(s_ctrl1 | CTRL1_STOP);
    if (err == ESP_OK) {
        err = rtc_write(w, sizeof w);
    }
    esp_err_t run = write_ctrl1(s_ctrl1); /* always: never leave the clock stopped */
    if (err == ESP_OK) {
        err = run;
    }
    if (err != ESP_OK) {
        ESP_LOGW(TAG, "set: %s", esp_err_to_name(err));
        return LCD349_ERR_IO;
    }
    return LCD349_OK;
}
