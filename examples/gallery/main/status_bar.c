/* SPDX-License-Identifier: Apache-2.0 */
#include <stdio.h>
#include <string.h>
#include <time.h>

#include "recipes.h"

/* recipe:start status-bar */
/* The top band: the screen's title on the left; Wi-Fi, battery and the time
 * on the right. Refresh it from a 1 s lv_timer; it only redraws what changed,
 * because every change costs a full-frame flush. */
typedef struct {
    lv_obj_t *title, *right;
    char shown[48];
} status_bar_t;

static void status_bar_free(lv_event_t *e)
{
    lv_free(lv_event_get_user_data(e));
}

lv_obj_t *status_bar_create(lv_obj_t *parent, const char *title)
{
    lv_obj_t *bar = ui_panel(parent, LCD349_LCD_W, UI_STATUS_H, UI_SURFACE);
    status_bar_t *s = lv_malloc_zeroed(sizeof *s);
    s->title = ui_label(bar, title, UI_FONT, UI_TEXT);
    lv_obj_align(s->title, LV_ALIGN_LEFT_MID, UI_PAD, 0);
    s->right = ui_label(bar, "", UI_FONT, UI_TEXT);
    lv_obj_align(s->right, LV_ALIGN_RIGHT_MID, -UI_PAD, 0);
    lv_obj_set_user_data(bar, s);
    lv_obj_add_event_cb(bar, status_bar_free, LV_EVENT_DELETE, s);
    status_bar_refresh(bar);
    return bar;
}

void status_bar_refresh(lv_obj_t *bar)
{
    status_bar_t *s = lv_obj_get_user_data(bar);
    uint16_t mv = 0;
    uint8_t pct = 0;
    lcd349_battery_read(&mv, &pct);
    const char *battery = pct > 80 ? LV_SYMBOL_BATTERY_FULL : pct > 55 ? LV_SYMBOL_BATTERY_3 : pct > 30 ? LV_SYMBOL_BATTERY_2
                        : pct > 10 ? LV_SYMBOL_BATTERY_1 : LV_SYMBOL_BATTERY_EMPTY;
    lcd349_wifi_status_t wifi = {0};
    lcd349_wifi_status(&wifi);
    int64_t now = 0;
    lcd349_rtc_get(&now);
    time_t t = (time_t)now;
    struct tm tm;
    gmtime_r(&t, &tm);
    char text[48];
    snprintf(text, sizeof text, "%s  %s %u%%  %02d:%02d", wifi.state == LCD349_WIFI_CONNECTED ? LV_SYMBOL_WIFI : "",
             battery, pct, tm.tm_hour, tm.tm_min);
    if (strcmp(text, s->shown) != 0) { /* unchanged text costs no flush */
        lv_label_set_text(s->right, text);
        snprintf(s->shown, sizeof s->shown, "%s", text);
    }
}
/* recipe:end */
