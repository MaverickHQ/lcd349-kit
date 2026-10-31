/* SPDX-License-Identifier: Apache-2.0
 *
 * Wi-Fi: the station with up to LCD349_WIFI_MAX_NETWORKS saved networks,
 * scans, events and modem power save. The net lane implements it (tasks
 * 7.1-7.2).
 *
 * The driver comes up at init with the radio off; the radio runs while the
 * station is started, or for a scan. The kit keeps the saved networks itself,
 * as the emulator does, so the driver's own NVS copy is off.
 *
 * The station behaves as the emulator's model (src/emu/wifi_sim.c): while
 * started and not connected it scans every LCD349_WIFI_SCAN_INTERVAL_S and
 * tries the highest-priority saved network it sees; a change of the saved
 * networks, or a lost connection, tries again at once. A task, lcd349_wifi,
 * runs it; the driver's events and the API calls only wake it. Logs never
 * name a network: test credentials pass through here (design D1-6).
 */
#include <stdlib.h>
#include <string.h>

#include "esp_event.h"
#include "esp_log.h"
#include "esp_netif.h"
#include "esp_wifi.h"
#include "freertos/FreeRTOS.h"
#include "freertos/event_groups.h"
#include "freertos/semphr.h"
#include "freertos/task.h"

#include "hw.h"

#define CONNECT_TIMEOUT_MS 20000 /* association, then DHCP */
#define SCAN_LOCK_MS 30000       /* a product's scan waits this long for the station's attempt */
#define EV_GOT_IP BIT0
#define EV_DISCONNECTED BIT1

static const char *TAG = "lcd349_wifi";

static esp_netif_t *s_netif;
static SemaphoreHandle_t s_radio; /* held for a scan or a connection attempt, and to switch the radio */
static EventGroupHandle_t s_events;
static TaskHandle_t s_task;
static portMUX_TYPE s_mux = portMUX_INITIALIZER_UNLOCKED; /* guards the rest, for short copies */
static bool s_radio_on;                                   /* under s_radio */

static lcd349_wifi_net_t s_saved[LCD349_WIFI_MAX_NETWORKS];
static size_t s_n_saved;
static uint32_t s_saved_gen;
static bool s_want_started;
static uint8_t s_disconnect_reason;
static lcd349_wifi_status_t s_status;

/* ---- Status and events ---- */

static void set_status(lcd349_wifi_state_t st, lcd349_wifi_reason_t reason, const char *ssid, int8_t rssi, uint32_t ip)
{
    lcd349_wifi_status_t s = {.state = st, .reason = reason, .rssi = rssi, .ip4 = ip};
    snprintf(s.ssid, sizeof s.ssid, "%s", ssid ? ssid : "");
    taskENTER_CRITICAL(&s_mux);
    bool changed = memcmp(&s, &s_status, sizeof s) != 0;
    s_status = s;
    taskEXIT_CRITICAL(&s_mux);
    if (changed) {
        lcd349__wifi_changed(&s);
        ESP_LOGD(TAG, "state %d reason %d", st, reason);
    }
}

lcd349_err_t be_wifi_status(lcd349_wifi_status_t *status)
{
    taskENTER_CRITICAL(&s_mux);
    *status = s_status;
    taskEXIT_CRITICAL(&s_mux);
    return LCD349_OK;
}

/* On the event loop's task: note what happened and wake the station. */
static void on_event(void *arg, esp_event_base_t base, int32_t id, void *data)
{
    (void)arg;
    if (base == WIFI_EVENT && id == WIFI_EVENT_STA_DISCONNECTED) {
        uint8_t reason = ((const wifi_event_sta_disconnected_t *)data)->reason;
        taskENTER_CRITICAL(&s_mux);
        s_disconnect_reason = reason;
        taskEXIT_CRITICAL(&s_mux);
        xEventGroupSetBits(s_events, EV_DISCONNECTED);
    } else if (base == IP_EVENT && id == IP_EVENT_STA_GOT_IP) {
        xEventGroupSetBits(s_events, EV_GOT_IP);
    } else {
        return;
    }
    xTaskNotifyGive(s_task);
}

/* ---- Scans ---- */

static int by_rssi(const void *a, const void *b)
{
    return ((const wifi_ap_record_t *)b)->rssi - ((const wifi_ap_record_t *)a)->rssi;
}

/* A blocking scan of every channel, the radio on already; *recs (free it) is
 * every network seen, strongest first. Called with s_radio held. */
static esp_err_t scan_records(wifi_ap_record_t **recs, uint16_t *found)
{
    *recs = NULL;
    *found = 0;
    esp_err_t err = esp_wifi_scan_start(NULL, true);
    if (err != ESP_OK) {
        return err;
    }
    esp_wifi_scan_get_ap_num(found);
    if (*found == 0) {
        return ESP_OK;
    }
    *recs = calloc(*found, sizeof **recs);
    if (!*recs) {
        esp_wifi_clear_ap_list();
        return ESP_ERR_NO_MEM;
    }
    err = esp_wifi_scan_get_ap_records(found, *recs);
    if (err != ESP_OK) {
        free(*recs);
        *recs = NULL;
        return err;
    }
    qsort(*recs, *found, sizeof **recs, by_rssi);
    return ESP_OK;
}

static lcd349_err_t to_lcd349(esp_err_t err)
{
    return err == ESP_OK ? LCD349_OK : err == ESP_ERR_NO_MEM ? LCD349_ERR_NO_MEM : LCD349_ERR_IO;
}

/* Hidden networks (no SSID) are left out: nothing can join them by name. */
lcd349_err_t be_wifi_scan(lcd349_wifi_ap_t *out, size_t *n)
{
    if (xSemaphoreTake(s_radio, pdMS_TO_TICKS(SCAN_LOCK_MS)) != pdTRUE) {
        return LCD349_ERR_TIMEOUT;
    }
    bool started_here = !s_radio_on;
    esp_err_t err = started_here ? esp_wifi_start() : ESP_OK;
    wifi_ap_record_t *recs = NULL;
    uint16_t found = 0;
    if (err == ESP_OK) {
        err = scan_records(&recs, &found);
        if (started_here) {
            esp_wifi_stop();
        }
    }
    xSemaphoreGive(s_radio);
    if (err != ESP_OK) {
        ESP_LOGE(TAG, "scan: %s", esp_err_to_name(err));
        return to_lcd349(err);
    }
    size_t k = 0;
    for (size_t i = 0; i < found && k < *n; i++) {
        if (recs[i].ssid[0] == '\0') {
            continue;
        }
        lcd349_wifi_ap_t *ap = &out[k++];
        memset(ap, 0, sizeof *ap);
        memcpy(ap->ssid, recs[i].ssid, sizeof ap->ssid - 1);
        ap->rssi = recs[i].rssi;
        ap->channel = recs[i].primary;
        ap->secure = recs[i].authmode != WIFI_AUTH_OPEN;
    }
    free(recs);
    *n = k;
    return LCD349_OK;
}

/* ---- The station ---- */

static lcd349_wifi_reason_t reason_from(uint8_t r)
{
    switch (r) {
    case WIFI_REASON_AUTH_FAIL:
    case WIFI_REASON_AUTH_EXPIRE:
    case WIFI_REASON_4WAY_HANDSHAKE_TIMEOUT:
    case WIFI_REASON_HANDSHAKE_TIMEOUT:
    case WIFI_REASON_MIC_FAILURE:
    case WIFI_REASON_802_1X_AUTH_FAILED:
        return LCD349_WIFI_REASON_AUTH;
    case WIFI_REASON_NO_AP_FOUND:
    case WIFI_REASON_NO_AP_FOUND_W_COMPATIBLE_SECURITY:
    case WIFI_REASON_NO_AP_FOUND_IN_AUTHMODE_THRESHOLD:
    case WIFI_REASON_NO_AP_FOUND_IN_RSSI_THRESHOLD:
        return LCD349_WIFI_REASON_NOT_FOUND;
    default:
        return LCD349_WIFI_REASON_LOST;
    }
}

/* Drops the association and waits for the driver to say so, so that its
 * event can't be taken for the next attempt's. Called with s_radio held. */
static void disconnect(void)
{
    xEventGroupClearBits(s_events, EV_DISCONNECTED);
    if (esp_wifi_disconnect() == ESP_OK) {
        xEventGroupWaitBits(s_events, EV_DISCONNECTED, pdTRUE, pdFALSE, pdMS_TO_TICKS(1000));
    }
}

static void radio(bool on)
{
    xSemaphoreTake(s_radio, portMAX_DELAY);
    if (on && !s_radio_on) {
        esp_err_t err = esp_wifi_start();
        s_radio_on = err == ESP_OK;
        if (err != ESP_OK) {
            ESP_LOGE(TAG, "radio on: %s", esp_err_to_name(err));
        }
    } else if (!on && s_radio_on) {
        disconnect();
        esp_wifi_stop();
        s_radio_on = false;
    }
    xSemaphoreGive(s_radio);
}

/* One scan-and-connect attempt: the highest-priority saved network the scan
 * sees, as the emulator does. */
static void attempt(void)
{
    lcd349_wifi_net_t saved[LCD349_WIFI_MAX_NETWORKS];
    taskENTER_CRITICAL(&s_mux);
    size_t n = s_n_saved;
    memcpy(saved, s_saved, sizeof saved);
    taskEXIT_CRITICAL(&s_mux);

    xSemaphoreTake(s_radio, portMAX_DELAY);
    if (!s_radio_on) {
        xSemaphoreGive(s_radio);
        memset(saved, 0, sizeof saved);
        return;
    }
    set_status(LCD349_WIFI_SCANNING, LCD349_WIFI_REASON_NONE, NULL, 0, 0);
    wifi_ap_record_t *recs = NULL;
    uint16_t found = 0;
    esp_err_t err = scan_records(&recs, &found);
    if (err != ESP_OK) {
        ESP_LOGW(TAG, "scan: %s", esp_err_to_name(err));
    }
    const wifi_ap_record_t *ap = NULL;
    size_t pick = 0;
    for (; pick < n && !ap; pick++) {
        for (uint16_t i = 0; i < found && !ap; i++) {
            if (strcmp((const char *)recs[i].ssid, saved[pick].ssid) == 0) {
                ap = &recs[i];
            }
        }
    }
    if (!ap) {
        free(recs);
        xSemaphoreGive(s_radio);
        memset(saved, 0, sizeof saved);
        set_status(LCD349_WIFI_DISCONNECTED, LCD349_WIFI_REASON_NOT_FOUND, NULL, 0, 0);
        return;
    }
    const lcd349_wifi_net_t *net = &saved[pick - 1];
    int8_t rssi = ap->rssi;
    free(recs);
    set_status(LCD349_WIFI_CONNECTING, LCD349_WIFI_REASON_NONE, net->ssid, rssi, 0);
    ESP_LOGI(TAG, "connecting to saved network %u", (unsigned)pick);

    wifi_config_t wc = {0};
    memcpy(wc.sta.ssid, net->ssid, strlen(net->ssid));
    memcpy(wc.sta.password, net->password, strlen(net->password));
    wc.sta.threshold.authmode = net->password[0] ? WIFI_AUTH_WPA2_PSK : WIFI_AUTH_OPEN;
    wc.sta.pmf_cfg.capable = true;
    xEventGroupClearBits(s_events, EV_GOT_IP | EV_DISCONNECTED);
    err = esp_wifi_set_config(WIFI_IF_STA, &wc);
    memset(&wc, 0, sizeof wc);
    if (err == ESP_OK) {
        err = esp_wifi_connect();
    }
    EventBits_t got = 0;
    if (err == ESP_OK) {
        got = xEventGroupWaitBits(s_events, EV_GOT_IP | EV_DISCONNECTED, pdTRUE, pdFALSE, pdMS_TO_TICKS(CONNECT_TIMEOUT_MS));
    } else {
        ESP_LOGW(TAG, "connect: %s", esp_err_to_name(err));
    }
    if (got & EV_GOT_IP) {
        esp_netif_ip_info_t ip = {0};
        wifi_ap_record_t info;
        esp_netif_get_ip_info(s_netif, &ip);
        if (esp_wifi_sta_get_ap_info(&info) == ESP_OK) {
            rssi = info.rssi;
        }
        xSemaphoreGive(s_radio);
        set_status(LCD349_WIFI_CONNECTED, LCD349_WIFI_REASON_NONE, net->ssid, rssi, ip.ip.addr);
        ESP_LOGI(TAG, "connected, RSSI %d dBm", rssi);
    } else {
        lcd349_wifi_reason_t reason = LCD349_WIFI_REASON_LOST;
        if (got & EV_DISCONNECTED) {
            taskENTER_CRITICAL(&s_mux);
            reason = reason_from(s_disconnect_reason);
            taskEXIT_CRITICAL(&s_mux);
        } else {
            disconnect(); /* timed out: give up on this attempt */
        }
        xSemaphoreGive(s_radio);
        set_status(LCD349_WIFI_DISCONNECTED, reason, net->ssid, 0, 0);
        ESP_LOGW(TAG, "not connected (reason %d)", reason);
    }
    memset(saved, 0, sizeof saved);
}

static bool is_saved(const char *ssid)
{
    bool found = false;
    taskENTER_CRITICAL(&s_mux);
    for (size_t i = 0; i < s_n_saved; i++) {
        found |= strcmp(s_saved[i].ssid, ssid) == 0;
    }
    taskEXIT_CRITICAL(&s_mux);
    return found;
}

static void wifi_task(void *arg)
{
    (void)arg;
    const TickType_t interval = pdMS_TO_TICKS(LCD349_WIFI_SCAN_INTERVAL_S * 1000u);
    bool running = false, due = false;
    uint32_t seen_gen = 0;
    for (;;) {
        lcd349_wifi_status_t now;
        be_wifi_status(&now);
        bool connected = now.state == LCD349_WIFI_CONNECTED;
        TickType_t wait = !running || connected ? portMAX_DELAY : due ? 0 : interval;
        bool woken = ulTaskNotifyTake(pdTRUE, wait) > 0;

        taskENTER_CRITICAL(&s_mux);
        bool want = s_want_started;
        uint32_t gen = s_saved_gen;
        taskEXIT_CRITICAL(&s_mux);
        EventBits_t ev = xEventGroupClearBits(s_events, EV_DISCONNECTED);

        if (want != running) {
            running = want;
            radio(running);
            if (!running) {
                due = false;
                set_status(LCD349_WIFI_IDLE, LCD349_WIFI_REASON_STOPPED, NULL, 0, 0);
                continue;
            }
            due = !connected;
        }
        if (!running) {
            continue;
        }
        if (connected && (ev & EV_DISCONNECTED)) {
            set_status(LCD349_WIFI_DISCONNECTED, LCD349_WIFI_REASON_LOST, now.ssid, 0, 0);
            connected = false;
            due = true;
        } else if (connected && gen != seen_gen && !is_saved(now.ssid)) {
            xSemaphoreTake(s_radio, portMAX_DELAY);
            disconnect();
            xSemaphoreGive(s_radio);
            set_status(LCD349_WIFI_DISCONNECTED, LCD349_WIFI_REASON_LOST, now.ssid, 0, 0);
            connected = false;
            due = true;
        }
        due |= !connected && (gen != seen_gen || !woken); /* new networks, or the periodic scan */
        seen_gen = gen;
        if (due) {
            due = false;
            attempt();
        }
    }
}

/* ---- Set-up and the API ---- */

lcd349_err_t be_wifi_init(void)
{
    esp_err_t err = esp_netif_init();
    if (err == ESP_OK) {
        err = esp_event_loop_create_default();
        if (err == ESP_ERR_INVALID_STATE) {
            err = ESP_OK; /* the product made it */
        }
    }
    if (err == ESP_OK) {
        s_netif = esp_netif_create_default_wifi_sta();
        err = s_netif ? ESP_OK : ESP_FAIL;
    }
    wifi_init_config_t cfg = WIFI_INIT_CONFIG_DEFAULT();
    cfg.nvs_enable = 0;
    if (err == ESP_OK) {
        err = esp_wifi_init(&cfg);
    }
    if (err == ESP_OK) {
        err = esp_wifi_set_mode(WIFI_MODE_STA);
    }
    if (err == ESP_OK) {
        err = esp_wifi_set_ps(WIFI_PS_MIN_MODEM); /* modem power save while connected */
    }
    if (err == ESP_OK) {
        s_radio = xSemaphoreCreateMutex();
        s_events = xEventGroupCreate();
        err = s_radio && s_events ? ESP_OK : ESP_ERR_NO_MEM;
    }
    if (err == ESP_OK && xTaskCreate(wifi_task, "lcd349_wifi", 4096, NULL, LCD349_PRIO_EVT, &s_task) != pdPASS) {
        err = ESP_ERR_NO_MEM;
    }
    if (err == ESP_OK) {
        err = esp_event_handler_register(WIFI_EVENT, WIFI_EVENT_STA_DISCONNECTED, on_event, NULL);
    }
    if (err == ESP_OK) {
        err = esp_event_handler_register(IP_EVENT, IP_EVENT_STA_GOT_IP, on_event, NULL);
    }
    if (err != ESP_OK) {
        ESP_LOGE(TAG, "init: %s", esp_err_to_name(err));
        return to_lcd349(err);
    }
    return LCD349_OK;
}

lcd349_err_t be_wifi_set_networks(const lcd349_wifi_net_t *nets, size_t n)
{
    taskENTER_CRITICAL(&s_mux);
    memset(s_saved, 0, sizeof s_saved);
    memcpy(s_saved, nets, n * sizeof *nets);
    s_n_saved = n;
    s_saved_gen++;
    taskEXIT_CRITICAL(&s_mux);
    xTaskNotifyGive(s_task);
    return LCD349_OK;
}

static lcd349_err_t want_started(bool on)
{
    taskENTER_CRITICAL(&s_mux);
    s_want_started = on;
    taskEXIT_CRITICAL(&s_mux);
    xTaskNotifyGive(s_task);
    return LCD349_OK;
}

lcd349_err_t be_wifi_start(void)
{
    return want_started(true);
}

lcd349_err_t be_wifi_stop(void)
{
    return want_started(false);
}
