/* SPDX-License-Identifier: Apache-2.0
 *
 * The AXS15231B touch-report decoder (axs_touch.h). The report's layout is
 * the one the board sent (spikes/hw/RESULTS.md §Touch): byte 0 the gesture,
 * byte 1 the point count, then 6 bytes per point.
 */
#include "axs_touch.h"

#include "lcd349_board.h"

/* A point's event, in the top two bits of its first byte (0 down, 2 contact). */
#define EVENT_LIFT 1
/* No-report reads in a row that release a held touch (requirement 2.4). */
#define SILENT_READS_RELEASE 3

/* No new report: the controller answers 0x30 in every byte, with no finger
 * and between reports while one is held. */
static bool is_no_report(const uint8_t *report)
{
    for (int i = 0; i < AXS_TOUCH_REPORT_LEN; i++) {
        if (report[i] != 0x30) {
            return false;
        }
    }
    return true;
}

/* A report the board sends has gesture 0 and one or two points. Anything else
 * (a frame shifted by a byte reads a point count of 0) is malformed. */
static bool is_malformed(const uint8_t *report)
{
    return report[0] != 0 || (report[1] != 1 && report[1] != 2);
}

axs_touch_point_t axs_touch_decode(const uint8_t report[AXS_TOUCH_REPORT_LEN], axs_touch_state_t *state)
{
    if (is_no_report(report)) {
        /* Only a held touch counts its silent reads. */
        if (state->point.pressed && ++state->silent >= SILENT_READS_RELEASE) {
            state->point.pressed = false;
        }
        return state->point;
    }
    /* A malformed frame changes nothing, the silent count included. */
    if (is_malformed(report)) {
        return state->point;
    }
    state->silent = 0;
    /* The first point: x[11:8] under the event bits, y[11:8] under the touch ID. */
    const uint8_t *p = report + 2;
    if (p[0] >> 6 == EVENT_LIFT) {
        state->point.pressed = false;
        return state->point;
    }
    uint16_t x = (uint16_t)((p[0] & 0x0f) << 8 | p[1]);
    uint16_t y = (uint16_t)((p[2] & 0x0f) << 8 | p[3]);
    /* The raw axes are landscape turned 180°: the panel's x is raw y, and its
     * y runs against raw x. */
    state->point = (axs_touch_point_t){.pressed = true, .px = y, .py = (uint16_t)(LCD349_PANEL_H - 1 - x)};
    return state->point;
}
