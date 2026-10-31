/* SPDX-License-Identifier: Apache-2.0
 *
 * inputs' guided checkpoint: on-screen prompts for the checks that need a
 * person, each result logged as an "@R {"guide":…}" line (see guide.c).
 */
#pragma once

#include <stdbool.h>

/** Builds the guide's screen and subscribes to input. Call once, after the
 * home screen is built and before the console is read. */
void guide_init(void);

/** The host's "@P guide [restart | from <step>]": starts the guide, or
 * resumes it at the step it reached; "restart" starts over, and "from" goes
 * on from a step, keeping the other steps' results. */
void guide_command(const char *args);

/** "@P ping" reached the console. */
void guide_ping(void);

/** The host's "@P wifi {"ssid":…,"password":…}": the network for the Wi-Fi step. */
void guide_wifi(const char *json);

/** True while the guide runs: the app then leaves the buttons to it. */
bool guide_running(void);
