/* SPDX-License-Identifier: Apache-2.0
 * The same source for both targets: no target checks (requirement 3.1).
 */
#include "lcd349.h"

void app_main(void)
{
    lcd349_err_t err = lcd349_init(NULL);
    if (!lcd349_display()) {
        return; /* no display: lcd349_board_status() says why */
    }
    lcd349_ui_lock();
    lv_obj_t *label = lv_label_create(lv_screen_active());
    lv_label_set_text_fmt(label, "lcd349 %s: %s", LCD349_VERSION_STR, lcd349_err_str(err));
    lv_obj_center(label);
    lcd349_ui_unlock();
}
