/* SPDX-License-Identifier: Apache-2.0
 * Tests for the common core running on the emulator backend: init, status,
 * argument checks, rendering through the rotation path, buttons, tags,
 * backlight and sleep, battery, counted memory and power-off on USB.
 */
#include <signal.h>
#include <string.h>

#include "defaults.h"
#include "emu.h"
#include "freertos/FreeRTOS.h"
#include "freertos/task.h"
#include "kit.h"
#include "tests.h"
#include "unity.h"
#include "emu.h"

static void test_calls_before_init_report_state(void)
{
    TEST_ASSERT_EQUAL(LCD349_ERR_STATE, lcd349_backlight_set(50));
    TEST_ASSERT_EQUAL(LCD349_ERR_STATE, lcd349_tone(440, 100, 50));
    TEST_ASSERT_EQUAL(LCD349_ERR_STATE, lcd349_power_off());
    TEST_ASSERT_NULL(lcd349_display());
    /* Board facts work before init. */
    TEST_ASSERT_EQUAL(640, lcd349_board_info()->lcd_w);
}

static void test_init(void)
{
    TEST_ASSERT_EQUAL(LCD349_OK, lcd349_init(NULL));
    TEST_ASSERT_EQUAL(LCD349_ERR_STATE, lcd349_init(NULL));
    TEST_ASSERT_NOT_NULL(lcd349_display());
    TEST_ASSERT_NOT_NULL(lcd349_touch_indev());
    TEST_ASSERT_EQUAL_HEX32(0, lcd349_board_status());
}

static lv_obj_t *square(int x, int y, uint32_t rgb)
{
    lv_obj_t *o = lv_obj_create(lv_screen_active());
    lv_obj_remove_style_all(o);
    lv_obj_set_style_bg_opa(o, LV_OPA_COVER, 0);
    lv_obj_set_style_bg_color(o, lv_color_hex(rgb), 0);
    lv_obj_set_size(o, 4, 4);
    lv_obj_set_pos(o, x, y);
    return o;
}

static void expect_px(const uint8_t *f, int row, int col, uint8_t b0, uint8_t b1)
{
    size_t off = 2u * (size_t)(row * LCD349_PANEL_W + col);
    TEST_ASSERT_EQUAL_HEX8(b0, f[off]);
    TEST_ASSERT_EQUAL_HEX8(b1, f[off + 1]);
}

static void test_render_reaches_panel_rotated_and_swapped(void)
{
    uint32_t before;
    lcd349__emu_last_frame(&before);
    lcd349_ui_lock();
    lv_obj_t *scr = lv_screen_active();
    lv_obj_remove_style_all(scr);
    lv_obj_set_style_bg_opa(scr, LV_OPA_COVER, 0);
    lv_obj_set_style_bg_color(scr, lv_color_hex(0x000000), 0);
    square(0, 0, 0xFF0000);     /* top-left: red */
    square(636, 0, 0x00FF00);   /* top-right: green */
    square(0, 168, 0x0000FF);   /* bottom-left: blue */
    square(636, 168, 0xFFFFFF); /* bottom-right: white */
    lcd349_ui_unlock();

    uint32_t frames = before;
    const uint8_t *f = NULL;
    for (int i = 0; i < 200 && frames == before; i++) {
        vTaskDelay(pdMS_TO_TICKS(10));
        f = lcd349__emu_last_frame(&frames);
    }
    TEST_ASSERT_TRUE_MESSAGE(frames > before, "a frame reached the panel");
    /* Same golden positions as the pure rotation test, through LVGL and the backend. */
    expect_px(f, 0, 171, 0xF8, 0x00);   /* red, big-endian RGB565 */
    expect_px(f, 639, 171, 0x07, 0xE0); /* green */
    expect_px(f, 0, 0, 0x00, 0x1F);     /* blue */
    expect_px(f, 639, 0, 0xFF, 0xFF);   /* white */
    expect_px(f, 320, 86, 0x00, 0x00);  /* black middle */
}

static void test_argument_checks_match_on_both_backends(void)
{
    TEST_ASSERT_EQUAL(LCD349_ERR_ARG, lcd349_tone(10, 100, 50));
    TEST_ASSERT_EQUAL(LCD349_ERR_ARG, lcd349_tone(440, 0, 50));
    TEST_ASSERT_EQUAL(LCD349_ERR_ARG, lcd349_tone(440, 100, 101));
    TEST_ASSERT_EQUAL(LCD349_OK, lcd349_tone(440, 100, 50));
    lcd349_tone_t seq[] = {{880, 50}, {0, 50}, {880, 50}};
    TEST_ASSERT_EQUAL(LCD349_OK, lcd349_tone_seq(seq, 3, 60));
    TEST_ASSERT_EQUAL(LCD349_ERR_ARG, lcd349_tone_seq(seq, 0, 60));

    TEST_ASSERT_EQUAL(LCD349_ERR_ARG, lcd349_kv_set_str("namespace-is-16c", "k", "v"));
    TEST_ASSERT_EQUAL(LCD349_ERR_ARG, lcd349_kv_set_str("ns", "", "v"));
    TEST_ASSERT_EQUAL(LCD349_OK, lcd349_kv_set_str("ns", "k", "v"));

    lcd349_wifi_net_t short_pw = {.ssid = "home", .password = "1234567"};
    lcd349_wifi_net_t five[5] = {0};
    TEST_ASSERT_EQUAL(LCD349_ERR_ARG, lcd349_wifi_set_networks(&short_pw, 1));
    TEST_ASSERT_EQUAL(LCD349_ERR_ARG, lcd349_wifi_set_networks(five, 5));
    TEST_ASSERT_EQUAL(LCD349_ERR_ARG, lcd349_backlight_set(101));

    char line[8];
    TEST_ASSERT_EQUAL(LCD349_ERR_ARG, lcd349_console_read_line(NULL, sizeof line, 0));
    TEST_ASSERT_EQUAL(LCD349_ERR_ARG, lcd349_console_read_line(line, 1, 0));
    uint32_t t0 = lcd349_time_ms();
    TEST_ASSERT_EQUAL(LCD349_ERR_TIMEOUT, lcd349_console_read_line(line, sizeof line, 50));
    TEST_ASSERT_TRUE(lcd349_time_ms() - t0 >= 50);
    TEST_ASSERT_EQUAL_STRING("", line);
}

static lcd349_btn_event_t s_got[8];
static volatile int s_ngot;

static void on_button(const lcd349_btn_event_t *ev, void *ctx)
{
    (void)ctx;
    if (s_ngot < 8) {
        s_got[s_ngot++] = *ev;
    }
}

static void test_buttons_debounced_with_press_time(void)
{
    TEST_ASSERT_EQUAL(LCD349_OK, lcd349_buttons_subscribe(on_button, NULL));
    uint32_t t = lcd349_time_ms();
    lcd349__button_raw(LCD349_BTN_PWR, true, t);
    lcd349__button_raw(LCD349_BTN_PWR, false, t + 2); /* bounce */
    lcd349__button_raw(LCD349_BTN_PWR, true, t + 3);
    vTaskDelay(pdMS_TO_TICKS(40));
    TEST_ASSERT_EQUAL(1, s_ngot);
    TEST_ASSERT_EQUAL(LCD349_BTN_PWR, s_got[0].btn);
    TEST_ASSERT_TRUE(s_got[0].down);
    TEST_ASSERT_EQUAL_UINT32(t + 3, s_got[0].t_ms);
    TEST_ASSERT_TRUE(lcd349_button_is_down(LCD349_BTN_PWR));

    lcd349__button_raw(LCD349_BTN_PWR, false, lcd349_time_ms());
    vTaskDelay(pdMS_TO_TICKS(40));
    TEST_ASSERT_EQUAL(2, s_ngot);
    TEST_ASSERT_FALSE(s_got[1].down);
    TEST_ASSERT_FALSE(lcd349_button_is_down(LCD349_BTN_PWR));
}

static void test_tags(void)
{
    char buf[64];
    lcd349_ui_lock();
    lv_obj_t *row = lv_button_create(lv_screen_active());
    TEST_ASSERT_EQUAL(LCD349_OK, lcd349_ui_tag(row, "list-row"));
    TEST_ASSERT_EQUAL(LCD349_OK, lcd349_ui_tag(row, "ignore:contrast"));
    TEST_ASSERT_EQUAL(LCD349_OK, lcd349_ui_tag(row, "list-row")); /* no duplicate */
    TEST_ASSERT_EQUAL(LCD349_ERR_ARG, lcd349_ui_tag(row, "List Row"));
    TEST_ASSERT_EQUAL(LCD349_ERR_ARG, lcd349_ui_tag(row, ""));
    TEST_ASSERT_TRUE(lcd349_ui_has_tag(row, "list-row"));
    TEST_ASSERT_FALSE(lcd349_ui_has_tag(row, "list"));
    TEST_ASSERT_EQUAL(2, lcd349_ui_tags(row, buf, sizeof buf));
    TEST_ASSERT_EQUAL_STRING("list-row,ignore:contrast", buf);
    lv_obj_t *plain = lv_obj_create(lv_screen_active());
    TEST_ASSERT_EQUAL(0, lcd349_ui_tags(plain, buf, sizeof buf));
    lv_obj_delete(row);
    lv_obj_delete(plain);
    lcd349_ui_unlock();
}

static void test_backlight_and_sleep(void)
{
    TEST_ASSERT_EQUAL(LCD349_BACKLIGHT_DEFAULT_PCT, lcd349_backlight_get());
    TEST_ASSERT_EQUAL(LCD349_OK, lcd349_backlight_set(50));
    TEST_ASSERT_EQUAL(50, lcd349_backlight_get());
    TEST_ASSERT_EQUAL(LCD349_OK, lcd349_display_sleep(true));
    TEST_ASSERT_TRUE(lcd349_display_is_asleep());
    /* Asleep: the level is remembered for the wake, and no frames are sent. */
    TEST_ASSERT_EQUAL(LCD349_OK, lcd349_backlight_set(70));
    uint32_t before, after;
    lcd349__emu_last_frame(&before);
    lcd349_ui_lock();
    square(300, 80, 0xFF00FF);
    lcd349_ui_unlock();
    vTaskDelay(pdMS_TO_TICKS(200));
    lcd349__emu_last_frame(&after);
    TEST_ASSERT_EQUAL_UINT32(before, after);
    TEST_ASSERT_EQUAL(LCD349_OK, lcd349_display_sleep(false));
    TEST_ASSERT_EQUAL(70, lcd349_backlight_get());
    vTaskDelay(pdMS_TO_TICKS(200));
    lcd349__emu_last_frame(&after);
    TEST_ASSERT_TRUE_MESSAGE(after > before, "waking redraws the screen");
}

static void test_battery(void)
{
    uint16_t mv;
    uint8_t pct;
    TEST_ASSERT_EQUAL(LCD349_OK, lcd349_battery_read(&mv, &pct));
    TEST_ASSERT_EQUAL_UINT16(LCD349_EMU_BATTERY_MV, mv);
    TEST_ASSERT_EQUAL(lcd349_battery_pct_from_mv(mv), pct);
    TEST_ASSERT_EQUAL(LCD349_OK, lcd349_battery_read(NULL, NULL));
}

static void test_counted_memory(void)
{
    lcd349_mem_stats_t a, b;
    lcd349_mem_stats(&a);
    /* The kit's two frame buffers (landscape and panel) are counted. */
    TEST_ASSERT_TRUE(a.internal_bytes + a.psram_bytes >= 2u * LCD349_LCD_W * LCD349_LCD_H * 2u);
    void *p = lcd349_alloc(1000, LCD349_MEM_INTERNAL);
    TEST_ASSERT_NOT_NULL(p);
    lcd349_mem_stats(&b);
    TEST_ASSERT_EQUAL_UINT32(a.internal_bytes + 1000, b.internal_bytes);
    lcd349_free(p);
    lcd349_mem_stats(&b);
    TEST_ASSERT_EQUAL_UINT32(a.internal_bytes, b.internal_bytes);
    TEST_ASSERT_TRUE(b.internal_peak >= a.internal_bytes + 1000);
    TEST_ASSERT_NULL(lcd349_alloc(0, LCD349_MEM_ANY));
    lcd349_free(NULL);
}

static volatile int s_nsource;
static volatile lcd349_power_source_t s_source_seen;

static void on_source(lcd349_power_source_t source, void *ctx)
{
    (void)ctx;
    s_source_seen = source;
    s_nsource++;
}

static void set_on_battery(emu_state_t *s, void *ctx)
{
    s->on_battery = *(bool *)ctx;
}

static bool wait_source(lcd349_power_source_t want, uint32_t ms)
{
    uint32_t t0 = lcd349_time_ms();
    while (lcd349_power_source() != want) {
        if (lcd349_time_ms() - t0 > ms) {
            return false;
        }
        vTaskDelay(pdMS_TO_TICKS(20));
    }
    return true;
}

/* R1.8: the source follows the model (--usb, --battery, state set) within
 * 1.5 s, and the callback hears each change once. */
static void test_power_source_follows_the_model(void)
{
    TEST_ASSERT_EQUAL(LCD349_POWER_SOURCE_USB, lcd349_power_source()); /* the instance starts on USB */
    TEST_ASSERT_EQUAL(LCD349_OK, lcd349_power_on_source_change(on_source, NULL));

    bool battery = true;
    emu_state_update(set_on_battery, &battery);
    TEST_ASSERT_TRUE_MESSAGE(wait_source(LCD349_POWER_SOURCE_BATTERY, 1500), "BATTERY within 1.5 s");
    vTaskDelay(pdMS_TO_TICKS(1200)); /* past the next poll: still one call */
    TEST_ASSERT_EQUAL(1, s_nsource);
    TEST_ASSERT_EQUAL(LCD349_POWER_SOURCE_BATTERY, s_source_seen);

    battery = false;
    emu_state_update(set_on_battery, &battery);
    TEST_ASSERT_TRUE_MESSAGE(wait_source(LCD349_POWER_SOURCE_USB, 1500), "USB again within 1.5 s");
    vTaskDelay(pdMS_TO_TICKS(100));
    TEST_ASSERT_EQUAL(2, s_nsource);
    TEST_ASSERT_EQUAL(LCD349_POWER_SOURCE_USB, s_source_seen);
    TEST_ASSERT_EQUAL(LCD349_OK, lcd349_power_on_source_change(NULL, NULL));
}

static void test_power_off_on_usb_returns(void)
{
    uint64_t t0 = lcd349_time_us();
    TEST_ASSERT_EQUAL(LCD349_ERR_ON_USB, lcd349_power_off());
    uint64_t ms = (lcd349_time_us() - t0) / 1000u;
    TEST_ASSERT_TRUE(ms >= LCD349_POWER_OFF_USB_CHECK_MS);
    TEST_ASSERT_TRUE(ms < LCD349_POWER_OFF_USB_CHECK_MS + 500);
}

static void test_storage(void)
{
    char buf[16];
    size_t len = sizeof buf;
    TEST_ASSERT_EQUAL(LCD349_ERR_NOT_FOUND, lcd349_kv_get_str("app", "name", buf, &len));
    TEST_ASSERT_EQUAL(LCD349_OK, lcd349_kv_set_str("app", "name", "pager"));
    len = sizeof buf;
    TEST_ASSERT_EQUAL(LCD349_OK, lcd349_kv_get_str("app", "name", buf, &len));
    TEST_ASSERT_EQUAL_STRING("pager", buf);
    TEST_ASSERT_EQUAL(6, len);
    /* Too small: NO_MEM with the size needed, including the NUL. */
    len = 3;
    TEST_ASSERT_EQUAL(LCD349_ERR_NO_MEM, lcd349_kv_get_str("app", "name", buf, &len));
    TEST_ASSERT_EQUAL(6, len);
    /* Typed like NVS: a string isn't a blob. */
    uint8_t blob[4] = {1, 2, 3, 4}, back[4] = {0};
    len = sizeof back;
    TEST_ASSERT_EQUAL(LCD349_ERR_ARG, lcd349_kv_get_blob("app", "name", back, &len));
    TEST_ASSERT_EQUAL(LCD349_OK, lcd349_kv_set_blob("app", "blob", blob, sizeof blob));
    TEST_ASSERT_EQUAL(LCD349_OK, lcd349_kv_get_blob("app", "blob", back, &len));
    TEST_ASSERT_EQUAL_MEMORY(blob, back, 4);
    TEST_ASSERT_EQUAL(LCD349_OK, lcd349_kv_erase("app", "name"));
    len = sizeof buf;
    TEST_ASSERT_EQUAL(LCD349_ERR_NOT_FOUND, lcd349_kv_get_str("app", "name", buf, &len));
    TEST_ASSERT_EQUAL(LCD349_OK, lcd349_kv_erase("app", NULL));
    len = sizeof back;
    TEST_ASSERT_EQUAL(LCD349_ERR_NOT_FOUND, lcd349_kv_get_blob("app", "blob", back, &len));
}

static lcd349_touch_sample_t s_touch[16];
static volatile int s_ntouch;
static volatile int s_clicks;

static void on_touch(const lcd349_touch_sample_t *t, void *ctx)
{
    (void)ctx;
    if (s_ntouch < 16) {
        s_touch[s_ntouch++] = *t;
    }
}

static void on_click(lv_event_t *e)
{
    (void)e;
    s_clicks++;
}

static void test_touch_reaches_lvgl_as_a_click(void)
{
    TEST_ASSERT_EQUAL(LCD349_OK, lcd349_touch_subscribe(on_touch, NULL));
    lcd349_ui_lock();
    lv_obj_t *b = lv_button_create(lv_screen_active());
    lv_obj_set_size(b, 100, 60);
    lv_obj_set_pos(b, 400, 60);
    lv_obj_add_event_cb(b, on_click, LV_EVENT_CLICKED, NULL);
    lcd349_ui_unlock();
    vTaskDelay(pdMS_TO_TICKS(50));

    /* A tap in landscape coordinates: three samples down, one up. */
    emu_touch_t tap[] = {{450, 90, true}, {450, 90, true}, {450, 90, true}, {450, 90, false}};
    uint64_t end;
    TEST_ASSERT_TRUE(emu_touch_push(tap, 4, false, &end));
    for (int i = 0; i < 100 && emu_touch_consumed() < end; i++) {
        vTaskDelay(pdMS_TO_TICKS(10));
    }
    vTaskDelay(pdMS_TO_TICKS(60));
    TEST_ASSERT_EQUAL(1, s_clicks);
    /* Subscribers saw landscape coordinates: the panel round trip is exact. */
    TEST_ASSERT_TRUE(s_ntouch >= 2);
    TEST_ASSERT_EQUAL(450, s_touch[0].x);
    TEST_ASSERT_EQUAL(90, s_touch[0].y);
    TEST_ASSERT_TRUE(s_touch[0].pressed);
    TEST_ASSERT_FALSE(s_touch[s_ntouch - 1].pressed);

    /* Asleep, touch isn't read and can't click (touch never wakes the device). */
    TEST_ASSERT_EQUAL(LCD349_OK, lcd349_display_sleep(true));
    TEST_ASSERT_TRUE(emu_touch_push(tap, 4, false, &end));
    vTaskDelay(pdMS_TO_TICKS(300));
    TEST_ASSERT_EQUAL(1, s_clicks);
    emu_touch_flush();
    TEST_ASSERT_EQUAL(LCD349_OK, lcd349_display_sleep(false));
    lcd349_ui_lock();
    lv_obj_delete(b);
    lcd349_ui_unlock();
}

/* A tap that arrives before LVGL has laid out a new screen still clicks: the
 * kit lays out before each touch read. Without that, the first read finds
 * the button where it isn't yet, and the next read (after a refresh lays it
 * out) sees the finger "dragged" onto it, which never clicks (task 10.2). */
static void test_touch_before_layout_still_clicks(void)
{
    int before = s_clicks;
    emu_touch_t tap[] = {{320, 88, true}, {320, 88, true}, {320, 88, false}};
    uint64_t end;
    lcd349_ui_lock();
    lv_obj_t *b = lv_button_create(lv_screen_active());
    lv_obj_set_size(b, 160, 72);
    lv_obj_align(b, LV_ALIGN_CENTER, 0, 2); /* laid out only at the next refresh */
    lv_obj_add_event_cb(b, on_click, LV_EVENT_CLICKED, NULL);
    TEST_ASSERT_TRUE(emu_touch_push(tap, 3, false, &end));
    lv_indev_t *in = lcd349_touch_indev();
    lv_indev_read(in);                               /* the press, before any refresh */
    lv_obj_update_layout(lv_screen_active());        /* what a refresh does next */
    lv_indev_read(in);
    lv_indev_read(in);                               /* the release */
    lv_obj_delete(b);
    lcd349_ui_unlock();
    TEST_ASSERT_EQUAL(end, emu_touch_consumed());
    TEST_ASSERT_EQUAL(before + 1, s_clicks);
}

/* A task blocked in a write when the FreeRTOS tick lands must not get EINTR:
 * a failed console write silences stdio for good on macOS (task 10.2). */
static void test_ticks_restart_interrupted_calls(void)
{
    struct sigaction sa;
    TEST_ASSERT_EQUAL(0, sigaction(SIGALRM, NULL, &sa));
    TEST_ASSERT_TRUE(sa.sa_handler != SIG_DFL && sa.sa_handler != SIG_IGN);
    TEST_ASSERT_TRUE(sa.sa_flags & SA_RESTART);
}

static void test_audio_events(void)
{
    emu_tone_event_t ev[16];
    uint64_t next;
    size_t before = emu_audio_events(0, ev, 16, &next);
    (void)before;
    lcd349_tone_t seq[] = {{880, 50}, {0, 30}, {660, 40}};
    TEST_ASSERT_EQUAL(LCD349_OK, lcd349_tone_seq(seq, 3, 70));
    size_t n = emu_audio_events(next, ev, 16, &next);
    TEST_ASSERT_EQUAL(4, n); /* stop (replace), then three steps */
    TEST_ASSERT_TRUE(ev[0].stop);
    TEST_ASSERT_EQUAL(880, ev[1].hz);
    TEST_ASSERT_EQUAL(0, ev[2].hz);
    TEST_ASSERT_EQUAL(660, ev[3].hz);
    TEST_ASSERT_EQUAL_UINT32(ev[1].t_ms + 80, ev[3].t_ms); /* steps are timed back to back */
    TEST_ASSERT_EQUAL(70, ev[3].volume);
    TEST_ASSERT_EQUAL(0, emu_audio_events(next, ev, 16, &next));
}

static lcd349_wifi_status_t s_wifi[16];
static volatile int s_nwifi;

static void on_wifi(const lcd349_wifi_status_t *st, void *ctx)
{
    (void)ctx;
    if (s_nwifi < 16) {
        s_wifi[s_nwifi++] = *st;
    }
}

static void set_air(emu_state_t *s, void *ctx)
{
    (void)ctx;
    s->n_nets = 3;
    s->nets[0] = (emu_net_t){.ssid = "cafe-5g", .password = "password123", .band_x10 = 50, .rssi = -30};
    s->nets[1] = (emu_net_t){.ssid = "home", .password = "password123", .band_x10 = 24, .rssi = -60};
    s->nets[2] = (emu_net_t){.ssid = "phone", .password = "", .band_x10 = 24, .rssi = -45};
}

static void drop_home(emu_state_t *s, void *ctx)
{
    (void)ctx;
    s->n_nets = 1; /* only the 5 GHz cafe is left */
}

static bool wait_wifi(lcd349_wifi_state_t want, int ms)
{
    for (int i = 0; i < ms / 20; i++) {
        lcd349_wifi_status_t st;
        lcd349_wifi_status(&st);
        if (st.state == want) {
            return true;
        }
        vTaskDelay(pdMS_TO_TICKS(20));
    }
    return false;
}

static void test_wifi_sim(void)
{
    emu_state_update(set_air, NULL);
    TEST_ASSERT_EQUAL(LCD349_OK, lcd349_wifi_subscribe(on_wifi, NULL));

    /* The 5 GHz network is invisible to the 2.4 GHz radio; the rest come strongest first. */
    lcd349_wifi_ap_t aps[8];
    size_t n = 8;
    TEST_ASSERT_EQUAL(LCD349_OK, lcd349_wifi_scan(aps, &n));
    TEST_ASSERT_EQUAL(2, n);
    TEST_ASSERT_EQUAL_STRING("phone", aps[0].ssid);
    TEST_ASSERT_FALSE(aps[0].secure);
    TEST_ASSERT_EQUAL_STRING("home", aps[1].ssid);

    /* Saved: the 5 GHz cafe first (never found), then home with a wrong password. */
    lcd349_wifi_net_t saved[] = {{.ssid = "cafe-5g", .password = "password123"}, {.ssid = "home", .password = "wrong-password"}};
    TEST_ASSERT_EQUAL(LCD349_OK, lcd349_wifi_set_networks(saved, 2));
    TEST_ASSERT_EQUAL(LCD349_OK, lcd349_wifi_start());
    TEST_ASSERT_TRUE(wait_wifi(LCD349_WIFI_DISCONNECTED, 4000));
    lcd349_wifi_status_t st;
    lcd349_wifi_status(&st);
    TEST_ASSERT_EQUAL(LCD349_WIFI_REASON_AUTH, st.reason);
    TEST_ASSERT_EQUAL_STRING("home", st.ssid);

    /* The right password connects (setting networks triggers a new attempt). */
    lcd349_wifi_net_t good[] = {{.ssid = "home", .password = "password123"}};
    TEST_ASSERT_EQUAL(LCD349_OK, lcd349_wifi_set_networks(good, 1));
    TEST_ASSERT_TRUE(wait_wifi(LCD349_WIFI_CONNECTED, 4000));
    lcd349_wifi_status(&st);
    TEST_ASSERT_EQUAL_STRING("home", st.ssid);
    TEST_ASSERT_EQUAL(-60, st.rssi);
    TEST_ASSERT_NOT_EQUAL(0, st.ip4);

    /* The network disappears: LOST, and it keeps looking (NOT_FOUND). */
    emu_state_update(drop_home, NULL);
    emu_wifi_env_changed();
    TEST_ASSERT_TRUE(wait_wifi(LCD349_WIFI_DISCONNECTED, 3000));
    vTaskDelay(pdMS_TO_TICKS(50));
    bool saw_lost = false;
    for (int i = 0; i < s_nwifi; i++) {
        saw_lost |= s_wifi[i].state == LCD349_WIFI_DISCONNECTED && s_wifi[i].reason == LCD349_WIFI_REASON_LOST &&
                    strcmp(s_wifi[i].ssid, "home") == 0;
    }
    TEST_ASSERT_TRUE_MESSAGE(saw_lost, "subscribers saw the drop as LOST");
    lcd349_wifi_stop();
    TEST_ASSERT_TRUE(wait_wifi(LCD349_WIFI_IDLE, 2000));
    lcd349_wifi_status(&st);
    TEST_ASSERT_EQUAL(LCD349_WIFI_REASON_STOPPED, st.reason);

    /* Subscribers saw the same sequence. */
    vTaskDelay(pdMS_TO_TICKS(50));
    bool saw_connecting = false, saw_connected = false;
    for (int i = 0; i < s_nwifi; i++) {
        saw_connecting |= s_wifi[i].state == LCD349_WIFI_CONNECTING;
        saw_connected |= s_wifi[i].state == LCD349_WIFI_CONNECTED;
    }
    TEST_ASSERT_TRUE(saw_connecting);
    TEST_ASSERT_TRUE(saw_connected);
}

/* Regression: the linux target's FreeRTOS preempts at every tick (SIGALRM).
 * A low-priority task suspended while holding a host lock deadlocks the
 * process as soon as a higher-priority task wants the same lock; the device
 * model used a pthread mutex and hung this way. Critical sections can't be
 * preempted. The update below holds the lock ~20 us to make the old failure
 * near-certain; the loop is bounded by wall time because ticks slow down
 * while signals are blocked. */
static volatile bool s_hammer_stop, s_hammer_done;
static volatile int s_hammer_iter;

static void hammer(void *arg)
{
    (void)arg;
    emu_state_t st;
    while (!s_hammer_stop) {
        emu_state_get(&st);
        s_hammer_iter++;
        vTaskDelay(1);
    }
    s_hammer_done = true;
    vTaskDelete(NULL);
}

static void slow_update(emu_state_t *s, void *ctx)
{
    (void)ctx;
    uint64_t t0 = lcd349_time_us();
    while (lcd349_time_us() - t0 < 20) {
    }
    s->board_rev[0] = 'V';
}

static void test_no_deadlock_under_preemption(void)
{
    s_hammer_stop = s_hammer_done = false;
    s_hammer_iter = 0;
    TEST_ASSERT_EQUAL(pdPASS, xTaskCreate(hammer, "hammer", 4096, NULL, 5, NULL));
    uint64_t end = lcd349_time_us() + 1000000u;
    int updates = 0;
    while (lcd349_time_us() < end) {
        emu_state_update(slow_update, NULL);
        updates++;
    }
    s_hammer_stop = true;
    for (int i = 0; i < 100 && !s_hammer_done; i++) {
        vTaskDelay(pdMS_TO_TICKS(10));
    }
    TEST_ASSERT_TRUE(s_hammer_done);
    TEST_ASSERT_TRUE(s_hammer_iter > 0);
    TEST_ASSERT_TRUE(updates > 1000);
}

void run_kit_tests(void)
{
    RUN_TEST(test_calls_before_init_report_state);
    RUN_TEST(test_init);
    RUN_TEST(test_render_reaches_panel_rotated_and_swapped);
    RUN_TEST(test_argument_checks_match_on_both_backends);
    RUN_TEST(test_buttons_debounced_with_press_time);
    RUN_TEST(test_tags);
    RUN_TEST(test_backlight_and_sleep);
    RUN_TEST(test_battery);
    RUN_TEST(test_counted_memory);
    RUN_TEST(test_storage);
    RUN_TEST(test_touch_reaches_lvgl_as_a_click);
    RUN_TEST(test_touch_before_layout_still_clicks);
    RUN_TEST(test_ticks_restart_interrupted_calls);
    RUN_TEST(test_audio_events);
    RUN_TEST(test_wifi_sim);
    RUN_TEST(test_no_deadlock_under_preemption);
    RUN_TEST(test_power_source_follows_the_model);
    RUN_TEST(test_power_off_on_usb_returns);
}
