/* SPDX-License-Identifier: Apache-2.0
 * Storage on the board (seam S1-4). The io lane adds its tests here, each
 * TEST_CASE (or TEST_CASE_MULTIPLE_STAGES, across a reboot) tagged "[kv]".
 */
#include <stdio.h>
#include <string.h>

#include "esp_attr.h"
#include "esp_random.h"
#include "esp_system.h"
#include "lcd349.h"
#include "unity.h"
#include "unity_test_runner.h"

#define NS "kvtest"
#define OK_STR lcd349_err_str(LCD349_OK)

/* What stage 1 wrote, kept through the restart in RTC memory: a fresh value
 * each run, so a value left in flash by an earlier run can't pass stage 2. */
static RTC_NOINIT_ATTR uint32_t s_written;

static void set_and_restart(void)
{
    uint32_t v = esp_random();
    char s[24];
    snprintf(s, sizeof s, "run %08lx", (unsigned long)v);
    TEST_ASSERT_EQUAL_STRING(OK_STR, lcd349_err_str(lcd349_kv_set_blob(NS, "u32", &v, sizeof v)));
    TEST_ASSERT_EQUAL_STRING(OK_STR, lcd349_err_str(lcd349_kv_set_str(NS, "str", s)));
    s_written = v;
    esp_restart();
}

static void read_back_and_erase(void)
{
    uint32_t want = s_written, got = 0;
    char want_s[24], got_s[24];
    snprintf(want_s, sizeof want_s, "run %08lx", (unsigned long)want);

    size_t len = sizeof got;
    TEST_ASSERT_EQUAL_STRING(OK_STR, lcd349_err_str(lcd349_kv_get_blob(NS, "u32", &got, &len)));
    TEST_ASSERT_EQUAL(sizeof got, len);
    TEST_ASSERT_EQUAL_HEX32(want, got);
    len = sizeof got_s;
    TEST_ASSERT_EQUAL_STRING(OK_STR, lcd349_err_str(lcd349_kv_get_str(NS, "str", got_s, &len)));
    TEST_ASSERT_EQUAL_STRING(want_s, got_s);
    TEST_ASSERT_EQUAL(strlen(want_s) + 1, len);

    TEST_ASSERT_EQUAL_STRING(OK_STR, lcd349_err_str(lcd349_kv_erase(NS, "u32")));
    len = sizeof got;
    TEST_ASSERT_EQUAL_STRING(lcd349_err_str(LCD349_ERR_NOT_FOUND), lcd349_err_str(lcd349_kv_get_blob(NS, "u32", &got, &len)));
    TEST_ASSERT_EQUAL_STRING(OK_STR, lcd349_err_str(lcd349_kv_erase(NS, NULL)));
    len = sizeof got_s;
    TEST_ASSERT_EQUAL_STRING(lcd349_err_str(LCD349_ERR_NOT_FOUND), lcd349_err_str(lcd349_kv_get_str(NS, "str", got_s, &len)));
}

/* Values written before a restart read back after it (requirement 3.4). */
TEST_CASE_MULTIPLE_STAGES("test_kv_survives_reboot", "[kv]", set_and_restart, read_back_and_erase);

#define TYPED_NS "kvtyped"
#define STR_IS(err, call) TEST_ASSERT_EQUAL_STRING(lcd349_err_str(err), lcd349_err_str(call))

/* Values are typed as in the emulator (kit.h; the host's test_storage): a
 * key reads back only as the type it was written as, a buffer too small
 * gets the size needed, and writing the other type replaces the value. */
TEST_CASE("test_kv_typed_like_the_emulator", "[kv]")
{
    char s[16];
    uint8_t blob[4] = {1, 2, 3, 4}, back[8] = {0};
    size_t len;
    STR_IS(LCD349_OK, lcd349_kv_erase(TYPED_NS, NULL));

    len = sizeof s;
    STR_IS(LCD349_ERR_NOT_FOUND, lcd349_kv_get_str(TYPED_NS, "k", s, &len));
    STR_IS(LCD349_OK, lcd349_kv_set_str(TYPED_NS, "k", "pager"));
    len = 3;
    STR_IS(LCD349_ERR_NO_MEM, lcd349_kv_get_str(TYPED_NS, "k", s, &len));
    TEST_ASSERT_EQUAL(6, len);
    len = sizeof back;
    STR_IS(LCD349_ERR_ARG, lcd349_kv_get_blob(TYPED_NS, "k", back, &len));

    STR_IS(LCD349_OK, lcd349_kv_set_blob(TYPED_NS, "k", blob, sizeof blob));
    len = sizeof back;
    STR_IS(LCD349_OK, lcd349_kv_get_blob(TYPED_NS, "k", back, &len));
    TEST_ASSERT_EQUAL(sizeof blob, len);
    TEST_ASSERT_EQUAL_MEMORY(blob, back, sizeof blob);
    len = 2;
    STR_IS(LCD349_ERR_NO_MEM, lcd349_kv_get_blob(TYPED_NS, "k", back, &len));
    TEST_ASSERT_EQUAL(sizeof blob, len);
    len = sizeof s;
    STR_IS(LCD349_ERR_ARG, lcd349_kv_get_str(TYPED_NS, "k", s, &len));

    STR_IS(LCD349_OK, lcd349_kv_set_str(TYPED_NS, "k", "back"));
    len = sizeof s;
    STR_IS(LCD349_OK, lcd349_kv_get_str(TYPED_NS, "k", s, &len));
    TEST_ASSERT_EQUAL_STRING("back", s);
    TEST_ASSERT_EQUAL(5, len);

    STR_IS(LCD349_OK, lcd349_kv_erase(TYPED_NS, "missing"));
    STR_IS(LCD349_OK, lcd349_kv_erase(TYPED_NS, NULL));
    len = sizeof s;
    STR_IS(LCD349_ERR_NOT_FOUND, lcd349_kv_get_str(TYPED_NS, "k", s, &len));
}
