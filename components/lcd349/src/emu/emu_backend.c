/* SPDX-License-Identifier: Apache-2.0
 *
 * Emulator backend (ESP-IDF linux target): the be_* functions the common
 * core calls, over the device model in devmodel.c. Storage is kv_file.c,
 * Wi-Fi is wifi_sim.c, and the control server is control.c.
 */
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <time.h>
#include <unistd.h>

#include "esp_log.h"
#include "freertos/FreeRTOS.h"
#include "freertos/semphr.h"
#include "freertos/task.h"

#include "defaults.h"
#include "emu.h"
#include "rotate.h"

static const char *TAG = "lcd349_emu";

/* ---- Start-up, before app_main ---- */

/* Runs before main(): read the configuration, capture serial output, and if
 * BOOT was held through the reset, stay in download mode like the ROM does,
 * without ever starting the product. */
__attribute__((constructor)) static void emu_boot(void)
{
    emu_devmodel_init();
    emu_serial_init();
    if (emu_boot_held()) {
        emu_download_mode();
    }
}

/* ---- Time ---- */

static uint64_t mono_us(void)
{
    struct timespec ts;
    clock_gettime(CLOCK_MONOTONIC, &ts);
    return (uint64_t)ts.tv_sec * 1000000u + (uint64_t)ts.tv_nsec / 1000u;
}

static uint64_t s_t0;

uint64_t be_time_us(void)
{
    if (!s_t0) {
        s_t0 = mono_us();
    }
    return mono_us() - s_t0;
}

lcd349_boot_reason_t be_boot_reason(void)
{
    return emu_boot_reason_env();
}

/* ---- Power ---- */

static SemaphoreHandle_t s_wake;
static volatile lcd349_btn_t s_woke_by;

static void header_battery(void)
{
    emu_state_t st;
    emu_state_get(&st);
    emu_fb_hdr_t *h = emu_fb_hdr();
    h->on_battery = st.on_battery;
    h->battery_mv = st.battery_mv;
    h->battery_pct = lcd349_battery_pct_from_mv(st.battery_mv);
    emu_fb_status_changed();
}

static void set_hold(emu_state_t *s, void *ctx)
{
    s->hold = *(bool *)ctx;
}

lcd349_err_t be_power_init(void)
{
    emu_serial_catch_crashes();
    emu_serial_restart_interrupted_calls();
    emu_devmodel_start_rtos();
    emu_fb_open();
    s_wake = xSemaphoreCreateBinary();
    bool hold = true;
    emu_state_update(set_hold, &hold);
    emu_set_power(EMU_POWER_ON);
    header_battery();
    return s_wake ? LCD349_OK : LCD349_ERR_NO_MEM;
}

lcd349_err_t be_power_hold(bool hold)
{
    emu_state_update(set_hold, &hold);
    emu_state_t st;
    emu_state_get(&st);
    if (!hold && st.on_battery) {
        /* On battery the board loses power at once: present OFF and stop (requirement 3.6). */
        ESP_LOGI(TAG, "power hold released on battery: off");
        emu_set_power(EMU_POWER_OFF);
        exit(LCD349_EXIT_OFF);
    }
    return LCD349_OK;
}

/* The model's source: --usb or --battery at start, state set after. */
lcd349_power_source_t be_power_source(void)
{
    emu_state_t st;
    emu_state_get(&st);
    return st.on_battery ? LCD349_POWER_SOURCE_BATTERY : LCD349_POWER_SOURCE_USB;
}

bool emu_in_light_sleep(void)
{
    emu_state_t st;
    emu_state_get(&st);
    return st.power == EMU_POWER_LIGHT_SLEEP;
}

lcd349_err_t be_sleep_until_button(uint32_t timeout_ms, lcd349_btn_t *woke_by)
{
    xSemaphoreTake(s_wake, 0); /* drop a stale wake */
    emu_set_power(EMU_POWER_LIGHT_SLEEP);
    TickType_t wait = timeout_ms ? pdMS_TO_TICKS(timeout_ms) + 1 : portMAX_DELAY;
    bool woke = xSemaphoreTake(s_wake, wait) == pdTRUE;
    emu_set_power(EMU_POWER_ON);
    if (!woke) {
        return LCD349_ERR_TIMEOUT;
    }
    if (woke_by) {
        *woke_by = s_woke_by;
    }
    return LCD349_OK;
}

/* ---- Display ---- */

static uint16_t *s_panel; /* panel-native, counted like the board's buffers */

lcd349_err_t be_display_init(void)
{
    s_panel = lcd349_alloc(EMU_PANEL_BYTES, LCD349_MEM_ANY);
    return s_panel ? LCD349_OK : LCD349_ERR_NO_MEM;
}

/* The whole frame at once: the same bytes as the board's strips (S1-2). */
void be_panel_flush(const uint16_t *landscape)
{
    lcd349__rotate_frame(landscape, s_panel, LCD349_ROTATION_CW90, LCD349_BYTE_SWAP);
    emu_fb_write((const uint8_t *)s_panel, EMU_PANEL_BYTES);
    /* The timing model: a flush takes as long as on the board (requirement 3.3). */
    uint32_t ms = emu_flush_ms();
    if (ms) {
        vTaskDelay(pdMS_TO_TICKS(ms));
    }
}

lcd349_err_t be_backlight_set(uint8_t pct)
{
    emu_fb_hdr()->backlight = pct;
    emu_fb_status_changed();
    return LCD349_OK;
}

void be_ui_poll(void)
{
}

/* ---- Touch: a queue of samples, one consumed per touch period ---- */

#define TOUCH_Q 512

static bool touch_push_locked(const emu_touch_t *samples, size_t n, bool live, uint64_t *end_pos);

static emu_touch_t s_tq[TOUCH_Q];
static uint64_t s_tq_head; /* next write position (control task) */
static uint64_t s_tq_tail; /* next read position (lcd349_ui) */
static emu_touch_t s_finger;

lcd349_err_t be_touch_init(void)
{
    return LCD349_OK;
}

bool emu_touch_push(const emu_touch_t *samples, size_t n, bool live, uint64_t *end_pos)
{
    emu_crit_enter(); /* the UI task reads the queue; keep it consistent under preemption */
    bool ok = touch_push_locked(samples, n, live, end_pos);
    emu_crit_exit();
    return ok;
}

static bool touch_push_locked(const emu_touch_t *samples, size_t n, bool live, uint64_t *end_pos)
{
    uint64_t head = __atomic_load_n(&s_tq_head, __ATOMIC_ACQUIRE);
    uint64_t tail = __atomic_load_n(&s_tq_tail, __ATOMIC_ACQUIRE);
    size_t i = 0;
    /* Live input (the viewer's mouse): replace a pending move instead of
     * queueing behind it, so a fast mouse never builds up lag. Presses and
     * releases are always kept. */
    if (live && n > 0 && samples[0].down && head > tail && s_tq[(head - 1) % TOUCH_Q].down) {
        s_tq[(head - 1) % TOUCH_Q] = samples[0];
        i = 1;
    }
    if (head - tail + (n - i) > TOUCH_Q) {
        return false;
    }
    for (; i < n; i++) {
        s_tq[head % TOUCH_Q] = samples[i];
        head++;
    }
    __atomic_store_n(&s_tq_head, head, __ATOMIC_RELEASE);
    if (end_pos) {
        *end_pos = head;
    }
    return true;
}

uint64_t emu_touch_consumed(void)
{
    return __atomic_load_n(&s_tq_tail, __ATOMIC_ACQUIRE);
}

void emu_touch_flush(void)
{
    emu_crit_enter();
    __atomic_store_n(&s_tq_tail, __atomic_load_n(&s_tq_head, __ATOMIC_ACQUIRE), __ATOMIC_RELEASE);
    s_finger.down = false;
    emu_crit_exit();
}

/* Called on lcd349_ui every touch period, only while the display is awake and
 * lit. Each call takes one queued sample; with none queued the finger stays
 * where it is (held down, or up). */
bool be_touch_read(uint16_t *px, uint16_t *py)
{
    emu_crit_enter();
    uint64_t tail = __atomic_load_n(&s_tq_tail, __ATOMIC_ACQUIRE);
    if (tail < __atomic_load_n(&s_tq_head, __ATOMIC_ACQUIRE)) {
        s_finger = s_tq[tail % TOUCH_Q];
        __atomic_store_n(&s_tq_tail, tail + 1, __ATOMIC_RELEASE);
    }
    emu_touch_t f = s_finger;
    emu_crit_exit();
    if (!f.down) {
        return false;
    }
    /* Input arrives in landscape; the controller reports panel-native points. */
    lcd349__landscape_to_panel(f.x, f.y, LCD349_ROTATION_CW90, px, py);
    return true;
}

/* ---- Buttons: virtual GPIO levels into the common debounce ---- */

static bool s_btn[LCD349_BTN_COUNT];

lcd349_err_t be_buttons_init(void)
{
    return LCD349_OK;
}

bool be_button_raw(lcd349_btn_t btn)
{
    return (unsigned)btn < LCD349_BTN_COUNT && s_btn[btn];
}

bool emu_button_get(lcd349_btn_t btn)
{
    return be_button_raw(btn);
}

void emu_button_set(lcd349_btn_t btn, bool down)
{
    if ((unsigned)btn >= LCD349_BTN_COUNT || s_btn[btn] == down) {
        return;
    }
    s_btn[btn] = down;
    lcd349__button_raw(btn, down, lcd349_time_ms());
    if (down && emu_in_light_sleep()) {
        s_woke_by = btn;
        xSemaphoreGive(s_wake);
    }
}

/* ---- Battery ---- */

lcd349_err_t be_battery_init(void)
{
    return LCD349_OK;
}

lcd349_err_t be_battery_mv(uint16_t *mv)
{
    emu_state_t st;
    emu_state_get(&st);
    *mv = st.battery_mv;
    return LCD349_OK;
}

/* ---- Audio: tones become events for audio.events and the viewer ---- */

#define AUDIO_RING 256

static emu_tone_event_t s_audio[AUDIO_RING];
static uint64_t s_audio_seq;

lcd349_err_t be_audio_init(void)
{
    return LCD349_OK;
}

static void audio_push_locked(uint32_t t_ms, uint16_t hz, uint16_t ms, uint8_t vol, bool stop)
{
    s_audio_seq++;
    s_audio[s_audio_seq % AUDIO_RING] = (emu_tone_event_t){s_audio_seq, t_ms, hz, ms, vol, stop};
}

lcd349_err_t be_tone_seq(const lcd349_tone_t *steps, size_t n, uint8_t volume)
{
    uint32_t t = lcd349_time_ms();
    emu_crit_enter();
    audio_push_locked(t, 0, 0, 0, true); /* a new tone replaces whatever plays */
    for (size_t i = 0; i < n; i++) {
        audio_push_locked(t, steps[i].hz, steps[i].ms, volume, false);
        t += steps[i].ms;
    }
    emu_crit_exit();
    return LCD349_OK;
}

lcd349_err_t be_tone_stop(void)
{
    emu_crit_enter();
    audio_push_locked(lcd349_time_ms(), 0, 0, 0, true);
    emu_crit_exit();
    return LCD349_OK;
}

size_t emu_audio_events(uint64_t since, emu_tone_event_t *out, size_t max, uint64_t *next)
{
    size_t n = 0;
    emu_crit_enter();
    uint64_t first = s_audio_seq > AUDIO_RING ? s_audio_seq - AUDIO_RING + 1 : 1;
    for (uint64_t s = since + 1 > first ? since + 1 : first; s <= s_audio_seq && n < max; s++) {
        out[n++] = s_audio[s % AUDIO_RING];
    }
    *next = n ? out[n - 1].seq : (since > s_audio_seq ? s_audio_seq : since);
    emu_crit_exit();
    return n;
}

/* ---- RTC: the host clock plus an offset kept in the device model ---- */

lcd349_err_t be_rtc_init(void)
{
    return LCD349_OK;
}

lcd349_err_t be_rtc_get(int64_t *unix_s)
{
    emu_state_t st;
    emu_state_get(&st);
    *unix_s = (int64_t)time(NULL) + st.rtc_offset_s;
    return LCD349_OK;
}

static void set_rtc(emu_state_t *s, void *ctx)
{
    s->rtc_offset_s = *(int64_t *)ctx - (int64_t)time(NULL);
}

lcd349_err_t be_rtc_set(int64_t unix_s)
{
    emu_state_update(set_rtc, &unix_s);
    return LCD349_OK;
}

/* ---- Serial console ---- */

/* Polls the stdin reader's queue (serial.c): FreeRTOS tasks must not block in
 * host calls, and the reader thread can't signal a FreeRTOS semaphore. */
lcd349_err_t be_console_read_line(char *buf, size_t size, uint32_t timeout_ms)
{
    uint64_t until = be_time_us() + (uint64_t)timeout_ms * 1000u;
    for (;;) {
        if (emu_console_take(buf, size)) {
            return LCD349_OK;
        }
        if (be_time_us() >= until) {
            return LCD349_ERR_TIMEOUT;
        }
        vTaskDelay(pdMS_TO_TICKS(10));
    }
}

/* ---- Start-up after the kit tasks run ---- */

lcd349_err_t be_kv_init(void)
{
    return emu_kv_init();
}

lcd349_err_t be_wifi_init(void)
{
    return emu_wifi_init();
}

void be_started(void)
{
    emu_control_start();
}
