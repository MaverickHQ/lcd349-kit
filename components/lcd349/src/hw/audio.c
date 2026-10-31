/* SPDX-License-Identifier: Apache-2.0
 *
 * Audio: the ES8311 through esp_codec_dev over I2S, the tone sequencer, and
 * the amplifier (TCA9554 P7) on only while a tone plays (design D1-5).
 *
 * A kit task plays each sequence. The codec and the I2S channel are on only
 * for a sequence, so between tones nothing holds the I2S driver's power
 * lock and light sleep stays possible. The amplifier goes on after the codec
 * is playing silence, and off before the codec stops, so neither edge
 * reaches the speaker.
 */
#include <math.h>
#include <string.h>

#include "driver/i2s_std.h"
#include "esp_codec_dev.h"
#include "esp_codec_dev_defaults.h"
#include "esp_log.h"
#include "freertos/FreeRTOS.h"
#include "freertos/task.h"

#include "expander.h"
#include "hw.h"

static const char *TAG = "lcd349_audio";

#define AUDIO_RATE LCD349_AUDIO_SAMPLE_RATE_HZ
/* The channel's port and the one esp_codec_dev is told must agree: it shares
 * a port's lock and pairing by that number (audio_codec_data_i2s.c). */
#define AUDIO_I2S_PORT I2S_NUM_0
#define AUDIO_TWO_PI (2.0f * (float)M_PI)
/* Four DMA buffers of 10 ms: a stop or a new tone is heard within 40 ms, and
 * the queue is still longer than a display flush. */
#define AUDIO_DMA_BUFS 4
#define AUDIO_CHUNK_FRAMES (AUDIO_RATE / 100)
#define AUDIO_DRAIN_MS (AUDIO_DMA_BUFS * AUDIO_CHUNK_FRAMES * 1000 / AUDIO_RATE)
/* The level task 0.3's spike played and the owner heard (spikes/hw); the
 * volume is the codec's. */
#define AUDIO_AMPLITUDE 8000.0f
/* Short bursts every 10 ms, above lcd349_ui so a flush can't starve the
 * queue. */
#define AUDIO_TASK_PRIO LCD349_PRIO_EVT
#define AUDIO_TASK_STACK 4096

static esp_codec_dev_handle_t s_dev;
static i2s_chan_handle_t s_tx;
static TaskHandle_t s_task;

/* The latest request, written by any task and taken by the audio task. Each
 * be_tone_seq or be_tone_stop bumps s_gen; n = 0 is a stop. */
static portMUX_TYPE s_mux = portMUX_INITIALIZER_UNLOCKED;
static lcd349_tone_t s_steps[LCD349_TONE_SEQ_MAX];
static size_t s_n;
static uint8_t s_volume;
static volatile uint32_t s_gen;
static uint32_t s_taken; /* the audio task's own */

typedef struct {
    lcd349_tone_t steps[LCD349_TONE_SEQ_MAX];
    size_t n;
    uint8_t volume;
    uint32_t gen;
} tone_req_t;

static void post(const lcd349_tone_t *steps, size_t n, uint8_t volume)
{
    taskENTER_CRITICAL(&s_mux);
    if (n) {
        memcpy(s_steps, steps, n * sizeof *steps);
    }
    s_n = n;
    s_volume = volume;
    s_gen++;
    taskEXIT_CRITICAL(&s_mux);
    xTaskNotifyGive(s_task);
}

/* The newest request not yet taken, if it plays something. */
static bool take(tone_req_t *req)
{
    bool fresh;
    taskENTER_CRITICAL(&s_mux);
    fresh = s_gen != s_taken;
    if (fresh) {
        memcpy(req->steps, s_steps, s_n * sizeof s_steps[0]);
        req->n = s_n;
        req->volume = s_volume;
        req->gen = s_taken = s_gen;
    }
    taskEXIT_CRITICAL(&s_mux);
    return fresh && req->n > 0;
}

/* Plays req's steps; returns early when a newer request arrives. */
static void play(const tone_req_t *req)
{
    static int16_t buf[AUDIO_CHUNK_FRAMES * 2];
    float phase = 0;
    for (size_t i = 0; i < req->n; i++) {
        const lcd349_tone_t *t = &req->steps[i];
        const float step = AUDIO_TWO_PI * t->hz / AUDIO_RATE;
        const uint32_t frames = (uint32_t)t->ms * AUDIO_RATE / 1000;
        if (t->hz == 0) {
            phase = 0; /* the next tone starts at a zero crossing */
        }
        for (uint32_t done = 0; done < frames;) {
            if (s_gen != req->gen) {
                return;
            }
            uint32_t n = frames - done < AUDIO_CHUNK_FRAMES ? frames - done : AUDIO_CHUNK_FRAMES;
            for (uint32_t f = 0; f < n; f++) {
                int16_t v = 0;
                if (t->hz) {
                    v = (int16_t)(AUDIO_AMPLITUDE * sinf(phase));
                    phase += step;
                    if (phase >= AUDIO_TWO_PI) {
                        phase -= AUDIO_TWO_PI;
                    }
                }
                buf[2 * f] = buf[2 * f + 1] = v;
            }
            if (esp_codec_dev_write(s_dev, buf, (int)(n * 2 * sizeof buf[0])) != ESP_CODEC_DEV_OK) {
                ESP_LOGE(TAG, "write failed");
                return;
            }
            done += n;
        }
    }
}

/* esp_codec_dev_open disables the channel before setting its format, and the
 * I2S driver logs an error when the channel isn't enabled; enabling it first
 * keeps the log clean. */
static bool codec_open(void)
{
    esp_codec_dev_sample_info_t fs = {.sample_rate = AUDIO_RATE, .channel = 2, .bits_per_sample = 16};
    i2s_channel_enable(s_tx);
    if (esp_codec_dev_open(s_dev, &fs) != ESP_CODEC_DEV_OK) {
        ESP_LOGE(TAG, "codec open failed");
        esp_codec_dev_close(s_dev);
        i2s_channel_disable(s_tx); /* in case open failed before taking the channel */
        return false;
    }
    return true;
}

static void amp(bool on)
{
    lcd349_err_t err = hw_expander_out(LCD349_EXPANDER_PIN_NS_MODE, on);
    if (err != LCD349_OK) {
        ESP_LOGE(TAG, "amplifier %s: %s", on ? "on" : "off", lcd349_err_str(err));
    }
}

static void audio_task(void *arg)
{
    (void)arg;
    static tone_req_t req;
    for (;;) {
        ulTaskNotifyTake(pdTRUE, portMAX_DELAY);
        bool open = false;
        while (take(&req)) {
            if (!open) {
                if (!codec_open()) {
                    break;
                }
                amp(true);
                open = true;
            }
            esp_codec_dev_set_out_vol(s_dev, req.volume);
            play(&req);
        }
        if (open) {
            /* What's queued plays out (auto_clear then sends silence). */
            vTaskDelay(pdMS_TO_TICKS(AUDIO_DRAIN_MS));
            amp(false);
            esp_codec_dev_close(s_dev);
        }
    }
}

lcd349_err_t be_audio_init(void)
{
    /* Off first: the expander keeps P7 through a warm restart, even mid-tone. */
    lcd349_err_t err = hw_expander_out(LCD349_EXPANDER_PIN_NS_MODE, false);
    if (err != LCD349_OK) {
        return err;
    }
    i2c_master_bus_handle_t bus = hw_i2c_main();
    if (!bus) {
        return LCD349_ERR_UNAVAILABLE;
    }

    /* The I2S channel (and so MCLK) before the codec: the ES8311 needs MCLK. */
    i2s_chan_config_t cc = I2S_CHANNEL_DEFAULT_CONFIG(AUDIO_I2S_PORT, I2S_ROLE_MASTER);
    cc.dma_desc_num = AUDIO_DMA_BUFS;
    cc.dma_frame_num = AUDIO_CHUNK_FRAMES;
    cc.auto_clear = true;
    if (i2s_new_channel(&cc, &s_tx, NULL) != ESP_OK) {
        return LCD349_ERR_IO;
    }
    i2s_std_config_t sc = {
        .clk_cfg = I2S_STD_CLK_DEFAULT_CONFIG(AUDIO_RATE),
        .slot_cfg = I2S_STD_PHILIPS_SLOT_DEFAULT_CONFIG(I2S_DATA_BIT_WIDTH_16BIT, I2S_SLOT_MODE_STEREO),
        .gpio_cfg = {
            .mclk = LCD349_PIN_I2S_MCLK,
            .bclk = LCD349_PIN_I2S_BCLK,
            .ws = LCD349_PIN_I2S_WS,
            .dout = LCD349_PIN_I2S_DOUT,
            .din = I2S_GPIO_UNUSED,
        },
    };
    sc.clk_cfg.mclk_multiple = I2S_MCLK_MULTIPLE_256;
    if (i2s_channel_init_std_mode(s_tx, &sc) != ESP_OK) {
        return LCD349_ERR_IO;
    }

    /* esp_codec_dev takes the 8-bit I2C address. It talks over the bus
     * handle, and uses the port number only to tell codecs apart
     * (codec_ref_mgr.c); hw_backend.c makes the main bus on I2C_NUM_0. The
     * amplifier isn't the codec's to switch (pa_pin -1): it's on the
     * expander. Adding the codec writes its registers, so a codec that
     * doesn't answer fails here. */
    audio_codec_i2c_cfg_t ctl = {.port = I2C_NUM_0, .addr = LCD349_CODEC_I2C_ADDR << 1, .bus_handle = bus};
    const audio_codec_ctrl_if_t *ctrl_if = audio_codec_new_i2c_ctrl(&ctl);
    if (!ctrl_if) {
        return LCD349_ERR_IO;
    }
    es8311_codec_cfg_t ec = {
        .ctrl_if = ctrl_if,
        .codec_mode = ESP_CODEC_DEV_WORK_MODE_DAC,
        .pa_pin = -1,
        .use_mclk = true,
    };
    const audio_codec_if_t *codec_if = es8311_codec_new(&ec);
    if (!codec_if) {
        return LCD349_ERR_IO;
    }
    audio_codec_i2s_cfg_t ic = {.port = AUDIO_I2S_PORT, .tx_handle = s_tx};
    esp_codec_dev_cfg_t dc = {
        .dev_type = ESP_CODEC_DEV_TYPE_OUT,
        .codec_if = codec_if,
        .data_if = audio_codec_new_i2s_data(&ic),
    };
    if (!dc.data_if || !(s_dev = esp_codec_dev_new(&dc))) {
        return LCD349_ERR_NO_MEM;
    }

    if (xTaskCreate(audio_task, "lcd349_audio", AUDIO_TASK_STACK, NULL, AUDIO_TASK_PRIO, &s_task) != pdPASS) {
        return LCD349_ERR_NO_MEM;
    }
    return LCD349_OK;
}

lcd349_err_t be_tone_seq(const lcd349_tone_t *steps, size_t n, uint8_t volume)
{
    post(steps, n, volume);
    return LCD349_OK;
}

lcd349_err_t be_tone_stop(void)
{
    post(NULL, 0, 0);
    return LCD349_OK;
}
