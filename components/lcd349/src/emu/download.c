/* SPDX-License-Identifier: Apache-2.0
 *
 * ROM download mode (requirement 3.5). On the board, holding BOOT (GPIO0)
 * through a reset starts the ROM's serial downloader and the product never
 * runs. The emulator does the same: emu_boot() calls this before main(), so
 * neither FreeRTOS nor app_main ever starts. It presents "DOWNLOAD MODE" and
 * serves a reduced control socket (info, state, screenshot, power) so a
 * reset can bring the product back.
 */
#include <stdio.h>
#include <stdlib.h>

#include "emu.h"
#include "lvgl.h"
#include "rotate.h"

void emu_control_serve_download(void);

static uint16_t *s_panel;

static uint32_t tick(void)
{
    return lcd349_time_ms();
}

static void flush(lv_display_t *d, const lv_area_t *area, uint8_t *px)
{
    (void)area;
    lcd349__rotate_frame((const uint16_t *)px, s_panel, LCD349_ROTATION_CW90, LCD349_BYTE_SWAP);
    emu_fb_write((const uint8_t *)s_panel, EMU_PANEL_BYTES);
    lv_display_flush_ready(d);
}

static void render(void)
{
    size_t bytes = (size_t)LCD349_LCD_W * LCD349_LCD_H * 2;
    void *draw = malloc(bytes);
    s_panel = malloc(bytes);
    if (!draw || !s_panel) {
        return;
    }
    lv_init();
    lv_tick_set_cb(tick);
    lv_display_t *d = lv_display_create(LCD349_LCD_W, LCD349_LCD_H);
    lv_display_set_color_format(d, LV_COLOR_FORMAT_RGB565);
    lv_display_set_buffers(d, draw, NULL, (uint32_t)bytes, LV_DISPLAY_RENDER_MODE_FULL);
    lv_display_set_flush_cb(d, flush);
    lv_obj_t *scr = lv_screen_active();
    lv_obj_set_style_bg_color(scr, lv_color_black(), 0);
    lv_obj_set_style_bg_opa(scr, LV_OPA_COVER, 0);
    lv_obj_t *l = lv_label_create(scr);
    lv_label_set_text(l, "DOWNLOAD MODE\nBOOT was held through the reset: the product isn't running.");
    lv_obj_set_style_text_color(l, lv_color_white(), 0);
    lv_obj_set_style_text_align(l, LV_TEXT_ALIGN_CENTER, 0);
    lv_obj_center(l);
    lv_refr_now(d);
}

void emu_download_mode(void)
{
    emu_fb_open();
    emu_fb_hdr()->backlight = 100;
    emu_set_power(EMU_POWER_DOWNLOAD);
    printf("lcd349 emulator: BOOT held at reset: ROM download mode; the product doesn't start\n");
    printf("waiting for download\n");
    render();
    emu_control_serve_download();
    exit(LCD349_EXIT_OFF);
}
