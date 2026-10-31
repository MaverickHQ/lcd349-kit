/* SPDX-License-Identifier: Apache-2.0
 *
 * Storage: NVS, one NVS namespace per kit namespace (requirement 3.4).
 * Strings are NVS strings and blobs NVS blobs, so values stay typed as
 * kit.h asks: NVS looks a key up by its type, and a key that exists with the
 * other type reads as LCD349_ERR_ARG. Setting a key with the other type
 * replaces it. NVS is safe from any task.
 */
#include <stdlib.h>
#include <string.h>

#include "esp_log.h"
#include "nvs.h"
#include "nvs_flash.h"

#include "hw.h"

static const char *TAG = "lcd349_kv";

static lcd349_err_t from_nvs(esp_err_t err)
{
    switch (err) {
    case ESP_OK: return LCD349_OK;
    case ESP_ERR_NVS_NOT_FOUND: return LCD349_ERR_NOT_FOUND;
    case ESP_ERR_NVS_NOT_ENOUGH_SPACE:
    case ESP_ERR_NO_MEM: return LCD349_ERR_NO_MEM;
    case ESP_ERR_NVS_TYPE_MISMATCH: /* there, with the other type */
    case ESP_ERR_NVS_VALUE_TOO_LONG:
    case ESP_ERR_NVS_INVALID_NAME:
    case ESP_ERR_NVS_KEY_TOO_LONG: return LCD349_ERR_ARG;
    default:
        ESP_LOGW(TAG, "NVS: %s", esp_err_to_name(err));
        return LCD349_ERR_IO;
    }
}

lcd349_err_t be_kv_init(void)
{
    esp_err_t err = nvs_flash_init();
    if (err == ESP_ERR_NVS_NO_FREE_PAGES || err == ESP_ERR_NVS_NEW_VERSION_FOUND) {
        /* The partition is full of stale pages, or from a newer NVS: it can't be read as it is. */
        ESP_LOGW(TAG, "NVS partition unreadable (%s): erasing it", esp_err_to_name(err));
        err = nvs_flash_erase();
        if (err == ESP_OK) {
            err = nvs_flash_init();
        }
    }
    if (err != ESP_OK) {
        ESP_LOGE(TAG, "nvs_flash_init: %s", esp_err_to_name(err));
        return LCD349_ERR_IO;
    }
    return LCD349_OK;
}

/* Why a lookup by type found nothing: the key may be there with the other type. */
static lcd349_err_t not_found(nvs_handle_t h, const char *key)
{
    nvs_type_t other;
    return nvs_find_key(h, key, &other) == ESP_OK ? LCD349_ERR_ARG : LCD349_ERR_NOT_FOUND;
}

static lcd349_err_t get_blob(nvs_handle_t h, const char *key, void *out, size_t *len)
{
    size_t need = 0;
    esp_err_t err = nvs_get_blob(h, key, NULL, &need);
    if (err == ESP_ERR_NVS_NOT_FOUND) {
        return not_found(h, key);
    }
    if (err != ESP_OK) {
        return from_nvs(err);
    }
    if (need > *len) {
        *len = need;
        return LCD349_ERR_NO_MEM;
    }
    err = nvs_get_blob(h, key, out, &need);
    *len = need;
    return from_nvs(err);
}

/* *len counts the string without its NUL. NVS writes the NUL too, which out
 * has no room for, so the string comes through a copy. */
static lcd349_err_t get_str(nvs_handle_t h, const char *key, char *out, size_t *len)
{
    size_t need = 0; /* with the NUL */
    esp_err_t err = nvs_get_str(h, key, NULL, &need);
    if (err == ESP_ERR_NVS_NOT_FOUND) {
        return not_found(h, key);
    }
    if (err != ESP_OK) {
        return from_nvs(err);
    }
    size_t size = need ? need - 1 : 0;
    if (size > *len) {
        *len = size;
        return LCD349_ERR_NO_MEM;
    }
    char *s = malloc(need);
    lcd349_err_t res = s ? from_nvs(nvs_get_str(h, key, s, &need)) : LCD349_ERR_NO_MEM;
    if (res == LCD349_OK) {
        memcpy(out, s, size);
        *len = size;
    }
    free(s);
    return res;
}

lcd349_err_t be_kv_get(const char *ns, const char *key, lcd349_kv_type_t type, void *out, size_t *len)
{
    nvs_handle_t h;
    esp_err_t err = nvs_open(ns, NVS_READONLY, &h);
    if (err != ESP_OK) {
        return from_nvs(err); /* NOT_FOUND: no such namespace yet */
    }
    lcd349_err_t res = type == LCD349_KV_STR ? get_str(h, key, out, len) : get_blob(h, key, out, len);
    nvs_close(h);
    return res;
}

lcd349_err_t be_kv_set(const char *ns, const char *key, lcd349_kv_type_t type, const void *val, size_t len)
{
    nvs_handle_t h;
    esp_err_t err = nvs_open(ns, NVS_READWRITE, &h);
    if (err != ESP_OK) {
        return from_nvs(err);
    }
    nvs_type_t was; /* a blob may report its index's type: compare string or not */
    if (nvs_find_key(h, key, &was) == ESP_OK && (was == NVS_TYPE_STR) != (type == LCD349_KV_STR)) {
        err = nvs_erase_key(h, key);
    }
    if (err == ESP_OK) {
        /* The core passes strings NUL-terminated at len. */
        err = type == LCD349_KV_STR ? nvs_set_str(h, key, (const char *)val) : nvs_set_blob(h, key, len ? val : "", len);
    }
    if (err == ESP_OK) {
        err = nvs_commit(h);
    }
    nvs_close(h);
    return from_nvs(err);
}

lcd349_err_t be_kv_erase(const char *ns, const char *key)
{
    nvs_handle_t h;
    esp_err_t err = nvs_open(ns, NVS_READWRITE, &h);
    if (err != ESP_OK) {
        return from_nvs(err);
    }
    err = key ? nvs_erase_key(h, key) : nvs_erase_all(h);
    if (err == ESP_ERR_NVS_NOT_FOUND) {
        err = ESP_OK; /* erasing what isn't there is OK */
    }
    if (err == ESP_OK) {
        err = nvs_commit(h);
    }
    nvs_close(h);
    return from_nvs(err);
}
