/* SPDX-License-Identifier: Apache-2.0 */
#include "debounce.h"

void lcd349__debounce_init(lcd349_debounce_t *d, bool level, uint32_t now_ms)
{
    d->stable = level;
    d->raw = level;
    d->raw_since = now_ms;
}

void lcd349__debounce_raw(lcd349_debounce_t *d, bool level, uint32_t t_ms)
{
    if (level != d->raw) {
        d->raw = level;
        d->raw_since = t_ms;
    }
}

bool lcd349__debounce_settle(lcd349_debounce_t *d, uint32_t now_ms, uint32_t ms, uint32_t *edge_ms)
{
    if (d->raw == d->stable || (uint32_t)(now_ms - d->raw_since) < ms) {
        return false;
    }
    d->stable = d->raw;
    if (edge_ms) {
        *edge_ms = d->raw_since;
    }
    return true;
}

uint32_t lcd349__debounce_due_in(const lcd349_debounce_t *d, uint32_t now_ms, uint32_t ms)
{
    if (d->raw == d->stable) {
        return UINT32_MAX;
    }
    uint32_t elapsed = now_ms - d->raw_since;
    return elapsed >= ms ? 0 : ms - elapsed;
}
