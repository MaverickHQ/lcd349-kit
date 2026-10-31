/* SPDX-License-Identifier: Apache-2.0
 *
 * The common core: initialisation, status, errors, time, counted memory, and
 * the argument checks every API call makes before reaching a backend. Doing
 * the checks here means the board and the emulator reject the same inputs.
 */
#include <string.h>

#include "esp_heap_caps.h"
#include "esp_log.h"
#include "freertos/FreeRTOS.h"
#include "freertos/task.h"

#include "kit.h"

static const char *TAG = "lcd349";

static uint32_t s_status;
static bool s_init;

/* ---- Status and errors ---- */

void lcd349__set_status(uint32_t bits)
{
    __atomic_fetch_or(&s_status, bits, __ATOMIC_RELAXED);
}

uint32_t lcd349_board_status(void)
{
    return __atomic_load_n(&s_status, __ATOMIC_RELAXED);
}

bool lcd349__ok(lcd349_subsystem_t sub)
{
    return s_init && !(lcd349_board_status() & sub);
}

bool lcd349__initialised(void)
{
    return s_init;
}

const char *lcd349_err_str(lcd349_err_t err)
{
    switch (err) {
    case LCD349_OK: return "LCD349_OK";
    case LCD349_ERR_ARG: return "LCD349_ERR_ARG";
    case LCD349_ERR_UNAVAILABLE: return "LCD349_ERR_UNAVAILABLE";
    case LCD349_ERR_ON_USB: return "LCD349_ERR_ON_USB";
    case LCD349_ERR_TIMEOUT: return "LCD349_ERR_TIMEOUT";
    case LCD349_ERR_IO: return "LCD349_ERR_IO";
    case LCD349_ERR_NO_MEM: return "LCD349_ERR_NO_MEM";
    case LCD349_ERR_NOT_FOUND: return "LCD349_ERR_NOT_FOUND";
    case LCD349_ERR_STATE: return "LCD349_ERR_STATE";
    }
    return "LCD349_ERR_?";
}

/* ---- Init ---- */

static void init_step(const char *name, lcd349_subsystem_t sub, lcd349_err_t (*fn)(void))
{
    lcd349_err_t err = fn();
    if (err != LCD349_OK) {
        lcd349__set_status(sub);
        ESP_LOGW(TAG, "%s unavailable: %s", name, lcd349_err_str(err));
    }
}

lcd349_err_t lcd349_init(const lcd349_config_t *cfg)
{
    if (s_init) {
        return LCD349_ERR_STATE;
    }
    lcd349_config_t c = LCD349_CONFIG_DEFAULT();
    if (cfg) {
        c = *cfg;
    }
    if (c.backlight_pct > 100) {
        return LCD349_ERR_ARG;
    }
    if (c.touch_period_ms == 0) {
        c.touch_period_ms = LCD349_TOUCH_PERIOD_MS;
    }

    /* The power hold comes first: on battery the board dies without it (requirement 1.3). */
    init_step("power hold", LCD349_SUB_POWER, be_power_init);

    lcd349_err_t err = lcd349__evt_start();
    if (err != LCD349_OK) {
        return err;
    }
    s_init = true;

    uint32_t failed = 0;
    lcd349_err_t display_err = lcd349__lvgl_start(&c, &failed);
    lcd349__set_status(failed);

    init_step("buttons", LCD349_SUB_BUTTONS, be_buttons_init);
    init_step("battery", LCD349_SUB_BATTERY, be_battery_init);
    init_step("audio", LCD349_SUB_AUDIO, be_audio_init);
    init_step("rtc", LCD349_SUB_RTC, be_rtc_init);
    init_step("storage", LCD349_SUB_STORAGE, be_kv_init);
    init_step("wifi", LCD349_SUB_WIFI, be_wifi_init);

    be_started();
    ESP_LOGI(TAG, "lcd349 %s up (device.json %s), status 0x%03x", LCD349_VERSION_STR, lcd349_board_info()->device_json_sha,
             (unsigned)lcd349_board_status());
    return display_err;
}

/* ---- Time ---- */

uint64_t lcd349_time_us(void)
{
    return be_time_us();
}

uint32_t lcd349_time_ms(void)
{
    return (uint32_t)(be_time_us() / 1000u);
}

lcd349_boot_reason_t lcd349_boot_reason(void)
{
    return be_boot_reason();
}

/* ---- Counted memory ---- */

typedef struct {
    size_t size;
    uint32_t where; /* LCD349_MEM_INTERNAL or LCD349_MEM_PSRAM */
    uint32_t magic;
} alloc_hdr_t;

#define ALLOC_MAGIC 0x4C333439u /* "L349" */
/* Keep the payload aligned like malloc's. */
#define HDR_SIZE ((sizeof(alloc_hdr_t) + 15u) & ~(size_t)15u)

static size_t s_mem_now[2], s_mem_peak[2], s_mem_largest;

static void count(int i, size_t delta, bool add)
{
    size_t now = add ? __atomic_add_fetch(&s_mem_now[i], delta, __ATOMIC_RELAXED)
                     : __atomic_sub_fetch(&s_mem_now[i], delta, __ATOMIC_RELAXED);
    size_t peak = __atomic_load_n(&s_mem_peak[i], __ATOMIC_RELAXED);
    while (now > peak && !__atomic_compare_exchange_n(&s_mem_peak[i], &peak, now, true, __ATOMIC_RELAXED, __ATOMIC_RELAXED)) {
    }
    if (add) {
        size_t big = __atomic_load_n(&s_mem_largest, __ATOMIC_RELAXED);
        while (delta > big && !__atomic_compare_exchange_n(&s_mem_largest, &big, delta, true, __ATOMIC_RELAXED, __ATOMIC_RELAXED)) {
        }
    }
}

void *lcd349_alloc(size_t size, lcd349_mem_t caps)
{
    if (size == 0 || size > SIZE_MAX - HDR_SIZE || !(caps & LCD349_MEM_ANY)) {
        return NULL;
    }
    uint8_t *p = NULL;
    uint32_t where = 0;
    if (caps & LCD349_MEM_PSRAM) {
        p = heap_caps_malloc(size + HDR_SIZE, MALLOC_CAP_SPIRAM | MALLOC_CAP_8BIT);
        where = LCD349_MEM_PSRAM;
    }
    if (!p && (caps & LCD349_MEM_INTERNAL)) {
        p = heap_caps_malloc(size + HDR_SIZE, MALLOC_CAP_INTERNAL | MALLOC_CAP_8BIT);
        where = LCD349_MEM_INTERNAL;
    }
    if (!p) {
        return NULL;
    }
    alloc_hdr_t *h = (alloc_hdr_t *)p;
    h->size = size;
    h->where = where;
    h->magic = ALLOC_MAGIC;
    count(where == LCD349_MEM_PSRAM, size, true);
    return p + HDR_SIZE;
}

void lcd349_free(void *ptr)
{
    if (!ptr) {
        return;
    }
    alloc_hdr_t *h = (alloc_hdr_t *)((uint8_t *)ptr - HDR_SIZE);
    if (h->magic != ALLOC_MAGIC) {
        ESP_LOGE(TAG, "lcd349_free: %p wasn't allocated by lcd349_alloc", ptr);
        return;
    }
    h->magic = 0;
    count(h->where == LCD349_MEM_PSRAM, h->size, false);
    heap_caps_free(h);
}

void lcd349_mem_stats(lcd349_mem_stats_t *st)
{
    if (!st) {
        return;
    }
    st->internal_bytes = __atomic_load_n(&s_mem_now[0], __ATOMIC_RELAXED);
    st->psram_bytes = __atomic_load_n(&s_mem_now[1], __ATOMIC_RELAXED);
    st->internal_peak = __atomic_load_n(&s_mem_peak[0], __ATOMIC_RELAXED);
    st->psram_peak = __atomic_load_n(&s_mem_peak[1], __ATOMIC_RELAXED);
    st->largest = __atomic_load_n(&s_mem_largest, __ATOMIC_RELAXED);
}

/* ---- Power and battery ---- */

lcd349_err_t lcd349_power_hold(bool hold)
{
    if (!lcd349__ok(LCD349_SUB_POWER)) {
        return s_init ? LCD349_ERR_UNAVAILABLE : LCD349_ERR_STATE;
    }
    return be_power_hold(hold);
}

lcd349_err_t lcd349_power_off(void)
{
    if (!lcd349__ok(LCD349_SUB_POWER)) {
        return s_init ? LCD349_ERR_UNAVAILABLE : LCD349_ERR_STATE;
    }
    ESP_LOGI(TAG, "power off");
    lcd349_err_t err = be_power_hold(false);
    if (err != LCD349_OK) {
        return err;
    }
    /* On battery we are gone by now. Still here: USB powers the board (requirement 2.4).
     * vTaskDelay(n) can return up to a tick early, so wait one more. */
    vTaskDelay(pdMS_TO_TICKS(LCD349_POWER_OFF_USB_CHECK_MS) + 1);
    be_power_hold(true);
    ESP_LOGI(TAG, "still running after %d ms: on USB", LCD349_POWER_OFF_USB_CHECK_MS);
    return LCD349_ERR_ON_USB;
}

lcd349_err_t lcd349_sleep_until_button(uint32_t timeout_ms, lcd349_btn_t *woke_by)
{
    if (!s_init) {
        return LCD349_ERR_STATE;
    }
    if (!lcd349__ok(LCD349_SUB_BUTTONS)) {
        return LCD349_ERR_UNAVAILABLE;
    }
    bool was_asleep = lcd349_display_is_asleep();
    lcd349_display_sleep(true);
    lcd349_err_t err = be_sleep_until_button(timeout_ms, woke_by);
    if (!was_asleep) {
        lcd349_display_sleep(false);
    }
    return err;
}

lcd349_err_t lcd349_battery_read(uint16_t *mv, uint8_t *pct)
{
    if (!lcd349__ok(LCD349_SUB_BATTERY)) {
        return s_init ? LCD349_ERR_UNAVAILABLE : LCD349_ERR_STATE;
    }
    uint16_t v = 0;
    lcd349_err_t err = be_battery_mv(&v);
    if (err != LCD349_OK) {
        return err;
    }
    if (mv) {
        *mv = v;
    }
    if (pct) {
        *pct = lcd349_battery_pct_from_mv(v);
    }
    return LCD349_OK;
}

/* ---- Audio ---- */

static lcd349_err_t check_sub(lcd349_subsystem_t sub)
{
    if (!s_init) {
        return LCD349_ERR_STATE;
    }
    return lcd349__ok(sub) ? LCD349_OK : LCD349_ERR_UNAVAILABLE;
}

static bool tone_ok(const lcd349_tone_t *t, bool allow_rest)
{
    bool hz_ok = (t->hz >= 20 && t->hz <= 20000) || (allow_rest && t->hz == 0);
    return hz_ok && t->ms >= 1 && t->ms <= 10000;
}

lcd349_err_t lcd349_tone(uint16_t hz, uint16_t ms, uint8_t volume)
{
    lcd349_tone_t t = {hz, ms};
    if (!tone_ok(&t, false) || volume > 100) {
        return LCD349_ERR_ARG;
    }
    lcd349_err_t err = check_sub(LCD349_SUB_AUDIO);
    return err != LCD349_OK ? err : be_tone_seq(&t, 1, volume);
}

lcd349_err_t lcd349_tone_seq(const lcd349_tone_t *steps, size_t n, uint8_t volume)
{
    if (!steps || n == 0 || n > LCD349_TONE_SEQ_MAX || volume > 100) {
        return LCD349_ERR_ARG;
    }
    for (size_t i = 0; i < n; i++) {
        if (!tone_ok(&steps[i], true)) {
            return LCD349_ERR_ARG;
        }
    }
    lcd349_err_t err = check_sub(LCD349_SUB_AUDIO);
    return err != LCD349_OK ? err : be_tone_seq(steps, n, volume);
}

lcd349_err_t lcd349_tone_stop(void)
{
    lcd349_err_t err = check_sub(LCD349_SUB_AUDIO);
    return err != LCD349_OK ? err : be_tone_stop();
}

/* ---- RTC ---- */

lcd349_err_t lcd349_rtc_get(int64_t *unix_s)
{
    if (!unix_s) {
        return LCD349_ERR_ARG;
    }
    lcd349_err_t err = check_sub(LCD349_SUB_RTC);
    return err != LCD349_OK ? err : be_rtc_get(unix_s);
}

lcd349_err_t lcd349_rtc_set(int64_t unix_s)
{
    if (unix_s < 0) {
        return LCD349_ERR_ARG;
    }
    lcd349_err_t err = check_sub(LCD349_SUB_RTC);
    return err != LCD349_OK ? err : be_rtc_set(unix_s);
}

/* ---- Serial console ---- */

lcd349_err_t lcd349_console_read_line(char *buf, size_t size, uint32_t timeout_ms)
{
    if (!buf || size < 2) {
        return LCD349_ERR_ARG;
    }
    buf[0] = '\0';
    return be_console_read_line(buf, size, timeout_ms);
}

/* ---- Key-value storage ---- */

static bool name_ok(const char *s)
{
    if (!s) {
        return false;
    }
    size_t n = strnlen(s, LCD349_KV_NAME_MAX + 1);
    return n >= 1 && n <= LCD349_KV_NAME_MAX;
}

lcd349_err_t lcd349_kv_get_str(const char *ns, const char *key, char *out, size_t *len)
{
    if (!name_ok(ns) || !name_ok(key) || !out || !len || *len == 0) {
        return LCD349_ERR_ARG;
    }
    lcd349_err_t err = check_sub(LCD349_SUB_STORAGE);
    if (err != LCD349_OK) {
        return err;
    }
    size_t cap = *len - 1; /* room for the NUL we add */
    size_t n = cap;
    err = be_kv_get(ns, key, LCD349_KV_STR, out, &n);
    if (err == LCD349_ERR_NO_MEM) {
        *len = n + 1;
        return err;
    }
    if (err != LCD349_OK) {
        return err;
    }
    out[n] = '\0';
    *len = n + 1;
    return LCD349_OK;
}

lcd349_err_t lcd349_kv_set_str(const char *ns, const char *key, const char *val)
{
    if (!name_ok(ns) || !name_ok(key) || !val) {
        return LCD349_ERR_ARG;
    }
    size_t n = strnlen(val, LCD349_KV_VALUE_MAX + 1);
    if (n > LCD349_KV_VALUE_MAX) {
        return LCD349_ERR_ARG;
    }
    lcd349_err_t err = check_sub(LCD349_SUB_STORAGE);
    return err != LCD349_OK ? err : be_kv_set(ns, key, LCD349_KV_STR, val, n);
}

lcd349_err_t lcd349_kv_get_blob(const char *ns, const char *key, void *out, size_t *len)
{
    if (!name_ok(ns) || !name_ok(key) || !out || !len) {
        return LCD349_ERR_ARG;
    }
    lcd349_err_t err = check_sub(LCD349_SUB_STORAGE);
    return err != LCD349_OK ? err : be_kv_get(ns, key, LCD349_KV_BLOB, out, len);
}

lcd349_err_t lcd349_kv_set_blob(const char *ns, const char *key, const void *val, size_t len)
{
    if (!name_ok(ns) || !name_ok(key) || (!val && len) || len > LCD349_KV_VALUE_MAX) {
        return LCD349_ERR_ARG;
    }
    lcd349_err_t err = check_sub(LCD349_SUB_STORAGE);
    return err != LCD349_OK ? err : be_kv_set(ns, key, LCD349_KV_BLOB, val, len);
}

lcd349_err_t lcd349_kv_erase(const char *ns, const char *key)
{
    if (!name_ok(ns) || (key && !name_ok(key))) {
        return LCD349_ERR_ARG;
    }
    lcd349_err_t err = check_sub(LCD349_SUB_STORAGE);
    return err != LCD349_OK ? err : be_kv_erase(ns, key);
}

/* ---- Wi-Fi ---- */

lcd349_err_t lcd349_wifi_set_networks(const lcd349_wifi_net_t *nets, size_t n)
{
    if ((!nets && n) || n > LCD349_WIFI_MAX_NETWORKS) {
        return LCD349_ERR_ARG;
    }
    for (size_t i = 0; i < n; i++) {
        size_t s = strnlen(nets[i].ssid, sizeof nets[i].ssid);
        size_t p = strnlen(nets[i].password, sizeof nets[i].password);
        /* SSID 1-32 bytes; password empty (open) or 8-64 (WPA2 passphrase or hex key). */
        if (s == 0 || s >= sizeof nets[i].ssid || p >= sizeof nets[i].password || (p != 0 && p < 8)) {
            return LCD349_ERR_ARG;
        }
    }
    lcd349_err_t err = check_sub(LCD349_SUB_WIFI);
    return err != LCD349_OK ? err : be_wifi_set_networks(nets, n);
}

lcd349_err_t lcd349_wifi_start(void)
{
    lcd349_err_t err = check_sub(LCD349_SUB_WIFI);
    return err != LCD349_OK ? err : be_wifi_start();
}

lcd349_err_t lcd349_wifi_stop(void)
{
    lcd349_err_t err = check_sub(LCD349_SUB_WIFI);
    return err != LCD349_OK ? err : be_wifi_stop();
}

lcd349_err_t lcd349_wifi_scan(lcd349_wifi_ap_t *out, size_t *n)
{
    if (!out || !n || *n == 0) {
        return LCD349_ERR_ARG;
    }
    lcd349_err_t err = check_sub(LCD349_SUB_WIFI);
    return err != LCD349_OK ? err : be_wifi_scan(out, n);
}

lcd349_err_t lcd349_wifi_status(lcd349_wifi_status_t *status)
{
    if (!status) {
        return LCD349_ERR_ARG;
    }
    lcd349_err_t err = check_sub(LCD349_SUB_WIFI);
    return err != LCD349_OK ? err : be_wifi_status(status);
}
