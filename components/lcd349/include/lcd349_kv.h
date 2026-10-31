/* SPDX-License-Identifier: Apache-2.0
 *
 * Namespaced key-value storage that survives power-off: NVS on the board, one
 * file per namespace in the emulator (cleared by --wipe). Flash wears out, so
 * don't write on every event. Safe from any task.
 */
#pragma once

#include <stddef.h>

#include "lcd349_core.h"

#ifdef __cplusplus
extern "C" {
#endif

/** Longest namespace or key, in bytes (the NVS limit). */
#define LCD349_KV_NAME_MAX 15
/** Largest blob or string value, in bytes. */
#define LCD349_KV_VALUE_MAX 4000

/**
 * Reads a string. On entry *len is the size of out; on success it is the
 * length including the NUL. Returns LCD349_ERR_NOT_FOUND if absent, and
 * LCD349_ERR_NO_MEM (with *len set to the size needed) if out is too small.
 */
lcd349_err_t lcd349_kv_get_str(const char *ns, const char *key, char *out, size_t *len);

/** Writes a string. */
lcd349_err_t lcd349_kv_set_str(const char *ns, const char *key, const char *val);

/**
 * Reads a blob; *len works as in lcd349_kv_get_str(), without the NUL.
 * Values are typed: reading a string key as a blob, or a blob key as a
 * string, returns LCD349_ERR_ARG.
 */
lcd349_err_t lcd349_kv_get_blob(const char *ns, const char *key, void *out, size_t *len);

/** Writes a blob of len bytes. */
lcd349_err_t lcd349_kv_set_blob(const char *ns, const char *key, const void *val, size_t len);

/** Erases one key, or the whole namespace when key is NULL. Erasing what isn't there is OK. */
lcd349_err_t lcd349_kv_erase(const char *ns, const char *key);

#ifdef __cplusplus
}
#endif
