/* SPDX-License-Identifier: Apache-2.0
 * Host test runner: exits 0 when every test passes.
 */
#include <stdlib.h>

#include "tests.h"
#include "unity.h"

void setUp(void) {}
void tearDown(void) {}

void app_main(void)
{
    UNITY_BEGIN();
    run_pure_tests();
    run_rotate_tests();
    run_touch_tests();
    run_kit_tests();
    run_lint_tests();
    exit(UNITY_END() == 0 ? 0 : 1);
}
