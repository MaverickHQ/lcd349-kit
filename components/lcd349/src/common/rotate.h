/* SPDX-License-Identifier: Apache-2.0
 *
 * Landscape <-> panel transforms, pure. The panel is portrait
 * (LCD349_PANEL_W x LCD349_PANEL_H) with no hardware rotation, so the kit
 * rotates each landscape frame in software and swaps RGB565 bytes into the
 * order the panel takes. Each backend rotates the landscape frame it is
 * handed (be_panel_flush), so the emulator sees exactly the board's bytes
 * (requirement 3.2).
 */
#pragma once

#include <stdbool.h>
#include <stdint.h>

/* Rotates a LCD349_LCD_W x LCD349_LCD_H frame into the panel orientation.
 * cw: rotate 90 degrees clockwise (else counter-clockwise). swap: swap each
 * pixel's two bytes. land and panel must not overlap. */
void lcd349__rotate_frame(const uint16_t *land, uint16_t *panel, bool cw, bool swap);

/* The same transform for panel rows [row0, row0 + rows) only, written to out
 * (rows x LCD349_PANEL_W pixels), byte for byte what lcd349__rotate_frame
 * puts in those rows. The rows must lie within LCD349_PANEL_H; nothing
 * checks it. The board rotates a frame in strips this way, each into a DMA
 * buffer while the previous one is on the bus. */
void lcd349__rotate_band(const uint16_t *land, uint16_t *out, int row0, int rows, bool cw, bool swap);

/* Maps a panel-native point to landscape coordinates for the same rotation. */
void lcd349__panel_to_landscape(uint16_t px, uint16_t py, bool cw, uint16_t *x, uint16_t *y);

/* The inverse, used by the emulator to turn landscape input into panel-native touch. */
void lcd349__landscape_to_panel(uint16_t x, uint16_t y, bool cw, uint16_t *px, uint16_t *py);
