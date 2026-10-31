/* SPDX-License-Identifier: Apache-2.0
 *
 * The emulated device's state: configuration from LCD349_* environment
 * variables (an ESP-IDF linux app gets no argv), and the state that survives
 * a reset, persisted in <instance>/devstate.json.
 */
#include <errno.h>
#include <fcntl.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/stat.h>
#include <time.h>
#include <unistd.h>

#include "cJSON.h"
#include "freertos/FreeRTOS.h"
#include "freertos/semphr.h"
#include "freertos/task.h"
#include "defaults.h"
#include "emu.h"

static portMUX_TYPE s_mux = portMUX_INITIALIZER_UNLOCKED;
static SemaphoreHandle_t s_save_mu; /* orders devstate.json writes between tasks */
static emu_state_t s_state;
static char s_dir[256];
static bool s_have_dir;
static uint32_t s_flush_ms = LCD349_FLUSH_MS;
static bool s_boot_held;
static lcd349_boot_reason_t s_boot_reason = LCD349_BOOT_POWER_ON;
static bool s_inited;

static bool rtos(void)
{
    return xTaskGetSchedulerState() != taskSCHEDULER_NOT_STARTED;
}

void emu_crit_enter(void)
{
    if (rtos()) {
        portENTER_CRITICAL(&s_mux);
    }
}

void emu_crit_exit(void)
{
    if (rtos()) {
        portEXIT_CRITICAL(&s_mux);
    }
}

void emu_devmodel_start_rtos(void)
{
    if (!s_save_mu) {
        s_save_mu = xSemaphoreCreateMutex();
    }
}

const char *emu_instance_dir(void)
{
    return s_have_dir ? s_dir : NULL;
}

uint32_t emu_flush_ms(void)
{
    return s_flush_ms;
}

bool emu_boot_held(void)
{
    return s_boot_held;
}

lcd349_boot_reason_t emu_boot_reason_env(void)
{
    return s_boot_reason;
}

bool emu_write_atomic(const char *path, const void *data, size_t len)
{
    char tmp[512];
    snprintf(tmp, sizeof tmp, "%s.tmp.%d", path, (int)getpid());
    int fd = open(tmp, O_WRONLY | O_CREAT | O_TRUNC, 0600);
    if (fd < 0) {
        return false;
    }
    const uint8_t *p = data;
    size_t left = len;
    while (left) {
        ssize_t n = write(fd, p, left);
        if (n < 0 && errno == EINTR) {
            continue;
        }
        if (n <= 0) {
            close(fd);
            unlink(tmp);
            return false;
        }
        p += n;
        left -= (size_t)n;
    }
    fsync(fd);
    close(fd);
    if (rename(tmp, path) != 0) {
        unlink(tmp);
        return false;
    }
    return true;
}

/* "2026-10-12T09:00:00Z", "...+01:00", or plain Unix seconds. */
bool emu_parse_clock(const char *s, int64_t *out)
{
    char *end;
    long long v = strtoll(s, &end, 10);
    if (*s && *end == '\0') {
        *out = v;
        return true;
    }
    struct tm tm = {0};
    char tz[8] = {0};
    if (sscanf(s, "%d-%d-%dT%d:%d:%d%7s", &tm.tm_year, &tm.tm_mon, &tm.tm_mday, &tm.tm_hour, &tm.tm_min, &tm.tm_sec, tz) < 6) {
        return false;
    }
    tm.tm_year -= 1900;
    tm.tm_mon -= 1;
    int64_t t = (int64_t)timegm(&tm);
    if (tz[0] == '+' || tz[0] == '-') {
        int hh = 0, mm = 0;
        if (sscanf(tz + 1, "%d:%d", &hh, &mm) < 1) {
            return false;
        }
        int64_t off = hh * 3600 + mm * 60;
        t -= tz[0] == '+' ? off : -off;
    } else if (tz[0] && strcmp(tz, "Z") != 0) {
        return false;
    }
    *out = t;
    return true;
}

static void path_in_dir(char *out, size_t n, const char *name)
{
    snprintf(out, n, "%s/%s", s_dir, name);
}

static void load_state(void)
{
    char path[512];
    path_in_dir(path, sizeof path, "devstate.json");
    FILE *f = fopen(path, "rb");
    if (!f) {
        return;
    }
    char buf[8192];
    size_t n = fread(buf, 1, sizeof buf - 1, f);
    fclose(f);
    buf[n] = '\0';
    cJSON *j = cJSON_Parse(buf);
    if (!j) {
        fprintf(stderr, "lcd349_emu: ignoring unreadable devstate.json\n");
        return;
    }
    cJSON *v;
    if ((v = cJSON_GetObjectItem(j, "battery_mv")) && cJSON_IsNumber(v)) {
        s_state.battery_mv = (uint16_t)v->valuedouble;
    }
    if ((v = cJSON_GetObjectItem(j, "source")) && cJSON_IsString(v)) {
        s_state.on_battery = strcmp(v->valuestring, "battery") == 0;
    }
    if ((v = cJSON_GetObjectItem(j, "board_rev")) && cJSON_IsString(v) && (!strcmp(v->valuestring, "V1") || !strcmp(v->valuestring, "V2"))) {
        strcpy(s_state.board_rev, v->valuestring);
    }
    if ((v = cJSON_GetObjectItem(j, "rtc_offset_s")) && cJSON_IsNumber(v)) {
        s_state.rtc_offset_s = (int64_t)v->valuedouble;
    }
    cJSON *nets = cJSON_GetObjectItem(j, "wifi");
    cJSON *it;
    s_state.n_nets = 0;
    cJSON_ArrayForEach(it, nets)
    {
        if (s_state.n_nets == EMU_MAX_NETS) {
            break;
        }
        emu_net_t *n2 = &s_state.nets[s_state.n_nets];
        memset(n2, 0, sizeof *n2);
        cJSON *ssid = cJSON_GetObjectItem(it, "ssid"), *pw = cJSON_GetObjectItem(it, "password");
        cJSON *band = cJSON_GetObjectItem(it, "band"), *rssi = cJSON_GetObjectItem(it, "rssi");
        if (!cJSON_IsString(ssid)) {
            continue;
        }
        snprintf(n2->ssid, sizeof n2->ssid, "%s", ssid->valuestring);
        if (cJSON_IsString(pw)) {
            snprintf(n2->password, sizeof n2->password, "%s", pw->valuestring);
        }
        n2->band_x10 = cJSON_IsNumber(band) ? (uint8_t)(band->valuedouble * 10 + 0.5) : 24;
        n2->rssi = cJSON_IsNumber(rssi) ? (int8_t)rssi->valuedouble : -55;
        s_state.n_nets++;
    }
    cJSON_Delete(j);
}

static void save_state(const emu_state_t *st)
{
    if (!s_have_dir) {
        return;
    }
    cJSON *j = cJSON_CreateObject();
    cJSON_AddNumberToObject(j, "battery_mv", st->battery_mv);
    cJSON_AddStringToObject(j, "source", st->on_battery ? "battery" : "usb");
    cJSON_AddStringToObject(j, "board_rev", st->board_rev);
    cJSON_AddNumberToObject(j, "rtc_offset_s", (double)st->rtc_offset_s);
    cJSON *nets = cJSON_AddArrayToObject(j, "wifi");
    for (size_t i = 0; i < st->n_nets; i++) {
        cJSON *n = cJSON_CreateObject();
        cJSON_AddStringToObject(n, "ssid", st->nets[i].ssid);
        cJSON_AddStringToObject(n, "password", st->nets[i].password);
        cJSON_AddNumberToObject(n, "band", st->nets[i].band_x10 / 10.0);
        cJSON_AddNumberToObject(n, "rssi", st->nets[i].rssi);
        cJSON_AddItemToArray(nets, n);
    }
    char *txt = cJSON_Print(j);
    cJSON_Delete(j);
    if (txt) {
        char path[512];
        path_in_dir(path, sizeof path, "devstate.json");
        if (!emu_write_atomic(path, txt, strlen(txt))) {
            fprintf(stderr, "lcd349_emu: can't write %s\n", path);
        }
        free(txt);
    }
}

void emu_devmodel_init(void)
{
    if (s_inited) {
        return;
    }
    s_inited = true;
    s_state.battery_mv = LCD349_EMU_BATTERY_MV;
    strcpy(s_state.board_rev, "V1");
    s_state.power = EMU_POWER_ON;

    const char *dir = getenv("LCD349_INSTANCE_DIR");
    if (dir && *dir && strlen(dir) < sizeof s_dir) {
        snprintf(s_dir, sizeof s_dir, "%s", dir);
        if (mkdir(s_dir, 0700) != 0 && errno != EEXIST) {
            fprintf(stderr, "lcd349_emu: can't create %s: %s\n", s_dir, strerror(errno));
        } else {
            s_have_dir = true;
            load_state();
        }
    }
    /* First-boot settings from lcd349 run (it passes them only on the first
     * start, so a reset keeps what the instance has since been set to). */
    const char *mv = getenv("LCD349_BATTERY_MV");
    const char *src = getenv("LCD349_SOURCE");
    if ((mv && *mv) || (src && *src)) {
        if (mv && *mv) {
            unsigned long v = strtoul(mv, NULL, 10);
            if (v >= EMU_BATTERY_MV_MIN && v <= EMU_BATTERY_MV_MAX) {
                s_state.battery_mv = (uint16_t)v;
            } else {
                fprintf(stderr, "lcd349_emu: LCD349_BATTERY_MV=%s is outside %d-%d; ignored\n", mv, EMU_BATTERY_MV_MIN, EMU_BATTERY_MV_MAX);
            }
        }
        if (src && (!strcmp(src, "usb") || !strcmp(src, "battery"))) {
            s_state.on_battery = !strcmp(src, "battery");
        } else if (src && *src) {
            fprintf(stderr, "lcd349_emu: LCD349_SOURCE=%s isn't usb or battery; ignored\n", src);
        }
        if (s_have_dir) {
            save_state(&s_state);
        }
    }
    const char *f = getenv("LCD349_FLUSH_MS");
    if (f && *f) {
        s_flush_ms = (uint32_t)strtoul(f, NULL, 10);
    }
    const char *bh = getenv("LCD349_BOOT_HELD");
    s_boot_held = bh && strcmp(bh, "1") == 0;
    const char *br = getenv("LCD349_BOOT_REASON");
    if (br) {
        if (!strcmp(br, "software")) {
            s_boot_reason = LCD349_BOOT_SOFTWARE;
        } else if (!strcmp(br, "crash")) {
            s_boot_reason = LCD349_BOOT_CRASH;
        }
    }
    const char *clk = getenv("LCD349_CLOCK");
    int64_t t;
    if (clk && *clk) {
        if (emu_parse_clock(clk, &t)) {
            s_state.rtc_offset_s = t - (int64_t)time(NULL);
        } else {
            fprintf(stderr, "lcd349_emu: LCD349_CLOCK=%s isn't RFC 3339 or Unix seconds; using the host clock\n", clk);
        }
    }
}

void emu_state_get(emu_state_t *out)
{
    emu_crit_enter();
    *out = s_state;
    emu_crit_exit();
}

void emu_state_update(void (*fn)(emu_state_t *s, void *ctx), void *ctx)
{
    bool ordered = rtos() && s_save_mu;
    if (ordered) {
        xSemaphoreTake(s_save_mu, portMAX_DELAY);
    }
    emu_crit_enter();
    fn(&s_state, ctx);
    emu_state_t snap = s_state;
    emu_crit_exit();
    save_state(&snap); /* JSON and file I/O outside the critical section */
    if (ordered) {
        xSemaphoreGive(s_save_mu);
    }
}

void emu_set_power(emu_power_t p)
{
    emu_crit_enter();
    s_state.power = p;
    emu_crit_exit();
    emu_fb_hdr()->power = (uint8_t)p;
    emu_fb_status_changed();
}
