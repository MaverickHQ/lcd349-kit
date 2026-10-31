/* SPDX-License-Identifier: Apache-2.0
 *
 * Internal to the lcd349 component. Two halves:
 *  - be_*: what each backend implements (src/hw for esp32s3, src/emu for linux).
 *    The common core validates arguments and checks subsystem status before
 *    calling them, so both backends see the same inputs.
 *  - lcd349__*: hooks the core offers to the backends.
 */
#pragma once

#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>

#include "sdkconfig.h"
#include "lcd349.h"

/* ---- Backend: initialisation, in the order lcd349_init() calls it ---- */

lcd349_err_t be_power_init(void); /* assert the power hold; always first */
lcd349_err_t be_display_init(void);
lcd349_err_t be_touch_init(void);
lcd349_err_t be_buttons_init(void); /* then report raw edges with lcd349__button_raw() */
lcd349_err_t be_battery_init(void);
lcd349_err_t be_audio_init(void);
lcd349_err_t be_rtc_init(void);
lcd349_err_t be_kv_init(void);
lcd349_err_t be_wifi_init(void);
void be_started(void); /* the kit tasks are running (the emulator starts its control server) */

/* ---- Backend: display and touch ---- */

/* Sends one landscape frame (LCD349_LCD_W x LCD349_LCD_H RGB565, as LVGL
 * renders it). The backend rotates and byte-swaps it into the panel's
 * orientation with rotate.h (LCD349_ROTATION_CW90, LCD349_BYTE_SWAP): the
 * board in strips while it sends, the emulator as one whole frame (design
 * D1-3). Blocks until the panel has it; the emulator also waits the board's
 * flush time here. Called on lcd349_ui. */
void be_panel_flush(const uint16_t *landscape);
lcd349_err_t be_backlight_set(uint8_t pct);
/* Reads the first touch point in panel-native coordinates. Returns false when
 * no finger is down. Called on lcd349_ui, only while the display is awake. */
bool be_touch_read(uint16_t *px, uint16_t *py);
/* Called on lcd349_ui every loop with the LVGL lock held. */
void be_ui_poll(void);

/* ---- Backend: buttons, power, sleep ---- */

bool be_button_raw(lcd349_btn_t btn); /* current raw level, true = pressed */
lcd349_err_t be_power_hold(bool hold); /* releasing it on battery doesn't return */
/* What powers the board now. Asked once a second on lcd349_evt (D1-10). */
lcd349_power_source_t be_power_source(void);
lcd349_err_t be_sleep_until_button(uint32_t timeout_ms, lcd349_btn_t *woke_by);
lcd349_boot_reason_t be_boot_reason(void);

/* ---- Backend: battery, audio, RTC, storage ---- */

lcd349_err_t be_battery_mv(uint16_t *mv); /* averaged, at the cell (divider applied) */
lcd349_err_t be_tone_seq(const lcd349_tone_t *steps, size_t n, uint8_t volume); /* replaces any tone */
lcd349_err_t be_tone_stop(void);
lcd349_err_t be_rtc_get(int64_t *unix_s);
lcd349_err_t be_rtc_set(int64_t unix_s);
/* Values are typed, as in NVS: reading a string key as a blob (or the reverse)
 * returns LCD349_ERR_ARG. Names are validated by the core. get: *len in =
 * capacity, out = size; LCD349_ERR_NO_MEM sets the size needed. */
typedef enum { LCD349_KV_STR = 1, LCD349_KV_BLOB = 2 } lcd349_kv_type_t;
lcd349_err_t be_kv_get(const char *ns, const char *key, lcd349_kv_type_t type, void *out, size_t *len);
lcd349_err_t be_kv_set(const char *ns, const char *key, lcd349_kv_type_t type, const void *val, size_t len);
lcd349_err_t be_kv_erase(const char *ns, const char *key); /* key NULL = namespace */

/* ---- Backend: Wi-Fi (state machine lives in the backend for now) ---- */

lcd349_err_t be_wifi_set_networks(const lcd349_wifi_net_t *nets, size_t n);
lcd349_err_t be_wifi_start(void);
lcd349_err_t be_wifi_stop(void);
lcd349_err_t be_wifi_scan(lcd349_wifi_ap_t *out, size_t *n);
lcd349_err_t be_wifi_status(lcd349_wifi_status_t *status);

/* ---- Backend: serial console ---- */

/* buf and size are checked by the core (size >= 2). Blocks up to timeout_ms. */
lcd349_err_t be_console_read_line(char *buf, size_t size, uint32_t timeout_ms);

/* ---- Backend: time ---- */

uint64_t be_time_us(void);

/* ---- Core hooks for backends ---- */

/* A raw button level change. Call from a task (not an interrupt). */
void lcd349__button_raw(lcd349_btn_t btn, bool pressed, uint32_t t_ms);
/* A Wi-Fi state change, delivered to subscribers on lcd349_evt. */
void lcd349__wifi_changed(const lcd349_wifi_status_t *status);
/* True once lcd349_init() has run. */
bool lcd349__initialised(void);

/* ---- Kit task priorities ----
 * On the board the UI and event tasks run above typical product tasks. On the
 * linux target FreeRTOS preempts host threads at every tick, and nothing
 * guards libc's own locks (malloc, stdio): a task blocked on such a lock held
 * by a preempted lower-priority task deadlocks the process. There, the kit's
 * tasks run at app_main's priority so time slicing resolves the contention. */
#if CONFIG_IDF_TARGET_LINUX
#define LCD349_PRIO_UI 1
#define LCD349_PRIO_EVT 1
#define LCD349_PRIO_EMU 1
#else
#define LCD349_PRIO_UI 5
#define LCD349_PRIO_EVT 6
#endif

/* ---- Core internals shared between common files ---- */

lcd349_err_t lcd349__lvgl_start(const lcd349_config_t *cfg, uint32_t *status);
lcd349_err_t lcd349__evt_start(void);
void lcd349__evt_touch(const lcd349_touch_sample_t *s);
void lcd349__set_status(uint32_t bits);
bool lcd349__ok(lcd349_subsystem_t sub);
