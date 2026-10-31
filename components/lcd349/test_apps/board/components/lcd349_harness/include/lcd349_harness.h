/* SPDX-License-Identifier: Apache-2.0
 *
 * The board test harness (design D1-6, seam S1-4): runs an esp32s3 Unity
 * app's tests as tools/boardtest asks, over the USB Serial/JTAG console. The
 * kit's test_apps/board uses it, and so can another project's board checks
 * (boardtest -C <project>): add this directory to EXTRA_COMPONENT_DIRS and
 * call lcd349_harness_run() from app_main.
 *
 * Tests register themselves with Unity's TEST_CASE, or
 * TEST_CASE_MULTIPLE_STAGES, and a subsystem tag such as "[power]".
 *
 * The protocol, one line each way:
 *   board: @R {"boot":{"reason":"power_on|software|crash|brownout|other"}}
 *          once per boot, before lcd349_init;
 *   board: @R {"init":...} and @R {"mem":{"internal_free":N,...}}, after it;
 *   board: @Q cmd, every 2 s until answered;
 *   host:  @P all | @P only <tag> | @P resume <tag|*> <index> <stage>;
 *   board: @R {"run":{"tag":...,"index":i,"stage":s,"stages":n,"name":...}}
 *          before each test (or stage), then Unity's result line;
 *   board: Unity's summary ("N Tests M Failures M Ignored") at the end, and
 *          before any stage that isn't a test's last; then @R {"done":true}.
 * A stage that isn't its test's last ends by restarting the board, and the
 * host answers the next boot's @Q cmd with @P resume for the next stage.
 * Opening the port resets the board, so a test that outlives a USB drop
 * (light sleep) is written in stages too, keeping its data in RTC memory.
 *
 * A test may ask the host for a line: it prints "@Q <what> [args]" and the
 * host answers "@P <what> ...". @Q wifi [n] gets the n-th network of
 * test-wifi.toml as JSON; any other request is answered "@P <what>" (for
 * example @Q ping, @P ping).
 */
#pragma once

#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>

#ifdef __cplusplus
extern "C" {
#endif

/* Installs the USB Serial/JTAG driver and its VFS unless something already
 * has (the kit's console does the same), prints the boot line, runs
 * lcd349_init(NULL), prints the init and memory lines, and then runs the
 * tests the host asks for. Never returns. */
void lcd349_harness_run(void);

/* Asks the host for a line: prints "@Q <what>" and waits up to timeout_ms for
 * "@P <word>" or "@P <word> <rest>", where word is what's first word; out
 * gets rest ("" if none). Other lines are dropped. False on timeout. */
bool lcd349_harness_request(const char *what, char *out, size_t size, uint32_t timeout_ms);

/* The n-th network (from 1) of the host's test-wifi.toml. The host never
 * prints them, and the board mustn't either. False if the host has none. */
bool lcd349_harness_wifi(int n, char *ssid, size_t ssid_size, char *password, size_t password_size);

#ifdef __cplusplus
}
#endif
