/* SPDX-License-Identifier: Apache-2.0
 *
 * Display: the AXS15231B panel over QSPI, the landscape frame rotated in
 * strips into two DMA buffers (design D1-3), and the LEDC backlight. The
 * backlight's boost (BL_EN, expander P1) is enabled by be_power_init. The
 * board tests read the flush timing and the backlight's duty through two
 * hooks at the end of this file.
 */
#include "driver/gpio.h"
#include "driver/ledc.h"
#include "driver/spi_master.h"
#include "esp_attr.h"
#include "esp_heap_caps.h"
#include "esp_lcd_axs15231b.h"
#include "esp_lcd_panel_io.h"
#include "esp_lcd_panel_ops.h"
#include "esp_log.h"
#include "esp_timer.h"
#include "freertos/FreeRTOS.h"
#include "freertos/semphr.h"
#include "freertos/task.h"

#include "hw.h"
#include "rotate.h"

static const char *TAG = "lcd349_disp";

#define LCD_HOST SPI3_HOST
#define STRIP_ROWS 64 /* panel rows per QSPI transfer: 640 / 64 = 10 strips */
#define STRIP_PX (LCD349_PANEL_W * STRIP_ROWS)
#define STRIP_BYTES (STRIP_PX * 2)
#define BL_FREQ_HZ 50000 /* spikes/hw/RESULTS.md */
#define BL_BITS LEDC_TIMER_8_BIT
#define BL_FULL (1u << BL_BITS) /* LEDC's duty for always on */
#define BL_TIMER LEDC_TIMER_0
#define BL_CHANNEL LEDC_CHANNEL_0

_Static_assert(LCD349_PANEL_H % STRIP_ROWS == 0, "whole strips cover the panel");

static esp_lcd_panel_handle_t s_panel;
/* Two strip buffers in internal DMA memory: one is filled while the other is
 * on the bus. s_strip_free counts the ones not on the bus. */
static uint16_t *s_strip[2];
static SemaphoreHandle_t s_strip_free;
static StaticSemaphore_t s_strip_free_buf;
/* The flush timing, written on lcd349_ui and read by the test hook. */
static portMUX_TYPE s_timing_lock = portMUX_INITIALIZER_UNLOCKED;
static uint32_t s_frames, s_last_us;

static lcd349_err_t check(esp_err_t err, const char *what)
{
    if (err == ESP_OK) {
        return LCD349_OK;
    }
    ESP_LOGE(TAG, "%s: %s", what, esp_err_to_name(err));
    return err == ESP_ERR_NO_MEM ? LCD349_ERR_NO_MEM : LCD349_ERR_IO;
}

/* Reset by hand, with longer delays than the component's own reset. */
static lcd349_err_t panel_reset(void)
{
    gpio_config_t rst = {.pin_bit_mask = 1ull << LCD349_PIN_LCD_RST, .mode = GPIO_MODE_OUTPUT};
    lcd349_err_t err = check(gpio_config(&rst), "reset pin");
    if (err != LCD349_OK) {
        return err;
    }
    gpio_set_level(LCD349_PIN_LCD_RST, 1);
    vTaskDelay(pdMS_TO_TICKS(10));
    gpio_set_level(LCD349_PIN_LCD_RST, 0);
    vTaskDelay(pdMS_TO_TICKS(20));
    gpio_set_level(LCD349_PIN_LCD_RST, 1);
    vTaskDelay(pdMS_TO_TICKS(150));
    return LCD349_OK;
}

/* A strip has left its buffer. In IRAM: the SPI interrupt may run while the
 * flash cache is off. */
static IRAM_ATTR bool on_strip_sent(esp_lcd_panel_io_handle_t io, esp_lcd_panel_io_event_data_t *e, void *ctx)
{
    (void)io;
    (void)e;
    (void)ctx;
    BaseType_t woken = pdFALSE;
    xSemaphoreGiveFromISR(s_strip_free, &woken);
    return woken == pdTRUE;
}

static lcd349_err_t strips_up(void)
{
    s_strip_free = xSemaphoreCreateCountingStatic(2, 2, &s_strip_free_buf);
    for (int i = 0; i < 2; i++) {
        s_strip[i] = heap_caps_malloc(STRIP_BYTES, MALLOC_CAP_DMA | MALLOC_CAP_INTERNAL);
        if (!s_strip[i]) {
            ESP_LOGE(TAG, "strip buffer: no internal DMA memory");
            return LCD349_ERR_NO_MEM;
        }
    }
    return LCD349_OK;
}

static lcd349_err_t panel_up(void)
{
    lcd349_err_t err = panel_reset();
    if (err != LCD349_OK) {
        return err;
    }
    spi_bus_config_t bus = AXS15231B_PANEL_BUS_QSPI_CONFIG(LCD349_PIN_LCD_PCLK, LCD349_PIN_LCD_D0, LCD349_PIN_LCD_D1,
                                                           LCD349_PIN_LCD_D2, LCD349_PIN_LCD_D3, STRIP_BYTES);
    err = check(spi_bus_initialize(LCD_HOST, &bus, SPI_DMA_CH_AUTO), "QSPI bus");
    if (err != LCD349_OK) {
        return err;
    }
    esp_lcd_panel_io_handle_t io;
    esp_lcd_panel_io_spi_config_t ioc = AXS15231B_PANEL_IO_QSPI_CONFIG(LCD349_PIN_LCD_CS, on_strip_sent, NULL);
    err = check(esp_lcd_new_panel_io_spi((esp_lcd_spi_bus_handle_t)LCD_HOST, &ioc, &io), "panel IO");
    if (err != LCD349_OK) {
        return err;
    }
    /* The panel comes configured: it needs only the standard DCS sleep-out
     * and display-on (spikes/hw/RESULTS.md; the component's default table is
     * for another panel). */
    static const axs15231b_lcd_init_cmd_t cmds[] = {
        {0x11, NULL, 0, 120},
        {0x29, NULL, 0, 20},
    };
    axs15231b_vendor_config_t vc = {
        .init_cmds = cmds,
        .init_cmds_size = sizeof cmds / sizeof cmds[0],
        .flags.use_qspi_interface = 1,
    };
    esp_lcd_panel_dev_config_t pc = {
        .reset_gpio_num = -1,
        .rgb_ele_order = LCD_RGB_ELEMENT_ORDER_RGB,
        .bits_per_pixel = 16,
        .vendor_config = &vc,
    };
    err = check(esp_lcd_new_panel_axs15231b(io, &pc, &s_panel), "panel");
    if (err == LCD349_OK) {
        err = check(esp_lcd_panel_init(s_panel), "panel init");
    }
    return err;
}

/* PWM on the backlight pin, starting dark. */
static lcd349_err_t backlight_up(void)
{
    ledc_timer_config_t t = {
        .speed_mode = LEDC_LOW_SPEED_MODE,
        .duty_resolution = BL_BITS,
        .timer_num = BL_TIMER,
        .freq_hz = BL_FREQ_HZ,
        .clk_cfg = LEDC_AUTO_CLK,
    };
    lcd349_err_t err = check(ledc_timer_config(&t), "backlight timer");
    if (err != LCD349_OK) {
        return err;
    }
    ledc_channel_config_t c = {
        .gpio_num = LCD349_PIN_LCD_BACKLIGHT,
        .speed_mode = LEDC_LOW_SPEED_MODE,
        .channel = BL_CHANNEL,
        .timer_sel = BL_TIMER,
        .duty = 0,
        .flags.output_invert = LCD349_BACKLIGHT_ACTIVE_LOW, /* duty is then the lit fraction */
    };
    return check(ledc_channel_config(&c), "backlight channel");
}

lcd349_err_t be_display_init(void)
{
    lcd349_err_t err = strips_up();
    if (err == LCD349_OK) {
        err = panel_up();
    }
    if (err == LCD349_OK) {
        err = backlight_up();
    }
    return err;
}

/* Over QSPI the panel takes no row address: the first strip starts the frame
 * and the rest continue it, so frames always go out whole, top to bottom.
 * Each strip is rotated into a free buffer while the other is on the bus
 * (spikes/hw/RESULTS.md: 13 ms against 75.7 ms for a whole-frame rotation
 * first). */
void be_panel_flush(const uint16_t *landscape)
{
    int64_t t0 = esp_timer_get_time();
    bool sent = true;
    for (int r = 0, i = 0; r < LCD349_PANEL_H; r += STRIP_ROWS, i ^= 1) {
        xSemaphoreTake(s_strip_free, portMAX_DELAY);
        lcd349__rotate_band(landscape, s_strip[i], r, STRIP_ROWS, LCD349_ROTATION_CW90, LCD349_BYTE_SWAP);
        esp_err_t err = esp_lcd_panel_draw_bitmap(s_panel, 0, r, LCD349_PANEL_W, r + STRIP_ROWS, s_strip[i]);
        if (err != ESP_OK) {
            ESP_LOGE(TAG, "strip at row %d: %s", r, esp_err_to_name(err));
            xSemaphoreGive(s_strip_free); /* nothing queued, so no callback */
            sent = false;
        }
    }
    /* Both buffers free again: the frame is on the panel. */
    xSemaphoreTake(s_strip_free, portMAX_DELAY);
    xSemaphoreTake(s_strip_free, portMAX_DELAY);
    xSemaphoreGive(s_strip_free);
    xSemaphoreGive(s_strip_free);
    uint32_t us = (uint32_t)(esp_timer_get_time() - t0);
    if (!sent) {
        return;
    }
    taskENTER_CRITICAL(&s_timing_lock);
    s_frames++;
    s_last_us = us;
    taskEXIT_CRITICAL(&s_timing_lock);
}

/* pct is 0-100 (the core checks it); 100 is always on. */
lcd349_err_t be_backlight_set(uint8_t pct)
{
    esp_err_t err = ledc_set_duty(LEDC_LOW_SPEED_MODE, BL_CHANNEL, pct * BL_FULL / 100u);
    if (err == ESP_OK) {
        err = ledc_update_duty(LEDC_LOW_SPEED_MODE, BL_CHANNEL);
    }
    return check(err, "backlight");
}

/* A test hook (test_apps/board/main/test_display.c): the frames sent to the
 * panel, and how long the last one took. */
void hw_display_flush_timing(uint32_t *frames, uint32_t *last_us);

void hw_display_flush_timing(uint32_t *frames, uint32_t *last_us)
{
    taskENTER_CRITICAL(&s_timing_lock);
    *frames = s_frames;
    *last_us = s_last_us;
    taskEXIT_CRITICAL(&s_timing_lock);
}

/* A test hook (test_display.c): the backlight PWM's duty, and full scale. */
void hw_display_backlight_duty(uint32_t *duty, uint32_t *full);

void hw_display_backlight_duty(uint32_t *duty, uint32_t *full)
{
    *duty = ledc_get_duty(LEDC_LOW_SPEED_MODE, BL_CHANNEL);
    *full = BL_FULL;
}
