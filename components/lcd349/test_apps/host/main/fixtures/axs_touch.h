/* SPDX-License-Identifier: Apache-2.0
 *
 * Touch reports captured on the board on 2026-10-18 (task 0.3), the expected
 * values for the decoder's tests (seam S1-3). Each frame is the 14 bytes the
 * AXS15231B answered, copied from spikes/hw/logs/touch-raw-two-minutes.log
 * with the log line, the read's t_ms and what the owner was doing. `twatch`
 * read every 32 ms and logged every no-report frame, every change of point
 * count and every two-point report, but a one-point report at most every
 * 100 ms.
 *
 * The format (spikes/hw/RESULTS.md §Touch): byte 0 is the gesture, byte 1 the
 * point count, then 6 bytes per point:
 *   0: event in the top two bits (0 down, 2 contact, 1 lift), then x[11:8];
 *   1: x[7:0];
 *   2: touch ID in the top nibble, then y[11:8];
 *   3: y[7:0];
 *   4, 5: two bytes that count up.
 * The raw coordinates are landscape rotated 180°: landscape x = 639 − raw x,
 * y = 171 − raw y. The panel's own (portrait) point, which the decoder
 * returns, is px = raw y, py = 639 − raw x.
 *
 * touch-calibration.log (`tcal`) logged decoded points only, no frames. Its
 * four touches, target landscape point ← raw point, picked that mapping:
 *   (60, 40) ← (578, 105);  (580, 40) ← (55, 125);
 *   (580, 132) ← (55, 17);  (60, 132) ← (606, 87), the owner's mistouch.
 */
#pragma once

#include <stdint.h>

/* The kit's header, from the include path: "axs_touch.h" would find this file. */
#include <axs_touch.h>

/* No new report: 0x30 in every byte. The controller answers this with no
 * finger, and between reports while a finger is held. Line 1331, t_ms 42390,
 * the read after the top-left hold's last report. */
static const uint8_t NO_REPORT[] = {
    0x30, 0x30, 0x30, 0x30, 0x30, 0x30, 0x30, 0x30, 0x30, 0x30, 0x30, 0x30, 0x30, 0x30,
};

/* The owner's finger lands on the top-left corner. Line 1313, t_ms 40216:
 * one point, event 0 (down), raw (639, 123): landscape (0, 48), panel
 * (123, 0). No second point (ff). */
static const uint8_t HOLD_TL_DOWN[] = {
    0x00, 0x01, 0x02, 0x7f, 0x00, 0x7b, 0x5c, 0x5d, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff,
};

/* The same finger held still, the next report. Line 1314, t_ms 40340:
 * event 2 (contact), raw (639, 123). The hold ended with no lift event: the
 * reads after line 1330 are all no-report frames. */
static const uint8_t HOLD_TL_CONTACT[] = {
    0x00, 0x01, 0x82, 0x7f, 0x00, 0x7b, 0x64, 0x65, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff,
};

/* The finger leaves the bottom-right corner. Line 1621, t_ms 56485: event 1
 * (lift), raw (13, 10): landscape (626, 161), panel (10, 626). */
static const uint8_t LIFT[] = {
    0x00, 0x01, 0x40, 0x0d, 0x00, 0x0a, 0x43, 0x44, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff,
};

/* Malformed: a report shifted right by one byte, while the bottom-left
 * corner was held. Line 1675, t_ms 59481. Its point count reads 0; the next
 * read (line 1676) is the report itself, 00 01 82 60 00 1a 42 43 ff …,
 * raw (608, 26). */
static const uint8_t SHIFTED[] = {
    0x00, 0x00, 0x01, 0x82, 0x60, 0x00, 0x1a, 0x40, 0x41, 0xff, 0xff, 0xff, 0xff, 0xff,
};

/* Two fingers, one on each half. Line 2189, t_ms 81835: two points.
 * The first: event 2 (contact), ID 0, raw (143, 91): panel (91, 496).
 * The second: event 0 (down), ID 1, raw (457, 55). */
static const uint8_t TWO_POINTS[] = {
    0x00, 0x02, 0x80, 0x8f, 0x00, 0x5b, 0x6a, 0x6b, 0x01, 0xc9, 0x10, 0x37, 0x6b, 0x6c,
};

/* The bytes after the counted points aren't always ff. Line 1602,
 * t_ms 54127, during the bottom-right hold: one point, event 2 (contact),
 * raw (11, 6), and bytes 10-13 repeat the frame's start. */
static const uint8_t ONE_POINT_ECHO_TAIL[] = {
    0x00, 0x01, 0x80, 0x0b, 0x00, 0x06, 0x21, 0x22, 0xff, 0xff, 0x00, 0x01, 0x80, 0x0b,
};

/* The same in a two-point report. Line 2201, t_ms 82222, the two-finger
 * hold: the first point, event 2 (contact), raw (147, 85), is sound; the
 * second point's slot holds a copy of the frame's start (bytes 8-13). The
 * reads either side give the second finger as raw (457, 55). */
static const uint8_t TWO_POINTS_ECHO_SECOND[] = {
    0x00, 0x02, 0x80, 0x93, 0x00, 0x55, 0x2b, 0x2c, 0x00, 0x02, 0x80, 0x94, 0x00, 0x55,
};

_Static_assert(sizeof NO_REPORT == AXS_TOUCH_REPORT_LEN, "NO_REPORT");
_Static_assert(sizeof HOLD_TL_DOWN == AXS_TOUCH_REPORT_LEN, "HOLD_TL_DOWN");
_Static_assert(sizeof HOLD_TL_CONTACT == AXS_TOUCH_REPORT_LEN, "HOLD_TL_CONTACT");
_Static_assert(sizeof LIFT == AXS_TOUCH_REPORT_LEN, "LIFT");
_Static_assert(sizeof SHIFTED == AXS_TOUCH_REPORT_LEN, "SHIFTED");
_Static_assert(sizeof TWO_POINTS == AXS_TOUCH_REPORT_LEN, "TWO_POINTS");
_Static_assert(sizeof ONE_POINT_ECHO_TAIL == AXS_TOUCH_REPORT_LEN, "ONE_POINT_ECHO_TAIL");
_Static_assert(sizeof TWO_POINTS_ECHO_SECOND == AXS_TOUCH_REPORT_LEN, "TWO_POINTS_ECHO_SECOND");
