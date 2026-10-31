// SPDX-License-Identifier: Apache-2.0
// Requirement 1.1: the public API compiles and links from C++.
#include "lcd349.h"

extern "C" int lcd349_test_cpp_sizes(void)
{
    lcd349_config_t cfg = LCD349_CONFIG_DEFAULT();
    lcd349_classifier_t c;
    lcd349_classifier_cfg_t cc = {{1000, 0, 0}, 150};
    if (lcd349_classifier_init(&c, &cc) != LCD349_OK) {
        return -1;
    }
    return cfg.backlight_pct + lcd349_battery_pct_from_mv(4200);
}
