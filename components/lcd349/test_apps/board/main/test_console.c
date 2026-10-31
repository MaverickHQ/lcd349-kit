/* SPDX-License-Identifier: Apache-2.0
 * The console on the board (seam S1-4). The io lane adds its tests here,
 * each TEST_CASE tagged "[console]". A test prints "@Q ping" and the harness
 * answers "@P ping" (lcd349_harness.h).
 */
#include <stdio.h>

#include "esp_log.h"
#include "lcd349.h"
#include "unity.h"
#include "unity_test_runner.h"

#define OK_STR lcd349_err_str(LCD349_OK)

/* Asks the harness for a line and reads its answer through the console. */
static void ping(void)
{
    char line[64];
    printf("@Q ping\n");
    fflush(stdout);
    uint32_t t0 = lcd349_time_ms();
    TEST_ASSERT_EQUAL_STRING(OK_STR, lcd349_err_str(lcd349_console_read_line(line, sizeof line, 2000)));
    printf("console: \"%s\" after %lu ms\n", line, (unsigned long)(lcd349_time_ms() - t0));
    TEST_ASSERT_EQUAL_STRING("@P ping", line);
}

/* The console reads a line the host sends, and output still reaches the
 * host afterwards: a log line, then a second request the harness answers
 * only if it saw it (requirement 3.5, design D1-6). */
TEST_CASE("test_console_reads_host_line", "[console]")
{
    ping();
    ESP_LOGI("test_console", "a log line after a console read");
    ping();
}
