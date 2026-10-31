/* SPDX-License-Identifier: Apache-2.0
 *
 * The host display's physical width, for physical scale. SDL's display DPI
 * isn't physical everywhere: Homebrew's sdl2-compat on macOS reports 96 times
 * the content scale (192 on a 2x Retina panel whose real density is 127
 * points per inch), so macOS asks CoreGraphics instead.
 */
#pragma once

/* The width in millimetres of the display whose bounds, in window points,
 * are (x, y, w, h); 0 if unknown on this platform. */
float view_display_mm_w(int x, int y, int w, int h);
