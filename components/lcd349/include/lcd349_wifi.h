/* SPDX-License-Identifier: Apache-2.0
 *
 * Wi-Fi station. The radio is 2.4 GHz only: 5 GHz networks are invisible (a
 * phone hotspot must offer 2.4 GHz). Up to LCD349_WIFI_MAX_NETWORKS saved
 * networks in priority order; while disconnected the kit scans every
 * LCD349_WIFI_SCAN_INTERVAL_S seconds and joins the highest-priority one it
 * sees. Products use BSD sockets for network I/O; the SDK doesn't wrap them.
 *
 * Event callbacks run on the kit task lcd349_evt.
 */
#pragma once

#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>

#include "lcd349_core.h"

#ifdef __cplusplus
extern "C" {
#endif

typedef struct {
    char ssid[33];
    char password[65]; /**< empty for an open network */
} lcd349_wifi_net_t;

typedef struct {
    char ssid[33];
    int8_t rssi; /**< dBm */
    uint8_t channel;
    bool secure;
} lcd349_wifi_ap_t;

typedef enum {
    LCD349_WIFI_IDLE = 0,     /**< stopped, or no saved networks */
    LCD349_WIFI_SCANNING,     /**< looking for a saved network */
    LCD349_WIFI_CONNECTING,
    LCD349_WIFI_CONNECTED,    /**< associated and has an IPv4 address */
    LCD349_WIFI_DISCONNECTED, /**< lost or failed; will scan again */
} lcd349_wifi_state_t;

typedef enum {
    LCD349_WIFI_REASON_NONE = 0,
    LCD349_WIFI_REASON_AUTH,      /**< wrong password */
    LCD349_WIFI_REASON_NOT_FOUND, /**< no saved network visible */
    LCD349_WIFI_REASON_LOST,      /**< connection dropped */
    LCD349_WIFI_REASON_STOPPED,   /**< lcd349_wifi_stop() */
} lcd349_wifi_reason_t;

typedef struct {
    lcd349_wifi_state_t state;
    lcd349_wifi_reason_t reason;
    char ssid[33];  /**< the network involved, or empty */
    int8_t rssi;    /**< dBm, when connected */
    uint32_t ip4;   /**< network byte order, when connected */
} lcd349_wifi_status_t;

typedef void (*lcd349_wifi_cb_t)(const lcd349_wifi_status_t *status, void *ctx);

/** Replaces the saved networks (copied, highest priority first). n <= LCD349_WIFI_MAX_NETWORKS. */
lcd349_err_t lcd349_wifi_set_networks(const lcd349_wifi_net_t *nets, size_t n);

/** Starts the station: scan, connect, reconnect. */
lcd349_err_t lcd349_wifi_start(void);

/** Stops the station and disconnects. */
lcd349_err_t lcd349_wifi_stop(void);

/**
 * Scans now and fills out with up to *n visible networks, strongest first;
 * *n is set to the number written. Blocks for a few seconds: call from a
 * product task, never from lcd349_ui or lcd349_evt.
 */
lcd349_err_t lcd349_wifi_scan(lcd349_wifi_ap_t *out, size_t *n);

/** The current state. */
lcd349_err_t lcd349_wifi_status(lcd349_wifi_status_t *status);

/** Subscribes to state changes. Up to 4 subscribers. */
lcd349_err_t lcd349_wifi_subscribe(lcd349_wifi_cb_t cb, void *ctx);

#ifdef __cplusplus
}
#endif
