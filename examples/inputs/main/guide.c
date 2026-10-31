/* SPDX-License-Identifier: Apache-2.0
 *
 * The guided checkpoint (phase 1 task 9.1, seam S1-5). The host,
 *   go run ./tools/boardtest -guide -C examples/inputs
 * sends "@P guide". The guide then takes the screen and prompts, one step at a
 * time, for the checks that need a person: a touch in each corner, the
 * picture, a finger held still for 10 s (P1-N2), BOOT then PWR, the tone, a
 * light sleep woken by each button, power off on battery, and a white screen
 * for the lit width (X-03). It checks the rest itself: the console ping, the
 * battery, joining the host's test network, and power off on USB.
 *
 * Each result is a line, @R {"guide":{"step":…,"result":"PASS"|"FAIL",…}},
 * kept in storage (namespace "guide") with the step reached. The guide runs
 * only when the host asks, so a board left alone never walks through steps
 * nobody does; after a reset, or powering off on battery, the host's next
 * "@P guide" resumes it at the step it reached. Steps done off USB never reach
 * the host, so at the end the guide prints every stored result again
 * ("summary") before @R {"guide":{"done":…}}.
 *
 * The same source runs in the emulator, where nothing sends "@P guide".
 */
#include <stdarg.h>
#include <stdint.h>
#include <stdio.h>
#include <string.h>

#include "esp_attr.h"
#include "esp_log.h"
#include "freertos/FreeRTOS.h"
#include "freertos/queue.h"
#include "freertos/task.h"
#include "guide.h"
#include "lcd349.h"

static const char *TAG = "guide";

#define NS "guide"
#define RECORD_MAX 512
#define DETAIL_MAX 400

#define COLOR_BG 0x0B1622
#define COLOR_TEXT 0xFFFFFF
#define COLOR_MUTED 0xB8C4D0
#define COLOR_BUTTON 0x1565C0
#define COLOR_TARGET 0xFFB300

/* Targets sit in the corners; text keeps clear of them. */
#define TARGET_PX LCD349_RULE_TOUCH_TARGET_PX
#define INSET 4
#define TEXT_X (INSET + TARGET_PX + 12)
#define TEXT_W (LCD349_LCD_W - 2 * TEXT_X)
#define SWATCH_H 32
#define CHOICE_W 140
#define CHOICE_H TARGET_PX

#define OWNER_MS (10 * 60 * 1000) /* how long a step waits for the owner before it fails */
#define HOLD_MS 10000             /* P1-N2: a finger held still for 10 s */
#define OFF_HOLD_MS 5000          /* parent task 3.5: a 5 s PWR hold powers off on battery */

/* ---- Events, from the kit's tasks and the console to the guide's task ---- */

typedef enum { EV_TOUCH_DOWN, EV_TOUCH_UP, EV_BUTTON, EV_PING, EV_CHOICE } ev_kind_t;
typedef enum { CHOICE_YES, CHOICE_NO, CHOICE_AGAIN } choice_t;

typedef struct {
    ev_kind_t kind;
    int x, y;  /* touch, in landscape pixels */
    int which; /* button: lcd349_btn_t; choice: choice_t */
    bool down; /* button */
    uint32_t t_ms;
} ev_t;

static QueueHandle_t s_q;
static QueueHandle_t s_netq; /* one lcd349_wifi_net_t from "@P wifi" */
static volatile bool s_running;
static volatile int s_step;
static volatile bool s_touch_down;
static volatile uint32_t s_samples; /* touch samples with a finger down */
static bool s_unplugged;            /* the owner pressed BOOT to say USB is out */

static lv_obj_t *s_scr, *s_home, *s_title, *s_prompt, *s_detail, *s_target, *s_swatches;
static lv_obj_t *s_choice[3];

static void post(const ev_t *ev)
{
    if (s_running) {
        xQueueSend(s_q, ev, 0);
    }
}

static void on_touch(const lcd349_touch_sample_t *t, void *ctx)
{
    (void)ctx;
    if (t->pressed) {
        s_samples++;
    }
    if (t->pressed == s_touch_down) {
        return;
    }
    s_touch_down = t->pressed;
    ev_t ev = {.kind = t->pressed ? EV_TOUCH_DOWN : EV_TOUCH_UP, .x = t->x, .y = t->y, .t_ms = t->t_ms};
    post(&ev);
}

static void on_button(const lcd349_btn_event_t *e, void *ctx)
{
    (void)ctx;
    ev_t ev = {.kind = EV_BUTTON, .which = e->btn, .down = e->down, .t_ms = e->t_ms};
    post(&ev);
}

static void on_choice(lv_event_t *e)
{
    ev_t ev = {.kind = EV_CHOICE, .which = (int)(intptr_t)lv_event_get_user_data(e), .t_ms = lcd349_time_ms()};
    post(&ev);
}

/* ---- Waiting ---- */

static uint32_t now(void)
{
    return lcd349_time_ms();
}

static bool passed(uint32_t deadline)
{
    return (int32_t)(now() - deadline) >= 0;
}

/* The next event before deadline (lcd349_time_ms), or false. */
static bool next_until(ev_t *ev, uint32_t deadline)
{
    int32_t left = (int32_t)(deadline - now());
    return xQueueReceive(s_q, ev, pdMS_TO_TICKS(left > 0 ? left : 0)) == pdTRUE;
}

/* The next event of this kind before deadline; other events are dropped. */
static bool wait_kind(ev_kind_t kind, ev_t *ev, uint32_t deadline)
{
    while (next_until(ev, deadline)) {
        if (ev->kind == kind) {
            return true;
        }
    }
    return false;
}

/* A press and release of btn before deadline. */
static bool wait_press(lcd349_btn_t btn, uint32_t deadline)
{
    ev_t ev;
    bool down = false;
    while (wait_kind(EV_BUTTON, &ev, deadline)) {
        if (ev.which != (int)btn) {
            continue;
        }
        if (ev.down) {
            down = true;
        } else if (down) {
            return true;
        }
    }
    return false;
}

/* Waits until no button is down, then forgets queued events. */
static void wait_buttons_up(uint32_t ms)
{
    uint32_t end = now() + ms;
    while ((lcd349_button_is_down(LCD349_BTN_BOOT) || lcd349_button_is_down(LCD349_BTN_PWR)) && !passed(end)) {
        vTaskDelay(pdMS_TO_TICKS(20));
    }
    xQueueReset(s_q);
}

static void wait_touch_up(uint32_t ms)
{
    uint32_t end = now() + ms;
    while (s_touch_down && !passed(end)) {
        vTaskDelay(pdMS_TO_TICKS(20));
    }
    xQueueReset(s_q);
}

static bool wait_source(lcd349_power_source_t want, uint32_t ms)
{
    uint32_t end = now() + ms;
    while (lcd349_power_source() != want && !passed(end)) {
        vTaskDelay(pdMS_TO_TICKS(200));
    }
    return lcd349_power_source() == want;
}

/* ---- Names and formatting ---- */

static const char *btn_name(int b)
{
    return b == LCD349_BTN_BOOT ? "BOOT" : b == LCD349_BTN_PWR ? "PWR" : "none";
}

static const char *source_name(lcd349_power_source_t s)
{
    return s == LCD349_POWER_SOURCE_USB ? "usb" : s == LCD349_POWER_SOURCE_BATTERY ? "battery" : "unknown";
}

static const char *boot_name(lcd349_boot_reason_t r)
{
    static const char *names[] = {"power_on", "software", "crash", "brownout", "other"};
    return (unsigned)r < sizeof names / sizeof names[0] ? names[r] : "other";
}

static const char *seconds(char *buf, size_t size, uint32_t ms)
{
    snprintf(buf, size, "%u.%u s", (unsigned)(ms / 1000), (unsigned)(ms % 1000 / 100));
    return buf;
}

/* Appends one JSON member to d (the members of a step's record). */
static void put(char *d, size_t size, const char *fmt, ...) __attribute__((format(printf, 3, 4)));

static void put(char *d, size_t size, const char *fmt, ...)
{
    size_t n = strlen(d);
    if (n && n + 1 < size) {
        d[n++] = ',';
        d[n] = 0;
    }
    if (n + 1 >= size) {
        return;
    }
    va_list ap;
    va_start(ap, fmt);
    vsnprintf(d + n, size - n, fmt, ap);
    va_end(ap);
}

/* ---- The screen ---- */

static void show_text(const char *title, const char *prompt)
{
    lcd349_ui_lock();
    lv_obj_set_style_bg_color(s_scr, lv_color_hex(COLOR_BG), 0);
    lv_label_set_text(s_title, title);
    lv_label_set_text(s_prompt, prompt);
    lv_label_set_text(s_detail, "");
    lv_obj_t *visible[] = {s_title, s_prompt, s_detail};
    for (size_t i = 0; i < sizeof visible / sizeof visible[0]; i++) {
        lv_obj_set_hidden(visible[i], false);
    }
    lv_obj_t *hide[] = {s_target, s_swatches, s_choice[0], s_choice[1], s_choice[2]};
    for (size_t i = 0; i < sizeof hide / sizeof hide[0]; i++) {
        lv_obj_set_hidden(hide[i], true);
    }
    lcd349_ui_unlock();
}

static void detail(const char *fmt, ...) __attribute__((format(printf, 1, 2)));

static void detail(const char *fmt, ...)
{
    char buf[120];
    va_list ap;
    va_start(ap, fmt);
    vsnprintf(buf, sizeof buf, fmt, ap);
    va_end(ap);
    lcd349_ui_lock();
    lv_label_set_text(s_detail, buf);
    lcd349_ui_unlock();
}

static void target(int cx, int cy)
{
    lcd349_ui_lock();
    lv_obj_set_pos(s_target, cx - TARGET_PX / 2, cy - TARGET_PX / 2);
    lv_obj_set_hidden(s_target, false);
    lcd349_ui_unlock();
}

static void swatches(void)
{
    lcd349_ui_lock();
    lv_obj_set_hidden(s_swatches, false);
    lcd349_ui_unlock();
}

/* YES and NO (and AGAIN first), along the bottom. */
static void choices(bool again)
{
    lcd349_ui_lock();
    int y = LCD349_LCD_H - CHOICE_H - INSET;
    if (again) {
        int gap = (TEXT_W - 3 * CHOICE_W) / 2;
        lv_obj_set_pos(s_choice[CHOICE_AGAIN], TEXT_X, y);
        lv_obj_set_pos(s_choice[CHOICE_YES], TEXT_X + CHOICE_W + gap, y);
        lv_obj_set_pos(s_choice[CHOICE_NO], TEXT_X + 2 * (CHOICE_W + gap), y);
        lv_obj_set_hidden(s_choice[CHOICE_AGAIN], false);
    } else {
        lv_obj_set_pos(s_choice[CHOICE_YES], LCD349_LCD_W / 2 - CHOICE_W - 30, y);
        lv_obj_set_pos(s_choice[CHOICE_NO], LCD349_LCD_W / 2 + 30, y);
    }
    lv_obj_set_hidden(s_choice[CHOICE_YES], false);
    lv_obj_set_hidden(s_choice[CHOICE_NO], false);
    lcd349_ui_unlock();
}

/* The whole panel white, nothing else on it, for the ruler. */
static void white(bool on)
{
    lcd349_ui_lock();
    lv_obj_set_style_bg_color(s_scr, lv_color_hex(on ? 0xFFFFFF : COLOR_BG), 0);
    lv_obj_t *text[] = {s_title, s_prompt, s_detail};
    for (size_t i = 0; i < sizeof text / sizeof text[0]; i++) {
        lv_obj_set_hidden(text[i], on);
    }
    lcd349_ui_unlock();
}

static lv_obj_t *text_label(int y, const lv_font_t *font, uint32_t color)
{
    lv_obj_t *l = lv_label_create(s_scr);
    lv_obj_set_pos(l, TEXT_X, y);
    lv_obj_set_width(l, TEXT_W);
    lv_label_set_long_mode(l, LV_LABEL_LONG_MODE_WRAP);
    lv_obj_set_style_text_color(l, lv_color_hex(color), 0);
    lv_obj_set_style_text_font(l, font, 0);
    lv_label_set_text(l, "");
    return l;
}

static lv_obj_t *choice_button(const char *text, choice_t c)
{
    /* No press animation: every frame is a full-panel flush on this board.
     * (A NULL transition crashes LVGL 9.6; use a zero-length one.) */
    static const lv_style_prop_t no_props[] = {LV_STYLE_PROP_INV};
    static lv_style_transition_dsc_t no_transition;
    lv_style_transition_dsc_init(&no_transition, no_props, lv_anim_path_linear, 0, 0, NULL);
    lv_obj_t *b = lv_button_create(s_scr);
    lv_obj_set_size(b, CHOICE_W, CHOICE_H);
    lv_obj_set_style_bg_color(b, lv_color_hex(COLOR_BUTTON), 0);
    lv_obj_set_style_transition(b, &no_transition, LV_STATE_DEFAULT);
    lv_obj_set_style_transition(b, &no_transition, LV_STATE_PRESSED);
    lv_obj_add_event_cb(b, on_choice, LV_EVENT_CLICKED, (void *)(intptr_t)c);
    lv_obj_t *l = lv_label_create(b);
    lv_obj_set_style_text_font(l, &lv_font_montserrat_20, 0);
    lv_obj_set_style_text_color(l, lv_color_hex(COLOR_TEXT), 0);
    lv_label_set_text(l, text);
    lv_obj_center(l);
    lv_obj_set_hidden(b, true);
    return b;
}

static void build_screen(void)
{
    static const struct {
        const char *name;
        uint32_t fill, text;
    } COLOURS[] = {{"RED", 0xFF0000, 0x000000}, {"GREEN", 0x00FF00, 0x000000}, {"BLUE", 0x0000FF, 0xFFFFFF}, {"WHITE", 0xFFFFFF, 0x000000}};
    const int n = sizeof COLOURS / sizeof COLOURS[0], gap = 16, w = (TEXT_W - (n - 1) * gap) / n;

    lcd349_ui_lock();
    s_scr = lv_obj_create(NULL);
    lv_obj_set_style_bg_color(s_scr, lv_color_hex(COLOR_BG), 0);
    lv_obj_set_style_bg_opa(s_scr, LV_OPA_COVER, 0);
    lv_obj_set_scrollable(s_scr, false);
    s_title = text_label(6, &lv_font_montserrat_14, COLOR_MUTED);
    s_prompt = text_label(26, &lv_font_montserrat_20, COLOR_TEXT);
    s_detail = text_label(LCD349_LCD_H - 22, &lv_font_montserrat_14, COLOR_TEXT);

    s_swatches = lv_obj_create(s_scr);
    lv_obj_remove_style_all(s_swatches);
    lv_obj_set_pos(s_swatches, TEXT_X, 76);
    lv_obj_set_size(s_swatches, TEXT_W, SWATCH_H);
    lv_obj_set_scrollable(s_swatches, false);
    for (int i = 0; i < n; i++) {
        lv_obj_t *sw = lv_obj_create(s_swatches);
        lv_obj_remove_style_all(sw);
        lv_obj_set_pos(sw, i * (w + gap), 0);
        lv_obj_set_size(sw, w, SWATCH_H);
        lv_obj_set_style_bg_color(sw, lv_color_hex(COLOURS[i].fill), 0);
        lv_obj_set_style_bg_opa(sw, LV_OPA_COVER, 0);
        lv_obj_t *l = lv_label_create(sw);
        lv_obj_set_style_text_font(l, &lv_font_montserrat_14, 0);
        lv_obj_set_style_text_color(l, lv_color_hex(COLOURS[i].text), 0);
        lv_label_set_text(l, COLOURS[i].name);
        lv_obj_center(l);
    }
    lv_obj_set_hidden(s_swatches, true);

    s_target = lv_obj_create(s_scr);
    lv_obj_remove_style_all(s_target);
    lv_obj_set_size(s_target, TARGET_PX, TARGET_PX);
    lv_obj_set_style_radius(s_target, LV_RADIUS_CIRCLE, 0);
    lv_obj_set_style_bg_color(s_target, lv_color_hex(COLOR_TARGET), 0);
    lv_obj_set_style_bg_opa(s_target, LV_OPA_COVER, 0);
    lv_obj_set_clickable(s_target, false);
    lv_obj_set_hidden(s_target, true);

    s_choice[CHOICE_YES] = choice_button("YES", CHOICE_YES);
    s_choice[CHOICE_NO] = choice_button("NO", CHOICE_NO);
    s_choice[CHOICE_AGAIN] = choice_button("AGAIN", CHOICE_AGAIN);
    lcd349_ui_unlock();
}

/* ---- The steps. Each fills d with its record's members and says whether it passed. ---- */

typedef bool (*step_fn_t)(int index, char *d, size_t size);

static bool step_corners(int index, char *d, size_t size);
static bool step_picture(int index, char *d, size_t size);
static bool step_hold(int index, char *d, size_t size);
static bool step_buttons(int index, char *d, size_t size);
static bool step_tone(int index, char *d, size_t size);
static bool step_ping(int index, char *d, size_t size);
static bool step_battery(int index, char *d, size_t size);
static bool step_wifi(int index, char *d, size_t size);
static bool step_power_usb(int index, char *d, size_t size);
static bool step_unplug(int index, char *d, size_t size);
static bool step_wake_boot(int index, char *d, size_t size);
static bool step_wake_pwr(int index, char *d, size_t size);
static bool step_power_off(int index, char *d, size_t size);
static bool step_lit_width(int index, char *d, size_t size);

static const struct {
    const char *id, *title;
    step_fn_t fn;
} STEPS[] = {
    {"corners", "touch each corner", step_corners},
    {"picture", "the picture", step_picture},
    {"hold", "hold a finger still", step_hold},
    {"buttons", "BOOT, then PWR", step_buttons},
    {"tone", "the tone", step_tone},
    {"ping", "a line from the host", step_ping},
    {"battery", "the battery", step_battery},
    {"wifi", "Wi-Fi", step_wifi},
    {"power_usb", "power off on USB", step_power_usb},
    {"unplug", "unplug USB", step_unplug},
    {"wake_boot", "light sleep, BOOT wakes", step_wake_boot},
    {"wake_pwr", "light sleep, PWR wakes", step_wake_pwr},
    {"power_off", "power off on battery", step_power_off},
    {"lit_width", "the lit width", step_lit_width},
};
#define N_STEPS ((int)(sizeof STEPS / sizeof STEPS[0]))

static void show(const char *prompt)
{
    char title[64];
    snprintf(title, sizeof title, "Checkpoint, step %d of %d: %s", s_step + 1, N_STEPS, STEPS[s_step].title);
    show_text(title, prompt);
}

/* ---- Records in storage ---- */

static void record_key(int index, char key[8])
{
    snprintf(key, 8, "r%d", index);
}

/* Prints a step's record and keeps it: "PASS", "FAIL", or "HOLDING" and
 * "PENDING" while power off is under way. */
static void save_record(int index, const char *result, const char *d)
{
    char rec[RECORD_MAX], key[8];
    snprintf(rec, sizeof rec, "{\"step\":\"%s\",\"index\":%d,\"of\":%d,\"result\":\"%s\"%s%s}", STEPS[index].id, index + 1,
             N_STEPS, result, d[0] ? "," : "", d);
    record_key(index, key);
    lcd349_kv_set_str(NS, key, rec);
    printf("@R {\"guide\":%s}\n", rec);
    fflush(stdout);
}

static bool load_record(int index, char *rec, size_t size)
{
    char key[8];
    size_t len = size;
    record_key(index, key);
    return lcd349_kv_get_str(NS, key, rec, &len) == LCD349_OK;
}

static void save_step(int step)
{
    uint32_t s = (uint32_t)step;
    lcd349_kv_set_blob(NS, "step", &s, sizeof s);
}

/* ---- 1: corners ---- */

static const struct {
    const char *id, *name;
    int cx, cy;
} CORNERS[] = {
    {"top_left", "top left", INSET + TARGET_PX / 2, INSET + TARGET_PX / 2},
    {"top_right", "top right", LCD349_LCD_W - INSET - TARGET_PX / 2, INSET + TARGET_PX / 2},
    {"bottom_right", "bottom right", LCD349_LCD_W - INSET - TARGET_PX / 2, LCD349_LCD_H - INSET - TARGET_PX / 2},
    {"bottom_left", "bottom left", INSET + TARGET_PX / 2, LCD349_LCD_H - INSET - TARGET_PX / 2},
};

/* A touch counts for a corner when it lands in that corner's own region, the
 * outer sixth of the width and the outer third of the height. A mirrored or
 * swapped axis lands it in another corner. */
static bool in_corner(int i, int x, int y)
{
    bool left = CORNERS[i].cx < LCD349_LCD_W / 2, top = CORNERS[i].cy < LCD349_LCD_H / 2;
    bool in_x = left ? x < LCD349_LCD_W / 6 : x >= LCD349_LCD_W - LCD349_LCD_W / 6;
    bool in_y = top ? y < LCD349_LCD_H / 3 : y >= LCD349_LCD_H - LCD349_LCD_H / 3;
    return in_x && in_y;
}

static bool step_corners(int index, char *d, size_t size)
{
    (void)index;
    bool all = true;
    for (int i = 0; i < (int)(sizeof CORNERS / sizeof CORNERS[0]); i++) {
        show("Touch the amber dot. It moves to each corner in turn.");
        target(CORNERS[i].cx, CORNERS[i].cy);
        detail("Corner %d of 4: %s", i + 1, CORNERS[i].name);
        ev_t ev;
        if (!wait_kind(EV_TOUCH_DOWN, &ev, now() + OWNER_MS)) {
            put(d, size, "\"%s\":null", CORNERS[i].id);
            all = false;
            continue;
        }
        bool ok = in_corner(i, ev.x, ev.y);
        all = all && ok;
        put(d, size, "\"%s\":{\"x\":%d,\"y\":%d,\"ok\":%s}", CORNERS[i].id, ev.x, ev.y, ok ? "true" : "false");
        ESP_LOGI(TAG, "corner %s: touched at %d %d, %s", CORNERS[i].name, ev.x, ev.y, ok ? "in it" : "outside it");
        wait_touch_up(5000);
    }
    return all;
}

/* ---- 2: the picture (right way up, colours) ---- */

static bool step_picture(int index, char *d, size_t size)
{
    (void)index;
    show("Is this text upright, each colour as named?");
    swatches();
    choices(false);
    ev_t ev;
    bool got = wait_kind(EV_CHOICE, &ev, now() + OWNER_MS);
    put(d, size, "\"answer\":\"%s\"", !got ? "none" : ev.which == CHOICE_YES ? "yes" : "no");
    return got && ev.which == CHOICE_YES;
}

/* ---- 3: a finger held still (P1-N2) ---- */

static bool step_hold(int index, char *d, size_t size)
{
    (void)index;
    show("Hold a finger still on the dot for 10 s.");
    target(LCD349_LCD_W / 2, LCD349_LCD_H - INSET - TARGET_PX / 2 - 20);
    char releases[96] = "", gaps[96] = "", buf[16];
    uint32_t last_up = 0;
    for (int attempt = 1; attempt <= 3; attempt++) {
        ev_t ev;
        if (!wait_kind(EV_TOUCH_DOWN, &ev, now() + OWNER_MS)) {
            break;
        }
        if (last_up) {
            put(gaps, sizeof gaps, "%u", (unsigned)(ev.t_ms - last_up));
        }
        uint32_t t0 = ev.t_ms, end = t0 + HOLD_MS;
        s_samples = 0;
        bool released = false;
        while (!passed(end)) {
            uint32_t tick = now() + 250;
            if (next_until(&ev, (int32_t)(tick - end) > 0 ? end : tick) && ev.kind == EV_TOUCH_UP) {
                released = true;
                last_up = ev.t_ms;
                put(releases, sizeof releases, "%u", (unsigned)(ev.t_ms - t0));
                ESP_LOGI(TAG, "hold: released after %u ms", (unsigned)(ev.t_ms - t0));
                break;
            }
            detail("Holding: %s of 10 s", seconds(buf, sizeof buf, now() - t0));
        }
        if (!released) {
            uint32_t samples = s_samples;
            put(d, size, "\"held_ms\":%u,\"samples\":%u,\"attempts\":%d,\"releases_ms\":[%s],\"repress_gaps_ms\":[%s]",
                (unsigned)HOLD_MS, (unsigned)samples, attempt, releases, gaps);
            ESP_LOGI(TAG, "hold: 10 s with no release, %u samples", (unsigned)samples);
            detail("Held 10 s with no release. Lift your finger.");
            wait_touch_up(10000);
            return true;
        }
        detail("Released after %s. Hold still again (attempt %d of 3).", seconds(buf, sizeof buf, last_up - t0), attempt + 1);
    }
    put(d, size, "\"held_ms\":0,\"releases_ms\":[%s],\"repress_gaps_ms\":[%s]", releases, gaps);
    return false;
}

/* ---- 4: BOOT, then PWR ---- */

static bool step_buttons(int index, char *d, size_t size)
{
    (void)index;
    show("Press BOOT, then PWR.");
    wait_buttons_up(5000);
    static const lcd349_btn_t order[] = {LCD349_BTN_BOOT, LCD349_BTN_PWR};
    bool all = true;
    for (int i = 0; i < 2; i++) {
        detail(i == 0 ? "Waiting for BOOT." : "BOOT pressed. Now PWR.");
        uint32_t down_t = 0, held = 0, end = now() + OWNER_MS;
        int others = 0;
        bool got = false;
        ev_t ev;
        while (!got && wait_kind(EV_BUTTON, &ev, end)) {
            if (ev.which != (int)order[i]) {
                others += ev.down;
            } else if (ev.down) {
                down_t = ev.t_ms;
            } else if (down_t) {
                held = ev.t_ms - down_t;
                got = true;
            }
        }
        put(d, size, "\"%s\":{\"pressed\":%s,\"held_ms\":%u,\"other_presses\":%d}", i == 0 ? "boot" : "pwr",
            got ? "true" : "false", (unsigned)held, others);
        ESP_LOGI(TAG, "buttons: %s %s", btn_name(order[i]), got ? "pressed" : "not pressed");
        all = all && got;
    }
    return all;
}

/* ---- 5: the tone ---- */

static bool step_tone(int index, char *d, size_t size)
{
    (void)index;
    static const lcd349_tone_t rising[] = {{660, 180}, {0, 60}, {880, 180}, {0, 60}, {1320, 240}};
    show("Listen: three rising tones. Did you hear them?");
    choices(true);
    lcd349_err_t err = LCD349_OK;
    int plays = 0;
    bool got = false;
    ev_t ev;
    for (;;) {
        err = lcd349_tone_seq(rising, sizeof rising / sizeof rising[0], 70);
        plays++;
        got = wait_kind(EV_CHOICE, &ev, now() + OWNER_MS);
        if (!got || ev.which != CHOICE_AGAIN || plays >= 10) {
            break;
        }
    }
    const char *answer = !got ? "none" : ev.which == CHOICE_YES ? "yes" : ev.which == CHOICE_NO ? "no" : "again";
    put(d, size, "\"play\":\"%s\",\"plays\":%d,\"answer\":\"%s\"", lcd349_err_str(err), plays, answer);
    return err == LCD349_OK && got && ev.which == CHOICE_YES;
}

/* ---- 6: a line from the host reaches lcd349_console_read_line ---- */

static bool step_ping(int index, char *d, size_t size)
{
    (void)index;
    show("The host sends a line over USB; the console reads it.");
    for (int tries = 1; tries <= 3; tries++) {
        uint32_t t0 = now();
        printf("@Q ping\n");
        fflush(stdout);
        ev_t ev;
        if (wait_kind(EV_PING, &ev, t0 + 3000)) {
            put(d, size, "\"ms\":%u,\"tries\":%d", (unsigned)(ev.t_ms - t0), tries);
            detail("The host's ping arrived after %u ms.", (unsigned)(ev.t_ms - t0));
            vTaskDelay(pdMS_TO_TICKS(1500));
            return true;
        }
    }
    put(d, size, "\"tries\":3");
    return false;
}

/* ---- 7: the battery ---- */

static bool step_battery(int index, char *d, size_t size)
{
    (void)index;
    show("The battery reading.");
    uint16_t mv = 0;
    uint8_t pct = 0;
    lcd349_err_t err = lcd349_battery_read(&mv, &pct);
    bool ok = err == LCD349_OK && mv >= 3000 && mv <= 4400 && pct <= 100; /* task 5.2's plausible range */
    put(d, size, "\"err\":\"%s\",\"mv\":%u,\"pct\":%u,\"source\":\"%s\"", lcd349_err_str(err), mv, pct,
        source_name(lcd349_power_source()));
    detail("%u mV, %u %%: %s", mv, pct, ok ? "plausible" : "not plausible");
    vTaskDelay(pdMS_TO_TICKS(2000));
    return ok;
}

/* ---- 8: Wi-Fi joins the host's test network ---- */

static bool step_wifi(int index, char *d, size_t size)
{
    (void)index;
    static const char *states[] = {"idle", "scanning", "connecting", "connected", "disconnected"};
    show("Joining the test network from the host's Wi-Fi file.");
    lcd349_wifi_net_t net;
    bool got = false;
    for (int tries = 0; tries < 3 && !got; tries++) {
        printf("@Q wifi\n");
        fflush(stdout);
        got = xQueueReceive(s_netq, &net, pdMS_TO_TICKS(5000)) == pdTRUE;
    }
    if (!got) {
        put(d, size, "\"error\":\"the host sent no network\"");
        return false;
    }
    char ssid[sizeof net.ssid];
    memcpy(ssid, net.ssid, sizeof ssid);
    lcd349_err_t err = lcd349_wifi_set_networks(&net, 1);
    memset(&net, 0, sizeof net);
    lcd349_wifi_start(); /* the app started it already; this is in case it stopped */
    uint32_t t0 = now();
    lcd349_wifi_status_t st = {0};
    bool ok = false;
    while (err == LCD349_OK && now() - t0 < 45000) {
        lcd349_wifi_status(&st);
        if (st.state == LCD349_WIFI_CONNECTED && strcmp(st.ssid, ssid) == 0) {
            ok = true;
            break;
        }
        detail("Wi-Fi: %s", states[st.state]);
        vTaskDelay(pdMS_TO_TICKS(250));
    }
    memset(ssid, 0, sizeof ssid);
    put(d, size, "\"set\":\"%s\",\"ms\":%u,\"state\":\"%s\",\"rssi\":%d,\"has_ip\":%s", lcd349_err_str(err),
        (unsigned)(now() - t0), states[st.state], ok ? st.rssi : 0, ok && st.ip4 ? "true" : "false");
    detail(ok ? "Joined the test network, RSSI %d dBm." : "Didn't join the test network (RSSI %d).", st.rssi);
    vTaskDelay(pdMS_TO_TICKS(1500));
    return ok;
}

/* ---- 9: power off on USB returns ON_USB ---- */

static bool step_power_usb(int index, char *d, size_t size)
{
    (void)index;
    show("Power off with USB plugged in: the board should say it can't.");
    if (!wait_source(LCD349_POWER_SOURCE_USB, 0)) {
        detail("Plug the USB cable back in.");
        wait_source(LCD349_POWER_SOURCE_USB, OWNER_MS);
    }
    lcd349_power_source_t src = lcd349_power_source();
    if (src != LCD349_POWER_SOURCE_USB) {
        /* Off USB this would power off: never call it here. */
        put(d, size, "\"source\":\"%s\",\"error\":\"not on USB\"", source_name(src));
        return false;
    }
    uint32_t t0 = now();
    lcd349_err_t err = lcd349_power_off();
    put(d, size, "\"err\":\"%s\",\"ms\":%u", lcd349_err_str(err), (unsigned)(now() - t0));
    detail("lcd349_power_off: %s", lcd349_err_str(err));
    vTaskDelay(pdMS_TO_TICKS(2000));
    return err == LCD349_ERR_ON_USB;
}

/* ---- 10: unplug USB ---- */

static bool step_unplug(int index, char *d, size_t size)
{
    (void)index;
    show("Unplug the USB cable: the board runs on its battery.");
    detail("If it hasn't noticed after 10 s, press BOOT.");
    uint32_t t0 = now(), end = t0 + OWNER_MS;
    bool boot = false;
    while (!passed(end) && lcd349_power_source() != LCD349_POWER_SOURCE_BATTERY) {
        ev_t ev;
        if (next_until(&ev, now() + 200) && ev.kind == EV_BUTTON && ev.which == LCD349_BTN_BOOT && !ev.down) {
            boot = true;
            break;
        }
    }
    lcd349_power_source_t src = lcd349_power_source();
    s_unplugged = src == LCD349_POWER_SOURCE_BATTERY || boot;
    put(d, size, "\"source\":\"%s\",\"ms\":%u,\"boot_pressed\":%s", source_name(src), (unsigned)(now() - t0),
        boot ? "true" : "false");
    return src == LCD349_POWER_SOURCE_BATTERY;
}

/* Light sleep and power off run off USB only: light sleep drops the USB link. */
static bool off_usb(char *d, size_t size)
{
    if (lcd349_power_source() == LCD349_POWER_SOURCE_BATTERY || s_unplugged) {
        return true;
    }
    detail("Unplug USB first.");
    if (wait_source(LCD349_POWER_SOURCE_BATTERY, OWNER_MS)) {
        return true;
    }
    put(d, size, "\"error\":\"still on USB\"");
    return false;
}

/* ---- 11, 12: light sleep, woken by each button ---- */

static bool sleep_and_wake(lcd349_btn_t want, char *d, size_t size)
{
    char prompt[96], buf[16];
    snprintf(prompt, sizeof prompt, "Light sleep: when the screen goes dark, press %s to wake it.", btn_name(want));
    show(prompt);
    if (!off_usb(d, size)) {
        return false;
    }
    wait_buttons_up(10000);
    for (int i = 3; i > 0; i--) {
        detail("Going dark in %d s.", i);
        vTaskDelay(pdMS_TO_TICKS(1000));
    }
    lcd349_btn_t by = LCD349_BTN_COUNT;
    uint32_t t0 = now();
    lcd349_err_t err = lcd349_sleep_until_button(30000, &by);
    uint32_t slept = now() - t0;
    bool ok = err == LCD349_OK && by == want;
    put(d, size, "\"err\":\"%s\",\"woke_by\":\"%s\",\"slept_ms\":%u", lcd349_err_str(err),
        btn_name(err == LCD349_OK ? (int)by : -1), (unsigned)slept);
    detail("Woke after %s: %s.", seconds(buf, sizeof buf, slept),
           err == LCD349_OK ? btn_name(by) : "no button within 30 s");
    wait_buttons_up(10000); /* the waking press only wakes */
    vTaskDelay(pdMS_TO_TICKS(1500));
    return ok;
}

static bool step_wake_boot(int index, char *d, size_t size)
{
    (void)index;
    return sleep_and_wake(LCD349_BTN_BOOT, d, size);
}

static bool step_wake_pwr(int index, char *d, size_t size)
{
    (void)index;
    return sleep_and_wake(LCD349_BTN_PWR, d, size);
}

/* ---- 13: a 5 s PWR hold powers off on battery ----
 * Off USB nothing reaches the host, so the step keeps its progress in
 * storage: "off" holds the record's members so far, and the record says
 * HOLDING from the press and PENDING just before lcd349_power_off(). The
 * call waits for PWR to be let go: if the button holds the power on while
 * it's down, the kit would see the board still running and say ON_USB.
 * The mark in RTC memory survives every reset but a loss of power, so after
 * the restart the step can tell a power off from a reset: the boot reason
 * can't, because boardtest's reopening of the port resets the chip too. */

#define OFF_MARK 0x0FF5AFE5u
static RTC_NOINIT_ATTR uint32_t s_off_mark;

static void save_off(int index, const char *result, const char *at)
{
    lcd349_kv_set_str(NS, "off", at);
    save_record(index, result, at);
}

static bool step_power_off(int index, char *d, size_t size)
{
    char rec[RECORD_MAX], buf[16];
    if (load_record(index, rec, sizeof rec)) {
        bool pending = strstr(rec, "\"result\":\"PENDING\"") != NULL;
        if (pending || strstr(rec, "\"result\":\"HOLDING\"")) {
            /* The board went off, or reset, during this step and has started again. */
            bool lost_power = s_off_mark != OFF_MARK;
            s_off_mark = 0;
            size_t len = size;
            if (lcd349_kv_get_str(NS, "off", d, &len) != LCD349_OK) {
                d[0] = 0;
            }
            lcd349_kv_erase(NS, "off");
            put(d, size, "\"called\":%s,\"power_lost\":%s,\"boot_reason\":\"%s\"", pending ? "true" : "false",
                lost_power ? "true" : "false", boot_name(lcd349_boot_reason()));
            show(!lost_power ? "The board reset without powering off."
                 : pending   ? "The board powered off, and has started again."
                             : "The board went off before the 5 s hold ended.");
            vTaskDelay(pdMS_TO_TICKS(3000));
            return pending && lost_power;
        }
    }
    show("Hold PWR for 5 s, then let go: the board powers off. Then plug USB back in.");
    if (!off_usb(d, size)) {
        return false;
    }
    wait_buttons_up(10000);
    char key[8];
    record_key(index, key);
    uint32_t end = now() + OWNER_MS;
    ev_t ev;
    while (wait_kind(EV_BUTTON, &ev, end)) {
        if (ev.which != LCD349_BTN_PWR || !ev.down) {
            continue;
        }
        uint32_t t0 = ev.t_ms, hold_end = t0 + OFF_HOLD_MS;
        uint16_t mv = 0;
        uint8_t pct = 0;
        lcd349_battery_read(&mv, &pct);
        char at[160];
        snprintf(at, sizeof at, "\"mv\":%u,\"pct\":%u,\"source\":\"%s\"", mv, pct, source_name(lcd349_power_source()));
        s_off_mark = OFF_MARK;
        save_off(index, "HOLDING", at);
        bool released = false;
        while (!passed(hold_end)) {
            uint32_t tick = now() + 200;
            if (next_until(&ev, (int32_t)(tick - hold_end) > 0 ? hold_end : tick) && ev.kind == EV_BUTTON &&
                ev.which == LCD349_BTN_PWR && !ev.down) {
                released = true;
                break;
            }
            detail("Holding PWR: %s of 5 s", seconds(buf, sizeof buf, now() - t0));
        }
        if (released) {
            s_off_mark = 0;
            lcd349_kv_erase(NS, "off");
            lcd349_kv_erase(NS, key);
            detail("Released after %s: hold PWR for the whole 5 s.", seconds(buf, sizeof buf, ev.t_ms - t0));
            continue;
        }
        detail("5 s. Let go of PWR: the board powers off.");
        bool let_go = false;
        for (uint32_t wait_end = now() + 10000; !let_go && wait_kind(EV_BUTTON, &ev, wait_end);) {
            let_go = ev.which == LCD349_BTN_PWR && !ev.down;
        }
        put(at, sizeof at, "\"held_ms\":%u,\"let_go\":%s", (unsigned)(let_go ? ev.t_ms - t0 : now() - t0),
            let_go ? "true" : "false");
        save_off(index, "PENDING", at);
        lcd349_err_t err = lcd349_power_off();
        /* Still running: it didn't power off. */
        s_off_mark = 0;
        lcd349_kv_erase(NS, "off");
        snprintf(d, size, "%s", at);
        put(d, size, "\"err\":\"%s\"", lcd349_err_str(err));
        return false;
    }
    put(d, size, "\"error\":\"PWR wasn't held for 5 s\"");
    return false;
}

/* ---- 14: a white screen for the ruler (X-03) ---- */

static bool step_lit_width(int index, char *d, size_t size)
{
    (void)index;
    show("Next, a white screen: measure the lit width with a ruler, then press BOOT.");
    wait_buttons_up(10000);
    for (int i = 5; i > 0; i--) {
        detail("White in %d s. Your reading goes in the checkpoint's notes.", i);
        vTaskDelay(pdMS_TO_TICKS(1000));
    }
    uint8_t backlight = lcd349_backlight_get();
    lcd349_backlight_set(100); /* the brightest edge for the ruler */
    white(true);
    uint32_t t0 = now();
    bool got = wait_press(LCD349_BTN_BOOT, t0 + 1800000);
    white(false);
    lcd349_backlight_set(backlight);
    put(d, size, "\"white_ms\":%u,\"ended_by\":\"%s\"", (unsigned)(now() - t0), got ? "BOOT" : "timeout");
    return got;
}

/* ---- Running ---- */

static int summary(void)
{
    int pass = 0;
    char rec[RECORD_MAX];
    for (int i = 0; i < N_STEPS; i++) {
        if (!load_record(i, rec, sizeof rec)) {
            snprintf(rec, sizeof rec, "{\"step\":\"%s\",\"index\":%d,\"of\":%d,\"result\":\"MISSING\"}", STEPS[i].id, i + 1,
                     N_STEPS);
        } else if (strstr(rec, "\"result\":\"PASS\"")) {
            pass++;
        }
        printf("@R {\"guide\":{\"summary\":%s}}\n", rec);
    }
    printf("@R {\"guide\":{\"done\":true,\"passed\":%d,\"failed\":%d,\"of\":%d}}\n", pass, N_STEPS - pass, N_STEPS);
    fflush(stdout);
    ESP_LOGI(TAG, "done: %d of %d steps passed", pass, N_STEPS);
    return pass;
}

static void guide_task(void *arg)
{
    (void)arg;
    lcd349_ui_lock();
    s_home = lv_screen_active();
    lv_screen_load(s_scr);
    lcd349_ui_unlock();
    for (int i = s_step; i < N_STEPS; i++) {
        s_step = i;
        save_step(i);
        xQueueReset(s_q);
        ESP_LOGI(TAG, "step %d of %d: %s", i + 1, N_STEPS, STEPS[i].title);
        char d[DETAIL_MAX] = "";
        bool ok = STEPS[i].fn(i, d, sizeof d);
        save_record(i, ok ? "PASS" : "FAIL", d);
    }
    save_step(N_STEPS);
    int pass = summary();
    char text[64];
    snprintf(text, sizeof text, "Checkpoint done: %d of %d steps passed.", pass, N_STEPS);
    show_text("Checkpoint", text);
    detail("The results are in the host's log. Press BOOT to go back.");
    wait_press(LCD349_BTN_BOOT, now() + 120000);
    lcd349_ui_lock();
    lv_screen_load(s_home);
    lcd349_ui_unlock();
    s_running = false;
    vTaskDelete(NULL);
}

/* Starts over (restart), at step from (if not -1, keeping the other steps'
 * results), or where the guide got to. */
static void start(bool restart, int from)
{
    uint32_t step = 0;
    size_t len = sizeof step;
    bool saved = lcd349_kv_get_blob(NS, "step", &step, &len) == LCD349_OK && len == sizeof step;
    bool fresh = restart || (from < 0 && (!saved || step >= (uint32_t)N_STEPS));
    if (fresh) {
        lcd349_kv_erase(NS, NULL);
        step = 0;
    }
    if (from >= 0) {
        step = (uint32_t)from;
    }
    s_step = (int)step;
    s_unplugged = false;
    s_running = true;
    printf("@R {\"guide\":{\"start\":\"%s\",\"at\":\"%s\",\"index\":%d,\"of\":%d}}\n",
           fresh ? "new" : from >= 0 ? "from" : "resumed", STEPS[step].id, (int)step + 1, N_STEPS);
    fflush(stdout);
    if (xTaskCreate(guide_task, "guide", 8192, NULL, 1, NULL) != pdPASS) {
        s_running = false;
        ESP_LOGE(TAG, "no memory for the guide's task");
    }
}

void guide_init(void)
{
    s_q = xQueueCreate(32, sizeof(ev_t));
    s_netq = xQueueCreate(1, sizeof(lcd349_wifi_net_t));
    build_screen();
    lcd349_touch_subscribe(on_touch, NULL);
    lcd349_buttons_subscribe(on_button, NULL);
}

void guide_command(const char *args)
{
    if (s_running) {
        printf("@R {\"guide\":{\"running\":\"%s\",\"index\":%d,\"of\":%d}}\n", STEPS[s_step].id, s_step + 1, N_STEPS);
        fflush(stdout);
        return;
    }
    if (args && strncmp(args, "from ", 5) == 0) {
        for (int i = 0; i < N_STEPS; i++) {
            if (strcmp(STEPS[i].id, args + 5) == 0) {
                start(false, i);
                return;
            }
        }
        printf("@R {\"guide\":{\"error\":\"no such step\"}}\n");
        fflush(stdout);
        return;
    }
    start(args && strcmp(args, "restart") == 0, -1);
}

void guide_ping(void)
{
    ev_t ev = {.kind = EV_PING, .t_ms = now()};
    post(&ev);
}

/* ---- "@P wifi": boardtest's {"ssid":…,"password":…} ---- */

static int hex(char c)
{
    return c >= '0' && c <= '9' ? c - '0' : c >= 'a' && c <= 'f' ? c - 'a' + 10 : c >= 'A' && c <= 'F' ? c - 'A' + 10 : -1;
}

/* Copies the string member key of a flat JSON object into out, as UTF-8;
 * false if it's absent, malformed or doesn't fit. */
static bool json_str(const char *json, const char *key, char *out, size_t size)
{
    char pat[24];
    snprintf(pat, sizeof pat, "\"%s\":", key);
    const char *p = strstr(json, pat);
    if (!p) {
        return false;
    }
    p += strlen(pat);
    while (*p == ' ') {
        p++;
    }
    if (*p++ != '"') {
        return false;
    }
    size_t n = 0;
    while (*p && *p != '"') {
        unsigned c = (unsigned char)*p++;
        if (c == '\\') {
            char e = *p++;
            const char *from = "\"\\/bfnrt", *to = "\"\\/\b\f\n\r\t";
            const char *at = e ? strchr(from, e) : NULL;
            if (at) {
                c = (unsigned char)to[at - from];
            } else if (e == 'u') {
                c = 0;
                for (int i = 0; i < 4; i++) {
                    int h = hex(*p++);
                    if (h < 0) {
                        return false;
                    }
                    c = c << 4 | (unsigned)h;
                }
                if (c >= 0x800) {
                    if (n + 3 >= size) {
                        return false;
                    }
                    out[n++] = (char)(0xE0 | c >> 12);
                    out[n++] = (char)(0x80 | (c >> 6 & 0x3F));
                    out[n++] = (char)(0x80 | (c & 0x3F));
                    continue;
                }
                if (c >= 0x80) {
                    if (n + 2 >= size) {
                        return false;
                    }
                    out[n++] = (char)(0xC0 | c >> 6);
                    out[n++] = (char)(0x80 | (c & 0x3F));
                    continue;
                }
            } else {
                return false;
            }
        }
        if (n + 1 >= size) {
            return false;
        }
        out[n++] = (char)c;
    }
    if (*p != '"') {
        return false;
    }
    out[n] = 0;
    return true;
}

void guide_wifi(const char *json)
{
    lcd349_wifi_net_t net = {0};
    if (json_str(json, "ssid", net.ssid, sizeof net.ssid) && json_str(json, "password", net.password, sizeof net.password)) {
        if (s_running) {
            xQueueOverwrite(s_netq, &net);
        }
    } else {
        ESP_LOGW(TAG, "a Wi-Fi line that isn't {\"ssid\":...,\"password\":...}");
    }
    memset(&net, 0, sizeof net);
}

bool guide_running(void)
{
    return s_running;
}
