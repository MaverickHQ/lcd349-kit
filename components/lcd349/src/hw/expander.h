/* SPDX-License-Identifier: Apache-2.0
 *
 * The TCA9554 expander's registers, written directly (kit ADR 0001): never
 * reset the chip, and set an output's level before its direction. The power
 * lane fills it (task 2.1); the audio lane uses it for the amplifier (P7).
 */
#pragma once

#include <stdbool.h>
#include <stdint.h>

#include "hw.h"

/* The TCA9554's registers (datasheet). Reading the output register gives the
 * levels the chip drives, not the pins' levels. */
#define HW_EXPANDER_REG_INPUT 0x00
#define HW_EXPANDER_REG_OUTPUT 0x01
#define HW_EXPANDER_REG_POLARITY 0x02
#define HW_EXPANDER_REG_CONFIG 0x03 /* 1 = input (the chip's reset state) */

/* Both calls return LCD349_ERR_UNAVAILABLE if the main bus or the device
 * couldn't be added (it is tried once), and LCD349_ERR_IO if the chip doesn't
 * answer. */

/* Reads one register. Also the board tests' hook. */
lcd349_err_t hw_expander_read(uint8_t reg, uint8_t *val);

/* Drives pin (P0-P7, LCD349_EXPANDER_PIN_*) as an output at the level high:
 * the output register first, then the pin's direction, keeping every other
 * pin as it is. Safe from any task. LCD349_ERR_ARG for a pin above 7. */
lcd349_err_t hw_expander_out(uint8_t pin, bool high);
