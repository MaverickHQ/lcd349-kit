/* SPDX-License-Identifier: Apache-2.0
 *
 * Button debounce, pure (no tasks, no hardware). A raw level must hold for
 * `ms` before it becomes the stable level; shorter glitches are dropped. The
 * reported edge time is when the final raw transition started, so hold
 * durations are measured from the real press.
 */
#pragma once

#include <stdbool.h>
#include <stdint.h>

typedef struct {
    bool stable;        /* debounced level, true = pressed */
    bool raw;           /* last raw level */
    uint32_t raw_since; /* when raw last changed */
} lcd349_debounce_t;

void lcd349__debounce_init(lcd349_debounce_t *d, bool level, uint32_t now_ms);

/* Records a raw level at t_ms. Repeating the current raw level changes nothing. */
void lcd349__debounce_raw(lcd349_debounce_t *d, bool level, uint32_t t_ms);

/* If raw has differed from stable for at least ms by now_ms, makes it stable
 * and returns true with *edge_ms set to when it changed. */
bool lcd349__debounce_settle(lcd349_debounce_t *d, uint32_t now_ms, uint32_t ms, uint32_t *edge_ms);

/* Milliseconds until settle could report an edge, or UINT32_MAX if none is pending. */
uint32_t lcd349__debounce_due_in(const lcd349_debounce_t *d, uint32_t now_ms, uint32_t ms);
