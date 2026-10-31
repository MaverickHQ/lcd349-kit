/* SPDX-License-Identifier: Apache-2.0
 *
 * Emulated NVS: typed, namespaced key-value storage kept in memory and written
 * through to one file per namespace in the instance's storage directory
 * (<instance>/storage, or LCD349_STORAGE_DIR), atomically. It survives resets
 * and is cleared by LCD349_WIPE=1 (requirement 3.10). Without an instance
 * directory it lives in memory only.
 */
#include <dirent.h>
#include <errno.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/stat.h>
#include <unistd.h>

#include "emu.h"
#include "freertos/FreeRTOS.h"
#include "freertos/semphr.h"

#define FILE_MAGIC "L349KV1\n"

typedef struct entry {
    char key[LCD349_KV_NAME_MAX + 1];
    lcd349_kv_type_t type;
    size_t len;
    uint8_t *data;
    struct entry *next;
} entry_t;

typedef struct ns {
    char name[LCD349_KV_NAME_MAX + 1];
    entry_t *entries;
    struct ns *next;
} ns_t;

/* A FreeRTOS mutex, not a critical section: calls hold it across malloc and file I/O. */
static SemaphoreHandle_t s_mu;
static ns_t *s_ns;
static char s_dir[400];
static bool s_have_dir;

/* File names hex-encode the namespace, so any name is a safe file name. */
static void ns_path(const char *ns, char *out, size_t n)
{
    char hex[2 * LCD349_KV_NAME_MAX + 1] = {0};
    for (size_t i = 0; ns[i] && i < LCD349_KV_NAME_MAX; i++) {
        snprintf(hex + 2 * i, 3, "%02x", (unsigned char)ns[i]);
    }
    snprintf(out, n, "%s/ns-%s.kv", s_dir, hex);
}

static ns_t *find_ns(const char *name, bool create)
{
    for (ns_t *n = s_ns; n; n = n->next) {
        if (strcmp(n->name, name) == 0) {
            return n;
        }
    }
    if (!create) {
        return NULL;
    }
    ns_t *n = calloc(1, sizeof *n);
    if (n) {
        snprintf(n->name, sizeof n->name, "%s", name);
        n->next = s_ns;
        s_ns = n;
    }
    return n;
}

static entry_t *find_entry(ns_t *n, const char *key)
{
    for (entry_t *e = n ? n->entries : NULL; e; e = e->next) {
        if (strcmp(e->key, key) == 0) {
            return e;
        }
    }
    return NULL;
}

static bool save_ns(const ns_t *n)
{
    if (!s_have_dir) {
        return true;
    }
    size_t total = strlen(FILE_MAGIC) + 1 + strlen(n->name);
    for (const entry_t *e = n->entries; e; e = e->next) {
        total += 1 + strlen(e->key) + 1 + 4 + e->len;
    }
    uint8_t *buf = malloc(total), *p = buf;
    if (!buf) {
        return false;
    }
    memcpy(p, FILE_MAGIC, strlen(FILE_MAGIC));
    p += strlen(FILE_MAGIC);
    *p++ = (uint8_t)strlen(n->name);
    memcpy(p, n->name, strlen(n->name));
    p += strlen(n->name);
    for (const entry_t *e = n->entries; e; e = e->next) {
        size_t kl = strlen(e->key);
        *p++ = (uint8_t)kl;
        memcpy(p, e->key, kl);
        p += kl;
        *p++ = (uint8_t)e->type;
        uint32_t l = (uint32_t)e->len;
        memcpy(p, &l, 4); /* little-endian hosts only: macOS and Linux on x86_64/arm64 */
        p += 4;
        memcpy(p, e->data, e->len);
        p += e->len;
    }
    char path[512];
    ns_path(n->name, path, sizeof path);
    bool ok = emu_write_atomic(path, buf, (size_t)(p - buf));
    free(buf);
    return ok;
}

static void load_file(const char *path)
{
    FILE *f = fopen(path, "rb");
    if (!f) {
        return;
    }
    /* On the heap: 64 KB is more than the main task's whole stack, and this
     * runs there, in lcd349_init, whenever an earlier boot left a file. */
    enum { FILE_MAX = 64 * 1024 };
    uint8_t *buf = malloc(FILE_MAX);
    if (!buf) {
        fclose(f);
        return;
    }
    size_t len = fread(buf, 1, FILE_MAX, f);
    fclose(f);
    size_t ml = strlen(FILE_MAGIC);
    if (len < ml + 1 || memcmp(buf, FILE_MAGIC, ml) != 0) {
        fprintf(stderr, "lcd349_emu: ignoring unreadable storage file %s\n", path);
        free(buf);
        return;
    }
    size_t i = ml;
    char name[LCD349_KV_NAME_MAX + 1] = {0};
    size_t nl = buf[i++];
    if (nl == 0 || nl > LCD349_KV_NAME_MAX || i + nl > len) {
        free(buf);
        return;
    }
    memcpy(name, buf + i, nl);
    i += nl;
    ns_t *n = find_ns(name, true);
    while (n && i < len) {
        entry_t *e = calloc(1, sizeof *e);
        size_t kl = buf[i++];
        if (!e || kl == 0 || kl > LCD349_KV_NAME_MAX || i + kl + 5 > len) {
            free(e);
            break;
        }
        memcpy(e->key, buf + i, kl);
        i += kl;
        e->type = (lcd349_kv_type_t)buf[i++];
        uint32_t l;
        memcpy(&l, buf + i, 4);
        i += 4;
        if (i + l > len || l > LCD349_KV_VALUE_MAX) {
            free(e);
            break;
        }
        e->len = l;
        e->data = malloc(l ? l : 1);
        memcpy(e->data, buf + i, l);
        i += l;
        e->next = n->entries;
        n->entries = e;
    }
    free(buf);
}

lcd349_err_t emu_kv_init(void)
{
    s_mu = xSemaphoreCreateMutex();
    if (!s_mu) {
        return LCD349_ERR_NO_MEM;
    }
    const char *inst = emu_instance_dir();
    const char *dir = getenv("LCD349_STORAGE_DIR");
    if (dir && *dir) {
        snprintf(s_dir, sizeof s_dir, "%s", dir);
    } else if (inst) {
        snprintf(s_dir, sizeof s_dir, "%s/storage", inst);
    } else {
        return LCD349_OK; /* memory only */
    }
    if (mkdir(s_dir, 0700) != 0 && errno != EEXIST) {
        fprintf(stderr, "lcd349_emu: can't create storage directory %s: %s\n", s_dir, strerror(errno));
        return LCD349_ERR_IO;
    }
    s_have_dir = true;
    const char *wipe = getenv("LCD349_WIPE");
    bool wiping = wipe && strcmp(wipe, "1") == 0;
    DIR *d = opendir(s_dir);
    struct dirent *de;
    while (d && (de = readdir(d))) {
        size_t l = strlen(de->d_name);
        if (strncmp(de->d_name, "ns-", 3) != 0 || l < 4 || strcmp(de->d_name + l - 3, ".kv") != 0) {
            continue;
        }
        char path[800];
        snprintf(path, sizeof path, "%s/%s", s_dir, de->d_name);
        if (wiping) {
            unlink(path);
        } else {
            load_file(path);
        }
    }
    if (d) {
        closedir(d);
    }
    return LCD349_OK;
}

lcd349_err_t be_kv_get(const char *ns, const char *key, lcd349_kv_type_t type, void *out, size_t *len)
{
    lcd349_err_t err = LCD349_OK;
    xSemaphoreTake(s_mu, portMAX_DELAY);
    entry_t *e = find_entry(find_ns(ns, false), key);
    if (!e) {
        err = LCD349_ERR_NOT_FOUND;
    } else if (e->type != type) {
        err = LCD349_ERR_ARG;
    } else if (e->len > *len) {
        *len = e->len;
        err = LCD349_ERR_NO_MEM;
    } else {
        memcpy(out, e->data, e->len);
        *len = e->len;
    }
    xSemaphoreGive(s_mu);
    return err;
}

lcd349_err_t be_kv_set(const char *ns, const char *key, lcd349_kv_type_t type, const void *val, size_t len)
{
    xSemaphoreTake(s_mu, portMAX_DELAY);
    ns_t *n = find_ns(ns, true);
    entry_t *e = n ? find_entry(n, key) : NULL;
    if (n && !e) {
        e = calloc(1, sizeof *e);
        if (e) {
            snprintf(e->key, sizeof e->key, "%s", key);
            e->next = n->entries;
            n->entries = e;
        }
    }
    uint8_t *copy = malloc(len ? len : 1);
    if (!e || !copy) {
        free(copy);
        xSemaphoreGive(s_mu);
        return LCD349_ERR_NO_MEM;
    }
    if (len) {
        memcpy(copy, val, len);
    }
    free(e->data);
    e->data = copy;
    e->len = len;
    e->type = type;
    bool ok = save_ns(n);
    xSemaphoreGive(s_mu);
    return ok ? LCD349_OK : LCD349_ERR_IO;
}

lcd349_err_t be_kv_erase(const char *ns, const char *key)
{
    xSemaphoreTake(s_mu, portMAX_DELAY);
    ns_t *n = find_ns(ns, false);
    bool ok = true;
    if (n) {
        for (entry_t **pp = &n->entries; *pp;) {
            entry_t *e = *pp;
            if (!key || strcmp(e->key, key) == 0) {
                *pp = e->next;
                free(e->data);
                free(e);
            } else {
                pp = &e->next;
            }
        }
        ok = save_ns(n);
    }
    xSemaphoreGive(s_mu);
    return ok ? LCD349_OK : LCD349_ERR_IO;
}
