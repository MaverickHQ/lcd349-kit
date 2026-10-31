/* SPDX-License-Identifier: Apache-2.0
 *
 * Control methods (requirement 5.3). Each takes a params object and either
 * answers at once or leaves the request pending; control.c polls pending
 * requests every loop. Coordinates are landscape (0-639, 0-171).
 */
#include <regex.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/stat.h>
#include <time.h>
#include <unistd.h>

#include "control.h"
#include "defaults.h"
#include "freertos/FreeRTOS.h"
#include "rotate.h"

#define WAIT_DEFAULT_MS 5000
#define WAIT_MAX_MS 120000
#define TEXT_CHECK_MS 30
#define RECORD_MAX_S 60
#define RECORD_MAX_FRAMES 600

typedef enum { OP_WAIT = 1, OP_PRESS, OP_TOUCH } op_kind_t;

static uint8_t s_held_by_ops; /* buttons an input.press is holding: bit 0 BOOT, bit 1 PWR */

static uint64_t now_us(void)
{
    return lcd349_time_us();
}

/* ---- Parameter helpers ---- */

static bool get_int(const cJSON *p, const char *name, int def, int min, int max, int *out, rpc_err_t *err)
{
    const cJSON *v = cJSON_GetObjectItem(p, name);
    if (!v) {
        *out = def;
        return true;
    }
    if (!cJSON_IsNumber(v) || v->valuedouble < min || v->valuedouble > max || v->valuedouble != (double)(int)v->valuedouble) {
        rpc_fail(err, RPC_INVALID_PARAMS, "%s must be an integer from %d to %d", name, min, max);
        return false;
    }
    *out = (int)v->valuedouble;
    return true;
}

static bool get_bool(const cJSON *p, const char *name, bool def)
{
    const cJSON *v = cJSON_GetObjectItem(p, name);
    return v ? cJSON_IsTrue(v) : def;
}

static bool get_point(const cJSON *v, uint16_t *x, uint16_t *y)
{
    if (!cJSON_IsArray(v) || cJSON_GetArraySize(v) != 2) {
        return false;
    }
    const cJSON *a = cJSON_GetArrayItem(v, 0), *b = cJSON_GetArrayItem(v, 1);
    if (!cJSON_IsNumber(a) || !cJSON_IsNumber(b) || a->valuedouble < 0 || a->valuedouble >= LCD349_LCD_W || b->valuedouble < 0 ||
        b->valuedouble >= LCD349_LCD_H) {
        return false;
    }
    *x = (uint16_t)a->valuedouble;
    *y = (uint16_t)b->valuedouble;
    return true;
}

/* ---- Shared state ---- */

bool emu_display_dark(void)
{
    if (emu_control_in_download()) {
        return false;
    }
    emu_state_t st;
    emu_state_get(&st);
    return st.power != EMU_POWER_ON || emu_fb_hdr()->backlight == 0 || lcd349_display_is_asleep();
}

static const char *power_name(emu_power_t p)
{
    switch (p) {
    case EMU_POWER_ON: return "on";
    case EMU_POWER_LIGHT_SLEEP: return "light_sleep";
    case EMU_POWER_OFF: return "off";
    case EMU_POWER_DOWNLOAD: return "download";
    }
    return "?";
}

static const char *wifi_state_name(lcd349_wifi_state_t s)
{
    static const char *n[] = {"idle", "scanning", "connecting", "connected", "disconnected"};
    return (unsigned)s < 5 ? n[s] : "?";
}

static const char *wifi_reason_name(lcd349_wifi_reason_t r)
{
    static const char *n[] = {"none", "auth", "not_found", "lost", "stopped"};
    return (unsigned)r < 5 ? n[r] : "?";
}

static void add_iso_time(cJSON *o, int64_t t)
{
    time_t tt = (time_t)t;
    struct tm tm;
    gmtime_r(&tt, &tm);
    char buf[32];
    strftime(buf, sizeof buf, "%Y-%m-%dT%H:%M:%SZ", &tm);
    cJSON_AddNumberToObject(o, "unix", (double)t);
    cJSON_AddStringToObject(o, "iso", buf);
}

static cJSON *memory_json(void)
{
    cJSON *m = cJSON_CreateObject();
    lcd349_mem_stats_t st;
    lcd349_mem_stats(&st);
    cJSON_AddNumberToObject(m, "kit_internal_bytes", (double)st.internal_bytes);
    cJSON_AddNumberToObject(m, "kit_psram_bytes", (double)st.psram_bytes);
    cJSON_AddNumberToObject(m, "kit_internal_peak", (double)st.internal_peak);
    cJSON_AddNumberToObject(m, "kit_psram_peak", (double)st.psram_peak);
    cJSON_AddNumberToObject(m, "largest_allocation", (double)st.largest);
    size_t lv_used = 0, lv_total = 0;
#if LV_USE_STDLIB_MALLOC == LV_STDLIB_BUILTIN
    lv_mem_monitor_t mon;
    lcd349_ui_lock();
    lv_mem_monitor(&mon);
    lcd349_ui_unlock();
    lv_total = mon.total_size;
    lv_used = mon.total_size - mon.free_size;
    cJSON_AddNumberToObject(m, "lvgl_used_bytes", (double)lv_used);
    cJSON_AddNumberToObject(m, "lvgl_total_bytes", (double)lv_total);
    cJSON_AddNumberToObject(m, "lvgl_peak_bytes", (double)mon.max_used);
#endif
    cJSON *b = cJSON_AddObjectToObject(m, "budgets_kb");
    LCD349_EMU_BUDGET_PSRAM_KB ? cJSON_AddNumberToObject(b, "psram", LCD349_EMU_BUDGET_PSRAM_KB) : cJSON_AddNullToObject(b, "psram");
    LCD349_EMU_BUDGET_INTERNAL_KB ? cJSON_AddNumberToObject(b, "internal", LCD349_EMU_BUDGET_INTERNAL_KB) : cJSON_AddNullToObject(b, "internal");
    LCD349_EMU_BUDGET_LVGL_KB ? cJSON_AddNumberToObject(b, "lvgl", LCD349_EMU_BUDGET_LVGL_KB) : cJSON_AddNullToObject(b, "lvgl");
    /* Requirement 3.13: warn past a known budget. Unmeasured budgets (null) never warn. */
    cJSON *w = cJSON_AddArrayToObject(m, "warnings");
    char msg[160];
    if (LCD349_EMU_BUDGET_PSRAM_KB && st.psram_bytes > (size_t)LCD349_EMU_BUDGET_PSRAM_KB * 1024u) {
        snprintf(msg, sizeof msg, "PSRAM use %zu KB is over the board's %d KB", st.psram_bytes / 1024, LCD349_EMU_BUDGET_PSRAM_KB);
        cJSON_AddItemToArray(w, cJSON_CreateString(msg));
    }
    if (LCD349_EMU_BUDGET_INTERNAL_KB && st.internal_bytes > (size_t)LCD349_EMU_BUDGET_INTERNAL_KB * 1024u) {
        snprintf(msg, sizeof msg, "internal RAM use %zu KB is over the board's %d KB", st.internal_bytes / 1024, LCD349_EMU_BUDGET_INTERNAL_KB);
        cJSON_AddItemToArray(w, cJSON_CreateString(msg));
    }
    if (LCD349_EMU_BUDGET_LVGL_KB && lv_used > (size_t)LCD349_EMU_BUDGET_LVGL_KB * 1024u) {
        snprintf(msg, sizeof msg, "LVGL heap use %zu KB is over the board's %d KB", lv_used / 1024, LCD349_EMU_BUDGET_LVGL_KB);
        cJSON_AddItemToArray(w, cJSON_CreateString(msg));
    }
    (void)lv_total;
    return m;
}

cJSON *emu_state_json(bool full)
{
    emu_state_t st;
    emu_state_get(&st);
    emu_fb_hdr_t *h = emu_fb_hdr();
    cJSON *o = cJSON_CreateObject();
    cJSON_AddStringToObject(o, "power", power_name(st.power));
    cJSON_AddBoolToObject(o, "power_hold", st.hold);
    cJSON_AddStringToObject(o, "source", st.on_battery ? "battery" : "usb");
    cJSON_AddStringToObject(o, "board_rev", st.board_rev);

    cJSON *d = cJSON_AddObjectToObject(o, "display");
    cJSON_AddNumberToObject(d, "backlight", h->backlight);
    cJSON_AddBoolToObject(d, "asleep", !emu_control_in_download() && lcd349_display_is_asleep());
    cJSON_AddBoolToObject(d, "dark", emu_display_dark());
    cJSON_AddNumberToObject(d, "frame_seq", h->seq);
    cJSON_AddNumberToObject(d, "frames", h->frames);
    cJSON_AddNumberToObject(d, "idle_ms", (double)(emu_fb_idle_us() / 1000u));
    cJSON_AddNumberToObject(d, "flush_ms", emu_flush_ms());

    cJSON *bat = cJSON_AddObjectToObject(o, "battery");
    cJSON_AddNumberToObject(bat, "mv", st.battery_mv);
    cJSON_AddNumberToObject(bat, "pct", lcd349_battery_pct_from_mv(st.battery_mv));
    cJSON_AddBoolToObject(bat, "curve_verified", lcd349_board_info()->battery_curve_verified);

    add_iso_time(cJSON_AddObjectToObject(o, "clock"), (int64_t)time(NULL) + st.rtc_offset_s);

    cJSON *btn = cJSON_AddObjectToObject(o, "buttons");
    cJSON_AddBoolToObject(btn, "boot", emu_button_get(LCD349_BTN_BOOT));
    cJSON_AddBoolToObject(btn, "pwr", emu_button_get(LCD349_BTN_PWR));

    cJSON *air = cJSON_CreateArray();
    for (size_t i = 0; i < st.n_nets; i++) {
        cJSON *n = cJSON_CreateObject();
        cJSON_AddStringToObject(n, "ssid", st.nets[i].ssid);
        cJSON_AddNumberToObject(n, "band", st.nets[i].band_x10 / 10.0);
        cJSON_AddNumberToObject(n, "rssi", st.nets[i].rssi);
        cJSON_AddBoolToObject(n, "secure", st.nets[i].password[0] != '\0');
        cJSON_AddItemToArray(air, n);
    }
    cJSON *wifi = cJSON_AddObjectToObject(o, "wifi");
    if (full && lcd349__ok(LCD349_SUB_WIFI)) {
        lcd349_wifi_status_t ws;
        be_wifi_status(&ws);
        cJSON_AddStringToObject(wifi, "state", wifi_state_name(ws.state));
        cJSON_AddStringToObject(wifi, "reason", wifi_reason_name(ws.reason));
        cJSON_AddStringToObject(wifi, "ssid", ws.ssid);
        if (ws.state == LCD349_WIFI_CONNECTED) {
            char ip[16];
            const uint8_t *b = (const uint8_t *)&ws.ip4;
            snprintf(ip, sizeof ip, "%u.%u.%u.%u", b[0], b[1], b[2], b[3]);
            cJSON_AddStringToObject(wifi, "ip", ip);
            cJSON_AddNumberToObject(wifi, "rssi", ws.rssi);
        }
    }
    cJSON_AddItemToObject(wifi, "networks", air);

    if (full) {
        cJSON_AddNumberToObject(o, "status", lcd349_board_status());
        cJSON_AddItemToObject(o, "memory", memory_json());
    }
    cJSON_AddNumberToObject(o, "serial_next", (double)emu_serial_next());
    return o;
}

/* ---- ping, info ---- */

static rpc_res_t m_ping(rpc_client_t *c, const cJSON *p, cJSON **res, rpc_err_t *err)
{
    (void)c;
    (void)p;
    (void)err;
    *res = cJSON_CreateObject();
    cJSON_AddBoolToObject(*res, "pong", true);
    cJSON_AddNumberToObject(*res, "t_ms", lcd349_time_ms());
    return RPC_DONE;
}

static rpc_res_t m_info(rpc_client_t *c, const cJSON *p, cJSON **res, rpc_err_t *err)
{
    (void)c;
    (void)p;
    (void)err;
    const lcd349_board_info_t *b = lcd349_board_info();
    cJSON *o = cJSON_CreateObject();
    cJSON_AddStringToObject(o, "kit_version", LCD349_VERSION_STR);
    cJSON_AddStringToObject(o, "device_json_sha", b->device_json_sha);
    cJSON_AddNumberToObject(o, "pid", getpid());
    cJSON_AddStringToObject(o, "instance_dir", emu_instance_dir() ? emu_instance_dir() : "");
    cJSON_AddBoolToObject(o, "download_mode", emu_control_in_download());
    static const char *reasons[] = {"power_on", "software", "crash", "brownout", "other"};
    cJSON_AddStringToObject(o, "boot_reason", reasons[emu_boot_reason_env()]);

    cJSON *bd = cJSON_AddObjectToObject(o, "board");
    cJSON_AddStringToObject(bd, "name", b->name);
    cJSON_AddStringToObject(bd, "vendor", b->vendor);
    cJSON_AddNumberToObject(bd, "width", b->lcd_w);
    cJSON_AddNumberToObject(bd, "height", b->lcd_h);
    cJSON_AddNumberToObject(bd, "ppi", b->ppi);
    cJSON_AddNumberToObject(bd, "px_per_mm", b->px_per_mm);
    cJSON_AddNumberToObject(bd, "touch_points", b->touch_points);
    cJSON_AddNumberToObject(bd, "touch_period_ms", b->touch_period_ms);
    cJSON_AddBoolToObject(bd, "touch_period_verified", b->touch_period_verified);
    cJSON_AddNumberToObject(bd, "flush_ms", emu_flush_ms());
    cJSON_AddBoolToObject(bd, "flush_ms_verified", b->flush_ms_verified && emu_flush_ms() == b->flush_ms);

    cJSON *fid = cJSON_AddArrayToObject(o, "not_modelled");
    for (uint8_t i = 0; i < b->fidelity_len; i++) {
        cJSON *f = cJSON_CreateObject();
        cJSON_AddStringToObject(f, "id", b->fidelity[i].id);
        cJSON_AddStringToObject(f, "what", b->fidelity[i].what);
        cJSON_AddStringToObject(f, "check_on_hardware", b->fidelity[i].check_on_hardware);
        cJSON_AddItemToArray(fid, f);
    }
    cJSON *ms = cJSON_AddArrayToObject(o, "methods");
    for (const rpc_method_t *m = emu_rpc_methods; m->name; m++) {
        cJSON_AddItemToArray(ms, cJSON_CreateString(m->name));
    }
    cJSON *ex = cJSON_AddObjectToObject(o, "exit_codes");
    cJSON_AddNumberToObject(ex, "reset", LCD349_EXIT_RESET);
    cJSON_AddNumberToObject(ex, "off", LCD349_EXIT_OFF);
    cJSON_AddNumberToObject(ex, "reset_boot_held", LCD349_EXIT_RESET_BOOT_HELD);
    *res = o;
    return RPC_DONE;
}

/* ---- state.get, state.set ---- */

static rpc_res_t m_state_get(rpc_client_t *c, const cJSON *p, cJSON **res, rpc_err_t *err)
{
    (void)c;
    (void)p;
    (void)err;
    *res = emu_state_json(!emu_control_in_download());
    return RPC_DONE;
}

typedef struct {
    bool battery, source, nets, rev, clock;
    uint16_t mv;
    bool on_battery;
    emu_net_t nets_v[EMU_MAX_NETS];
    size_t n_nets;
    char rev_v[3];
    int64_t clock_v;
} state_change_t;

static void apply_change(emu_state_t *s, void *ctx)
{
    state_change_t *ch = ctx;
    if (ch->battery) {
        s->battery_mv = ch->mv;
    }
    if (ch->source) {
        s->on_battery = ch->on_battery;
    }
    if (ch->nets) {
        memcpy(s->nets, ch->nets_v, sizeof s->nets);
        s->n_nets = ch->n_nets;
    }
    if (ch->rev) {
        memcpy(s->board_rev, ch->rev_v, sizeof s->board_rev);
    }
    if (ch->clock) {
        s->rtc_offset_s = ch->clock_v - (int64_t)time(NULL);
    }
}

static rpc_res_t m_state_set(rpc_client_t *c, const cJSON *p, cJSON **res, rpc_err_t *err)
{
    (void)c;
    state_change_t ch = {0};
    const cJSON *v;
    /* Validate everything first, then apply it all at once. */
    if ((v = cJSON_GetObjectItem(p, "battery"))) {
        const cJSON *mv = cJSON_IsObject(v) ? cJSON_GetObjectItem(v, "mv") : v;
        if (!cJSON_IsNumber(mv) || mv->valuedouble < EMU_BATTERY_MV_MIN || mv->valuedouble > EMU_BATTERY_MV_MAX) {
            return rpc_fail(err, RPC_INVALID_PARAMS, "battery.mv must be 2500-4500 (millivolts at the cell)");
        }
        if (cJSON_IsObject(v) && cJSON_IsTrue(cJSON_GetObjectItem(v, "drain"))) {
            return rpc_fail(err, RPC_UNAVAILABLE,
                            "the drain model needs the board's measured current draw and capacity, which task 0.3 hasn't recorded yet; set mv directly");
        }
        ch.battery = true;
        ch.mv = (uint16_t)mv->valuedouble;
    }
    if ((v = cJSON_GetObjectItem(p, "source"))) {
        if (!cJSON_IsString(v) || (strcmp(v->valuestring, "usb") && strcmp(v->valuestring, "battery"))) {
            return rpc_fail(err, RPC_INVALID_PARAMS, "source must be \"usb\" or \"battery\"");
        }
        ch.source = true;
        ch.on_battery = strcmp(v->valuestring, "battery") == 0;
    }
    if ((v = cJSON_GetObjectItem(p, "board_rev"))) {
        if (!cJSON_IsString(v) || (strcmp(v->valuestring, "V1") && strcmp(v->valuestring, "V2"))) {
            return rpc_fail(err, RPC_INVALID_PARAMS, "board_rev must be \"V1\" or \"V2\"");
        }
        ch.rev = true;
        snprintf(ch.rev_v, sizeof ch.rev_v, "%s", v->valuestring);
    }
    if ((v = cJSON_GetObjectItem(p, "clock"))) {
        bool ok = cJSON_IsNumber(v) ? (ch.clock_v = (int64_t)v->valuedouble, true)
                                    : cJSON_IsString(v) && emu_parse_clock(v->valuestring, &ch.clock_v);
        if (!ok) {
            return rpc_fail(err, RPC_INVALID_PARAMS, "clock must be RFC 3339 (\"2026-10-12T09:00:00Z\") or Unix seconds");
        }
        ch.clock = true;
    }
    if ((v = cJSON_GetObjectItem(p, "wifi"))) {
        const cJSON *nets = cJSON_IsObject(v) ? cJSON_GetObjectItem(v, "networks") : NULL;
        if (!cJSON_IsArray(nets) || cJSON_GetArraySize(nets) > EMU_MAX_NETS) {
            return rpc_fail(err, RPC_INVALID_PARAMS, "wifi.networks must be an array of up to %d networks", EMU_MAX_NETS);
        }
        const cJSON *it;
        cJSON_ArrayForEach(it, nets)
        {
            emu_net_t *n = &ch.nets_v[ch.n_nets];
            const cJSON *ssid = cJSON_GetObjectItem(it, "ssid"), *pw = cJSON_GetObjectItem(it, "password");
            const cJSON *band = cJSON_GetObjectItem(it, "band"), *rssi = cJSON_GetObjectItem(it, "rssi");
            if (!cJSON_IsString(ssid) || !ssid->valuestring[0] || strlen(ssid->valuestring) > 32) {
                return rpc_fail(err, RPC_INVALID_PARAMS, "each network needs an ssid of 1-32 bytes");
            }
            size_t pl = cJSON_IsString(pw) ? strlen(pw->valuestring) : 0;
            if (pw && (!cJSON_IsString(pw) || (pl && (pl < 8 || pl > 64)))) {
                return rpc_fail(err, RPC_INVALID_PARAMS, "password must be empty (open) or 8-64 characters");
            }
            double bd = band ? (cJSON_IsNumber(band) ? band->valuedouble : -1) : 2.4;
            if (bd != 2.4 && bd != 5) {
                return rpc_fail(err, RPC_INVALID_PARAMS, "band must be 2.4 or 5");
            }
            double rs = rssi ? (cJSON_IsNumber(rssi) ? rssi->valuedouble : 1) : -55;
            if (rs < -100 || rs > 0) {
                return rpc_fail(err, RPC_INVALID_PARAMS, "rssi must be -100 to 0 dBm");
            }
            snprintf(n->ssid, sizeof n->ssid, "%s", ssid->valuestring);
            snprintf(n->password, sizeof n->password, "%s", pl ? pw->valuestring : "");
            n->band_x10 = bd == 5 ? 50 : 24;
            n->rssi = (int8_t)rs;
            ch.n_nets++;
        }
        ch.nets = true;
    }
    emu_state_update(apply_change, &ch);
    if (ch.battery || ch.source) {
        emu_state_t st;
        emu_state_get(&st);
        emu_fb_hdr_t *h = emu_fb_hdr();
        h->on_battery = st.on_battery;
        h->battery_mv = st.battery_mv;
        h->battery_pct = lcd349_battery_pct_from_mv(st.battery_mv);
        emu_fb_status_changed();
    }
    if (ch.nets && !emu_control_in_download()) {
        emu_wifi_env_changed();
    }
    *res = emu_state_json(!emu_control_in_download());
    return RPC_DONE;
}

/* ---- power ---- */

static rpc_res_t m_power(rpc_client_t *c, const cJSON *p, cJSON **res, rpc_err_t *err)
{
    const cJSON *a = cJSON_GetObjectItem(p, "action");
    if (!cJSON_IsString(a)) {
        return rpc_fail(err, RPC_INVALID_PARAMS, "action must be \"reset\", \"off\" or \"on\"");
    }
    *res = cJSON_CreateObject();
    if (!strcmp(a->valuestring, "on")) {
        cJSON_AddBoolToObject(*res, "already_on", true);
        return RPC_DONE;
    }
    if (!strcmp(a->valuestring, "reset")) {
        bool held = get_bool(p, "boot_held", false);
        cJSON_AddStringToObject(*res, "action", held ? "reset_boot_held" : "reset");
        cJSON_AddStringToObject(*res, "note", "the instance exits now; lcd349 run starts it again");
        emu_control_exit_after_reply(c, held ? LCD349_EXIT_RESET_BOOT_HELD : LCD349_EXIT_RESET);
        return RPC_DONE;
    }
    if (!strcmp(a->valuestring, "off")) {
        cJSON_AddStringToObject(*res, "action", "off");
        emu_control_exit_after_reply(c, LCD349_EXIT_OFF);
        return RPC_DONE;
    }
    cJSON_Delete(*res);
    *res = NULL;
    return rpc_fail(err, RPC_INVALID_PARAMS, "action must be \"reset\", \"off\" or \"on\"");
}

/* ---- screenshot ---- */

/* PNG of the screen as a person sees it, minus backlight dimming: black when
 * the display is dark. raw: the panel-native portrait framebuffer instead. */
static char *screenshot_png(bool raw, int scale, int *w, int *h, uint32_t *seq, bool *dark)
{
    uint8_t *panel = malloc(EMU_PANEL_BYTES);
    if (!panel) {
        return NULL;
    }
    *seq = emu_fb_read(panel);
    *dark = emu_display_dark();
    uint8_t *rgb = emu_panel_to_rgb(panel, raw, scale, w, h);
    free(panel);
    if (!rgb) {
        return NULL;
    }
    if (*dark) {
        memset(rgb, 0, (size_t)*w * (size_t)*h * 3);
    }
    size_t len;
    uint8_t *png = emu_png_encode(rgb, *w, *h, &len);
    free(rgb);
    char *b64 = png ? emu_base64(png, len) : NULL;
    free(png);
    return b64;
}

static bool write_png_file(const char *path)
{
    uint8_t *panel = malloc(EMU_PANEL_BYTES);
    int w, h;
    if (!panel) {
        return false;
    }
    emu_fb_read(panel);
    uint8_t *rgb = emu_panel_to_rgb(panel, false, 1, &w, &h);
    free(panel);
    size_t len = 0;
    uint8_t *png = rgb ? emu_png_encode(rgb, w, h, &len) : NULL;
    free(rgb);
    bool ok = png && emu_write_atomic(path, png, len);
    free(png);
    return ok;
}

static rpc_res_t m_screenshot(rpc_client_t *c, const cJSON *p, cJSON **res, rpc_err_t *err)
{
    (void)c;
    int scale;
    if (!get_int(p, "scale", 1, 1, 4, &scale, err)) {
        return RPC_FAIL;
    }
    bool raw = get_bool(p, "raw", false);
    const cJSON *fmt = cJSON_GetObjectItem(p, "format");
    bool rgb565 = cJSON_IsString(fmt) && !strcmp(fmt->valuestring, "rgb565be");
    if (fmt && !rgb565 && !(cJSON_IsString(fmt) && !strcmp(fmt->valuestring, "png"))) {
        return rpc_fail(err, RPC_INVALID_PARAMS, "format must be \"png\" or \"rgb565be\"");
    }
    cJSON *o = cJSON_CreateObject();
    uint32_t seq;
    bool dark;
    int w, h;
    char *b64;
    if (rgb565) {
        uint8_t *panel = malloc(EMU_PANEL_BYTES);
        seq = panel ? emu_fb_read(panel) : 0;
        dark = emu_display_dark();
        b64 = panel ? emu_base64(panel, EMU_PANEL_BYTES) : NULL;
        free(panel);
        w = LCD349_PANEL_W;
        h = LCD349_PANEL_H;
    } else {
        b64 = screenshot_png(raw, scale, &w, &h, &seq, &dark);
    }
    if (!b64) {
        cJSON_Delete(o);
        return rpc_fail(err, RPC_INTERNAL, "out of memory encoding the screenshot");
    }
    emu_state_t st;
    emu_state_get(&st);
    cJSON_AddStringToObject(o, "format", rgb565 ? "rgb565be" : "png");
    cJSON_AddNumberToObject(o, "width", w);
    cJSON_AddNumberToObject(o, "height", h);
    cJSON_AddNumberToObject(o, "frame_seq", seq);
    cJSON_AddNumberToObject(o, "backlight", emu_fb_hdr()->backlight);
    cJSON_AddStringToObject(o, "power", power_name(st.power));
    cJSON_AddBoolToObject(o, "dark", dark);
    cJSON_AddStringToObject(o, "data", b64);
    free(b64);
    *res = o;
    return RPC_DONE;
}

/* ---- ui.tree, ui.lint ---- */

static rpc_res_t m_ui_tree(rpc_client_t *c, const cJSON *p, cJSON **res, rpc_err_t *err)
{
    (void)c;
    int depth;
    if (!get_int(p, "depth", 32, 1, 64, &depth, err)) {
        return RPC_FAIL;
    }
    const cJSON *path = cJSON_GetObjectItem(p, "path");
    if (path && !cJSON_IsString(path)) {
        return rpc_fail(err, RPC_INVALID_PARAMS, "path must be a string such as \"screen/0:obj/2:button\"");
    }
    if (!lcd349_display()) {
        return rpc_fail(err, RPC_UNAVAILABLE, "no display: the product hasn't called lcd349_init(), or the display failed");
    }
    int count = 0;
    bool truncated = false;
    lcd349_ui_lock();
    cJSON *tree = emu_ui_tree(path ? path->valuestring : NULL, depth, &count, &truncated);
    lcd349_ui_unlock();
    if (!tree) {
        return rpc_fail(err, RPC_INVALID_PARAMS, "no object at path %s", path ? path->valuestring : "(screen)");
    }
    *res = cJSON_CreateObject();
    cJSON_AddItemToObject(*res, "tree", tree);
    cJSON_AddNumberToObject(*res, "count", count);
    cJSON_AddBoolToObject(*res, "truncated", truncated);
    return RPC_DONE;
}

static rpc_res_t m_ui_lint(rpc_client_t *c, const cJSON *p, cJSON **res, rpc_err_t *err)
{
    (void)c;
    (void)p;
    if (!lcd349_display()) {
        return rpc_fail(err, RPC_UNAVAILABLE, "no display: the product hasn't called lcd349_init(), or the display failed");
    }
    int errors = 0, warnings = 0;
    lcd349_ui_lock();
    cJSON *findings = emu_ui_lint(&errors, &warnings);
    lcd349_ui_unlock();
    *res = cJSON_CreateObject();
    cJSON_AddNumberToObject(*res, "errors", errors);
    cJSON_AddNumberToObject(*res, "warnings", warnings);
    cJSON_AddItemToObject(*res, "findings", findings);
    return RPC_DONE;
}

/* ---- input.press ---- */

typedef struct {
    op_kind_t kind;
    uint8_t mask; /* bit 0 BOOT, bit 1 PWR */
    uint32_t ms;
    uint64_t release_at, done_at;
    bool released, woke;
    bool hold_release; /* the "up" of a state hold */
} press_op_t;

static void press_set(uint8_t mask, bool down)
{
    if (mask & 1) {
        emu_button_set(LCD349_BTN_BOOT, down);
    }
    if (mask & 2) {
        emu_button_set(LCD349_BTN_PWR, down);
    }
}

static rpc_res_t press_poll(rpc_client_t *c, cJSON **res, rpc_err_t *err)
{
    (void)err;
    press_op_t *op = c->op;
    uint64_t t = now_us();
    if (!op->released) {
        if (t < op->release_at) {
            return RPC_PENDING;
        }
        press_set(op->mask, false);
        s_held_by_ops &= (uint8_t)~op->mask;
        op->released = true;
        /* Answer once the release has been debounced and delivered. */
        op->done_at = t + 2000u * LCD349_BUTTON_DEBOUNCE_MS + 5000u;
    }
    if (t < op->done_at) {
        return RPC_PENDING;
    }
    *res = cJSON_CreateObject();
    cJSON_AddStringToObject(*res, "button", op->mask == 3 ? "chord" : op->mask == 1 ? "boot" : "pwr");
    cJSON_AddNumberToObject(*res, "ms", op->ms);
    if (op->hold_release) {
        cJSON_AddStringToObject(*res, "state", "up");
    } else {
        cJSON_AddBoolToObject(*res, "woke", op->woke);
    }
    return RPC_DONE;
}

/* input.press {state: "down" | "up"}: a hold for as long as a person holds a
 * key or a drawn button in the viewer. "down" answers at once; "up" answers
 * once the release has been debounced, like a timed press. */
static uint64_t s_hold_since[2]; /* when each button's hold began: [0] BOOT, [1] PWR */

static rpc_res_t press_hold(rpc_client_t *c, uint8_t mask, bool down, cJSON **res, rpc_err_t *err)
{
    const char *name = mask == 3 ? "chord" : mask == 1 ? "boot" : "pwr";
    if (down) {
        emu_state_t st;
        emu_state_get(&st);
        if (st.power == EMU_POWER_OFF) {
            return rpc_fail(err, RPC_UNAVAILABLE, "the device is off");
        }
        if (s_held_by_ops & mask) {
            return rpc_fail(err, RPC_UNAVAILABLE, "that button is already held by another input.press");
        }
        s_held_by_ops |= mask;
        c->held |= mask;
        for (int i = 0; i < 2; i++) {
            if (mask & (1u << i)) {
                s_hold_since[i] = now_us();
            }
        }
        press_set(mask, true);
        *res = cJSON_CreateObject();
        cJSON_AddStringToObject(*res, "button", name);
        cJSON_AddStringToObject(*res, "state", "down");
        cJSON_AddBoolToObject(*res, "woke", st.power == EMU_POWER_LIGHT_SLEEP);
        return RPC_DONE;
    }
    if ((c->held & mask) != mask) {
        return rpc_fail(err, RPC_UNAVAILABLE, "this client isn't holding %s; hold it first with state \"down\"", name);
    }
    press_op_t *op = calloc(1, sizeof *op);
    if (!op) {
        return rpc_fail(err, RPC_INTERNAL, "out of memory");
    }
    op->kind = OP_PRESS;
    op->mask = mask;
    uint64_t since = (mask & 1) ? s_hold_since[0] : s_hold_since[1];
    if ((mask & 2) && s_hold_since[1] < since) {
        since = s_hold_since[1];
    }
    op->ms = (uint32_t)((now_us() - since) / 1000u);
    op->release_at = 0; /* release now */
    op->hold_release = true;
    c->held &= (uint8_t)~mask;
    c->op = op;
    c->poll = press_poll;
    return RPC_PENDING;
}

void emu_methods_client_closed(rpc_client_t *c)
{
    if (c->held) {
        press_set(c->held, false);
        s_held_by_ops &= (uint8_t)~c->held;
        c->held = 0;
    }
}

static rpc_res_t m_input_press(rpc_client_t *c, const cJSON *p, cJSON **res, rpc_err_t *err)
{
    int ms;
    const cJSON *state = cJSON_GetObjectItem(p, "state");
    if (state && (!cJSON_IsString(state) || (strcmp(state->valuestring, "down") && strcmp(state->valuestring, "up")))) {
        return rpc_fail(err, RPC_INVALID_PARAMS, "state is \"down\" or \"up\"");
    }
    if (state && cJSON_GetObjectItem(p, "ms")) {
        return rpc_fail(err, RPC_INVALID_PARAMS, "give ms for a timed press, or state for a hold, not both");
    }
    if (!get_int(p, "ms", 100, 20, 60000, &ms, err)) {
        return RPC_FAIL;
    }
    uint8_t mask = 0;
    const cJSON *b = cJSON_GetObjectItem(p, "button");
    if (get_bool(p, "chord", false)) {
        mask = 3;
    } else if (cJSON_IsString(b) && !strcmp(b->valuestring, "boot")) {
        mask = 1;
    } else if (cJSON_IsString(b) && !strcmp(b->valuestring, "pwr")) {
        mask = 2;
    } else {
        return rpc_fail(err, RPC_INVALID_PARAMS, "give button \"boot\" or \"pwr\", or chord true; then ms (the hold time, 20-60000) or state (\"down\" or \"up\")");
    }
    if (state) {
        return press_hold(c, mask, !strcmp(state->valuestring, "down"), res, err);
    }
    emu_state_t st;
    emu_state_get(&st);
    if (st.power == EMU_POWER_OFF) {
        return rpc_fail(err, RPC_UNAVAILABLE, "the device is off");
    }
    if (s_held_by_ops & mask) {
        return rpc_fail(err, RPC_UNAVAILABLE, "that button is already held by another input.press");
    }
    press_op_t *op = calloc(1, sizeof *op);
    if (!op) {
        return rpc_fail(err, RPC_INTERNAL, "out of memory");
    }
    op->kind = OP_PRESS;
    op->mask = mask;
    op->ms = (uint32_t)ms;
    op->woke = st.power == EMU_POWER_LIGHT_SLEEP;
    op->release_at = now_us() + (uint64_t)ms * 1000u;
    s_held_by_ops |= mask;
    press_set(mask, true);
    c->op = op;
    c->poll = press_poll;
    return RPC_PENDING;
}

/* ---- input.touch ---- */

#define TOUCH_MAX_SAMPLES 400

typedef struct {
    op_kind_t kind;
    uint64_t end_pos;
    uint64_t deadline;
    int n;
} touch_op_t;

static rpc_res_t touch_result(cJSON **res, bool delivered, int n, const char *reason)
{
    *res = cJSON_CreateObject();
    cJSON_AddBoolToObject(*res, "delivered", delivered);
    cJSON_AddNumberToObject(*res, "samples", n);
    if (reason) {
        cJSON_AddStringToObject(*res, "reason", reason);
    }
    return RPC_DONE;
}

static const char *DARK_REASON = "the display is dark (asleep, off or backlight 0): touch is never a wake source; press a button";

static rpc_res_t touch_poll(rpc_client_t *c, cJSON **res, rpc_err_t *err)
{
    touch_op_t *op = c->op;
    if (emu_touch_consumed() >= op->end_pos) {
        return touch_result(res, true, op->n, NULL);
    }
    if (emu_display_dark()) {
        emu_touch_flush();
        return touch_result(res, false, op->n, DARK_REASON);
    }
    if (now_us() > op->deadline) {
        emu_touch_flush();
        return rpc_fail(err, RPC_INTERNAL, "touch samples weren't read in time: is the UI task blocked?");
    }
    return RPC_PENDING;
}

static int line_samples(emu_touch_t *s, int max, uint16_t x0, uint16_t y0, uint16_t x1, uint16_t y1, int steps)
{
    int n = 0;
    for (int i = 0; i < steps && n < max; i++) {
        s[n++] = (emu_touch_t){(uint16_t)(x0 + (x1 - x0) * i / (steps - 1)), (uint16_t)(y0 + (y1 - y0) * i / (steps - 1)), true};
    }
    return n;
}

static rpc_res_t m_input_touch(rpc_client_t *c, const cJSON *p, cJSON **res, rpc_err_t *err)
{
    static emu_touch_t s[TOUCH_MAX_SAMPLES];
    int n = 0;
    int period = LCD349_TOUCH_PERIOD_MS;
    uint16_t x, y, x1, y1;
    const cJSON *v;
    bool live = get_bool(p, "live", false);
    if ((v = cJSON_GetObjectItem(p, "tap"))) {
        if (!get_point(v, &x, &y)) {
            return rpc_fail(err, RPC_INVALID_PARAMS, "tap must be [x, y] inside 640x172");
        }
        for (int i = 0; i < 3; i++) {
            s[n++] = (emu_touch_t){x, y, true};
        }
        s[n++] = (emu_touch_t){x, y, false};
    } else if ((v = cJSON_GetObjectItem(p, "long_press"))) {
        int ms;
        if (!get_point(v, &x, &y)) {
            return rpc_fail(err, RPC_INVALID_PARAMS, "long_press must be [x, y] inside 640x172");
        }
        if (!get_int(p, "ms", 800, 100, 10000, &ms, err)) {
            return RPC_FAIL;
        }
        for (int i = 0; i < (ms + period - 1) / period && n < TOUCH_MAX_SAMPLES - 1; i++) {
            s[n++] = (emu_touch_t){x, y, true};
        }
        s[n++] = (emu_touch_t){x, y, false};
    } else if ((v = cJSON_GetObjectItem(p, "swipe"))) {
        int ms;
        if (!get_point(cJSON_GetObjectItem(v, "from"), &x, &y) || !get_point(cJSON_GetObjectItem(v, "to"), &x1, &y1)) {
            return rpc_fail(err, RPC_INVALID_PARAMS, "swipe must be {\"from\": [x, y], \"to\": [x, y]} inside 640x172");
        }
        if (!get_int(p, "ms", 300, 60, 5000, &ms, err)) {
            return RPC_FAIL;
        }
        int steps = ms / period < 2 ? 2 : ms / period;
        n = line_samples(s, TOUCH_MAX_SAMPLES - 1, x, y, x1, y1, steps);
        s[n++] = (emu_touch_t){x1, y1, false};
    } else if ((v = cJSON_GetObjectItem(p, "points"))) {
        const cJSON *it;
        if (!cJSON_IsArray(v) || cJSON_GetArraySize(v) == 0 || cJSON_GetArraySize(v) > TOUCH_MAX_SAMPLES) {
            return rpc_fail(err, RPC_INVALID_PARAMS, "points must be 1-%d items of {\"x\", \"y\", \"down\"}", TOUCH_MAX_SAMPLES);
        }
        cJSON_ArrayForEach(it, v)
        {
            const cJSON *px = cJSON_GetObjectItem(it, "x"), *py = cJSON_GetObjectItem(it, "y");
            if (!cJSON_IsNumber(px) || !cJSON_IsNumber(py) || px->valuedouble < 0 || px->valuedouble >= LCD349_LCD_W ||
                py->valuedouble < 0 || py->valuedouble >= LCD349_LCD_H) {
                return rpc_fail(err, RPC_INVALID_PARAMS, "each point needs x 0-639 and y 0-171");
            }
            s[n++] = (emu_touch_t){(uint16_t)px->valuedouble, (uint16_t)py->valuedouble, cJSON_IsTrue(cJSON_GetObjectItem(it, "down"))};
        }
    } else {
        return rpc_fail(err, RPC_INVALID_PARAMS, "give one of tap [x, y], long_press [x, y] (+ ms), swipe {from, to} (+ ms), or points [...]");
    }
    if (emu_display_dark()) {
        return touch_result(res, false, n, DARK_REASON);
    }
    touch_op_t *op = calloc(1, sizeof *op);
    if (!op) {
        return rpc_fail(err, RPC_INTERNAL, "out of memory");
    }
    if (!emu_touch_push(s, (size_t)n, live, &op->end_pos)) {
        free(op);
        return rpc_fail(err, RPC_UNAVAILABLE, "the touch queue is full; wait for earlier input to finish");
    }
    op->kind = OP_TOUCH;
    op->n = n;
    op->deadline = now_us() + (uint64_t)(n + 20) * (uint64_t)period * 2000u + 2000000u;
    c->op = op;
    c->poll = touch_poll;
    return RPC_PENDING;
}

/* ---- wait ---- */

typedef struct {
    op_kind_t kind;
    uint64_t start, deadline, next_text;
    bool has_re, has_text, has_idle;
    regex_t re;
    uint64_t cursor; /* next serial line to examine */
    char text[256];
    uint32_t idle_ms;
} wait_op_t;

static cJSON *wait_result(wait_op_t *op, bool matched, const char *by)
{
    cJSON *o = cJSON_CreateObject();
    cJSON_AddBoolToObject(o, "matched", matched);
    if (by) {
        cJSON_AddStringToObject(o, "by", by);
    }
    cJSON_AddNumberToObject(o, "elapsed_ms", (double)((now_us() - op->start) / 1000u));
    cJSON_AddNumberToObject(o, "serial_next", (double)emu_serial_next());
    return o;
}

static rpc_res_t wait_poll(rpc_client_t *c, cJSON **res, rpc_err_t *err)
{
    (void)err;
    wait_op_t *op = c->op;
    char line[512];
    if (op->has_re) {
        uint64_t end = emu_serial_next();
        for (; op->cursor < end; op->cursor++) {
            if (emu_serial_line(op->cursor, line, sizeof line) && regexec(&op->re, line, 0, NULL, 0) == 0) {
                *res = wait_result(op, true, "log");
                cJSON_AddStringToObject(*res, "line", line);
                cJSON_AddNumberToObject(*res, "line_number", (double)op->cursor);
                return RPC_DONE;
            }
        }
    }
    uint64_t t = now_us();
    if (op->has_text && t >= op->next_text) {
        op->next_text = t + TEXT_CHECK_MS * 1000u;
        char found[256];
        lcd349_ui_lock();
        bool hit = !emu_display_dark() && emu_ui_find_text(op->text, found, sizeof found);
        lcd349_ui_unlock();
        if (hit) {
            *res = wait_result(op, true, "text");
            cJSON_AddStringToObject(*res, "text", found);
            return RPC_DONE;
        }
    }
    if (op->has_idle && emu_fb_idle_us() >= (uint64_t)op->idle_ms * 1000u) {
        *res = wait_result(op, true, "idle");
        return RPC_DONE;
    }
    if (t < op->deadline) {
        return RPC_PENDING;
    }
    *res = wait_result(op, false, NULL);
    cJSON *tail = cJSON_AddArrayToObject(*res, "tail");
    uint64_t end = emu_serial_next();
    for (uint64_t n = end > 20 ? end - 20 : 1; n < end; n++) {
        if (emu_serial_line(n, line, sizeof line)) {
            cJSON_AddItemToArray(tail, cJSON_CreateString(line));
        }
    }
    if (emu_instance_dir()) {
        char path[512];
        snprintf(path, sizeof path, "%s/wait-timeout.png", emu_instance_dir());
        if (write_png_file(path)) {
            cJSON_AddStringToObject(*res, "screenshot", path);
        }
    }
    return RPC_DONE;
}

static rpc_res_t m_wait(rpc_client_t *c, const cJSON *p, cJSON **res, rpc_err_t *err)
{
    (void)res;
    int timeout, idle;
    if (!get_int(p, "timeout_ms", WAIT_DEFAULT_MS, 0, WAIT_MAX_MS, &timeout, err) || !get_int(p, "idle_ms", 0, 0, WAIT_MAX_MS, &idle, err)) {
        return RPC_FAIL;
    }
    const cJSON *text = cJSON_GetObjectItem(p, "text"), *log = cJSON_GetObjectItem(p, "log"), *since = cJSON_GetObjectItem(p, "since");
    if ((text && (!cJSON_IsString(text) || !text->valuestring[0] || strlen(text->valuestring) > 255)) || (log && !cJSON_IsString(log)) ||
        (since && !cJSON_IsNumber(since))) {
        return rpc_fail(err, RPC_INVALID_PARAMS, "text (1-255 bytes) and log (a regular expression) are strings; since is a serial line number");
    }
    if (!text && !log && !idle) {
        return rpc_fail(err, RPC_INVALID_PARAMS, "give at least one of text, log or idle_ms");
    }
    wait_op_t *op = calloc(1, sizeof *op);
    if (!op) {
        return rpc_fail(err, RPC_INTERNAL, "out of memory");
    }
    op->kind = OP_WAIT;
    if (log) {
        int rc = regcomp(&op->re, log->valuestring, REG_EXTENDED | REG_NOSUB);
        if (rc != 0) {
            char msg[128];
            regerror(rc, &op->re, msg, sizeof msg);
            free(op);
            return rpc_fail(err, RPC_INVALID_PARAMS, "log isn't a valid extended regular expression: %s", msg);
        }
        op->has_re = true;
        /* By default also look at lines logged since this client's previous
         * request began, so "press, then wait for its log line" can't miss it. */
        op->cursor = since ? (uint64_t)since->valuedouble : c->serial_prev;
    }
    if (text) {
        snprintf(op->text, sizeof op->text, "%s", text->valuestring);
        op->has_text = true;
    }
    op->has_idle = idle > 0;
    op->idle_ms = (uint32_t)idle;
    op->start = now_us();
    op->deadline = op->start + (uint64_t)timeout * 1000u;
    c->op = op;
    c->poll = wait_poll;
    return RPC_PENDING;
}

/* ---- audio.events ---- */

static rpc_res_t m_audio_events(rpc_client_t *c, const cJSON *p, cJSON **res, rpc_err_t *err)
{
    (void)c;
    const cJSON *since = cJSON_GetObjectItem(p, "since");
    if (since && (!cJSON_IsNumber(since) || since->valuedouble < 0)) {
        return rpc_fail(err, RPC_INVALID_PARAMS, "since must be an event sequence number");
    }
    static emu_tone_event_t ev[256];
    uint64_t next;
    size_t n = emu_audio_events(since ? (uint64_t)since->valuedouble : 0, ev, 256, &next);
    *res = cJSON_CreateObject();
    cJSON *arr = cJSON_AddArrayToObject(*res, "events");
    for (size_t i = 0; i < n; i++) {
        cJSON *e = cJSON_CreateObject();
        cJSON_AddNumberToObject(e, "seq", (double)ev[i].seq);
        cJSON_AddNumberToObject(e, "t_ms", ev[i].t_ms);
        if (ev[i].stop) {
            cJSON_AddBoolToObject(e, "stop", true);
        } else {
            cJSON_AddNumberToObject(e, "hz", ev[i].hz);
            cJSON_AddNumberToObject(e, "ms", ev[i].ms);
            cJSON_AddNumberToObject(e, "volume", ev[i].volume);
        }
        cJSON_AddItemToArray(arr, e);
    }
    cJSON_AddNumberToObject(*res, "next", (double)next);
    return RPC_DONE;
}

/* ---- record.start, record.stop (cut-list item 4) ---- */

static struct {
    bool on;
    char dir[480];
    int frames;
    uint32_t last_seq;
    uint64_t start, until;
} s_rec;

static rpc_res_t m_record_start(rpc_client_t *c, const cJSON *p, cJSON **res, rpc_err_t *err)
{
    (void)c;
    int max_s;
    if (!get_int(p, "max_s", 10, 1, RECORD_MAX_S, &max_s, err)) {
        return RPC_FAIL;
    }
    if (!emu_instance_dir()) {
        return rpc_fail(err, RPC_UNAVAILABLE, "recording needs an instance directory");
    }
    if (s_rec.on) {
        return rpc_fail(err, RPC_UNAVAILABLE, "already recording to %s", s_rec.dir);
    }
    snprintf(s_rec.dir, sizeof s_rec.dir, "%s/record-%lld", emu_instance_dir(), (long long)time(NULL));
    if (mkdir(s_rec.dir, 0700) != 0) {
        return rpc_fail(err, RPC_INTERNAL, "can't create %s", s_rec.dir);
    }
    s_rec.on = true;
    s_rec.frames = 0;
    s_rec.last_seq = UINT32_MAX;
    s_rec.start = now_us();
    s_rec.until = s_rec.start + (uint64_t)max_s * 1000000u;
    *res = cJSON_CreateObject();
    cJSON_AddStringToObject(*res, "dir", s_rec.dir);
    cJSON_AddNumberToObject(*res, "max_s", max_s);
    return RPC_DONE;
}

static rpc_res_t m_record_stop(rpc_client_t *c, const cJSON *p, cJSON **res, rpc_err_t *err)
{
    (void)c;
    (void)p;
    (void)err;
    *res = cJSON_CreateObject();
    cJSON_AddStringToObject(*res, "dir", s_rec.dir);
    cJSON_AddNumberToObject(*res, "frames", s_rec.frames);
    cJSON_AddNumberToObject(*res, "duration_ms", s_rec.start ? (double)((now_us() - s_rec.start) / 1000u) : 0);
    cJSON_AddBoolToObject(*res, "was_recording", s_rec.on);
    s_rec.on = false;
    return RPC_DONE;
}

static void record_tick(void)
{
    if (!s_rec.on) {
        return;
    }
    uint32_t seq = __atomic_load_n(&emu_fb_hdr()->seq, __ATOMIC_ACQUIRE);
    if (now_us() > s_rec.until || s_rec.frames >= RECORD_MAX_FRAMES) {
        s_rec.on = false;
        return;
    }
    if (seq & 1 || seq == s_rec.last_seq) {
        return;
    }
    s_rec.last_seq = seq;
    char path[560];
    snprintf(path, sizeof path, "%s/frame-%05d-%08llu.png", s_rec.dir, s_rec.frames, (unsigned long long)((now_us() - s_rec.start) / 1000u));
    if (write_png_file(path)) {
        s_rec.frames++;
    }
}

/* ---- Housekeeping ---- */

void emu_methods_tick(void)
{
    record_tick();
    if (emu_control_in_download()) {
        return;
    }
    emu_fb_hdr_t *h = emu_fb_hdr();
    uint8_t asleep = lcd349_display_is_asleep();
    if (h->display_asleep != asleep) {
        h->display_asleep = asleep;
        emu_fb_status_changed();
    }
}

void emu_methods_cancel(rpc_client_t *c)
{
    if (!c->op) {
        return;
    }
    switch (*(op_kind_t *)c->op) {
    case OP_PRESS: {
        press_op_t *op = c->op;
        if (!op->released) {
            press_set(op->mask, false);
            s_held_by_ops &= (uint8_t)~op->mask;
        }
        break;
    }
    case OP_WAIT: {
        wait_op_t *op = c->op;
        if (op->has_re) {
            regfree(&op->re);
        }
        break;
    }
    case OP_TOUCH:
        break;
    }
}

const rpc_method_t emu_rpc_methods[] = {
    {"ping", m_ping, true},
    {"info", m_info, true},
    {"state.get", m_state_get, true},
    {"state.set", m_state_set, true},
    {"power", m_power, true},
    {"screenshot", m_screenshot, true},
    {"ui.tree", m_ui_tree, false},
    {"ui.lint", m_ui_lint, false},
    {"input.press", m_input_press, false},
    {"input.touch", m_input_touch, false},
    {"wait", m_wait, false},
    {"audio.events", m_audio_events, false},
    {"record.start", m_record_start, true},
    {"record.stop", m_record_stop, true},
    {NULL, NULL, false},
};
