/* SPDX-License-Identifier: Apache-2.0
 *
 * Battery: the cell through a divider (LCD349_BATTERY_DIVIDER) into ADC1
 * channel 3, read one-shot with the chip's curve-fitting calibration and
 * averaged over 8 samples (requirement 3.1). A full cell (4.2 V) is 1.4 V at
 * the pin, inside the 12 dB attenuation's range.
 */
#include "esp_adc/adc_cali.h"
#include "esp_adc/adc_cali_scheme.h"
#include "esp_adc/adc_oneshot.h"
#include "esp_log.h"

#include "hw.h"

#define SAMPLES 8
#define BAT_UNIT ((adc_unit_t)(ADC_UNIT_1 + LCD349_BATTERY_ADC_UNIT - 1))
#define BAT_CHANNEL ((adc_channel_t)LCD349_BATTERY_ADC_CHANNEL)
#define BAT_ATTEN ADC_ATTEN_DB_12

static const char *TAG = "lcd349_bat";
static adc_oneshot_unit_handle_t s_adc;
static adc_cali_handle_t s_cali;

lcd349_err_t be_battery_init(void)
{
    adc_oneshot_unit_init_cfg_t uc = {.unit_id = BAT_UNIT};
    esp_err_t err = adc_oneshot_new_unit(&uc, &s_adc);
    if (err == ESP_OK) {
        adc_oneshot_chan_cfg_t cc = {.atten = BAT_ATTEN, .bitwidth = ADC_BITWIDTH_DEFAULT};
        err = adc_oneshot_config_channel(s_adc, BAT_CHANNEL, &cc);
    }
    if (err != ESP_OK) {
        ESP_LOGE(TAG, "ADC: %s", esp_err_to_name(err));
        return LCD349_ERR_IO;
    }
    /* Without calibration a reading can be off by a tenth: no reading is better. */
    adc_cali_curve_fitting_config_t cal = {
        .unit_id = BAT_UNIT,
        .chan = BAT_CHANNEL,
        .atten = BAT_ATTEN,
        .bitwidth = ADC_BITWIDTH_DEFAULT,
    };
    err = adc_cali_create_scheme_curve_fitting(&cal, &s_cali);
    if (err != ESP_OK) {
        ESP_LOGE(TAG, "ADC calibration: %s", esp_err_to_name(err));
        return LCD349_ERR_IO;
    }
    return LCD349_OK;
}

lcd349_err_t be_battery_mv(uint16_t *mv)
{
    int sum = 0;
    for (int i = 0; i < SAMPLES; i++) {
        int pin_mv;
        esp_err_t err = adc_oneshot_get_calibrated_result(s_adc, s_cali, BAT_CHANNEL, &pin_mv);
        if (err != ESP_OK) {
            ESP_LOGW(TAG, "ADC read: %s", esp_err_to_name(err));
            return LCD349_ERR_IO;
        }
        sum += pin_mv;
    }
    int cell = sum * LCD349_BATTERY_DIVIDER / SAMPLES;
    *mv = (uint16_t)(cell > UINT16_MAX ? UINT16_MAX : cell);
    return LCD349_OK;
}
