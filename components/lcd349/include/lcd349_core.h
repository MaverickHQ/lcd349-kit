/* SPDX-License-Identifier: Apache-2.0
 *
 * lcd349 core: version, errors, initialisation, status, time and memory.
 *
 * Contracts that hold for the whole SDK:
 * - Every call returns an lcd349_err_t or a documented value, and never aborts
 *   on a hardware error (requirement 1.4).
 * - A subsystem that failed at init, or is absent, is reported by
 *   lcd349_board_status(), and its calls return LCD349_ERR_UNAVAILABLE.
 * - Callbacks run on a kit task (lcd349_ui or lcd349_evt, as documented per
 *   call), never in interrupt context (requirement 1.7).
 * - The same calls behave the same on the board (esp32s3) and in the emulator
 *   (linux target). Products never need a target check.
 */
#pragma once

#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>

#include "lcd349_board.h"

#ifdef __cplusplus
extern "C" {
#endif

#define LCD349_VERSION_MAJOR 0
#define LCD349_VERSION_MINOR 2
#define LCD349_VERSION_PATCH 0
#define LCD349_VERSION_STR "0.2.0"

typedef enum {
    LCD349_OK = 0,
    LCD349_ERR_ARG,         /**< an argument is invalid */
    LCD349_ERR_UNAVAILABLE, /**< the subsystem failed at init or isn't present; see lcd349_board_status() */
    LCD349_ERR_ON_USB,      /**< lcd349_power_off(): still running after the check delay, so USB powers the board */
    LCD349_ERR_TIMEOUT,     /**< the operation didn't finish in time */
    LCD349_ERR_IO,          /**< a bus, file or radio operation failed */
    LCD349_ERR_NO_MEM,      /**< out of memory */
    LCD349_ERR_NOT_FOUND,   /**< no such key, network or item */
    LCD349_ERR_STATE,       /**< not allowed now, for example before lcd349_init() */
} lcd349_err_t;

/** Returns a short constant name for err, such as "LCD349_ERR_ON_USB". Never NULL. */
const char *lcd349_err_str(lcd349_err_t err);

/** Subsystems, as bits in lcd349_board_status(). */
typedef enum {
    LCD349_SUB_POWER = 1u << 0,
    LCD349_SUB_DISPLAY = 1u << 1,
    LCD349_SUB_TOUCH = 1u << 2,
    LCD349_SUB_BUTTONS = 1u << 3,
    LCD349_SUB_BATTERY = 1u << 4,
    LCD349_SUB_AUDIO = 1u << 5,
    LCD349_SUB_RTC = 1u << 6,
    LCD349_SUB_STORAGE = 1u << 7,
    LCD349_SUB_WIFI = 1u << 8,
} lcd349_subsystem_t;

typedef struct {
    uint8_t backlight_pct;    /**< initial backlight, 0-100 */
    uint16_t touch_period_ms; /**< touch sampling period; 0 = the board's period */
    bool wifi_power_save;     /**< modem power save while connected */
} lcd349_config_t;

/** Default configuration: the board's backlight default and touch period, Wi-Fi power save on. */
#define LCD349_CONFIG_DEFAULT() {LCD349_BACKLIGHT_DEFAULT_PCT, 0, true}

/**
 * Brings the board up. On the board it asserts the power hold before anything
 * slow (requirement 1.3), then starts the display, touch, buttons, battery,
 * audio, RTC and storage, and the kit tasks lcd349_ui and lcd349_evt. Wi-Fi
 * starts only with lcd349_wifi_start().
 *
 * A subsystem that fails doesn't stop the others: check lcd349_board_status().
 * Call once, from app_main or a product task. cfg may be NULL for defaults.
 *
 * Returns LCD349_OK when the power hold and display are up (other failures
 * show in the status), LCD349_ERR_STATE if already initialised, or the
 * display's error.
 */
lcd349_err_t lcd349_init(const lcd349_config_t *cfg);

/** Bitmask of lcd349_subsystem_t values that failed at init. 0 = all good. */
uint32_t lcd349_board_status(void);

/**
 * Monotonic time in microseconds since start. Use this rather than esp_timer,
 * which the emulator doesn't simulate. Safe from any task, before init too.
 */
uint64_t lcd349_time_us(void);

/** lcd349_time_us() in milliseconds, wrapping after about 49 days. */
uint32_t lcd349_time_ms(void);

/** Boot reasons, for crash detection. */
typedef enum {
    LCD349_BOOT_POWER_ON = 0, /**< cold start, or power-on in the emulator */
    LCD349_BOOT_SOFTWARE,     /**< esp_restart or an emulator reset */
    LCD349_BOOT_CRASH,        /**< panic or watchdog */
    LCD349_BOOT_BROWNOUT,
    LCD349_BOOT_OTHER,
} lcd349_boot_reason_t;

/** Why this boot happened. */
lcd349_boot_reason_t lcd349_boot_reason(void);

/** Memory capabilities for lcd349_alloc(). */
typedef enum {
    LCD349_MEM_INTERNAL = 1u << 0, /**< internal RAM: fast, scarce */
    LCD349_MEM_PSRAM = 1u << 1,    /**< external PSRAM: large, slower */
    LCD349_MEM_ANY = LCD349_MEM_INTERNAL | LCD349_MEM_PSRAM, /**< PSRAM first, then internal */
} lcd349_mem_t;

/**
 * Allocates size bytes with the given capability and counts it, so the
 * emulator can report usage against the board's budgets (requirement 3.13).
 * Returns NULL on failure. Free with lcd349_free(). Safe from any task.
 */
void *lcd349_alloc(size_t size, lcd349_mem_t caps);

/** Frees memory from lcd349_alloc(). NULL is ignored. */
void lcd349_free(void *ptr);

typedef struct {
    size_t internal_bytes; /**< currently allocated through lcd349_alloc */
    size_t psram_bytes;
    size_t internal_peak;
    size_t psram_peak;
    size_t largest; /**< largest single allocation so far */
} lcd349_mem_stats_t;

/** Fills stats with lcd349_alloc() usage. */
void lcd349_mem_stats(lcd349_mem_stats_t *stats);

#ifdef __cplusplus
}
#endif
