/* SPDX-License-Identifier: Apache-2.0
 *
 * The serial console: the board's USB-C port (USB Serial/JTAG), or in the
 * emulator the pseudo-terminal `lcd349 run` publishes as the instance's serial
 * port. Write to it with printf or ESP_LOG; read lines a host tool sends with
 * lcd349_console_read_line(). Host tools see the same bytes either way.
 */
#pragma once

#include <stddef.h>
#include <stdint.h>

#include "lcd349_core.h"

#ifdef __cplusplus
extern "C" {
#endif

/** Longest line the console keeps, without its line ending. */
#define LCD349_CONSOLE_LINE_MAX 1023

/**
 * Waits up to timeout_ms for the next line from the host (0 = don't wait) and
 * copies it into buf without its line ending (LF, CR or CRLF), NUL-terminated.
 * A line longer than size - 1 bytes (or LCD349_CONSOLE_LINE_MAX) is cut and
 * the rest of it dropped. Lines arriving while nobody reads are kept up to a
 * small limit, then dropped, as a full receive buffer would.
 *
 * Works before lcd349_init(). Read from one task, and never from lcd349_ui:
 * it blocks.
 *
 * Returns LCD349_OK, LCD349_ERR_TIMEOUT, LCD349_ERR_ARG (buf NULL or size < 2),
 * or LCD349_ERR_UNAVAILABLE when the console can't be read.
 */
lcd349_err_t lcd349_console_read_line(char *buf, size_t size, uint32_t timeout_ms);

#ifdef __cplusplus
}
#endif
