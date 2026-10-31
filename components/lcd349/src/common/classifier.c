/* SPDX-License-Identifier: Apache-2.0
 *
 * Button gesture classifier: pure, so it is unit-tested on the host and
 * behaves identically on the board and in the emulator.
 */
#include <string.h>

#include "lcd349_input.h"

lcd349_err_t lcd349_classifier_init(lcd349_classifier_t *c, const lcd349_classifier_cfg_t *cfg)
{
    if (!c || !cfg) {
        return LCD349_ERR_ARG;
    }
    uint8_t n = 0;
    while (n < LCD349_CLASSIFIER_MAX_HOLDS && cfg->hold_ms[n] != 0) {
        if (n > 0 && cfg->hold_ms[n] <= cfg->hold_ms[n - 1]) {
            return LCD349_ERR_ARG;
        }
        n++;
    }
    for (uint8_t i = n; i < LCD349_CLASSIFIER_MAX_HOLDS; i++) {
        if (cfg->hold_ms[i] != 0) {
            return LCD349_ERR_ARG; /* a gap: thresholds after a 0 */
        }
    }
    if (cfg->chord_window_ms != 0 && n > 0 && cfg->chord_window_ms >= cfg->hold_ms[0]) {
        return LCD349_ERR_ARG;
    }
    memset(c, 0, sizeof *c);
    c->cfg = *cfg;
    c->n_holds = n;
    return LCD349_OK;
}

static size_t emit(lcd349_gesture_t *out, size_t max, size_t n, lcd349_gesture_t g)
{
    if (n < max) {
        out[n] = g;
        return n + 1;
    }
    return n;
}

size_t lcd349_classifier_tick(lcd349_classifier_t *c, uint32_t now_ms, lcd349_gesture_t *out, size_t max)
{
    size_t n = 0;
    for (int i = 0; i < LCD349_BTN_COUNT; i++) {
        if (!c->b[i].down || c->b[i].consumed) {
            continue;
        }
        while (c->b[i].level < c->n_holds && (uint32_t)(now_ms - c->b[i].t_down) >= c->cfg.hold_ms[c->b[i].level]) {
            uint16_t th = c->cfg.hold_ms[c->b[i].level];
            c->b[i].level++;
            n = emit(out, max, n,
                     (lcd349_gesture_t){
                         .kind = LCD349_GESTURE_HOLD,
                         .btn = (lcd349_btn_t)i,
                         .level = c->b[i].level,
                         .t_ms = c->b[i].t_down + th,
                         .held_ms = th,
                     });
        }
    }
    return n;
}

size_t lcd349_classifier_feed(lcd349_classifier_t *c, const lcd349_btn_event_t *ev, lcd349_gesture_t *out, size_t max)
{
    if (!c || !ev || (unsigned)ev->btn >= LCD349_BTN_COUNT) {
        return 0;
    }
    /* Holds that completed before this edge come first. */
    size_t n = lcd349_classifier_tick(c, ev->t_ms, out, max);
    int i = ev->btn;
    int o = 1 - i;

    if (ev->down) {
        if (c->b[i].down) {
            return n; /* duplicate */
        }
        c->b[i].down = true;
        c->b[i].consumed = false;
        c->b[i].level = 0;
        c->b[i].t_down = ev->t_ms;
        if (c->cfg.chord_window_ms && c->b[o].down && !c->b[o].consumed && c->b[o].level == 0 &&
            (uint32_t)(ev->t_ms - c->b[o].t_down) <= c->cfg.chord_window_ms) {
            c->b[i].consumed = true;
            c->b[o].consumed = true;
            c->chord_active = true;
            c->chord_first = (lcd349_btn_t)o;
            n = emit(out, max, n,
                     (lcd349_gesture_t){.kind = LCD349_GESTURE_CHORD, .btn = (lcd349_btn_t)o, .t_ms = ev->t_ms});
        }
        return n;
    }

    if (!c->b[i].down) {
        return n; /* up without down */
    }
    c->b[i].down = false;
    uint32_t held = ev->t_ms - c->b[i].t_down;
    if (c->b[i].consumed) {
        if (c->chord_active && !c->b[o].down) {
            c->chord_active = false;
            n = emit(out, max, n,
                     (lcd349_gesture_t){.kind = LCD349_GESTURE_CHORD_END, .btn = c->chord_first, .t_ms = ev->t_ms});
        }
        return n;
    }
    if (c->b[i].level == 0) {
        return emit(out, max, n,
                    (lcd349_gesture_t){.kind = LCD349_GESTURE_SHORT, .btn = (lcd349_btn_t)i, .t_ms = ev->t_ms, .held_ms = held});
    }
    return emit(out, max, n,
                (lcd349_gesture_t){
                    .kind = LCD349_GESTURE_HOLD_END,
                    .btn = (lcd349_btn_t)i,
                    .level = c->b[i].level,
                    .t_ms = ev->t_ms,
                    .held_ms = held,
                });
}

uint16_t lcd349_classifier_hold_permille(const lcd349_classifier_t *c, lcd349_btn_t btn, uint32_t now_ms)
{
    if (!c || (unsigned)btn >= LCD349_BTN_COUNT) {
        return 0;
    }
    if (!c->b[btn].down || c->b[btn].consumed || c->n_holds == 0) {
        return 0;
    }
    uint8_t lvl = c->b[btn].level;
    if (lvl >= c->n_holds) {
        return 1000;
    }
    uint32_t from = lvl ? c->cfg.hold_ms[lvl - 1] : 0;
    uint32_t to = c->cfg.hold_ms[lvl];
    uint32_t held = now_ms - c->b[btn].t_down;
    if (held <= from) {
        return 0;
    }
    if (held >= to) {
        return 1000;
    }
    return (uint16_t)((held - from) * 1000u / (to - from));
}
