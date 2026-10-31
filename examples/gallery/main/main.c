/* SPDX-License-Identifier: Apache-2.0
 *
 * gallery: every recipe from the lcd349 skill, at real size. PWR (short
 * press) moves to the next page; BOOT and touch work each page:
 *   list     BOOT steps, a hold or a tap opens a row as a detail card
 *   tiles    BOOT steps, a hold or a tap opens a tile (a toast says which)
 *   confirm  "Delete all" asks first: BOOT steps, a hold or a tap chooses
 *   hold     hold BOOT 1 s to approve, with the visibility guard
 *   setup    brightness and sound, saved to storage
 * The status bar is always on top. Every action is logged for scenarios.
 */
#include <stdio.h>

#include "esp_log.h"
#include "freertos/FreeRTOS.h"
#include "freertos/queue.h"
#include "lcd349.h"
#include "recipes.h"

static const char *TAG = "gallery";

typedef enum { PAGE_LIST, PAGE_CARD, PAGE_TILES, PAGE_CONFIRM, PAGE_HOLD, PAGE_SETUP, PAGE_COUNT } page_t;
static const char *const PAGE_NAMES[PAGE_COUNT] = {"list", "card", "tiles", "confirm", "hold", "setup"};
static const char *const PAGE_TITLES[PAGE_COUNT] = {"Inbox", "Message", "Menu", "Danger zone", "Approve", "Setup"};

static const char *const MESSAGES[] = {"Build finished on ci-runner-3", "Agent needs approval: run tests",
                                       "Battery low on pager-02", "Deploy to staging complete",
                                       "New device paired: desk", "Nightly report ready"};
#define N_MESSAGES (int)(sizeof MESSAGES / sizeof MESSAGES[0])

static const tile_t TILES[] = {{LV_SYMBOL_HOME, "Home"},   {LV_SYMBOL_BELL, "Alerts"},  {LV_SYMBOL_AUDIO, "Sound"},
                               {LV_SYMBOL_WIFI, "Wi-Fi"}, {LV_SYMBOL_SETTINGS, "Setup"}, {LV_SYMBOL_POWER, "Power"}};

static QueueHandle_t s_buttons;
static lv_obj_t *s_status, *s_body, *s_view, *s_dialog;
static page_t s_page;
static int s_opened; /* the message the card shows */

static void show(page_t page);

/* ---- Actions (LVGL lock held) ---- */

static void open_message(int i, void *ctx)
{
    (void)ctx;
    s_opened = i;
    ESP_LOGI(TAG, "opened message %d", i);
    show(PAGE_CARD);
}

static void card_back(lv_event_t *e)
{
    (void)e;
    show(PAGE_LIST);
}

static void open_tile(int i, void *ctx)
{
    (void)ctx;
    char text[40];
    snprintf(text, sizeof text, "Opened %s", TILES[i].label);
    ESP_LOGI(TAG, "opened tile %s", TILES[i].label);
    toast_show(text, 2000);
}

static void confirmed(bool yes, void *ctx)
{
    (void)ctx;
    s_dialog = NULL;
    ESP_LOGI(TAG, "%s", yes ? "confirmed" : "cancelled");
    toast_show(yes ? "Deleted" : "Kept", 2000);
}

static void ask(lv_event_t *e)
{
    (void)e;
    if (!s_dialog) {
        s_dialog = confirm_show("Delete every message?", "Delete", confirmed, NULL);
        ESP_LOGI(TAG, "asking");
    }
}

static void setup_done(lv_event_t *e)
{
    (void)e;
    ESP_LOGI(TAG, "setup saved");
    toast_show("Saved", 2000);
}

/* ---- Pages ---- */

static void show(page_t page)
{
    if (s_dialog) {
        lv_obj_delete(s_dialog);
        s_dialog = NULL;
    }
    lv_obj_clean(lv_layer_top()); /* toasts */
    lv_obj_clean(s_body);
    s_page = page;
    lv_obj_delete(s_status);
    s_status = status_bar_create(lv_screen_active(), PAGE_TITLES[page]);
    switch (page) {
    case PAGE_LIST:
        s_view = list_create(s_body, MESSAGES, N_MESSAGES, open_message, NULL);
        break;
    case PAGE_CARD:
        s_view = card_create(s_body, MESSAGES[s_opened],
                             "Tap Back or press BOOT to return. Long text ends in an ellipsis rather than running off "
                             "the card, so nothing is ever clipped mid-word on this small screen.",
                             card_back, NULL);
        break;
    case PAGE_TILES:
        s_view = tiles_create(s_body, TILES, (int)(sizeof TILES / sizeof TILES[0]), open_tile, NULL);
        break;
    case PAGE_CONFIRM: {
        s_view = ui_panel(s_body, LCD349_LCD_W, UI_BODY_H, UI_BG);
        lv_obj_t *hint = ui_label(s_view, "Clears the inbox for good.", UI_FONT, UI_MUTED);
        lv_obj_align(hint, LV_ALIGN_LEFT_MID, UI_PAD, 0);
        lv_obj_t *b = ui_button(s_view, "Delete all", 200, UI_TARGET + 16, ask, NULL);
        lv_obj_set_style_bg_color(b, lv_color_hex(UI_DANGER), 0);
        lv_obj_align(b, LV_ALIGN_RIGHT_MID, -UI_PAD, 0);
        break;
    }
    case PAGE_HOLD:
        s_view = hold_create(s_body, "Hold BOOT to approve the deploy", LCD349_BTN_BOOT);
        break;
    case PAGE_SETUP:
        s_view = setup_create(s_body, setup_done, NULL);
        break;
    default:
        break;
    }
    ESP_LOGI(TAG, "page %s", PAGE_NAMES[page]);
}

/* Buttons for the page showing. PWR's short press is the gallery's own. */
static void on_gesture(const lcd349_gesture_t *g)
{
    bool boot = g->btn == LCD349_BTN_BOOT;
    bool step = boot && g->kind == LCD349_GESTURE_SHORT;
    bool hold = boot && g->kind == LCD349_GESTURE_HOLD && g->level == 1;
    if (g->btn == LCD349_BTN_PWR && g->kind == LCD349_GESTURE_SHORT) {
        show(s_page == PAGE_LIST ? PAGE_TILES : s_page == PAGE_CARD ? PAGE_TILES : (page_t)((s_page + 1) % PAGE_COUNT));
        return;
    }
    switch (s_page) {
    case PAGE_LIST:
        if (step) {
            list_step(s_view, 1);
        } else if (hold) {
            list_open_selected(s_view);
        }
        break;
    case PAGE_CARD:
        if (step) {
            show(PAGE_LIST);
        }
        break;
    case PAGE_TILES:
        if (step) {
            tiles_step(s_view, 1);
        } else if (hold) {
            tiles_open_selected(s_view);
        }
        break;
    case PAGE_CONFIRM:
        if (s_dialog && step) {
            confirm_step(s_dialog);
        } else if (s_dialog && hold) {
            confirm_choose(s_dialog);
        } else if (hold) {
            ask(NULL);
        }
        break;
    case PAGE_HOLD:
        if (g->kind == LCD349_GESTURE_HOLD && boot && g->level == 1) {
            bool ok = hold_accepts(s_view, g);
            ESP_LOGI(TAG, "%s", ok ? "approved" : "hold ignored: it began before the prompt had shown for 500 ms");
            toast_show(ok ? "Approved" : "Release, then hold again", 2000);
        }
        break;
    default:
        break;
    }
}

static void tick_status(lv_timer_t *t)
{
    (void)t;
    status_bar_refresh(s_status);
}

/* recipe:start app-loop */
static void on_button(const lcd349_btn_event_t *ev, void *ctx)
{
    (void)ctx;
    xQueueSend(s_buttons, ev, 0);
}

void app_main(void)
{
    s_buttons = xQueueCreate(16, sizeof(lcd349_btn_event_t));
    lcd349_err_t err = lcd349_init(NULL);
    ESP_LOGI(TAG, "lcd349_init: %s, status 0x%03x", lcd349_err_str(err), (unsigned)lcd349_board_status());
    if (!lcd349_display()) {
        return;
    }
    uint8_t brightness;
    size_t len = 1;
    if (lcd349_kv_get_blob("setup", "brightness", &brightness, &len) == LCD349_OK) {
        lcd349_backlight_set(brightness);
    }

    lcd349_ui_lock();
    lv_obj_t *scr = lv_screen_active();
    lv_obj_set_style_bg_color(scr, lv_color_hex(UI_BG), 0);
    lv_obj_set_style_bg_opa(scr, LV_OPA_COVER, 0);
    lv_obj_set_scrollable(scr, false);
    s_status = status_bar_create(scr, "");
    s_body = ui_panel(scr, LCD349_LCD_W, UI_BODY_H, UI_BG);
    lv_obj_set_pos(s_body, 0, UI_STATUS_H);
    show(PAGE_LIST);
    lv_timer_create(tick_status, 1000, NULL);
    lcd349_ui_unlock();
    lcd349_buttons_subscribe(on_button, NULL);

    lcd349_classifier_t cls;
    lcd349_classifier_cfg_t cfg = {.hold_ms = {1000}, .chord_window_ms = 150};
    lcd349_classifier_init(&cls, &cfg);
    ESP_LOGI(TAG, "ready");

    for (;;) {
        lcd349_gesture_t g[LCD349_CLASSIFIER_MAX_OUT];
        size_t n;
        lcd349_btn_event_t ev;
        if (xQueueReceive(s_buttons, &ev, pdMS_TO_TICKS(20)) == pdTRUE) {
            n = lcd349_classifier_feed(&cls, &ev, g, LCD349_CLASSIFIER_MAX_OUT);
        } else {
            n = lcd349_classifier_tick(&cls, lcd349_time_ms(), g, LCD349_CLASSIFIER_MAX_OUT);
        }
        lcd349_ui_lock();
        for (size_t i = 0; i < n; i++) {
            on_gesture(&g[i]);
        }
        if (s_page == PAGE_HOLD) {
            hold_progress(s_view, lcd349_classifier_hold_permille(&cls, LCD349_BTN_BOOT, lcd349_time_ms()));
        }
        lcd349_ui_unlock();
    }
}
/* recipe:end */
