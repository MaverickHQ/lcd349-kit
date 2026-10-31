/* SPDX-License-Identifier: Apache-2.0
 *
 * Power, sleep and battery.
 *
 * On battery the board stays on only while the power hold is asserted;
 * lcd349_init() asserts it first. Releasing it powers the board off, unless
 * USB is supplying power. Touch never wakes the board: only BOOT or PWR do.
 */
#pragma once

#include <stdbool.h>
#include <stdint.h>

#include "lcd349_core.h"
#include "lcd349_input.h"

#ifdef __cplusplus
extern "C" {
#endif

/** Asserts (true) or releases (false) the power hold. Releasing it on battery powers off. */
lcd349_err_t lcd349_power_hold(bool hold);

/**
 * Powers the board off by releasing the power hold. On battery this call
 * doesn't return. If the board is still running after the USB check delay
 * (LCD349_POWER_OFF_USB_CHECK_MS), USB is powering it: the hold is
 * re-asserted and the call returns LCD349_ERR_ON_USB (requirement 2.4).
 * Show the user something like "unplug USB to power off".
 */
lcd349_err_t lcd349_power_off(void);

/**
 * Light sleep until BOOT or PWR is pressed, or until timeout_ms passes
 * (0 = no timeout). The display sleeps first, and Wi-Fi stays associated with
 * modem power save. On a button wake, *woke_by (if not NULL) is set and the
 * press is also delivered as a normal button event, so a product can treat the
 * waking press as wake-only.
 * Returns LCD349_OK on a button wake, LCD349_ERR_TIMEOUT on timeout.
 * Call from a product task, not from lcd349_ui or lcd349_evt.
 */
lcd349_err_t lcd349_sleep_until_button(uint32_t timeout_ms, lcd349_btn_t *woke_by);

/** Where the board's power comes from (requirement 1.8). */
typedef enum {
    LCD349_POWER_SOURCE_UNKNOWN = 0, /**< the board can't tell, or before lcd349_init */
    LCD349_POWER_SOURCE_USB,         /**< USB is powering the board */
    LCD349_POWER_SOURCE_BATTERY,     /**< the battery is powering the board */
} lcd349_power_source_t;

/**
 * The power source, as the kit last saw it: it checks once a second, so a
 * change shows within about a second. Unlike lcd349_power_off(), this never
 * powers the board off to find out.
 *
 * On the board, USB means a USB host is connected: a charger with no data
 * (a wall adapter, a power bank) reads as BATTERY, though it charges the
 * battery. UNKNOWN when the board can't tell. The emulator reports its model
 * (lcd349 run --usb or --battery, and lcd349 state set).
 */
lcd349_power_source_t lcd349_power_source(void);

/** Called when the power source changes, with the new one. */
typedef void (*lcd349_power_source_cb_t)(lcd349_power_source_t source, void *ctx);

/**
 * Sets the one power-source callback (NULL removes it), replacing any
 * earlier one. It runs on the kit's event task, like the button and Wi-Fi
 * callbacks, once for each change; not when it is set.
 */
lcd349_err_t lcd349_power_on_source_change(lcd349_power_source_cb_t cb, void *ctx);

/**
 * Reads the battery: millivolts at the cell (averaged, divider applied) and
 * an estimated percentage from the discharge curve. Either pointer may be NULL.
 */
lcd349_err_t lcd349_battery_read(uint16_t *mv, uint8_t *pct);

/** Maps cell millivolts to 0-100 % with the board's discharge curve. Pure. */
uint8_t lcd349_battery_pct_from_mv(uint16_t mv);

#ifdef __cplusplus
}
#endif
