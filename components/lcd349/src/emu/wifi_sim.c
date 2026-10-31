/* SPDX-License-Identifier: Apache-2.0
 *
 * Simulated Wi-Fi station (requirement 3.9). The device model holds the
 * networks "in the air" (state.set); the product saves up to 4 in priority
 * order. 5 GHz networks are invisible: the ESP32-S3 has a 2.4 GHz radio only.
 * Connecting, failing and dropping raise the same status events as the
 * hardware backend. Sockets always use the host's network, whatever the
 * simulated state (see the fidelity list).
 *
 * Timing is compressed where waiting teaches nothing: a change to the
 * simulated networks triggers a scan at once instead of at the next 60 s scan.
 */
#include <string.h>

#include "esp_log.h"
#include "freertos/FreeRTOS.h"
#include "freertos/queue.h"
#include "freertos/task.h"

#include "emu.h"

#define SCAN_MS 1000   /* emulator choice: a scan's duration */
#define CONNECT_MS 400 /* emulator choice: association plus DHCP */
#define IP_10_0_2_15 0x0F02000Au /* network byte order on little-endian hosts */

typedef enum { CMD_START, CMD_STOP, CMD_NETS, CMD_ENV } cmd_t;

static const char *TAG = "lcd349_wifi";
static QueueHandle_t s_q;
static lcd349_wifi_net_t s_saved[LCD349_WIFI_MAX_NETWORKS];
static size_t s_n_saved;
static lcd349_wifi_status_t s_status;
static bool s_started;

static void set_status(lcd349_wifi_state_t st, lcd349_wifi_reason_t reason, const char *ssid, int8_t rssi, uint32_t ip)
{
    lcd349_wifi_status_t s = {.state = st, .reason = reason, .rssi = rssi, .ip4 = ip};
    snprintf(s.ssid, sizeof s.ssid, "%s", ssid ? ssid : "");
    emu_crit_enter();
    bool changed = memcmp(&s, &s_status, sizeof s) != 0;
    s_status = s;
    emu_crit_exit();
    if (changed) {
        emu_fb_hdr()->wifi_state = (uint8_t)st;
        emu_fb_status_changed();
        lcd349__wifi_changed(&s);
        ESP_LOGD(TAG, "state %d reason %d ssid '%s'", st, reason, s.ssid);
    }
}

/* The visible network with this SSID, if any (2.4 GHz only). */
static bool visible(const char *ssid, emu_net_t *out)
{
    emu_state_t st;
    emu_state_get(&st);
    for (size_t i = 0; i < st.n_nets; i++) {
        if (st.nets[i].band_x10 == 24 && strcmp(st.nets[i].ssid, ssid) == 0) {
            *out = st.nets[i];
            return true;
        }
    }
    return false;
}

static bool is_saved(const char *ssid)
{
    emu_crit_enter();
    bool found = false;
    for (size_t i = 0; i < s_n_saved; i++) {
        found |= strcmp(s_saved[i].ssid, ssid) == 0;
    }
    emu_crit_exit();
    return found;
}

/* One scan-and-connect attempt over the saved networks, highest priority first. */
static void attempt(void)
{
    set_status(LCD349_WIFI_SCANNING, LCD349_WIFI_REASON_NONE, NULL, 0, 0);
    vTaskDelay(pdMS_TO_TICKS(SCAN_MS));
    lcd349_wifi_net_t saved[LCD349_WIFI_MAX_NETWORKS];
    emu_crit_enter();
    size_t n = s_n_saved;
    memcpy(saved, s_saved, sizeof saved);
    emu_crit_exit();
    for (size_t i = 0; i < n; i++) {
        emu_net_t air;
        if (!visible(saved[i].ssid, &air)) {
            continue;
        }
        set_status(LCD349_WIFI_CONNECTING, LCD349_WIFI_REASON_NONE, air.ssid, air.rssi, 0);
        vTaskDelay(pdMS_TO_TICKS(CONNECT_MS));
        if (strcmp(air.password, saved[i].password) != 0) {
            set_status(LCD349_WIFI_DISCONNECTED, LCD349_WIFI_REASON_AUTH, air.ssid, 0, 0);
            return;
        }
        set_status(LCD349_WIFI_CONNECTED, LCD349_WIFI_REASON_NONE, air.ssid, air.rssi, IP_10_0_2_15);
        return;
    }
    set_status(LCD349_WIFI_DISCONNECTED, LCD349_WIFI_REASON_NOT_FOUND, NULL, 0, 0);
}

static void wifi_task(void *arg)
{
    (void)arg;
    TickType_t interval = pdMS_TO_TICKS(LCD349_WIFI_SCAN_INTERVAL_S * 1000u);
    bool due = false;
    for (;;) {
        cmd_t cmd;
        TickType_t wait = !s_started ? portMAX_DELAY : (due ? 0 : interval);
        bool got = xQueueReceive(s_q, &cmd, wait) == pdTRUE;
        lcd349_wifi_status_t now;
        be_wifi_status(&now);
        if (got) {
            switch (cmd) {
            case CMD_START:
                s_started = true;
                due = now.state != LCD349_WIFI_CONNECTED;
                break;
            case CMD_STOP:
                s_started = false;
                due = false;
                set_status(LCD349_WIFI_IDLE, LCD349_WIFI_REASON_STOPPED, NULL, 0, 0);
                break;
            case CMD_NETS:
            case CMD_ENV: {
                emu_net_t air;
                if (now.state == LCD349_WIFI_CONNECTED && (!visible(now.ssid, &air) || !is_saved(now.ssid))) {
                    set_status(LCD349_WIFI_DISCONNECTED, LCD349_WIFI_REASON_LOST, now.ssid, 0, 0);
                    now.state = LCD349_WIFI_DISCONNECTED;
                } else if (now.state == LCD349_WIFI_CONNECTED && air.rssi != now.rssi) {
                    set_status(LCD349_WIFI_CONNECTED, LCD349_WIFI_REASON_NONE, air.ssid, air.rssi, now.ip4);
                }
                due = s_started && now.state != LCD349_WIFI_CONNECTED;
                break;
            }
            }
            if (!due) {
                continue;
            }
        } else if (!s_started || now.state == LCD349_WIFI_CONNECTED) {
            continue; /* periodic tick while connected: nothing to do */
        }
        due = false;
        attempt();
    }
}

lcd349_err_t emu_wifi_init(void)
{
    s_q = xQueueCreate(8, sizeof(cmd_t));
    if (!s_q || xTaskCreate(wifi_task, "lcd349_wifi", 4096, NULL, LCD349_PRIO_EMU, NULL) != pdPASS) {
        return LCD349_ERR_NO_MEM;
    }
    return LCD349_OK;
}

static lcd349_err_t send(cmd_t c)
{
    return xQueueSend(s_q, &c, pdMS_TO_TICKS(100)) == pdTRUE ? LCD349_OK : LCD349_ERR_TIMEOUT;
}

void emu_wifi_env_changed(void)
{
    if (s_q) {
        send(CMD_ENV);
    }
}

lcd349_err_t be_wifi_set_networks(const lcd349_wifi_net_t *nets, size_t n)
{
    emu_crit_enter();
    memcpy(s_saved, nets, n * sizeof *nets);
    s_n_saved = n;
    emu_crit_exit();
    return send(CMD_NETS);
}

lcd349_err_t be_wifi_start(void)
{
    return send(CMD_START);
}

lcd349_err_t be_wifi_stop(void)
{
    return send(CMD_STOP);
}

lcd349_err_t be_wifi_scan(lcd349_wifi_ap_t *out, size_t *n)
{
    vTaskDelay(pdMS_TO_TICKS(SCAN_MS));
    emu_state_t st;
    emu_state_get(&st);
    size_t k = 0;
    for (size_t i = 0; i < st.n_nets && k < *n; i++) {
        if (st.nets[i].band_x10 != 24) {
            continue; /* the radio can't see 5 GHz */
        }
        lcd349_wifi_ap_t *ap = &out[k++];
        memset(ap, 0, sizeof *ap);
        snprintf(ap->ssid, sizeof ap->ssid, "%s", st.nets[i].ssid);
        ap->rssi = st.nets[i].rssi;
        ap->channel = (uint8_t)(1 + (i * 5) % 11);
        ap->secure = st.nets[i].password[0] != '\0';
    }
    /* Strongest first. */
    for (size_t i = 1; i < k; i++) {
        for (size_t j = i; j > 0 && out[j].rssi > out[j - 1].rssi; j--) {
            lcd349_wifi_ap_t t = out[j];
            out[j] = out[j - 1];
            out[j - 1] = t;
        }
    }
    *n = k;
    return LCD349_OK;
}

lcd349_err_t be_wifi_status(lcd349_wifi_status_t *status)
{
    emu_crit_enter();
    *status = s_status;
    emu_crit_exit();
    return LCD349_OK;
}
