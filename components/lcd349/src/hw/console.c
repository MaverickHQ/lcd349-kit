/* SPDX-License-Identifier: Apache-2.0
 *
 * Console: lines from the USB Serial/JTAG port (requirement 3.5). Reading
 * needs the port's driver, and once the driver is installed stdout must go
 * through it too (its VFS), or printing and reading fight over the FIFOs.
 * Whoever reads first installs both, unless something already has: the board
 * harness does the same (design D1-6). So the console works before
 * lcd349_init(), and logging keeps printing.
 *
 * Lines end at LF, CR or CRLF, as in the emulator. A partial line waits in
 * s_line for the next call, so a timeout never splits a line.
 */
#include <string.h>

#include "driver/usb_serial_jtag.h"
#include "driver/usb_serial_jtag_vfs.h"
#include "esp_log.h"
#include "freertos/FreeRTOS.h"
#include "freertos/task.h"

#include "hw.h"

static const char *TAG = "lcd349_console";
static char s_line[LCD349_CONSOLE_LINE_MAX + 1];
static size_t s_len;
static bool s_after_cr;

static bool console_up(void)
{
    if (usb_serial_jtag_is_driver_installed()) {
        return true;
    }
    usb_serial_jtag_driver_config_t cfg = USB_SERIAL_JTAG_DRIVER_CONFIG_DEFAULT();
    cfg.tx_buffer_size = 4096;
    cfg.rx_buffer_size = 1024;
    esp_err_t err = usb_serial_jtag_driver_install(&cfg);
    if (err != ESP_OK) {
        ESP_LOGE(TAG, "usb_serial_jtag_driver_install: %s", esp_err_to_name(err));
        return false;
    }
    usb_serial_jtag_vfs_use_driver();
    return true;
}

lcd349_err_t be_console_read_line(char *buf, size_t size, uint32_t timeout_ms)
{
    if (!console_up()) {
        return LCD349_ERR_UNAVAILABLE;
    }
    TickType_t start = xTaskGetTickCount(), wait = pdMS_TO_TICKS(timeout_ms);
    for (;;) {
        TickType_t gone = xTaskGetTickCount() - start;
        TickType_t left = gone < wait ? wait - gone : 0;
        uint8_t c;
        if (usb_serial_jtag_read_bytes(&c, 1, left) != 1) {
            if (left == 0) {
                return LCD349_ERR_TIMEOUT;
            }
            continue;
        }
        if (c == '\n' && s_after_cr) {
            s_after_cr = false; /* the LF of a CRLF */
            continue;
        }
        s_after_cr = c == '\r';
        if (c == '\r' || c == '\n') {
            size_t n = s_len < size - 1 ? s_len : size - 1;
            memcpy(buf, s_line, n);
            buf[n] = '\0';
            s_len = 0;
            return LCD349_OK;
        }
        if (s_len < LCD349_CONSOLE_LINE_MAX) {
            s_line[s_len++] = (char)c; /* the rest of an overlong line is dropped */
        }
    }
}
