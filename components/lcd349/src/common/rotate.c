/* SPDX-License-Identifier: Apache-2.0 */
#include "rotate.h"

#include "lcd349_board.h"

#define LW LCD349_LCD_W
#define LH LCD349_LCD_H
#define PW LCD349_PANEL_W

_Static_assert(LCD349_PANEL_W == LCD349_LCD_H && LCD349_PANEL_H == LCD349_LCD_W, "panel is the landscape frame rotated");

static inline uint16_t swap16(uint16_t c)
{
    return (uint16_t)((c >> 8) | (c << 8));
}

void lcd349__rotate_frame(const uint16_t *land, uint16_t *panel, bool cw, bool swap)
{
    /* Landscape (x, y) lands on panel row x. Clockwise: column LH-1-y.
     * Counter-clockwise: row LW-1-x, column y. Writes run along panel rows. */
    for (int x = 0; x < LW; x++) {
        uint16_t *row = panel + (cw ? x : LW - 1 - x) * PW;
        for (int y = 0; y < LH; y++) {
            uint16_t c = land[y * LW + x];
            row[cw ? LH - 1 - y : y] = swap ? swap16(c) : c;
        }
    }
}

void lcd349__rotate_band(const uint16_t *land, uint16_t *out, int row0, int rows, bool cw, bool swap)
{
    /* Panel row r holds landscape column r (clockwise) or LW-1-r. This reads
     * the landscape frame a row at a time, which PSRAM's cache handles well;
     * the whole-frame version reads it a column at a time. */
    for (int y = 0; y < LH; y++) {
        const uint16_t *src = land + y * LW;
        uint16_t *dst = out + (cw ? LH - 1 - y : y);
        for (int i = 0; i < rows; i++) {
            uint16_t c = src[cw ? row0 + i : LW - 1 - (row0 + i)];
            dst[i * PW] = swap ? swap16(c) : c;
        }
    }
}

void lcd349__panel_to_landscape(uint16_t px, uint16_t py, bool cw, uint16_t *x, uint16_t *y)
{
    if (cw) {
        *x = py;
        *y = (uint16_t)(LH - 1 - px);
    } else {
        *x = (uint16_t)(LW - 1 - py);
        *y = px;
    }
}

void lcd349__landscape_to_panel(uint16_t x, uint16_t y, bool cw, uint16_t *px, uint16_t *py)
{
    if (cw) {
        *px = (uint16_t)(LH - 1 - y);
        *py = x;
    } else {
        *px = y;
        *py = (uint16_t)(LW - 1 - x);
    }
}
