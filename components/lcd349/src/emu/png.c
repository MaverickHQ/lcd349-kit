/* SPDX-License-Identifier: Apache-2.0
 *
 * Screenshot encoding: panel bytes to RGB, a small PNG writer (stored
 * deflate blocks, so no compression library), and base64. The Go tools
 * recompress PNGs when size matters.
 */
#include <stdlib.h>
#include <string.h>

#include "emu.h"
#include "rotate.h"

static uint32_t s_crc_table[256];

static void crc_init(void)
{
    if (s_crc_table[1]) {
        return;
    }
    for (uint32_t n = 0; n < 256; n++) {
        uint32_t c = n;
        for (int k = 0; k < 8; k++) {
            c = c & 1 ? 0xEDB88320u ^ (c >> 1) : c >> 1;
        }
        s_crc_table[n] = c;
    }
}

static uint32_t crc(const uint8_t *p, size_t n, uint32_t c)
{
    c ^= 0xFFFFFFFFu;
    while (n--) {
        c = s_crc_table[(c ^ *p++) & 0xFF] ^ (c >> 8);
    }
    return c ^ 0xFFFFFFFFu;
}

static uint8_t *put32(uint8_t *p, uint32_t v)
{
    p[0] = (uint8_t)(v >> 24);
    p[1] = (uint8_t)(v >> 16);
    p[2] = (uint8_t)(v >> 8);
    p[3] = (uint8_t)v;
    return p + 4;
}

static uint8_t *chunk(uint8_t *p, const char *type, const uint8_t *data, size_t len)
{
    p = put32(p, (uint32_t)len);
    uint8_t *start = p;
    memcpy(p, type, 4);
    p += 4;
    if (len) {
        memcpy(p, data, len);
    }
    p += len;
    return put32(p, crc(start, len + 4, 0));
}

uint8_t *emu_png_encode(const uint8_t *rgb, int w, int h, size_t *out_len)
{
    crc_init();
    size_t row = (size_t)w * 3 + 1;
    size_t raw_len = row * (size_t)h;
    size_t nblocks = (raw_len + 65534) / 65535;
    size_t z_len = 2 + raw_len + nblocks * 5 + 4;
    uint8_t *raw = malloc(raw_len);
    uint8_t *z = malloc(z_len);
    uint8_t *png = malloc(8 + 25 + 12 + z_len + 12);
    if (!raw || !z || !png) {
        free(raw);
        free(z);
        free(png);
        return NULL;
    }
    for (int y = 0; y < h; y++) {
        raw[row * (size_t)y] = 0; /* filter: none */
        memcpy(raw + row * (size_t)y + 1, rgb + (size_t)y * (size_t)w * 3, (size_t)w * 3);
    }
    /* zlib stream of stored blocks, then Adler-32. */
    uint8_t *q = z;
    *q++ = 0x78;
    *q++ = 0x01;
    uint32_t a = 1, b = 0;
    for (size_t off = 0; off < raw_len;) {
        size_t n = raw_len - off > 65535 ? 65535 : raw_len - off;
        *q++ = off + n == raw_len ? 1 : 0;
        *q++ = (uint8_t)n;
        *q++ = (uint8_t)(n >> 8);
        *q++ = (uint8_t)~n;
        *q++ = (uint8_t)(~n >> 8);
        memcpy(q, raw + off, n);
        for (size_t i = 0; i < n; i++) {
            a = (a + raw[off + i]) % 65521u;
            b = (b + a) % 65521u;
        }
        q += n;
        off += n;
    }
    q = put32(q, (b << 16) | a);

    uint8_t ihdr[13];
    put32(ihdr, (uint32_t)w);
    put32(ihdr + 4, (uint32_t)h);
    ihdr[8] = 8;  /* bit depth */
    ihdr[9] = 2;  /* RGB */
    ihdr[10] = 0; /* deflate */
    ihdr[11] = 0;
    ihdr[12] = 0;
    uint8_t *p = png;
    memcpy(p, "\x89PNG\r\n\x1a\n", 8);
    p += 8;
    p = chunk(p, "IHDR", ihdr, sizeof ihdr);
    p = chunk(p, "IDAT", z, (size_t)(q - z));
    p = chunk(p, "IEND", NULL, 0);
    *out_len = (size_t)(p - png);
    free(raw);
    free(z);
    return png;
}

char *emu_base64(const uint8_t *d, size_t n)
{
    static const char tbl[] = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/";
    char *out = malloc(4 * ((n + 2) / 3) + 1);
    if (!out) {
        return NULL;
    }
    char *o = out;
    size_t i = 0;
    for (; i + 2 < n; i += 3) {
        uint32_t v = (uint32_t)d[i] << 16 | (uint32_t)d[i + 1] << 8 | d[i + 2];
        *o++ = tbl[v >> 18];
        *o++ = tbl[(v >> 12) & 63];
        *o++ = tbl[(v >> 6) & 63];
        *o++ = tbl[v & 63];
    }
    if (i < n) {
        uint32_t v = (uint32_t)d[i] << 16 | (i + 1 < n ? (uint32_t)d[i + 1] << 8 : 0);
        *o++ = tbl[v >> 18];
        *o++ = tbl[(v >> 12) & 63];
        *o++ = i + 1 < n ? tbl[(v >> 6) & 63] : '=';
        *o++ = '=';
    }
    *o = '\0';
    return out;
}

static inline void rgb565_to_888(uint16_t c, uint8_t *o)
{
    uint32_t r = (c >> 11) & 0x1F, g = (c >> 5) & 0x3F, b = c & 0x1F;
    o[0] = (uint8_t)((r * 527 + 23) >> 6);
    o[1] = (uint8_t)((g * 259 + 33) >> 6);
    o[2] = (uint8_t)((b * 527 + 23) >> 6);
}

static inline uint16_t panel_px(const uint8_t *panel, int px, int py)
{
    const uint8_t *p = panel + 2 * ((size_t)py * LCD349_PANEL_W + (size_t)px);
    return LCD349_BYTE_SWAP ? (uint16_t)(p[0] << 8 | p[1]) : (uint16_t)(p[1] << 8 | p[0]);
}

uint8_t *emu_panel_to_rgb(const uint8_t *panel, bool raw, int scale, int *w, int *h)
{
    if (scale < 1) {
        scale = 1;
    }
    int bw = raw ? LCD349_PANEL_W : LCD349_LCD_W;
    int bh = raw ? LCD349_PANEL_H : LCD349_LCD_H;
    *w = bw * scale;
    *h = bh * scale;
    uint8_t *rgb = malloc((size_t)*w * (size_t)*h * 3);
    if (!rgb) {
        return NULL;
    }
    for (int y = 0; y < bh; y++) {
        for (int x = 0; x < bw; x++) {
            uint16_t px, py;
            if (raw) {
                px = (uint16_t)x;
                py = (uint16_t)y;
            } else {
                lcd349__landscape_to_panel((uint16_t)x, (uint16_t)y, LCD349_ROTATION_CW90, &px, &py);
            }
            uint8_t c[3];
            rgb565_to_888(panel_px(panel, px, py), c);
            for (int sy = 0; sy < scale; sy++) {
                uint8_t *o = rgb + (((size_t)(y * scale + sy) * (size_t)*w) + (size_t)x * (size_t)scale) * 3;
                for (int sx = 0; sx < scale; sx++, o += 3) {
                    memcpy(o, c, 3);
                }
            }
        }
    }
    return rgb;
}
