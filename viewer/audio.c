/* SPDX-License-Identifier: Apache-2.0 */
#include "audio.h"

#include <SDL.h>
#include <string.h>

#define MAX_NOTES 32
#define LATENCY_MS 60   /* event time to audio time: enough to keep a burst's spacing */
#define RESYNC_MS 250   /* re-anchor when an event lands this far in the past */
#define FULL_SCALE 6000 /* amplitude at volume 100: gentle on laptop speakers */

typedef struct {
    uint64_t start, end; /* in samples */
    uint32_t hz;
    int16_t amp;
    bool started;
} note_t;

static SDL_AudioDeviceID s_dev;
static int s_rate;
/* Shared with the callback; changed only with the device locked. */
static note_t s_notes[MAX_NOTES];
static int s_n;
static uint64_t s_pos; /* samples rendered so far */
static double s_phase;
static unsigned s_started;
/* Main thread only. */
static bool s_synced;
static int64_t s_offset_ms;
static unsigned s_received;

static void callback(void *ud, Uint8 *stream, int len)
{
    (void)ud;
    int16_t *out = (int16_t *)stream;
    int n = len / (int)sizeof *out;
    for (int i = 0; i < n; i++) {
        uint64_t pos = s_pos + (uint64_t)i;
        while (s_n > 0 && s_notes[0].end <= pos) {
            memmove(s_notes, s_notes + 1, (size_t)(--s_n) * sizeof *s_notes);
        }
        int16_t v = 0;
        if (s_n > 0 && s_notes[0].start <= pos) {
            note_t *nt = &s_notes[0];
            if (!nt->started) {
                nt->started = true;
                s_started++;
                s_phase = 0;
            }
            if (nt->hz) {
                s_phase += (double)nt->hz / s_rate;
                if (s_phase >= 1.0) {
                    s_phase -= (int)s_phase;
                }
                v = s_phase < 0.5 ? nt->amp : (int16_t)-nt->amp;
            }
        }
        out[i] = v;
    }
    s_pos += (uint64_t)n;
}

bool view_audio_open(void)
{
    if (SDL_InitSubSystem(SDL_INIT_AUDIO) != 0) {
        return false;
    }
    SDL_AudioSpec want = {0}, have;
    want.freq = 48000;
    want.format = AUDIO_S16SYS;
    want.channels = 1;
    want.samples = 512;
    want.callback = callback;
    s_dev = SDL_OpenAudioDevice(NULL, 0, &want, &have, SDL_AUDIO_ALLOW_FREQUENCY_CHANGE);
    if (!s_dev) {
        SDL_QuitSubSystem(SDL_INIT_AUDIO);
        return false;
    }
    s_rate = have.freq;
    SDL_PauseAudioDevice(s_dev, 0);
    return true;
}

void view_audio_close(void)
{
    if (s_dev) {
        SDL_CloseAudioDevice(s_dev);
        s_dev = 0;
        SDL_QuitSubSystem(SDL_INIT_AUDIO);
    }
}

/* Ends every note at sample `at`: a later tone or a stop replaces it. */
static void cut_at(uint64_t at)
{
    int keep = 0;
    for (int i = 0; i < s_n; i++) {
        if (s_notes[i].start >= at) {
            continue;
        }
        if (s_notes[i].end > at) {
            s_notes[i].end = at;
        }
        s_notes[keep++] = s_notes[i];
    }
    s_n = keep;
}

/* Instance time to audio sample position. Call with the device locked. */
static uint64_t to_samples(uint32_t t_ms)
{
    int64_t now_ms = (int64_t)(s_pos * 1000u / (uint64_t)s_rate);
    int64_t at = (int64_t)t_ms + s_offset_ms;
    if (!s_synced || at < now_ms - RESYNC_MS || at > now_ms + 2000) {
        s_offset_ms = now_ms + LATENCY_MS - (int64_t)t_ms;
        at = now_ms + LATENCY_MS;
        s_synced = true;
    }
    if (at < now_ms) {
        at = now_ms;
    }
    return (uint64_t)at * (uint64_t)s_rate / 1000u;
}

void view_audio_tone(uint32_t t_ms, uint16_t hz, uint16_t ms, uint8_t volume)
{
    s_received++;
    if (!s_dev) {
        return;
    }
    SDL_LockAudioDevice(s_dev);
    uint64_t start = to_samples(t_ms);
    cut_at(start);
    if (s_n < MAX_NOTES) {
        s_notes[s_n++] = (note_t){start, start + (uint64_t)ms * (uint64_t)s_rate / 1000u, hz,
                                  (int16_t)(FULL_SCALE * (volume > 100 ? 100 : volume) / 100), false};
    }
    SDL_UnlockAudioDevice(s_dev);
}

void view_audio_stop(uint32_t t_ms)
{
    if (!s_dev) {
        return;
    }
    SDL_LockAudioDevice(s_dev);
    cut_at(to_samples(t_ms));
    SDL_UnlockAudioDevice(s_dev);
}

void view_audio_resync(void)
{
    s_synced = false;
}

unsigned view_audio_received(void)
{
    return s_received;
}

unsigned view_audio_started(void)
{
    unsigned n = 0;
    if (s_dev) {
        SDL_LockAudioDevice(s_dev);
        n = s_started;
        SDL_UnlockAudioDevice(s_dev);
    }
    return n;
}
