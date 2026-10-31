/* SPDX-License-Identifier: Apache-2.0
 *
 * The AXS15231B touch-report decoder, pure (seam S1-3; kit ADR 0002). The
 * hardware backend reads a report over I2C and hands it here; the host tests
 * feed it the frames captured on the board. The decoder keeps the touch state
 * across reads: a no-report frame (0x30 in every byte) or a malformed one
 * changes nothing, a lift event or enough silent reads in a row release, and
 * a two-point report keeps the first point (requirement 2.2-2.6).
 */
#pragma once

#include <stdbool.h>
#include <stdint.h>

#define AXS_TOUCH_REPORT_LEN 14

/* What one read decodes to. px, py: panel-native, the last point seen while
 * pressed. */
typedef struct {
    bool pressed;
    uint16_t px, py;
} axs_touch_point_t;

/* The state carried from one read to the next. Zero it to start released. */
typedef struct {
    axs_touch_point_t point; /* what the last decode returned */
    uint8_t silent;          /* no-report reads in a row */
} axs_touch_state_t;

/* Decodes one report into state and returns the touch it now describes. */
axs_touch_point_t axs_touch_decode(const uint8_t report[AXS_TOUCH_REPORT_LEN], axs_touch_state_t *state);
