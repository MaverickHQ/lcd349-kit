/* SPDX-License-Identifier: Apache-2.0
 * Wi-Fi on the board (seam S1-4). The net lane adds its tests here, each
 * TEST_CASE tagged "[wifi]". lcd349_harness_wifi() gets a network from the
 * host's test-wifi.toml; never print it.
 */
#include <stdio.h>
#include <string.h>

#include "esp_log.h"
#include "freertos/FreeRTOS.h"
#include "freertos/task.h"
#include "lcd349.h"
#include "lcd349_harness.h"
#include "unity.h"
#include "unity_test_runner.h"

#define SCAN_MAX 16

/* Requirement 4.1: the station sees the networks around it, strongest first.
 * Only counts and signal levels are printed: the SSIDs are the neighbours'
 * (and the test network's, which boardtest masks only once it has read its
 * file). */
TEST_CASE("test_wifi_scan_finds_networks", "[wifi]")
{
    static lcd349_wifi_ap_t aps[SCAN_MAX];
    memset(aps, 0, sizeof aps);
    size_t n = SCAN_MAX;
    TEST_ASSERT_EQUAL_STRING("LCD349_OK", lcd349_err_str(lcd349_wifi_scan(aps, &n)));
    printf("scan: %u access points, RSSI", (unsigned)n);
    for (size_t i = 0; i < n; i++) {
        printf(" %d", aps[i].rssi);
    }
    printf(" dBm\n");
    TEST_ASSERT_GREATER_OR_EQUAL_UINT(1, n);
    TEST_ASSERT_LESS_OR_EQUAL_UINT(SCAN_MAX, n);
    for (size_t i = 0; i < n; i++) {
        /* A hidden network has no SSID; at least the strongest visible one must. */
        TEST_ASSERT_LESS_THAN_UINT(sizeof aps[i].ssid, strnlen(aps[i].ssid, sizeof aps[i].ssid));
        /* RSSI is a real reading: negative dBm, above the noise floor. */
        TEST_ASSERT_INT_WITHIN(55, -55, aps[i].rssi);
        TEST_ASSERT_TRUE(aps[i].channel >= 1 && aps[i].channel <= 14);
        if (i > 0) {
            TEST_ASSERT_TRUE(aps[i].rssi <= aps[i - 1].rssi);
        }
    }
    size_t named = 0;
    for (size_t i = 0; i < n; i++) {
        named += aps[i].ssid[0] != '\0';
    }
    TEST_ASSERT_GREATER_OR_EQUAL_UINT(1, named);
}

/* ---- Joining ---- */

/* What the Wi-Fi events showed, recorded on lcd349_evt. */
static struct {
    volatile int changes;
    volatile bool connecting_test, connected_test, tried_made_up;
} s_seen;
static char s_test_ssid[33];

#define MADE_UP_SSID "lcd349-no-such-network-7f3a"

static void on_wifi(const lcd349_wifi_status_t *st, void *ctx)
{
    (void)ctx;
    bool test = strcmp(st->ssid, s_test_ssid) == 0;
    s_seen.connecting_test |= st->state == LCD349_WIFI_CONNECTING && test;
    s_seen.connected_test |= st->state == LCD349_WIFI_CONNECTED && test;
    s_seen.tried_made_up |= st->state == LCD349_WIFI_CONNECTING && strcmp(st->ssid, MADE_UP_SSID) == 0;
    s_seen.changes++;
}

/* Network 1 of test-wifi.toml, if the scan sees it; the test is ignored
 * (the environment, not the kit) if it doesn't. */
static bool test_network(lcd349_wifi_net_t *net)
{
    memset(net, 0, sizeof *net);
    TEST_ASSERT_TRUE_MESSAGE(lcd349_harness_wifi(1, net->ssid, sizeof net->ssid, net->password, sizeof net->password),
                             "the host sent no test network");
    snprintf(s_test_ssid, sizeof s_test_ssid, "%s", net->ssid);
    static lcd349_wifi_ap_t aps[SCAN_MAX];
    size_t n = SCAN_MAX;
    TEST_ASSERT_EQUAL_STRING("LCD349_OK", lcd349_err_str(lcd349_wifi_scan(aps, &n)));
    for (size_t i = 0; i < n; i++) {
        if (strcmp(aps[i].ssid, net->ssid) == 0) {
            return true;
        }
    }
    return false;
}

static bool wait_for_state(lcd349_wifi_state_t want, uint32_t timeout_ms, lcd349_wifi_status_t *st)
{
    uint32_t t0 = lcd349_time_ms();
    do {
        if (lcd349_wifi_status(st) == LCD349_OK && st->state == want) {
            return true;
        }
        vTaskDelay(pdMS_TO_TICKS(100));
    } while (lcd349_time_ms() - t0 < timeout_ms);
    return false;
}

/* Requirement 4.1: with two saved networks, a made-up one first and the test
 * network second, the station skips the invisible one and joins the test
 * network, reporting connecting and connected. Neither SSID nor the password
 * is printed: assertions compare them without quoting them, and the driver's
 * own log (which names the network) is quietened. */
TEST_CASE("test_wifi_joins_priority_network", "[wifi]")
{
    esp_log_level_set("wifi", ESP_LOG_WARN);
    lcd349_wifi_net_t nets[2];
    if (!test_network(&nets[1])) {
        memset(&nets[1], 0, sizeof nets[1]);
        TEST_IGNORE_MESSAGE("environment: the scan doesn't see the test network");
    }
    memset(&nets[0], 0, sizeof nets[0]);
    snprintf(nets[0].ssid, sizeof nets[0].ssid, "%s", MADE_UP_SSID);
    snprintf(nets[0].password, sizeof nets[0].password, "%s", "not-the-password");

    static bool subscribed;
    if (!subscribed) {
        TEST_ASSERT_EQUAL_STRING("LCD349_OK", lcd349_err_str(lcd349_wifi_subscribe(on_wifi, NULL)));
        subscribed = true;
    }
    memset((void *)&s_seen, 0, sizeof s_seen);

    lcd349_err_t set = lcd349_wifi_set_networks(nets, 2);
    memset(nets, 0, sizeof nets);
    TEST_ASSERT_EQUAL_STRING("LCD349_OK", lcd349_err_str(set));
    TEST_ASSERT_EQUAL_STRING("LCD349_OK", lcd349_err_str(lcd349_wifi_start()));

    lcd349_wifi_status_t st;
    uint32_t t0 = lcd349_time_ms();
    bool connected = wait_for_state(LCD349_WIFI_CONNECTED, 45000, &st);
    printf("join: %s after %u ms, state %d, reason %d, RSSI %d dBm, %d events\n", connected ? "connected" : "not connected",
           (unsigned)(lcd349_time_ms() - t0), st.state, st.reason, st.rssi, s_seen.changes);
    TEST_ASSERT_TRUE_MESSAGE(connected, "not connected within 45 s");
    TEST_ASSERT_TRUE_MESSAGE(strcmp(st.ssid, s_test_ssid) == 0, "the status names another network");
    TEST_ASSERT_NOT_EQUAL_UINT32(0, st.ip4);
    TEST_ASSERT_INT_WITHIN(55, -55, st.rssi);
    /* Events reach subscribers on lcd349_evt shortly after the state changes. */
    vTaskDelay(pdMS_TO_TICKS(200));
    TEST_ASSERT_TRUE_MESSAGE(s_seen.connecting_test, "no connecting event for the test network");
    TEST_ASSERT_TRUE_MESSAGE(s_seen.connected_test, "no connected event for the test network");
    TEST_ASSERT_FALSE_MESSAGE(s_seen.tried_made_up, "tried the made-up network, which isn't visible");

    TEST_ASSERT_EQUAL_STRING("LCD349_OK", lcd349_err_str(lcd349_wifi_stop()));
    TEST_ASSERT_TRUE_MESSAGE(wait_for_state(LCD349_WIFI_IDLE, 5000, &st), "still not idle 5 s after stopping");
    memset(s_test_ssid, 0, sizeof s_test_ssid);
}
