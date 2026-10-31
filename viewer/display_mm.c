/* SPDX-License-Identifier: Apache-2.0 */
#include "display_mm.h"

#ifdef __APPLE__
#include <CoreGraphics/CoreGraphics.h>
#include <math.h>

float view_display_mm_w(int x, int y, int w, int h)
{
    CGDirectDisplayID ids[16];
    uint32_t n = 0;
    if (CGGetActiveDisplayList(16, ids, &n) != kCGErrorSuccess) {
        return 0;
    }
    for (uint32_t i = 0; i < n; i++) {
        CGRect b = CGDisplayBounds(ids[i]);
        if (fabs(b.origin.x - x) < 1 && fabs(b.origin.y - y) < 1 && fabs(b.size.width - w) < 1 && fabs(b.size.height - h) < 1) {
            CGSize mm = CGDisplayScreenSize(ids[i]);
            return mm.width > 0 ? (float)mm.width : 0;
        }
    }
    return 0;
}
#else
float view_display_mm_w(int x, int y, int w, int h)
{
    (void)x;
    (void)y;
    (void)w;
    (void)h;
    return 0; /* SDL2's DPI is measured on X11; see view.c */
}
#endif
