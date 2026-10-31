/* SPDX-License-Identifier: Apache-2.0
 *
 * The board test harness (lcd349_harness.h): a command loop on the USB
 * Serial/JTAG console that runs the registered Unity tests by tag, and
 * multi-stage tests one stage per boot.
 */
#include <stdarg.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

#include "cJSON.h"
#include "driver/usb_serial_jtag.h"
#include "driver/usb_serial_jtag_vfs.h"
#include "esp_heap_caps.h"
#include "freertos/FreeRTOS.h"
#include "freertos/task.h"
#include "lcd349.h"
#include "lcd349_harness.h"
#include "unity.h"
#include "unity_test_runner.h"

#define MAX_TESTS 256
#define LINE_MAX_LEN 320
#define ASK_EVERY_MS 2000

/* ---- The registered tests ---- */

static test_desc_t *s_tests[MAX_TESTS];
static int s_ntests;

void __real_unity_testcase_register(test_desc_t *desc);

/* Every TEST_CASE registers itself here (the component links with
 * --wrap=unity_testcase_register) before app_main runs. */
void __wrap_unity_testcase_register(test_desc_t *desc)
{
    if (s_ntests < MAX_TESTS) {
        s_tests[s_ntests++] = desc;
    }
    __real_unity_testcase_register(desc);
}

/* File, then line: the same order on every boot of the same build, so an
 * index sent back after a restart names the same test. */
static int by_place(const void *a, const void *b)
{
    const test_desc_t *x = *(test_desc_t *const *)a, *y = *(test_desc_t *const *)b;
    int c = strcmp(x->file, y->file);
    return c ? c : x->line - y->line;
}

static bool has_tag(const test_desc_t *t, const char *tag)
{
    if (strcmp(tag, "*") == 0) {
        return strstr(t->desc, "[ignore]") == NULL;
    }
    char want[40];
    snprintf(want, sizeof want, "[%s]", tag);
    return strstr(t->desc, want) != NULL;
}

/* ---- Lines in and out ---- */

static void say(const char *fmt, ...) __attribute__((format(printf, 1, 2)));

static void say(const char *fmt, ...)
{
    va_list ap;
    va_start(ap, fmt);
    vprintf(fmt, ap);
    va_end(ap);
    fputc('\n', stdout);
    fflush(stdout);
}

static void say_json(const char *key, cJSON *val)
{
    cJSON *o = cJSON_CreateObject();
    cJSON_AddItemToObject(o, key, val);
    char *s = cJSON_PrintUnformatted(o);
    say("@R %s", s ? s : "{}");
    cJSON_free(s);
    cJSON_Delete(o);
}

/* One line from the host, without its line ending; false after timeout_ms. */
static bool read_line(char *buf, size_t size, uint32_t timeout_ms)
{
    size_t n = 0;
    TickType_t end = xTaskGetTickCount() + pdMS_TO_TICKS(timeout_ms);
    for (;;) {
        TickType_t left = end - xTaskGetTickCount();
        if ((int32_t)left <= 0) {
            return false;
        }
        uint8_t c;
        if (usb_serial_jtag_read_bytes(&c, 1, left) != 1) {
            continue;
        }
        if (c == '\r' || c == '\n') {
            if (n) {
                buf[n] = 0;
                return true;
            }
        } else if (n < size - 1) {
            buf[n++] = (char)c;
        }
    }
}

bool lcd349_harness_request(const char *what, char *out, size_t size, uint32_t timeout_ms)
{
    char word[32];
    size_t wl = strcspn(what, " ");
    if (wl == 0 || wl >= sizeof word || size == 0) {
        return false;
    }
    memcpy(word, what, wl);
    word[wl] = 0;
    say("@Q %s", what);
    char line[LINE_MAX_LEN];
    TickType_t end = xTaskGetTickCount() + pdMS_TO_TICKS(timeout_ms);
    for (;;) {
        TickType_t left = end - xTaskGetTickCount();
        if ((int32_t)left <= 0 || !read_line(line, sizeof line, pdTICKS_TO_MS(left))) {
            return false;
        }
        if (strncmp(line, "@P ", 3) == 0 && strncmp(line + 3, word, wl) == 0 && (line[3 + wl] == 0 || line[3 + wl] == ' ')) {
            const char *rest = line[3 + wl] ? line + 4 + wl : "";
            snprintf(out, size, "%s", rest);
            memset(line, 0, sizeof line);
            return true;
        }
    }
}

bool lcd349_harness_wifi(int n, char *ssid, size_t ssid_size, char *password, size_t password_size)
{
    char what[16], reply[LINE_MAX_LEN];
    snprintf(what, sizeof what, n > 1 ? "wifi %d" : "wifi", n);
    if (!lcd349_harness_request(what, reply, sizeof reply, 5000)) {
        return false;
    }
    cJSON *j = cJSON_Parse(reply);
    memset(reply, 0, sizeof reply);
    const cJSON *s = cJSON_GetObjectItemCaseSensitive(j, "ssid");
    const cJSON *p = cJSON_GetObjectItemCaseSensitive(j, "password");
    bool ok = cJSON_IsString(s) && cJSON_IsString(p);
    if (ok) {
        snprintf(ssid, ssid_size, "%s", s->valuestring);
        snprintf(password, password_size, "%s", p->valuestring);
    }
    if (cJSON_IsString(p)) {
        memset(p->valuestring, 0, strlen(p->valuestring)); /* cJSON_Delete frees it unwiped */
    }
    cJSON_Delete(j);
    return ok;
}

/* ---- Running tests ---- */

static void say_run(const char *tag, int index, int stage, const test_desc_t *t)
{
    cJSON *r = cJSON_CreateObject();
    cJSON_AddStringToObject(r, "tag", tag);
    cJSON_AddNumberToObject(r, "index", index);
    cJSON_AddNumberToObject(r, "stage", stage);
    cJSON_AddNumberToObject(r, "stages", t->test_fn_count);
    cJSON_AddStringToObject(r, "name", t->name);
    say_json("run", r);
}

/* Runs the tests tagged tag ("*": all), from test index and its stage on. */
static void run_plan(const char *tag, int from_index, int from_stage)
{
    static test_desc_t *plan[MAX_TESTS];
    int n = 0;
    for (int i = 0; i < s_ntests; i++) {
        if (has_tag(s_tests[i], tag)) {
            plan[n++] = s_tests[i];
        }
    }
    UNITY_BEGIN();
    for (int i = from_index < 0 ? 0 : from_index; i < n; i++) {
        test_desc_t *t = plan[i];
        int stages = t->test_fn_count > 0 ? t->test_fn_count : 1;
        for (int s = i == from_index && from_stage > 1 ? from_stage : 1; s <= stages; s++) {
            if (s < stages) {
                /* This stage ends by restarting the board: report what ran so far. */
                UNITY_END();
                UNITY_BEGIN();
            }
            say_run(tag, i, s, t);
            UNITY_UINT failed = Unity.TestFailures;
            Unity.TestFile = t->file;
            Unity.CurrentDetail1 = t->desc;
            UnityDefaultTestRun(t->fn[s - 1], t->name, t->line);
            if (Unity.TestFailures != failed) {
                break; /* a failed stage ends its test */
            }
        }
    }
    UNITY_END();
    say("@R {\"done\":true}");
}

static const char *boot_reason(void)
{
    switch (lcd349_boot_reason()) {
    case LCD349_BOOT_POWER_ON: return "power_on";
    case LCD349_BOOT_SOFTWARE: return "software";
    case LCD349_BOOT_CRASH: return "crash";
    case LCD349_BOOT_BROWNOUT: return "brownout";
    default: return "other";
    }
}

static void console_up(void)
{
    if (usb_serial_jtag_is_driver_installed()) {
        return;
    }
    usb_serial_jtag_driver_config_t cfg = USB_SERIAL_JTAG_DRIVER_CONFIG_DEFAULT();
    cfg.tx_buffer_size = 4096;
    cfg.rx_buffer_size = 1024;
    if (usb_serial_jtag_driver_install(&cfg) == ESP_OK) {
        usb_serial_jtag_vfs_use_driver();
    }
}

void lcd349_harness_run(void)
{
    console_up();
    qsort(s_tests, (size_t)s_ntests, sizeof s_tests[0], by_place);

    cJSON *b = cJSON_CreateObject();
    cJSON_AddStringToObject(b, "reason", boot_reason());
    cJSON_AddNumberToObject(b, "tests", s_ntests);
    say_json("boot", b);

    lcd349_err_t err = lcd349_init(NULL);
    char status[8];
    snprintf(status, sizeof status, "0x%03x", (unsigned)lcd349_board_status());
    cJSON *in = cJSON_CreateObject();
    cJSON_AddStringToObject(in, "result", lcd349_err_str(err));
    cJSON_AddStringToObject(in, "status", status);
    say_json("init", in);
    cJSON *m = cJSON_CreateObject();
    cJSON_AddNumberToObject(m, "internal_free", (double)heap_caps_get_free_size(MALLOC_CAP_INTERNAL));
    cJSON_AddNumberToObject(m, "internal_largest", (double)heap_caps_get_largest_free_block(MALLOC_CAP_INTERNAL));
    cJSON_AddNumberToObject(m, "psram_free", (double)heap_caps_get_free_size(MALLOC_CAP_SPIRAM));
    say_json("mem", m);

    char line[LINE_MAX_LEN], tag[32];
    int index, stage;
    for (;;) {
        say("@Q cmd");
        if (!read_line(line, sizeof line, ASK_EVERY_MS)) {
            continue;
        }
        if (strcmp(line, "@P all") == 0) {
            run_plan("*", 0, 1);
        } else if (sscanf(line, "@P only %31s", tag) == 1) {
            run_plan(tag, 0, 1);
        } else if (sscanf(line, "@P resume %31s %d %d", tag, &index, &stage) == 3) {
            run_plan(tag, index, stage);
        }
    }
}
