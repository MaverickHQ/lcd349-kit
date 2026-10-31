/* SPDX-License-Identifier: Apache-2.0
 *
 * Internal to the hardware backend (src/hw): what the subsystem files share.
 * Each subsystem file (power.c, display.c, ...) implements its own be_*
 * functions from kit.h; hw_backend.c keeps the rest and owns the two I2C
 * buses.
 */
#pragma once

#include "driver/i2c_master.h"

#include "kit.h"

/* The main I2C bus (LCD349_PIN_I2C_*: the expander, the codec, the RTC, the
 * IMU) and the touch controller's own bus (LCD349_PIN_TOUCH_*). Both buses
 * come up on the first call to either; be_power_init makes it, on
 * lcd349_init's thread, before anything else touches a bus. Add devices with
 * i2c_master_bus_add_device at their own speed. NULL if the bus couldn't be
 * created. */
i2c_master_bus_handle_t hw_i2c_main(void);
i2c_master_bus_handle_t hw_i2c_touch(void);

/* Every subsystem reports this until its lane implements it. */
#define HW_TODO LCD349_ERR_UNAVAILABLE
