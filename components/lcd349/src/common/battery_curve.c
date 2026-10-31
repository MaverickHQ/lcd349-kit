/* SPDX-License-Identifier: Apache-2.0 */
#include "lcd349_power.h"

uint8_t lcd349_battery_pct_from_mv(uint16_t mv)
{
    const lcd349_board_info_t *b = lcd349_board_info();
    const lcd349_battery_point_t *c = b->battery_curve;
    uint8_t n = b->battery_curve_len;
    if (n == 0) {
        return 0;
    }
    if (mv >= c[0].mv) {
        return c[0].pct;
    }
    for (uint8_t i = 1; i < n; i++) {
        if (mv >= c[i].mv) {
            /* Linear between c[i] (lower) and c[i-1] (higher), rounded. */
            uint32_t span_mv = c[i - 1].mv - c[i].mv;
            uint32_t span_pct = c[i - 1].pct - c[i].pct;
            uint32_t into = mv - c[i].mv;
            return (uint8_t)(c[i].pct + (into * span_pct + span_mv / 2) / span_mv);
        }
    }
    return c[n - 1].pct;
}
