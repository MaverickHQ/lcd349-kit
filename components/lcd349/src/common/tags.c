/* SPDX-License-Identifier: Apache-2.0
 *
 * UI tags for the checker (requirement 6.4). Tags live in a side table keyed
 * by object, so products keep LVGL's user_data for themselves. An entry is
 * dropped when its object is deleted.
 */
#include <string.h>

#include "kit.h"

#define TAGS_LEN 64
#define TAG_MAX 31

typedef struct {
    const lv_obj_t *obj;
    char tags[TAGS_LEN]; /* comma-separated */
} entry_t;

static entry_t *s_entries;
static size_t s_n, s_cap;

static entry_t *find(const lv_obj_t *obj)
{
    for (size_t i = 0; i < s_n; i++) {
        if (s_entries[i].obj == obj) {
            return &s_entries[i];
        }
    }
    return NULL;
}

static void on_delete(lv_event_t *e)
{
    const lv_obj_t *obj = lv_event_get_target(e);
    entry_t *en = find(obj);
    if (en) {
        *en = s_entries[--s_n];
    }
}

/* Tags are short words: lower-case letters, digits, '-', '_' and ':'. */
static bool tag_ok(const char *tag)
{
    size_t n = strnlen(tag, TAG_MAX + 1);
    if (n == 0 || n > TAG_MAX) {
        return false;
    }
    for (size_t i = 0; i < n; i++) {
        char c = tag[i];
        if (!((c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-' || c == '_' || c == ':')) {
            return false;
        }
    }
    return true;
}

static bool contains(const char *list, const char *tag)
{
    size_t n = strlen(tag);
    for (const char *p = list; *p;) {
        const char *end = strchr(p, ',');
        size_t len = end ? (size_t)(end - p) : strlen(p);
        if (len == n && strncmp(p, tag, n) == 0) {
            return true;
        }
        if (!end) {
            break;
        }
        p = end + 1;
    }
    return false;
}

lcd349_err_t lcd349_ui_tag(lv_obj_t *obj, const char *tag)
{
    if (!obj || !tag || !tag_ok(tag)) {
        return LCD349_ERR_ARG;
    }
    entry_t *en = find(obj);
    if (!en) {
        if (s_n == s_cap) {
            size_t cap = s_cap ? s_cap * 2 : 16;
            entry_t *grown = lv_realloc(s_entries, cap * sizeof *grown);
            if (!grown) {
                return LCD349_ERR_NO_MEM;
            }
            s_entries = grown;
            s_cap = cap;
        }
        en = &s_entries[s_n++];
        en->obj = obj;
        en->tags[0] = '\0';
        lv_obj_add_event_cb(obj, on_delete, LV_EVENT_DELETE, NULL);
    }
    if (contains(en->tags, tag)) {
        return LCD349_OK;
    }
    size_t have = strlen(en->tags);
    size_t need = strlen(tag) + (have ? 1 : 0);
    if (have + need >= TAGS_LEN) {
        return LCD349_ERR_NO_MEM;
    }
    if (have) {
        en->tags[have++] = ',';
    }
    strcpy(en->tags + have, tag);
    return LCD349_OK;
}

bool lcd349_ui_has_tag(const lv_obj_t *obj, const char *tag)
{
    const entry_t *en = obj && tag ? find(obj) : NULL;
    return en && contains(en->tags, tag);
}

size_t lcd349_ui_tags(const lv_obj_t *obj, char *out, size_t out_len)
{
    if (!out || out_len == 0) {
        return 0;
    }
    out[0] = '\0';
    const entry_t *en = obj ? find(obj) : NULL;
    if (!en || !en->tags[0]) {
        return 0;
    }
    strncpy(out, en->tags, out_len - 1);
    out[out_len - 1] = '\0';
    size_t n = 1;
    for (const char *p = en->tags; *p; p++) {
        n += *p == ',';
    }
    return n;
}
