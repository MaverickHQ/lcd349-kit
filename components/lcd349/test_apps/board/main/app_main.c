/* SPDX-License-Identifier: Apache-2.0
 * The board test app: every test_*.c in this directory, run by the harness
 * as tools/boardtest asks.
 */
#include "lcd349_harness.h"

void app_main(void)
{
    lcd349_harness_run();
}
