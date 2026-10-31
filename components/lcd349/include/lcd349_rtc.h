/* SPDX-License-Identifier: Apache-2.0
 *
 * The real-time clock, in UTC seconds since 1970. In the emulator it starts
 * from the host clock, or from a fixed time for repeatable screenshots.
 */
#pragma once

#include <stdint.h>

#include "lcd349_core.h"

#ifdef __cplusplus
extern "C" {
#endif

/** Reads the clock. */
lcd349_err_t lcd349_rtc_get(int64_t *unix_s);

/** Sets the clock. */
lcd349_err_t lcd349_rtc_set(int64_t unix_s);

#ifdef __cplusplus
}
#endif
