/* SPDX-License-Identifier: Apache-2.0
 *
 * Plays the instance's tone events (control method audio.events) as square
 * waves through SDL audio (requirement 3.8). Tones keep their spacing: event
 * times are mapped onto the audio clock with a small fixed latency.
 */
#pragma once

#include <stdbool.h>
#include <stdint.h>

/* Opens the default output. False if there is none: the viewer stays silent. */
bool view_audio_open(void);
void view_audio_close(void);
/* A tone at instance time t_ms. A new tone replaces the one playing, as on the board. */
void view_audio_tone(uint32_t t_ms, uint16_t hz, uint16_t ms, uint8_t volume);
void view_audio_stop(uint32_t t_ms);
/* The instance restarted: its clock starts again. */
void view_audio_resync(void);
/* Tones received, and tones whose first sample has been played. */
unsigned view_audio_received(void);
unsigned view_audio_started(void);
