/* SPDX-License-Identifier: Apache-2.0
 *
 * Display and UI threading.
 *
 * Products draw with LVGL 9 directly, in landscape 640x172. The kit renders
 * full frames, rotates them into the panel's portrait orientation, swaps the
 * RGB565 bytes and sends the result to the panel (or, in the emulator, to the
 * same framebuffer bytes), so both targets see identical pixels.
 *
 * Threading: LVGL runs on the kit task lcd349_ui. Touch LVGL only
 * - inside LVGL callbacks (event handlers, timers), which run on lcd349_ui, or
 * - between lcd349_ui_lock() and lcd349_ui_unlock() from any other task.
 */
#pragma once

#include <stdbool.h>
#include <stdint.h>

#include "lvgl.h"
#include "lcd349_core.h"

#ifdef __cplusplus
extern "C" {
#endif

/** The landscape 640x172 LVGL display, or NULL before lcd349_init() or if the display failed. */
lv_display_t *lcd349_display(void);

/** Sets the backlight, 0-100 %. 0 is dark, and touch isn't read while dark (requirement 1.5). */
lcd349_err_t lcd349_backlight_set(uint8_t pct);

/** The current backlight level, 0-100. */
uint8_t lcd349_backlight_get(void);

/**
 * Puts the display to sleep (backlight off, rendering paused, touch not read)
 * or wakes it (backlight restored, the current screen redrawn). Touch never
 * wakes the display: wake it from a button handler.
 */
lcd349_err_t lcd349_display_sleep(bool sleep);

/** True while the display is asleep. */
bool lcd349_display_is_asleep(void);

/** Takes the LVGL lock. Recursive: safe inside LVGL callbacks. Never call from an interrupt. */
void lcd349_ui_lock(void);

/** Releases the LVGL lock. */
void lcd349_ui_unlock(void);

/**
 * Queues fn(ctx) to run on lcd349_ui with the LVGL lock held, and returns at
 * once. Use it to update the UI from network or timer tasks.
 * Returns LCD349_ERR_NO_MEM if the queue is full, LCD349_ERR_STATE before init.
 */
lcd349_err_t lcd349_ui_call(void (*fn)(void *ctx), void *ctx);

/**
 * Tags an object for the UI checker (requirement 6.4). Tags are short words:
 * "list-row" exempts a dense list row from the touch-target rule, and
 * "ignore:<rule-id>" silences one rule for this object. Call it several times
 * to add several tags. Tags show up in ui.tree, so exceptions are reviewable.
 * Call on lcd349_ui or with the lock held. Tags are removed with the object.
 */
lcd349_err_t lcd349_ui_tag(lv_obj_t *obj, const char *tag);

/** True if obj carries tag. */
bool lcd349_ui_has_tag(const lv_obj_t *obj, const char *tag);

/**
 * Copies obj's tags, comma-separated, into out (always NUL-terminated).
 * Returns the number of tags.
 */
size_t lcd349_ui_tags(const lv_obj_t *obj, char *out, size_t out_len);

#ifdef __cplusplus
}
#endif
