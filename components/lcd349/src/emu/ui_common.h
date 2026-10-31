/* SPDX-License-Identifier: Apache-2.0
 * Internal: helpers shared by ui.tree (introspect.c) and ui.lint (lint.c).
 * Call with the LVGL lock held.
 */
#pragma once

#include <stdbool.h>
#include <stddef.h>

#include "lvgl.h"

const char *emu_ui_class(const lv_obj_t *obj);       /* "button", "label", ... */
bool emu_ui_visible(const lv_obj_t *obj);            /* not hidden, nor any ancestor */
bool emu_ui_interactive(const lv_obj_t *obj);        /* something a finger is meant to press */
bool emu_ui_is_label(const lv_obj_t *obj);
lv_color_t emu_ui_bg(const lv_obj_t *obj);           /* the colour behind obj's content */
lv_color_t emu_ui_text_color(const lv_obj_t *obj);   /* blended over emu_ui_bg */
const char *emu_ui_font_name(const lv_font_t *font); /* "montserrat_14", or "custom" */
/* "screen/0:obj/2:button/0:label"; returns out. */
char *emu_ui_path(const lv_obj_t *obj, char *out, size_t n);
/* Calls fn for obj and every descendant, depth first. */
void emu_ui_walk(lv_obj_t *obj, void (*fn)(lv_obj_t *o, void *ctx), void *ctx);
