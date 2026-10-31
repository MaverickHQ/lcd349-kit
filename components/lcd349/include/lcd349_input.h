/* SPDX-License-Identifier: Apache-2.0
 *
 * Touch, buttons, and the button gesture classifier.
 *
 * Touch is single-point, sampled every touch period while the display is
 * awake, and never wakes the device. Buttons are BOOT and PWR, debounced by
 * the kit. Holding BOOT through a reset enters the ROM download mode, so never
 * design a boot-time BOOT combination.
 *
 * Subscriber callbacks run on the kit task lcd349_evt, in order. Keep them
 * short; to touch LVGL from one, use lcd349_ui_call() or the UI lock.
 */
#pragma once

#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>

#include "lvgl.h"
#include "lcd349_core.h"

#ifdef __cplusplus
extern "C" {
#endif

/* ---- Touch ---- */

/** One touch sample in landscape coordinates (0-639, 0-171). */
typedef struct {
    uint16_t x;
    uint16_t y;
    bool pressed;  /**< false: the finger lifted; x and y repeat the last point */
    uint32_t t_ms; /**< lcd349_time_ms() when sampled */
} lcd349_touch_sample_t;

typedef void (*lcd349_touch_cb_t)(const lcd349_touch_sample_t *sample, void *ctx);

/** The LVGL pointer input device for the touch screen, or NULL if touch failed. */
lv_indev_t *lcd349_touch_indev(void);

/**
 * Subscribes to raw first-point samples: one per touch period while a finger
 * is down, plus one with pressed=false when it lifts. Up to 4 subscribers.
 */
lcd349_err_t lcd349_touch_subscribe(lcd349_touch_cb_t cb, void *ctx);

/* ---- Buttons ---- */

typedef enum {
    LCD349_BTN_BOOT = 0,
    LCD349_BTN_PWR = 1,
    LCD349_BTN_COUNT = 2,
} lcd349_btn_t;

/** A debounced button edge. */
typedef struct {
    lcd349_btn_t btn;
    bool down;
    uint32_t t_ms; /**< when the edge happened (start of the debounced transition) */
} lcd349_btn_event_t;

typedef void (*lcd349_btn_cb_t)(const lcd349_btn_event_t *ev, void *ctx);

/** Subscribes to debounced down and up edges of both buttons. Up to 4 subscribers. */
lcd349_err_t lcd349_buttons_subscribe(lcd349_btn_cb_t cb, void *ctx);

/** True while btn is held (debounced). */
bool lcd349_button_is_down(lcd349_btn_t btn);

/* ---- Gesture classifier (optional, pure: no tasks, no hardware) ----
 *
 * Feed it button edges and call lcd349_classifier_tick() regularly (every
 * 10-20 ms, for example from an lv_timer) while a button is down. It turns
 * edges into gestures:
 *   SHORT     released before the first hold threshold
 *   HOLD      still down and has just reached hold threshold `level` (1-based);
 *             use it to commit at the moment the hold completes
 *   HOLD_END  released after reaching threshold `level`
 *   CHORD     the second button went down within chord_window_ms of the first,
 *             before either reached a hold; both are then consumed until released
 *   CHORD_END both buttons are up again after a chord
 */

#define LCD349_CLASSIFIER_MAX_HOLDS 3
/** Room to pass for `out`: no call emits more gestures than this. */
#define LCD349_CLASSIFIER_MAX_OUT 8

typedef struct {
    uint16_t hold_ms[LCD349_CLASSIFIER_MAX_HOLDS]; /**< strictly ascending; 0 ends the list */
    uint16_t chord_window_ms;                      /**< 0 disables chords; must be below hold_ms[0] */
} lcd349_classifier_cfg_t;

typedef enum {
    LCD349_GESTURE_SHORT,
    LCD349_GESTURE_HOLD,
    LCD349_GESTURE_HOLD_END,
    LCD349_GESTURE_CHORD,
    LCD349_GESTURE_CHORD_END,
} lcd349_gesture_kind_t;

typedef struct {
    lcd349_gesture_kind_t kind;
    lcd349_btn_t btn; /**< CHORD and CHORD_END: the button that went down first */
    uint8_t level;    /**< HOLD and HOLD_END: 1-based threshold index; otherwise 0 */
    uint32_t t_ms;    /**< when the gesture happened */
    uint32_t held_ms; /**< SHORT and HOLD_END: how long the button was down; HOLD: the threshold */
} lcd349_gesture_t;

/** Classifier state. Treat the fields as private. */
typedef struct {
    lcd349_classifier_cfg_t cfg;
    uint8_t n_holds;
    struct {
        bool down;
        bool consumed;
        uint8_t level;
        uint32_t t_down;
    } b[LCD349_BTN_COUNT];
    bool chord_active;
    lcd349_btn_t chord_first;
} lcd349_classifier_t;

/** Initialises c. Returns LCD349_ERR_ARG if thresholds aren't ascending or the chord window isn't below the first. */
lcd349_err_t lcd349_classifier_init(lcd349_classifier_t *c, const lcd349_classifier_cfg_t *cfg);

/**
 * Feeds one debounced edge. Emits any hold reached before ev->t_ms first.
 * Writes up to max gestures to out and returns how many.
 */
size_t lcd349_classifier_feed(lcd349_classifier_t *c, const lcd349_btn_event_t *ev, lcd349_gesture_t *out, size_t max);

/** Emits HOLD gestures whose threshold has passed by now_ms. Returns how many. */
size_t lcd349_classifier_tick(lcd349_classifier_t *c, uint32_t now_ms, lcd349_gesture_t *out, size_t max);

/**
 * Progress toward btn's next hold threshold, 0-1000, for a fill bar.
 * 0 when the button is up or consumed by a chord; 1000 once the last threshold is reached.
 */
uint16_t lcd349_classifier_hold_permille(const lcd349_classifier_t *c, lcd349_btn_t btn, uint32_t now_ms);

#ifdef __cplusplus
}
#endif
