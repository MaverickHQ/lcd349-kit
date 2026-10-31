/* SPDX-License-Identifier: Apache-2.0
 *
 * Power: the power latch (TCA9554 P6) and BL_EN (P1), written by register
 * (kit ADR 0001), power off, and the power source.
 */
#include "driver/usb_serial_jtag.h"

#include "expander.h"
#include "hw.h"

/* The latch first: on battery the board stays on only while P6 is high. */
lcd349_err_t be_power_init(void)
{
    lcd349_err_t err = hw_expander_out(LCD349_EXPANDER_PIN_SYS_EN, true);
    if (err == LCD349_OK) {
        err = hw_expander_out(LCD349_EXPANDER_PIN_BL_EN, true);
    }
    return err;
}

/* P6 low lets the latch go: on battery the board powers off. */
lcd349_err_t be_power_hold(bool hold)
{
    return hw_expander_out(LCD349_EXPANDER_PIN_SYS_EN, hold);
}

/* The board routes no VBUS or charger signal to a GPIO or the expander
 * (spikes/hw/RESULTS.md), so a USB host's start-of-frame packets on the
 * USB Serial/JTAG port stand for USB (D1-10). A charger with no data sends
 * none, and reads as BATTERY. */
lcd349_power_source_t be_power_source(void)
{
    return usb_serial_jtag_is_connected() ? LCD349_POWER_SOURCE_USB : LCD349_POWER_SOURCE_BATTERY;
}
