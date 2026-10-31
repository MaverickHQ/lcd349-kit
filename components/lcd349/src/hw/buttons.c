/* SPDX-License-Identifier: Apache-2.0
 *
 * Buttons: BOOT (GPIO0) and PWR (GPIO16), active low with pull-ups
 * (requirement 2.8). An edge interrupt on either wakes a task, which reads
 * both levels and hands every change to the shared debounce
 * (lcd349__button_raw, which mustn't be called from an interrupt).
 *
 * The pins interrupt on any edge. Light sleep's GPIO wake (sleep.c) sets
 * them to a level interrupt while it sleeps, and must set them back to
 * GPIO_INTR_ANYEDGE after waking.
 */
#include "driver/gpio.h"
#include "esp_log.h"
#include "freertos/FreeRTOS.h"
#include "freertos/task.h"

#include "hw.h"

static const char *TAG = "lcd349_btn";
static const gpio_num_t s_pin[LCD349_BTN_COUNT] = {
    [LCD349_BTN_BOOT] = LCD349_PIN_BOOT,
    [LCD349_BTN_PWR] = LCD349_PIN_PWR,
};
static TaskHandle_t s_task;

bool be_button_raw(lcd349_btn_t btn)
{
    return (unsigned)btn < LCD349_BTN_COUNT && gpio_get_level(s_pin[btn]) == 0;
}

static void on_edge(void *arg)
{
    (void)arg;
    BaseType_t woken = pdFALSE;
    vTaskNotifyGiveFromISR(s_task, &woken);
    portYIELD_FROM_ISR(woken);
}

/* Reports each level that differs from the last one reported; the debounce
 * starts from released, so a button already held at start is reported too.
 * Edges while it reads leave a notification, so the last level always gets
 * read. */
static void buttons_task(void *arg)
{
    (void)arg;
    bool last[LCD349_BTN_COUNT] = {false};
    for (;;) {
        ulTaskNotifyTake(pdTRUE, portMAX_DELAY);
        for (int b = 0; b < LCD349_BTN_COUNT; b++) {
            bool down = be_button_raw((lcd349_btn_t)b);
            if (down != last[b]) {
                last[b] = down;
                lcd349__button_raw((lcd349_btn_t)b, down, lcd349_time_ms());
            }
        }
    }
}

lcd349_err_t be_buttons_init(void)
{
    gpio_config_t c = {
        .pin_bit_mask = (1ull << LCD349_PIN_BOOT) | (1ull << LCD349_PIN_PWR),
        .mode = GPIO_MODE_INPUT,
        .pull_up_en = GPIO_PULLUP_ENABLE,
        .intr_type = GPIO_INTR_ANYEDGE,
    };
    esp_err_t err = gpio_config(&c);
    if (err != ESP_OK) {
        ESP_LOGE(TAG, "gpio_config: %s", esp_err_to_name(err));
        return LCD349_ERR_IO;
    }
    if (xTaskCreate(buttons_task, "lcd349_btn", 3072, NULL, LCD349_PRIO_EVT, &s_task) != pdPASS) {
        return LCD349_ERR_NO_MEM;
    }
    /* Another subsystem may have installed the shared GPIO interrupt service. */
    err = gpio_install_isr_service(0);
    if (err != ESP_OK && err != ESP_ERR_INVALID_STATE) {
        ESP_LOGE(TAG, "gpio_install_isr_service: %s", esp_err_to_name(err));
        return LCD349_ERR_IO;
    }
    for (int b = 0; b < LCD349_BTN_COUNT; b++) {
        err = gpio_isr_handler_add(s_pin[b], on_edge, NULL);
        if (err != ESP_OK) {
            ESP_LOGE(TAG, "gpio_isr_handler_add(%d): %s", s_pin[b], esp_err_to_name(err));
            return LCD349_ERR_IO;
        }
    }
    /* The first read, now that no edge can be missed. */
    xTaskNotifyGive(s_task);
    return LCD349_OK;
}
