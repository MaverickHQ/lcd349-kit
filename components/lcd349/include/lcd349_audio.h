/* SPDX-License-Identifier: Apache-2.0
 *
 * Tones on the board's speaker. Calls return at once; playback runs on a kit
 * task. A new tone or sequence replaces whatever is playing. In the emulator,
 * tones are recorded as events and played by the viewer.
 */
#pragma once

#include <stddef.h>
#include <stdint.h>

#include "lcd349_core.h"

#ifdef __cplusplus
extern "C" {
#endif

/** One step of a tone sequence. hz = 0 is a rest. */
typedef struct {
    uint16_t hz;
    uint16_t ms;
} lcd349_tone_t;

#define LCD349_TONE_SEQ_MAX 32

/** Plays hz for ms at volume 0-100. hz 20-20000; ms 1-10000. */
lcd349_err_t lcd349_tone(uint16_t hz, uint16_t ms, uint8_t volume);

/** Plays a sequence of up to LCD349_TONE_SEQ_MAX steps (copied) at volume 0-100. */
lcd349_err_t lcd349_tone_seq(const lcd349_tone_t *steps, size_t n, uint8_t volume);

/** Stops any tone. */
lcd349_err_t lcd349_tone_stop(void);

#ifdef __cplusplus
}
#endif
