/* SPDX-License-Identifier: Apache-2.0
 *
 * Internal to the emulator backend (ESP-IDF linux target).
 *
 * Threads: the control server, the UI task and the Wi-Fi task are FreeRTOS
 * tasks; the serial reader is a plain pthread. The linux target's FreeRTOS is
 * preemptive: the SIGALRM tick can switch tasks anywhere. So emulator state
 * shared between tasks is guarded by emu_crit_enter()/emu_crit_exit() (signals
 * blocked: no preemption), held only for short copies with no malloc, I/O or
 * blocking FreeRTOS calls inside. Never hold a pthread mutex in a FreeRTOS
 * task outside a critical section: a preempted holder can deadlock the process.
 */
#pragma once

#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>

#include "kit.h"

/* ---- Process exit codes, read by the supervisor (lcd349 run) ---- */

#define LCD349_EXIT_RESET 3           /* power {action: reset}: start again */
#define LCD349_EXIT_OFF 4             /* powered off (battery) or power {action: off} */
#define LCD349_EXIT_RESET_BOOT_HELD 5 /* reset with BOOT held: start again in download mode */

/* ---- Critical sections (devmodel.c) ----
 * No-ops before the scheduler starts (download mode runs without it). */
void emu_crit_enter(void);
void emu_crit_exit(void);

/* ---- Device model (devmodel.c) ---- */

#define EMU_MAX_NETS 16
#define EMU_BATTERY_MV_MIN 2500 /* settable cell voltage, state.set and LCD349_BATTERY_MV */
#define EMU_BATTERY_MV_MAX 4500

typedef enum {
    EMU_POWER_ON = 0,
    EMU_POWER_LIGHT_SLEEP = 1,
    EMU_POWER_OFF = 2,
    EMU_POWER_DOWNLOAD = 3,
} emu_power_t;

typedef struct {
    char ssid[33];
    char password[65]; /* empty = open */
    uint8_t band_x10;  /* 24 or 50 */
    int8_t rssi;
} emu_net_t;

typedef struct {
    /* Persisted in <instance>/devstate.json across resets. */
    uint16_t battery_mv;
    bool on_battery;
    char board_rev[3];   /* "V1" or "V2" */
    int64_t rtc_offset_s; /* RTC = host clock + offset */
    emu_net_t nets[EMU_MAX_NETS];
    size_t n_nets;
    /* Runtime only. */
    emu_power_t power;
    bool hold;
} emu_state_t;

/* Reads configuration from LCD349_* variables and loads devstate.json. Safe
 * before the scheduler starts (download mode). */
void emu_devmodel_init(void);
/* Once the scheduler runs: creates the lock that orders devstate.json writes. */
void emu_devmodel_start_rtos(void);
const char *emu_instance_dir(void); /* NULL when LCD349_INSTANCE_DIR is unset */
void emu_state_get(emu_state_t *out);
/* Applies fn to the state under the lock, then saves devstate.json. */
void emu_state_update(void (*fn)(emu_state_t *s, void *ctx), void *ctx);
void emu_set_power(emu_power_t p);
uint32_t emu_flush_ms(void);
bool emu_boot_held(void);
lcd349_boot_reason_t emu_boot_reason_env(void);
/* Parses RFC 3339 ("2026-10-12T09:00:00Z", "...+01:00") or Unix seconds. */
bool emu_parse_clock(const char *s, int64_t *out);
/* Writes path's content atomically (temporary file, then rename). */
bool emu_write_atomic(const char *path, const void *data, size_t len);

/* ---- Framebuffer file (fb.c) ---- */

#define EMU_FB_MAGIC "L349FB01"
#define EMU_FB_VERSION 1
#define EMU_PANEL_BYTES (LCD349_PANEL_W * LCD349_PANEL_H * 2)

/* The 64-byte header at the start of fb.bin; panel bytes follow. Shared with
 * the viewer and the Go tools: change only with EMU_FB_VERSION. */
typedef struct {
    char magic[8];          /* "L349FB01" */
    uint32_t version;       /* EMU_FB_VERSION */
    uint16_t width;         /* panel-native: 172 */
    uint16_t height;        /* 640 */
    uint32_t seq;           /* seqlock: odd while a frame is being written */
    uint32_t frames;        /* frames written since start */
    uint64_t last_flush_us; /* lcd349_time_us() of the last frame */
    uint32_t pid;
    uint8_t backlight;      /* 0-100 as set by the product */
    uint8_t power;          /* emu_power_t */
    uint8_t display_asleep; /* lcd349_display_sleep(true) */
    uint8_t on_battery;
    uint16_t battery_mv;
    uint8_t battery_pct;
    uint8_t wifi_state; /* lcd349_wifi_state_t */
    uint32_t status_seq; /* bumped whenever a field above changes */
    /* How the panel bytes relate to the landscape frame. 0 means unknown, so
     * reserved bytes can become fields without a version change. */
    uint8_t rotation;  /* 1 = landscape rotated 90 degrees clockwise into the panel, 2 = counter-clockwise */
    uint8_t byte_swap; /* 1 = RGB565 bytes swapped (big-endian), 2 = native */
    uint8_t pad[14];   /* reserved: zero */
} emu_fb_hdr_t;

_Static_assert(sizeof(emu_fb_hdr_t) == 64, "fb.bin header is 64 bytes");
/* Readers use these offsets: viewer/core.h and internal/emutest. */
_Static_assert(offsetof(emu_fb_hdr_t, seq) == 16 && offsetof(emu_fb_hdr_t, pid) == 32 && offsetof(emu_fb_hdr_t, backlight) == 36 &&
                   offsetof(emu_fb_hdr_t, battery_mv) == 40 && offsetof(emu_fb_hdr_t, status_seq) == 44 &&
                   offsetof(emu_fb_hdr_t, rotation) == 48 && offsetof(emu_fb_hdr_t, byte_swap) == 49,
               "fb.bin header offsets are shared with the viewer and the Go tools");

/* Maps <instance>/fb.bin, or an in-memory buffer without an instance directory. */
bool emu_fb_open(void);
void emu_fb_write(const uint8_t *panel, size_t len);
/* Copies a consistent frame (seqlock read). Returns its seq. */
uint32_t emu_fb_read(uint8_t *panel_out);
emu_fb_hdr_t *emu_fb_hdr(void);
void emu_fb_status_changed(void);
uint64_t emu_fb_idle_us(void); /* time since the last frame */

/* ---- Serial capture (serial.c) ---- */

/* Routes stdout and stderr through a pipe so log lines can be matched by
 * `wait`; everything is still written to the original stdout. */
void emu_serial_init(void);
/* Line cursor API: lines are numbered from 1. Copies line n into out and
 * returns true, or false if n is gone from the ring or not written yet. */
uint64_t emu_serial_next(void); /* the number the next line will get */
bool emu_serial_line(uint64_t n, char *out, size_t out_len);
/* Sends the pipe's last bytes on before a crash ends the process. Call after
 * ESP-IDF's main() has installed its own handlers (from lcd349_init). */
void emu_serial_catch_crashes(void);
void emu_serial_restart_interrupted_calls(void);
/* The next console input line (from stdin), without waiting; false if none. */
bool emu_console_take(char *out, size_t size);

/* ---- Touch queue (emu_backend.c) ---- */

typedef struct {
    uint16_t x, y; /* landscape */
    bool down;
} emu_touch_t;

/* Producer: the control task. Returns the queue position after the last
 * sample, for emu_touch_consumed(). live: coalesce with a pending move. */
bool emu_touch_push(const emu_touch_t *samples, size_t n, bool live, uint64_t *end_pos);
uint64_t emu_touch_consumed(void);
void emu_touch_flush(void);

/* ---- Buttons and sleep (emu_backend.c) ---- */

void emu_button_set(lcd349_btn_t btn, bool down); /* virtual GPIO level */
bool emu_button_get(lcd349_btn_t btn);
bool emu_in_light_sleep(void);

/* ---- Audio events (emu_backend.c) ---- */

typedef struct {
    uint64_t seq;
    uint32_t t_ms;
    uint16_t hz; /* 0 = rest or stop */
    uint16_t ms;
    uint8_t volume;
    bool stop;
} emu_tone_event_t;

/* Copies events with seq > since into out; returns how many. */
size_t emu_audio_events(uint64_t since, emu_tone_event_t *out, size_t max, uint64_t *next);

/* ---- Storage (kv_file.c) ---- */

lcd349_err_t emu_kv_init(void);

/* ---- Wi-Fi (wifi_sim.c) ---- */

lcd349_err_t emu_wifi_init(void);
void emu_wifi_env_changed(void); /* the simulated networks changed */

/* ---- Control server (control.c) and its methods ---- */

void emu_control_start(void);
void emu_download_mode(void); /* never returns */

/* ---- PNG and base64 (png.c) ---- */

/* Encodes an RGB888 image as PNG (stored deflate). Caller frees with free(). */
uint8_t *emu_png_encode(const uint8_t *rgb, int w, int h, size_t *out_len);
/* Base64-encodes into a malloc'd NUL-terminated string. */
char *emu_base64(const uint8_t *data, size_t len);
/* Panel bytes -> landscape RGB888 (w*h*3), scaled by an integer factor. */
uint8_t *emu_panel_to_rgb(const uint8_t *panel, bool raw, int scale, int *w, int *h);

/* ---- UI introspection and lint (introspect.c, lint.c), with the LVGL lock held ---- */

struct cJSON;
struct cJSON *emu_ui_tree(const char *path, int depth, int *count, bool *truncated);
struct cJSON *emu_ui_lint(int *errors, int *warnings);
/* Whether LVGL has invalidated areas it hasn't drawn yet: a widget that just
 * changed is drawn only at the next refresh. Call with the UI lock held. */
bool emu_ui_draw_pending(void);
/* True if visible text on the active screen (or top layer) contains needle. */
bool emu_ui_find_text(const char *needle, char *found, size_t found_len);

/* ---- Host tests ---- */

/* The last frame sent to the panel (panel-native bytes) and the frame count. */
const uint8_t *lcd349__emu_last_frame(uint32_t *frames);
