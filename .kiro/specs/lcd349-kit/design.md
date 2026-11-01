# Design Document

## Overview

```
                    one product source tree (ESP-IDF project, depends on component "lcd349")
                         │                                        │
     idf.py set-target esp32s3                          lcd349 build  (ESP-IDF linux target, build-emu/)
                         │                                        │
              ┌──────────▼──────────┐               ┌─────────────▼──────────────────────────────┐
              │ firmware.bin         │               │ product.elf  (always headless)             │
              │  lcd349 SDK          │               │  lcd349 SDK                                 │
              │   common + hw/       │               │   common + emu/  ── device model            │
              │   (esp_lcd_axs15231b,│               │     framebuffer file fb.bin (mmap) ────────┼──► lcd349-view (SDL2 window,
              │    tca9554, codec,   │               │     control server (FreeRTOS task) ◄───────┼──┐  optional, separate process)
              │    esp_wifi, NVS)    │               │     stdio on a pty pair ───────────────────┼─┐│
              └──────────┬──────────┘               └────────────────────────────────────────────┘ ││
                         ▼                                                                         ││ JSON-RPC over a
                Waveshare ESP32-S3-Touch-LCD-3.49            serial.log + virtual serial port ◄──────┘│ Unix socket (0600)
                                                                                                      │
          lcd349 (Go): CLI  ──────────────────────────────────────────────────────────────────────────┤
          lcd349 mcp (stdio MCP server) ◄── Claude Code plugin "lcd349" (skill + .mcp.json) ────────────┘
```

- **The SDK draws the hardware boundary.** Products use LVGL directly for their UI and the `lcd349` API for everything physical. The SDK has one shared core (`src/common`) and two thin backends (`src/hw`, `src/emu`), chosen by `IDF_TARGET`.
- **The emulated product always runs headless.** A window is a separate viewer process attached to the instance's shared framebuffer and control socket.
- **People, scripts and agents use the same controls.** The CLI, the MCP server and the viewer are all clients of one control protocol.
- **device.json is the single source of truth** for facts, limits, timings and design rules. A generator feeds it to the SDK, emulator, lint, skill and docs.

## Key Decisions

| ID | Decision | Choice | Rationale |
|---|---|---|---|
| D1 | How products run in the emulator | Build the unchanged product for ESP-IDF's `linux` target (FreeRTOS tasks as pthreads) with the SDK's emulator backend. **Confirmed by spike 0.2 on 2026-10-12** (`spikes/emu/RESULTS.md`). | One build system and one source tree; FreeRTOS, esp_event, NVS and logging behave as on the device. The spike built LVGL 9.6.0 unmodified, used host BSD sockets from FreeRTOS tasks, and kept board and emulator builds side by side. The target's limits (preview status, preemptive scheduling of host threads without libc-lock safety, partial component support) are confined to the SDK, which replaces every unsupported component (esp_lcd, esp_wifi, driver); see §Threading on the `linux` target. |
| D2 | API shape | A small C API for the physical board. Products use LVGL 9 directly for UI. | Agents and developers already know LVGL, and LVGL's own docs and MCP exist. A UI abstraction on top would be one more thing to learn and to emulate. |
| D3 | Window process | The product never links SDL. A separate viewer (`lcd349-view`, C + SDL2) maps the framebuffer file and sends input over the control socket. | On macOS, SDL video must run on the main thread, and the `linux` target's main thread is parked inside the FreeRTOS scheduler (`port.c` waits for a signal there), with ticks delivered as SIGALRM. A separate process avoids both problems, keeps products SDL-free, and means agents and people drive the exact same instance. Spike 0.2 proved the loop: viewer click → touch → LVGL → frame → viewer. |
| D4 | Control path | JSON-RPC 2.0 over a Unix socket, served by a FreeRTOS task inside the product. Screen work runs on the LVGL task through a request queue. The CLI and MCP server are thin Go clients. | There is no maintained C MCP SDK, and putting MCP inside products would couple them to agent tooling. A Unix socket is local-only (R13.1). The `linux` target can't block in FreeRTOS from foreign threads, so the server is itself a FreeRTOS task using non-blocking I/O. |
| D5 | Device knowledge | `device/device.json` plus a schema and a Go generator feed the SDK constants, emulator defaults, lint thresholds, skill references and docs. | Facts can't drift between the code, the lint and what the agent is told. Every fact carries a source and a date (R10.3). |
| D6 | Agent observability | Structured data first: `ui.tree`, `ui.lint` and `wait`, with screenshots alongside. Deterministic options: `--clock`, `--wipe`, fixed instance storage. | Pixels alone make agents guess. A tree and a rule-based lint turn "looks off" into "button 40 px tall, minimum 56". Determinism makes goldens stable. |
| D7 | Distribution | One repo: the component, emulator, viewer, Go CLI, the plugin in `plugin/`, and a marketplace at the root. Binaries ship as release assets; a launcher verifies their checksums. | Plugin installs copy the plugin directory, so keeping it small and in a subdirectory avoids copying the source tree. There is no documented pattern for per-platform plugin binaries, so the launcher fetches and verifies them. |
| D8 | Breadth | ESP-IDF + LVGL 9 in C only, with framework-neutral headers. Adapters wait for the decision gate (R16). | Broad support multiplies the build and test matrix (frameworks × backends × hosts) before one path works. The three "door-open" rules in requirements §Scope keep later adapters cheap. |

### Rejected Alternatives (deviations that break the architecture)

- **D1 — QEMU first.** Rejected for the MVP: nothing models this panel, touch, buttons or audio, and the agent loop would be slower. Kept as Phase 2 (R15).
- **D1 — Wokwi.** Rejected: commercial, needs an account token, and has no model of this board, although it does offer an experimental MCP server.
- **D1 — LVGL's stock PC simulator alone.** Rejected: it renders LVGL but models no board (power latch, buttons, battery, Wi-Fi, timing, limits).
- **D1 fallback, not rejected:** a plain CMake host build of the product's components against the FreeRTOS POSIX port with kit shims. Use it only if task 0.2 shows the `linux` target can't host the emulator backend. `lcd349 build` hides which one is in use.
- **D3 — SDL inside the product.** Rejected: the macOS main-thread rule, and SDL would become a dependency of every product build.
- **D4 — TCP control port.** Rejected: a network listener (R13.1).
- **D8 — Supporting Arduino, ESPHome and MicroPython from day one.** Rejected: see D8. ESPHome already has its own SDL host display.

## Architecture

### Repository layout

```
lcd349-kit/
  .claude-plugin/marketplace.json     marketplace; plugin source "./plugin"
  .kiro/specs/lcd349-kit/             requirements.md, design.md, tasks.md
  device/
    device.json                       board facts, budgets, fidelity list, power states, design rules
    device.schema.json
  components/lcd349/                  the ESP-IDF component (idf_component.yml, CMakeLists.txt, Kconfig)
    include/                          lcd349.h and per-subsystem headers (public API)
    test_apps/host                    Unity tests on the linux target
    test_apps/build_check             a minimal product built for both targets in CI
    test_apps/board                   Unity tests on the board, with the lcd349_harness component (phase 1)
    src/common/                       LVGL port and rotation, button debounce and classifier, battery curve,
                                      tags, status, error strings, generated board_info.c
    src/hw/                           hardware backend (esp32s3)
    src/emu/                          emulator backend (linux): devmodel, fb_file (mmap + seqlock), flush timing, input queue,
                                      control server, introspect (ui.tree), lint, wifi sim, storage, audio events
  viewer/                             lcd349-view (C, SDL2, CMake), built on demand by lcd349
  cmd/lcd349/                         Go main
  kit.go                              package lcd349kit: the version, and the embedded viewer sources and template
  internal/                           Go packages: rpc, instance (directories, supervisor, serial relay), pty, build,
                                      project (manifests, path confinement), doctor, scaffold, scenario, golden,
                                      mcpserver, devicegen, repocheck (header and skill-size rules), clitest and
                                      emutest (end-to-end tests)
  .github/                            CI workflow and PR template
  plugin/
    .claude-plugin/plugin.json
    .mcp.json                         {"lcd349": {"command": "${CLAUDE_PLUGIN_ROOT}/scripts/lcd349-launch", "args": ["mcp"]}}
    scripts/lcd349-launch             PATH check, download (HTTPS only; LCD349_RELEASE_BASE for tests), SHA-256 verify, exec
    scripts/checksums.txt             per release
    skills/lcd349/SKILL.md
    skills/lcd349/references/         device.md*, fidelity.md*, design-guide.md, sdk-api.md, recipes.md,
                                      scenarios.md, hardware-checklist.md, troubleshooting.md   (* generated)
  templates/app/                      lcd349 new
  examples/hello, examples/gallery, examples/inputs
  evals/                              KN6 briefs, runner, results
  tools/                              boardtest (the board tests and the on-device guide), measure, soak, eval
  docs/                               getting-started, sdk, emulator, scenarios, hardware (generated), plugin
  go.mod                              module at repo root (go:embed reaches templates/ and device/)
  LICENSE (Apache-2.0), THIRD_PARTY.md, CHANGELOG.md, VERSIONS
```

### SDK API (C, `include/lcd349*.h`)

Implemented in task 2.1. `lcd349.h` includes everything; per-subsystem headers hold the doc comments with the threading and error contracts. Abridged:

```c
/* lcd349_core.h */
typedef enum { LCD349_OK = 0, LCD349_ERR_ARG, LCD349_ERR_UNAVAILABLE, LCD349_ERR_ON_USB, LCD349_ERR_TIMEOUT,
               LCD349_ERR_IO, LCD349_ERR_NO_MEM, LCD349_ERR_NOT_FOUND, LCD349_ERR_STATE } lcd349_err_t;
typedef struct { uint8_t backlight_pct; uint16_t touch_period_ms; bool wifi_power_save; } lcd349_config_t;
#define LCD349_CONFIG_DEFAULT() {LCD349_BACKLIGHT_DEFAULT_PCT, 0, true}
lcd349_err_t lcd349_init(const lcd349_config_t *cfg);      /* NULL = defaults; asserts the power hold first (R1.3) */
uint32_t     lcd349_board_status(void);                    /* bitmask of failed lcd349_subsystem_t */
const char  *lcd349_err_str(lcd349_err_t err);
uint64_t     lcd349_time_us(void);  uint32_t lcd349_time_ms(void);   /* esp_timer isn't simulated on linux */
lcd349_boot_reason_t lcd349_boot_reason(void);             /* power-on, software, crash, brown-out */
void *lcd349_alloc(size_t size, lcd349_mem_t caps);  void lcd349_free(void *p);  void lcd349_mem_stats(lcd349_mem_stats_t *);
/* lcd349_board.h (generated): LCD349_LCD_W/H, LCD349_PANEL_W/H, LCD349_MM_TO_PX(), pins, kit constants,
   const lcd349_board_info_t *lcd349_board_info(void) */

/* lcd349_display.h */
lv_display_t *lcd349_display(void);                        /* landscape 640x172 */
lcd349_err_t  lcd349_backlight_set(uint8_t pct);  uint8_t lcd349_backlight_get(void);
lcd349_err_t  lcd349_display_sleep(bool sleep);  bool lcd349_display_is_asleep(void);
void lcd349_ui_lock(void);  void lcd349_ui_unlock(void);   /* recursive */
lcd349_err_t  lcd349_ui_call(void (*fn)(void *), void *ctx); /* run on lcd349_ui with the lock held */
lcd349_err_t  lcd349_ui_tag(lv_obj_t *obj, const char *tag);   /* "list-row", "ignore:<rule-id>" */
bool lcd349_ui_has_tag(const lv_obj_t *, const char *);  size_t lcd349_ui_tags(const lv_obj_t *, char *, size_t);

/* lcd349_input.h */
lv_indev_t  *lcd349_touch_indev(void);
lcd349_err_t lcd349_touch_subscribe(lcd349_touch_cb_t cb, void *ctx);   /* landscape samples */
lcd349_err_t lcd349_buttons_subscribe(lcd349_btn_cb_t cb, void *ctx);   /* debounced edges, press-time stamped */
bool         lcd349_button_is_down(lcd349_btn_t btn);
/* pure classifier: SHORT, HOLD (at each threshold), HOLD_END, CHORD, CHORD_END, plus hold progress for fill bars */
lcd349_err_t lcd349_classifier_init(lcd349_classifier_t *, const lcd349_classifier_cfg_t *);
size_t lcd349_classifier_feed(lcd349_classifier_t *, const lcd349_btn_event_t *, lcd349_gesture_t *out, size_t max);
size_t lcd349_classifier_tick(lcd349_classifier_t *, uint32_t now_ms, lcd349_gesture_t *out, size_t max);
uint16_t lcd349_classifier_hold_permille(const lcd349_classifier_t *, lcd349_btn_t, uint32_t now_ms);

/* lcd349_power.h */
lcd349_err_t lcd349_power_hold(bool hold);
lcd349_err_t lcd349_power_off(void);                       /* LCD349_ERR_ON_USB if still alive after 1 s */
lcd349_err_t lcd349_sleep_until_button(uint32_t timeout_ms, lcd349_btn_t *woke_by);  /* 0 = no timeout */
lcd349_power_source_t lcd349_power_source(void);          /* USB, BATTERY or UNKNOWN (added by phase 1, D1-10) */
lcd349_err_t lcd349_power_on_source_change(lcd349_power_source_cb_t cb, void *ctx);   /* on lcd349_evt */
lcd349_err_t lcd349_battery_read(uint16_t *mv, uint8_t *pct);  uint8_t lcd349_battery_pct_from_mv(uint16_t mv);

/* lcd349_audio.h, lcd349_rtc.h, lcd349_kv.h, lcd349_wifi.h */
lcd349_err_t lcd349_tone(uint16_t hz, uint16_t ms, uint8_t volume);
lcd349_err_t lcd349_tone_seq(const lcd349_tone_t *steps, size_t n, uint8_t volume);  lcd349_err_t lcd349_tone_stop(void);
lcd349_err_t lcd349_rtc_get(int64_t *unix_s);  lcd349_err_t lcd349_rtc_set(int64_t unix_s);
lcd349_err_t lcd349_kv_get_str / kv_set_str / kv_get_blob / kv_set_blob / kv_erase(...);   /* names <= 15 bytes */
lcd349_err_t lcd349_wifi_set_networks(const lcd349_wifi_net_t *nets, size_t n);   /* <= 4, priority order */
lcd349_err_t lcd349_wifi_start(void);  lcd349_wifi_stop(void);  lcd349_wifi_scan(lcd349_wifi_ap_t *, size_t *);
lcd349_err_t lcd349_wifi_status(lcd349_wifi_status_t *);  lcd349_wifi_subscribe(lcd349_wifi_cb_t, void *);

/* lcd349_console.h (added in task 6.1, for R5.7: host tools talk to the product over the serial console) */
lcd349_err_t lcd349_console_read_line(char *buf, size_t size, uint32_t timeout_ms);  /* output: printf / ESP_LOG */
```

- **Argument checks live in the common core** (`src/common/core.c`), before any backend call, so the board and the emulator reject exactly the same inputs.
- **Backend interface:** `src/common/kit.h` lists the `be_*` functions each backend implements, and the `lcd349__*` hooks the core gives backends (raw button levels, Wi-Fi changes).
- **Sleep:** asleep, LVGL invalidation is off and the flush sends nothing to the panel; waking redraws the whole screen. LVGL resumes its own refresh timer, so pausing that timer doesn't work (found in task 2.2).
- **Threading:** LVGL runs on one kit task, `lcd349_ui`. Products touch LVGL either from inside callbacks the kit runs on that task, or after `lcd349_ui_lock()`/`lcd349_ui_unlock()`. Button, touch and Wi-Fi callbacks run on the `lcd349_evt` task. Neither runs in interrupt context.
- **The common LVGL port hands the backend the landscape frame; the backend does rotation and byte order.** (Amended 2026-10-23 by phase 1, `.kiro/specs/phase-1-kit-mvp/`, design D1-3.) LVGL renders landscape into a full-frame buffer, and the port passes it to `be_panel_flush(const uint16_t *landscape)`, which replaced `be_panel_write(panel, len)`. Both backends turn it into the panel-native 172×640 orientation with swapped RGB565 bytes, using the same `rotate.c`:
  - the hardware backend rotates in 64-row strips (`lcd349__rotate_band`) into two internal DMA buffers, filling one while the other is on the bus, so rotation overlaps the transfer: a full-frame flush takes about 13 ms instead of 75.7 ms with the whole frame rotated first;
  - the emulator rotates the whole frame (`lcd349__rotate_frame`) into its framebuffer.

  A host test proves the strips assemble to exactly the whole-frame transform's bytes, for both rotations and both byte orders (seam S1-2, `test_rotate_band_matches_whole_frame`). So the emulator still holds the bytes the board sends to the panel, which is how it catches rotation and byte-order bugs (R3.2).

### Hardware backend (`src/hw`)

| Subsystem | Implementation |
|---|---|
| Power | I2C bus, then TCA9554 with P6 asserted, before anything else. `power_off` releases P6, waits 1 s, then returns `LCD349_ERR_ON_USB` if still running. |
| Display | `esp_lcd_axs15231b` over QSPI (CS 9, PCLK 10, D0–D3 11–14, RST 21), full-frame writes only, DMA from internal RAM in chunks. Backlight via LEDC on GPIO8. |
| Touch | The kit's own report reader on I2C 0x3B (the 11-byte read command, a 14-byte report, decoded by the shared `axs_touch.c`), polled every `touch_period_ms` only while the display is awake; no touch interrupt on either revision. First point only. Never a wake source. (Amended 2026-10-23 by phase 1, design D1-2 and D1-4: the component's touch driver and TP_INT aren't used; kit ADRs 0002 and 0003.) |
| Buttons | GPIO0 and GPIO16 with pull-ups, edge ISR feeding a 10 ms debounce timer, events posted to `lcd349_evt`. Light-sleep wake via `gpio_wakeup_enable` on both. |
| Battery | ADC1 channel 3 one-shot with calibration, ×3, 16-sample average, Li-Po curve from device.json. |
| Audio | `esp_codec_dev` with the ES8311 over I2S at 16 kHz; amp enable via the expander pin found in task 0.3; tones are generated square waves on an audio task. |
| RTC | PCF85063 at 0x51, a small driver written from the datasheet. |
| Storage | NVS namespace per `ns`. |
| Wi-Fi | `esp_wifi` station; saved networks in NVS; while disconnected, scan every 60 s and join the highest-priority visible one. |
| Sleep | `esp_pm` with light sleep and tickless idle, allowed only during `lcd349_sleep_until_button` (Kconfig `LCD349_LIGHT_SLEEP`, phase 1's D1-11); Wi-Fi stays in modem power save (`WIFI_PS_MIN_MODEM`). Experimental on the board (phase 1's D1-12). |

### Emulator backend (`src/emu`)

Built in task group 4. One module per concern: `devmodel.c`, `fb.c`, `serial.c`, `emu_backend.c` (power, display, touch, buttons, battery, audio, RTC), `kv_file.c`, `wifi_sim.c`, `control.c` and `control_methods.c`, `introspect.c`, `lint.c`, `png.c`, `download.c`.

- **Start-up:** a constructor runs before `main()`. It reads the configuration, starts serial capture and, if BOOT was held through the reset, enters download mode, so `app_main` never runs. The rest starts inside `lcd349_init()`. The control server starts last (`be_started`).
- **Configuration (environment, because a `linux`-target `app_main` gets no argv):**
  - `LCD349_INSTANCE_DIR`: without it, the instance runs with an in-memory framebuffer and storage, and no control socket (host tests);
  - `LCD349_CLOCK` (RFC 3339 or Unix seconds), `LCD349_WIPE=1`, `LCD349_BOOT_HELD=1`;
  - `LCD349_BOOT_REASON` (`software` or `crash`, set by the supervisor on a restart);
  - `LCD349_STORAGE_DIR`;
  - `LCD349_BATTERY_MV` and `LCD349_SOURCE` (`usb` or `battery`): first-boot settings, saved to `devstate.json` at once;
  - `LCD349_FLUSH_MS` (an override for experiments).
  - The supervisor passes `LCD349_CLOCK`, `LCD349_WIPE`, `LCD349_BATTERY_MV` and `LCD349_SOURCE` on the first boot only, so a reset keeps the clock, storage and battery the instance has since reached, as the board would.
- **Device model (`devmodel.c`):**
  - It holds battery millivolts, USB or battery, board revision, RTC offset, the simulated Wi-Fi networks, the power state (on, light sleep, off, download) and the power hold.
  - Everything except the power state and the hold persists in `<instance>/devstate.json`, written atomically, so a reset keeps it.
  - The battery drain model is unavailable until the current draw is measured (phase 3; the capacity is recorded), and `state.set` says so (cut-list item 3).
- **Framebuffer (`fb.bin`, 0600, mapped `MAP_SHARED`):**
  - A 64-byte header (`_Static_assert`) holds:
    - magic `L349FB01`, version 1, width 172, height 640;
    - `seq` (the seqlock: odd while a frame is written), frames written, `last_flush_us`, pid;
    - backlight, power state, display asleep, on battery, battery mV and %, Wi-Fi state;
    - the rotation and byte order of the panel bytes (0 = unknown, so reserved bytes can become fields without a version change);
    - `status_seq`, bumped whenever a status field changes.
  - The header is followed by the 172×640×2 panel bytes, exactly as the panel receives them.
  - `be_panel_flush` rotates the frame in, then waits the flush time from device.json: that wait is the timing model (R3.3, KN4).
- **Serial capture (`serial.c`):**
  - stdout and stderr go through a pipe to a plain pthread (all signals blocked).
  - The thread writes everything on to the real stdout (the pty, or `serial.log`) and keeps the last 1,024 lines in a ring for `wait {log}`, catching `ESP_LOG` and `printf` alike.
  - At exit the pipe is drained, so no line is lost. stdout is line-buffered.
  - **Crashes:** ESP-IDF's `linux` `main()` catches SIGABRT and SIGSEGV, prints a message (a backtrace for SIGSEGV) and calls `_exit(1)`, which skips the drain. So `lcd349_init` puts the kit's own handler in front: it points stdout and stderr straight at the console, sends on what the pipe still holds, then runs ESP-IDF's handler. A crash therefore shows as exit code 1, with the product's last lines and ESP-IDF's message intact (found in task 6.1: about one crash in 15 lost them).
  - **Console input:** a second pthread reads stdin, splits lines (LF, CR or CRLF; at most 1,023 bytes, the rest dropped) into a 16-line queue that drops new lines when full, as a full receive buffer would. `lcd349_console_read_line` polls it every 10 ms: FreeRTOS tasks must not block in host calls. When the console ends (end of file or EIO on a terminal) and the parent process has changed, the supervisor is gone, and the instance exits rather than outlive it.
- **Input:**
  - `input.touch` builds samples in landscape: a tap is three down samples and one up; a long press repeats until the requested duration; a swipe is interpolated; raw points can be sent as they are.
  - Samples go into a queue, and the touch read callback takes one per touch period.
  - The emulator converts samples to panel-native points, so touch runs through the same transform as the board's controller.
  - Viewer input uses `live:true`, which merges pending moves so the mouse never lags.
  - When the display is dark, touch is refused with a reason (it is never a wake source); a gesture under way when the display goes dark is dropped.
  - `input.press` drives virtual GPIO levels into the common debounce and classifier. It answers after the release has been debounced, with `woke:true` if the press ended a light sleep.
- **Light sleep:** `be_sleep_until_button` sets the power state and blocks on a semaphore that a button press releases. The core has already put the display to sleep, so nothing renders.
- **Power:** releasing the hold on battery sets the header to OFF and exits with code 4 (R3.6). On USB the core's 1 s check returns `LCD349_ERR_ON_USB`.
- **Process exit codes** for the supervisor: 3 = reset; 4 = off; 5 = reset with BOOT held (start again with `LCD349_BOOT_HELD=1`).
- **Download mode (`download.c`, R3.5):**
  - It runs from the constructor, before FreeRTOS starts.
  - It renders "DOWNLOAD MODE" with LVGL directly into the framebuffer and prints "waiting for download".
  - It serves a reduced control socket (`ping`, `info`, `state.*`, `power`, `screenshot`, `record.*`) by blocking in `poll()`. Every other method answers `RPC_UNAVAILABLE` with the way out.
- **Wi-Fi (`wifi_sim.c`):**
  - A task-driven state machine over the simulated networks. 5 GHz networks are invisible to scans and connections.
  - Scans take 1 s and associating takes 0.4 s (emulator choices). After a failure it rescans every 60 s (`LCD349_WIFI_SCAN_INTERVAL_S`).
  - A change to the simulated networks triggers a scan at once, compressing time where waiting teaches nothing.
  - Events are the same as the hardware backend's: CONNECTING, CONNECTED (10.0.2.15), and DISCONNECTED with reason AUTH, NOT_FOUND, LOST or STOPPED.
  - Sockets always use the host network; the fidelity list says so.
- **Storage (`kv_file.c`):** typed like NVS (string versus blob mismatches return `LCD349_ERR_ARG`). It's held in memory and written through to `storage/ns-<hex name>.kv`, atomically. `LCD349_WIPE=1` clears it.
- **Audio:** tones become events `{seq, t_ms, hz, ms, volume}` in a 256-entry ring. A new tone first appends a stop event, because it replaces what's playing.
- **Memory report** (`state.get`): `lcd349_alloc` counters and LVGL's `lv_mem_monitor`, compared with the device.json budgets. A budget not measured yet (`null`) never warns.
- **Control server (`control.c`):**
  - A FreeRTOS task with a non-blocking Unix socket. It allows 8 clients; a ninth is told so and closed.
  - Each loop polls with timeout 0, serves at most one request per client, and calls `vTaskDelay(1)`.
  - A client's next request waits for the answer to its previous one. Requests that take time (`wait`, `input.*`) stay pending.
  - Screen methods take the LVGL lock from the control task, which gives the same exclusion as queueing work to `lcd349_ui`.
  - A line over 64 KB, or nesting more than 16 arrays and objects deep (requests are parsed recursively on the control task's 32 KB stack), is refused with `-32600` and the connection carries on. A client queueing over 1 MB of unanswered input is closed. `EINTR` is retried.
  - A client that has finished sending (end of file, as `printf ... | nc -U` does) is answered before it's closed. Replies use `send` with `MSG_NOSIGNAL` (`SO_NOSIGPIPE` on macOS), so a vanished client can't end the instance with SIGPIPE (both found in task 10.4).
  - Notifications (no `id`) get no answer.
  - JSON comes from ESP-IDF's own cJSON (`json` component), so there is no new dependency.
- **Methods (R5.3):**
  - `ping`, `info` (board, fidelity list, methods, exit codes), `state.get`, `state.set` (validated as a whole before applying), `power`.
  - `screenshot`: PNG (stored deflate) or raw `rgb565be`. `raw:true` gives the panel-native image, and `scale` 1–4. It is black when the display is dark, and backlight dimming isn't applied (the level is reported).
  - `ui.tree` (`path`, `depth`; the top layer is included), `ui.lint`.
  - `input.press` (`button` or `chord`, with `ms` for a timed press or `state` `down`/`up` for a hold; a client's held buttons are released when it disconnects), `input.touch` (`tap`, `long_press`, `swipe`, `points`, `live`).
  - `wait` (`log` regex, `text`, `idle_ms`, `timeout_ms`, `since`). By default `wait {log}` also scans the lines logged since the same client's previous request began, so "press, then wait for its log line" can't miss it. On timeout it returns the last 20 lines and writes `wait-timeout.png`.
  - `audio.events` (`since`), `record.start`/`record.stop` (PNG frames on each new frame, at most 60 s or 600 frames).
  - Requirement 5.4 asked for screenshots "optionally at physical scale". The instance offers integer `scale`; physical-size output belongs to the viewer and the Go tools.
- **Introspection (`introspect.c`):**
  - It walks the active screen (and the top layer).
  - Per object: class (from LVGL's private class name), coordinates, visible, clickable, interactive, scrollable, checked and disabled states, label text, font (built-in name or "custom", and line height), and text and background colours.
  - The background colour is composited through every ancestor; the text colour is blended over it.
  - It also gives tags and a path like `screen/0:button/0:label`.
- **Lint (`lint.c`):**
  - Interactive means clickable and visible, and either a pressable widget class or an object with event handlers: LVGL makes plain containers clickable by default.
  - Contrast uses WCAG 2 luminance on RGB565-quantised colours.
  - Clipping checks wrap and clip modes against the content box, and a label running outside a parent that clips it (a parent that can't actually scroll). Dots and scroll modes are deliberate truncation and pass.
  - Off-screen reports only the outermost offending object, and ignores content of containers that really scroll.
  - Momentum counts only containers whose content actually overflows.
  - Gradients warn only when the stops differ in RGB565.
  - The fixtures (a firing and a passing screen per rule) are host tests in `test_apps/host/main/test_lint.c`, not a separate app.

### Threading on the `linux` target (found in task group 4)

ESP-IDF's documentation describes the host target as cooperative. **The v5.5.5 port is preemptive:** `configUSE_PREEMPTION` 1 with time slicing, and the SIGALRM tick handler switches tasks wherever they are. Three consequences, all handled in the kit:

1. **A host lock held across a preemption can deadlock the process.** A higher-priority task that blocks on a pthread mutex held by a suspended lower-priority task keeps being chosen to run, so the holder never resumes. The device model used a pthread mutex and hung this way. Emulator state shared between tasks is now guarded by FreeRTOS critical sections (signals blocked: no preemption), held only for short copies. Storage, which mallocs and writes files while locked, uses a FreeRTOS mutex. The serial ring keeps a pthread mutex for its reader thread, and FreeRTOS tasks take it only inside a critical section. A host test reproduces the old deadlock: the pthread version hangs 3 runs out of 3, the fixed version passes.
2. **libc's own locks (malloc, stdio) aren't protected by the port.** So on the `linux` target the kit runs `lcd349_ui`, `lcd349_evt` and its emulator tasks at `app_main`'s priority (1), where time slicing resolves contention. The board keeps 5 and 6. Product tasks above priority 1 that share malloc or printf with lower ones can still, rarely, hang an instance; the fidelity list (`scheduling`) says so. The supervisor (task 6.1) should report a hung instance (no answer to `ping`) with a stack sample.
3. **Long critical sections starve the tick.** While signals are blocked, SIGALRMs coalesce, and macOS rarely delivers the pending one in a short unblocked window: one experiment processed 16 ticks in 6 s. Kit critical sections are therefore a few struct copies, never I/O, malloc or loops.

### Viewer (`viewer/`, C + SDL2)

Built in task group 5.

- **Process and build:**
  - A standalone CMake project that includes no component header, so `lcd349` can build it from a copy (task 6.2). Its board facts come from the generated `viewer/device_gen.h`.
  - Started by `lcd349 run` (not with `--headless`), or with `lcd349 view <instance>`. It runs on its own main thread, which satisfies the macOS rule.
  - Files: `core.c` (pure: header decoding, inverse rotation, a JSON tokenizer, layout, scale, the touch queue, a 5×7 font), `view.c` (SDL, sockets, drawing, self-test), `audio.c`, `display_mm.c`.
  - Exit codes: 0 closed, 1 error, 2 usage, 3 no window (`lcd349 run` falls back to headless).
- **Display:**
  - It maps `fb.bin` read-only, and never a file shorter than a frame. A frame is read when `seq` becomes a new even value: copied, then `seq` is checked again, so a torn copy is retried.
  - It undoes the rotation and byte swap as the header says (the generated defaults when the header says 0), and dims linearly with the backlight.
  - A dark display (asleep, light sleep, off, backlight 0) shows black. Download mode shows the instance's notice at full brightness.
- **Instances come and go:** the header's pid identifies the instance. A reset starts a new process; the viewer notices, reattaches (frames, both connections, the audio clock) and logs it. A replaced `fb.bin` is remapped. Badges show NO INSTANCE, NOT RUNNING or OFF.
- **Physical scale (R4.1):** window points per device pixel. Sources, in order:
  1. `--scale`;
  2. `LCD349_VIEW_PPI`, the display's pixels per inch, divided by the window's pixel ratio;
  3. on macOS, CoreGraphics' physical display width (`CGDisplayScreenSize`);
  4. SDL's display DPI, flagged when it is a nominal 96 × k value.

  If the window wouldn't fit, it shrinks and says so. The source is printed at start; `--print-scale` prints it as JSON, for `lcd349 doctor`. Recon: Homebrew's sdl2-compat reports 96 × the content scale (192 on this Mac's 2× panel), which would draw the device about 1.5 times too big. CoreGraphics gives 286.9 mm across 1440 points, so the 640-pixel screen is drawn 85.3 mm wide.
- **Skin (R4.2):**
  - The case outline from device.json `case` (Case B, 98.5 × 34.8 mm). The screen's place in it is assumed centred until task 0.3 measures it.
  - BOOT and PWR keys below the case, plus caps on the case edge once their positions are measured. Both are `null` now, and a caption says so.
  - Badges: power state, USB or battery %, Wi-Fi, backlight.
  - The skin's text uses a built-in 5×7 font, so there is no SDL_ttf dependency.
- **Input (R4.3):** two control connections, so a pending touch never delays a button.
  - Mouse down, drag and up on the screen become `input.touch {points, live}` samples. While a request is in flight, moves are coalesced into the newest position. A press and its release are never dropped: a full queue drops whole touches. Sliding off the glass releases.
  - `b` and `p`, or the drawn keys, use `input.press {state: "down"|"up"}`. The instance releases a client's held buttons when it disconnects, and the viewer releases everything when it loses focus.
  - Refusals (a dark display, download mode) show as a message under the keys.
- **Audio (R3.8):** polls `audio.events` every 40 ms and plays square waves through SDL audio. Event times map onto the audio clock with 60 ms of latency, so bursts keep their spacing; a new tone replaces the playing one. Without an audio device it stays silent and says so; `--mute` skips audio.
- **Self-test (CI, SDL's dummy drivers):** `--selftest out.bmp` clicks the example's TAP button and presses `b` through the same handlers as real input. It then waits for the tone and a settled frame, and saves the window. The Go test compares the saved window pixel by pixel with the instance's own screenshot, scaled by the backlight.

### Instances, serial and the `lcd349` CLI (Go)

- **Instance directory:** `$TMPDIR/lcd349-$UID/<id>/` (mode 0700), with ids of at most 12 characters. macOS limits Unix socket paths to 104 bytes; if `<dir>/control.sock` would exceed 100, `lcd349` uses `/tmp/lcd349-$UID/<id>/` instead. The directory holds:
  - `control.sock` (0600);
  - `fb.bin`: the framebuffer file;
  - `instance.json`: pid, project, serial pty path, start time, kit version;
  - `serial.log`, rotated at 10 MB;
  - `storage/`, unless `--storage <dir>` is given.
- **Serial:**
  - The supervisor creates two pseudo-terminals.
  - The product's stdin/stdout/stderr go to pty A.
  - Pty B's slave path is published as the virtual serial port (`instance.json` `serial`), mode 0600 (KT8).
  - The supervisor relays between the two masters and tees the product's output to `serial.log`, rotated to `serial.log.1` at 10 MB. `lcd349 logs` numbers lines across rotation (`log_lines_before`).
  - Host tools open pty B exactly like `/dev/cu.usbmodem…` (R5.7). Both ptys are raw (no echo, no line editing, bytes unchanged), as a USB serial port is.
  - Output nobody reads is discarded oldest first: when pty B's buffer is full, the relay flushes it and writes the newest, so the relay never stalls the product and a tool that opens the port finds recent output.
  - Supervisor notes (`lcd349: boot 2 (software), pid …`, crashes, hangs) go to `serial.log` and the terminal, never to the port.
  - macOS facts found in task 6.1: a terminal's settings reset when its last descriptor closes, so the supervisor holds a slave descriptor of each pty for its life; and when a session leader exits, macOS revokes its controlling terminal, so pty A is deliberately *not* the product's controlling terminal (the product gets its own session, which also keeps a terminal's Ctrl-C away from it).
- **Process supervision:**
  - Every instance has a supervisor process, `lcd349 supervise <dir>`, started by `lcd349 run`, the MCP server or the scenario runner. Foreground `run` ties it to itself through a pipe (fd 3) and shares the terminal, so the serial output is shown and Ctrl-C stops the instance; `--detach` starts it in its own session.
  - It holds an exclusive `flock` on `supervisor.lock` for its whole life. Liveness is that lock, never a pid, so a recycled pid can't fake it; `list` shows an instance whose supervisor died as `gone`.
  - The product is started with a fixed argv (just the ELF path) and the `LCD349_*` environment variables from §Emulator backend; other `LCD349_*` variables from the caller's environment are dropped, except `LCD349_FLUSH_MS` and `LCD349_WIFI_SCAN_INTERVAL_S`.
  - Exit codes: 3 restarts with `LCD349_BOOT_REASON=software`; 5 restarts in download mode (`LCD349_BOOT_HELD=1`); 4 stays off. Any other exit is a crash: `last_exit` records the code or signal and the last 200 serial lines, and the instance stays down so the evidence isn't buried by a restart.
  - **While the product is down** (off or crashed) the supervisor answers on `control.sock` itself. Off: `ping`, `info`, `state.get` (power off), a dark `screenshot`, and `input.press` PWR or `power {action: on}` boot it (reason power-on); everything else answers `RPC_UNAVAILABLE` with the way out. Crashed: every request answers `instance_exited` (code −32001) with the exit and last lines in `data`; `power {action: reset|on}` boots it with reason crash. So the CLI, MCP server and viewer get the same clear answer.
  - **Hang detection:** it pings the product every second on its own connection. No answer for 5 s (or no control socket 15 s after start) marks the instance `hung` and writes a stack sample of every thread: `/usr/bin/sample` on macOS (by absolute path: python.org installers shadow it), `gdb -batch` on Linux, or each thread's kernel wait channel from `/proc` when gdb is missing or may not attach. An answer clears it. A hung instance isn't killed: `lcd349 stop` does that.
  - `lcd349 stop` sends SIGTERM to a live supervisor, which stops the viewer and the product and removes the directory. For a dead supervisor it kills the product only after the product confirms, over its own socket, the recorded pid and directory.
  - The viewer, unless `--headless`, is started once by the supervisor (own process group, output in `viewer.log`) and reattaches across resets by itself. Exit code 3 means no window: the instance carries on headless and says so.
- **Build:**
  - `idf.py -C <project> -B <project>/build-emu -D SDKCONFIG=<project>/build-emu/sdkconfig --preview set-target linux`, then `build`, as exec argv with the ESP-IDF environment from `IDF_PATH`/`export.sh` captured once by `doctor`.
  - Projects must keep one component lock file per target: the template's root `CMakeLists.txt` sets `idf_build_set_property(DEPENDENCIES_LOCK dependencies.lock.${IDF_TARGET})` before `project()`. Without it, the shared `dependencies.lock` flips its `target:` on every switch (spike 0.2). `doctor` warns when a project lacks it.
  - TLS: if Python can't verify certificates, as with python.org Python on macOS without its certificate step, `doctor` says to set `SSL_CERT_FILE=/etc/ssl/cert.pem` (spike 0.1).
  - `set-target` runs only when `build-emu/CMakeCache.txt` isn't configured for `linux`, so later builds are incremental (0.9 s with nothing to do). It runs ESP-IDF's `fullclean`, which refuses a non-empty directory that isn't a CMake build directory yet, so the first configure's output is written to `build-emu/lcd349-build.log` only afterwards.
  - The environment comes from ESP-IDF's own `idf_tools.py export --format key-value` (no shell), cached in `~/.cache/lcd349/idf-env-<hash>.json` with `PATH` kept relative to the caller's; `doctor` refreshes it. When ESP-IDF's Python has no CA certificates on macOS, builds get `SSL_CERT_FILE=/etc/ssl/cert.pem`.
  - ESP-IDF is found at `$IDF_PATH`, `~/esp/<pinned version>/esp-idf`, `~/esp/esp-idf-<version>`, `~/esp/esp-idf` or `/opt/esp/idf`.
  - Errors are parsed into file, line, column and message: compiler errors, CMake errors (with the file only when it is the project's), undefined symbols (GNU and Apple linkers) and component resolution failures. Each error naming a component the emulator lacks gets a hint with the kit API that replaces it, from the table that also generates `docs/emulator-components.md`. In ESP-IDF v5.5.5 such a component fails in one of three ways: it doesn't resolve (esp_lcd, esp_wifi, esp_adc), its header is missing (`driver/gpio.h`, `driver/uart.h`), or it compiles and doesn't link (I2C, USB Serial/JTAG, light sleep).
  - Viewer build: `lcd349-view` is found at `$LCD349_VIEW`, then beside the `lcd349` binary (release archives), then in `~/.cache/lcd349/viewer/<version>-<source hash>/`, where it is built with CMake from the sources embedded in the binary (one build at a time, under a lock).
  - The wrapper-project fallback of task 0.2 wasn't needed (D1 held).
- **Doctor:** checks ESP-IDF (found, pinned version, environment), Python TLS, the host compiler, CMake and Ninja (libbsd on Linux), SDL2, the viewer (built if needed, and its physical-scale source: a nominal DPI warns), a project's kit pin (a version, or an `override_path` checkout whose header version must match) and per-target lock files, and instances left by dead supervisors. Each failure or warning comes with a fix; any failure exits 1.
- **Scenario format (`*.scenario.yaml`):**

```yaml
name: list-to-card
run: { clock: "2026-10-12T09:00:00Z", wipe: true, battery_mv: 3900, usb: false }
steps:
  - wait: { idle_ms: 300 }
  - shot: { golden: goldens/list.png }
  - lint: { max_errors: 0 }
  - touch: { tap: [320, 82] }
  - wait: { text: "DETAIL" }
  - press: { button: boot, ms: 1100 }
  - wait: { log: "approved", timeout_ms: 2000 }
  - state: { set: { wifi: { networks: [] } } }
  - shot: { golden: goldens/no-wifi.png, tolerance_px: 0 }
```

- **Steps** (built in task 6.4): `run` (start again with new options), `press` (`boot`, `pwr` or `boot+pwr`, with `ms`; a bare `press: pwr` works), `touch`, `wait` (`log`, `text`, `idle_ms`, `timeout_ms`, default 5000), `shot` (`golden`, `tolerance_px`, `raw`, `scale`; a bare `shot: path` works), `lint` (`max_errors`, default 0; `max_warnings`, default any), `state` (`set` an object as `state.set` takes it, and/or `expect` values that must equal the same paths in `state.get`), and `power` (`on`, `off`, `reset`, `download`).
- **Timing margins:** a press's hold time is applied on the instance's 1 ms tick, so on a busy host it can run long (a 150 ms press measured 176 ms with the whole test suite running in parallel). Hold for a threshold with a margin (1100 ms for a 1 s hold, as above) and match gestures, not exact milliseconds.
- **Parsing is strict:** an unknown key or step is an error naming its line, so a typo can't silently skip a check. `run` defaults to `wipe: true`.
- **Running:** a fresh headless instance per scenario, with its supervisor tied to the runner. The first failing step ends the run (R7.7) and the command exits 1, printing the step, its line, and the golden, actual and diff paths; a failed `wait` keeps the instance's screen as `<scenario>_step<N>_screen.png` beside the scenario. `--keep` leaves the instance running after a failure. After a `power` step, or `press: pwr` while off, the runner waits for the new boot and reconnects. The first `wait {log}` on a new connection matches from line 1 of the boot, since the instance's own look-back starts at the connection's previous request.
- **The product may change by itself between steps** (it powers off, resets or crashes). Before each step the runner checks the instance and follows a new boot, or the supervisor's stand-in socket. A request whose connection drops mid-call is asked again if it changes nothing (state, screenshot, tree, lint); an input counts as done, because a PWR hold that powers off ends the process before the press can be answered, unless the product crashed. While the device is off, the stand-in socket answers `wait {log}` from the last boot's lines, at once, so a scenario can check what a device said before it switched itself off.
- **Paths:** the scenario and its goldens (relative to the scenario file) must resolve inside the project, symlinks evaluated, for the CLI as for MCP (R7.5). A scenario file's path is made absolute when it's loaded (task 9.1 found goldens written under a doubled path when it was given relative from outside the project).
- **Golden compare:** RGB565-exact by default (colours the panel can't show differently don't count); `tolerance_px` allows that many differing pixels. On a mismatch it writes `<name>_actual.png` and `<name>_diff.png` (the golden dimmed, differing pixels magenta) next to the golden, and reports where the pixels differ. A missing golden is written, but the step still fails unless `--update-goldens` is given (R7.8). Goldens are re-encoded compressed (the instance sends uncompressed PNGs for speed: 330 KB becomes 5.5 KB) and keep the panel's pixel density.
- **Drive commands** (`shot`, `tree`, `lint`, `press`, `touch`, `wait`, `state`; task 6.3) take the instance as an optional first argument: with one instance running it can be left out. `shot` writes a PNG with a `pHYs` chunk at the panel's density (from `info`), so DPI-aware viewers show it at physical size, which is how the CLI meets R5.4's "physical scale". `--timings` prints the request time and, for input, when the next frame came (KN1). `lint` and a timed-out `wait` exit 1.
- **The CLI's log cursor:** the instance's `wait {log}` looks back to the start of the same connection's previous request, but each CLI command is a new connection. So `press`, `touch` and `state set` record the serial position and the product's pid in `<instance>/cli-cursor`, and `wait --log` starts there by default (from line 1 if the product has restarted since). "Press, then wait for its log line" works across two commands as it does within one connection.
- **Scaffold (`lcd349 new`, task 6.5):** writes `templates/app` (embedded in the binary): a project that pins the kit, keeps a lock file per target, has one lint-clean screen, a scenario whose golden ships with the template (the screen doesn't show the project name, so one golden fits every project), a `.gitignore` and a `CLAUDE.md` pointing at the skill. It pins `version: v<kit version>` from `RepoURL`; a development or pre-release build refuses unless `--kit-path` names a local `components/lcd349` (written as `override_path`). A new project builds for the emulator in about 100 s (LVGL from scratch) and passes its scenario on the first run.

### MCP server (`lcd349 mcp`, go-sdk v1.8.0, stdio)

| Tool | Input | Output |
|---|---|---|
| `device_info` | — | device.json summary: facts, limits, design rules, fidelity list |
| `new_project` | `dir`, `name` | created files list |
| `build` | `project` | ok, or errors parsed from compiler output (file, line, message) |
| `run` | `project`, `clock?`, `wipe?`, `battery_mv?`, `usb?` | `instance` id, serial pty path |
| `stop` | `instance` | — |
| `screenshot` | `instance`, `raw?`, `physical?` | **image** (PNG) plus `frame_seq` and backlight |
| `ui_tree` | `instance`, `path?`, `depth?` | JSON tree |
| `ui_lint` | `instance` | findings |
| `press` | `instance`, `button`, `ms` / `chord` | — |
| `touch` | `instance`, `tap`/`long_press`/`swipe`/`points` | — |
| `logs` | `instance`, `since?`, `grep?` | lines and next cursor |
| `wait` | `instance`, `text?`/`log?`/`idle_ms?`, `timeout_ms` | matched or timeout |
| `get_state` / `set_state` | `instance`, partial state | state |
| `run_scenario` | `project`, `file`, `update_goldens?` | per-step results, diff image paths |
| `record` | `instance`, `start`/`stop` | path of the frames or GIF |
| `power` (added in 7.1) | `instance`, `on`/`off`/`reset`/`download` | the new boot's state and reason |

Built in task 7.1 (`internal/mcpserver`, `lcd349 mcp [--root dir]`):

- **Paths:** `project` must resolve (symlinks evaluated) to a directory whose `main/idf_component.yml` or root manifest depends on `lcd349`; anything else is refused with the reason and a pointer to `new_project`. Scenario and golden paths must resolve inside it (R7.5). Relative paths resolve against the workspace root (the server's working directory, or `--root`). `new_project` writes only inside the workspace; its `kit_path` (default `$LCD349_KIT_PATH`) names a local kit for development builds, which have no release to pin.
- **Instances:** tools act on any of the user's instances (so an agent can drive one a person started with a window), and `instance` may be left out when exactly one runs. The server records the instances it started and stops them when the session ends (R7.6). Their supervisors are also tied to the server process, so even a server killed outright takes its instances with it.
- **`run` and `run_scenario` build first** by default (incremental: about a second with nothing to do), so an agent never tests stale code; `build: false` skips it. `run` is headless unless `window`.
- **`screenshot`:** image content (the PNG compressed: a 330 KB stored PNG becomes about 5 KB) plus JSON with the frame number, backlight, power, darkness and the screen's size in mm. `physical` resizes it to the size the window shows on this computer's display, from the viewer's `--print-scale` (1.339 display pixels per device pixel on the development Mac), or sends it 1:1 with the reason when that isn't known (headless CI).
- **`wait`** uses the CLI's log cursor, so "press, then wait for its log line" works across tool calls, and after a restart it matches from the new boot's first line.
- **`logs`** returns numbered lines and `next`: without `since`, the last `limit` (default 200); with it, the next `limit` lines and `more`.
- **`run_scenario`** returns the per-step results and, for a failing screenshot step, the actual and diff images as image content (a failed `wait`'s screen likewise).
- **`record stop`** turns the recorded frames into an animated GIF that keeps their timing, in `<project>/build-emu/recordings/` (or the temporary directory for an instance without a project).
- **Output size:** tool results are capped at 1 MB. `ui_tree` beyond that sheds its deepest levels until it fits, marks cut objects `children_omitted`, and adds a note to narrow it with `path` or `depth`; `logs` stops early and moves `next` back.
- **Errors** are tool errors (`isError`) the agent can read and act on: build failures carry the parsed errors and hints, crashed instances the last serial lines.
- **Verified with a real client (2026-10-13):** a headless Claude Code session (Haiku 5.5, `--mcp-config` pointing at the binary) ran the template project, read "COUNT 0" from the screenshot, tapped the button's centre, read "COUNT 1" and stopped the instance: 8 turns, $0.005.

### Skill (`plugin/skills/lcd349`)

- **SKILL.md** (≤ 500 lines) contains:
  - when to use it;
  - the workflow from R8.2, as a checklist;
  - the ten design rules that matter most, each with its threshold, generated into a marked block;
  - the button grammar patterns: short = step, hold = commit, the 500 ms visibility guard, wake-only first press;
  - "never do" items: SDL in products, esp_lcd/esp_wifi calls, kinetic scroll, touch wake, a BOOT combination at reset;
  - pointers to each reference file.
- **References:**
  - `device.md` and `fidelity.md` (generated);
  - `design-guide.md`: layout bands, target sizes in px and mm, type scale, colour and contrast in RGB565, gestures along the long axis, stepping instead of animation (LVGL's default press transition cost about 7 full-frame flushes per tap in spike 0.2, so turn transitions off or make them short), sleep and wake patterns;
  - `sdk-api.md`;
  - `recipes.md`: code for each R8.1 recipe, compiled in `gallery` so it can't rot;
  - `scenarios.md`;
  - `hardware-checklist.md`;
  - `troubleshooting.md`.
- **Wireframes:** a fixed text grid where one character is 8 × 16 px, so 80 × 10.75 cells. The skill asks for one before any code.

Built in task 8.1:

- **Generated, never hand-copied (R8.3):** `internal/devicegen` renders the skill's facts. SKILL.md is hand-written around a marked block (the board at a glance, the wireframe grid, the rules table) that the generator fills. `design-guide.md` and `hardware-checklist.md` are templates (`internal/devicegen/templates/skill/`) filled from device.json. `sdk-api.md` is the public headers (leading comments as prose, declarations with their doc comments). `recipes.md` quotes `examples/gallery` between `/* recipe:start NAME */` and `/* recipe:end */` markers, and the generator fails on a region the template doesn't show or a name with no region. `devicegen --check` keeps all of it current in CI.
- **Repository checks:** SKILL.md ≤ 500 lines, `description` + `when_to_use` ≤ 1,536 characters (KN8); every reference linked from SKILL.md and every link resolving; and no pixel, millimetre or ppi values, nor the screen's dimensions, in the hand-written text (SKILL.md outside its block, `scenarios.md`, `troubleshooting.md`).
- **The gallery** (`examples/gallery`, built ahead of task 9.1 because the recipes live there): the eight R8.1 recipes plus the app loop, one file each, behind a shell where PWR's short press changes page. Its scenario checks every page against a golden and lint, and runs the flows (open a row as a card and back, a tile tap with its toast, both answers of the confirm dialog, the hold guard rejecting an early hold then approving, a setting surviving a reset): 55 steps in about 11 s. It builds for both targets with no warnings.
- **New board-header constants:** `LCD349_RULE_TOUCH_TARGET_PX`, `LCD349_RULE_FONT_MIN_PX` and `LCD349_RULE_CONTRAST_X100`, generated from the design rules, so products size UI with the lint's own thresholds.
- **Lint corrections found by the gallery:** an object counts as interactive only through a handler for input (a DELETE handler freeing a status bar's state made a 24 px bar fail the touch-target rule); the momentum rule applies only to objects a drag can reach (something in them is clickable), not to a truncated label; under a modal backdrop (a clickable full-screen object on the top layer) screen objects aren't compared with the modal's for overlap; and `ui.lint` and `ui.tree` lay out the top layer too, so a dialog or toast made since the last refresh has its real coordinates.
- **LVGL 9.6 notes for products** (in the design guide and recipes): `lv_obj_add_flag`/`lv_obj_has_flag` are deprecated for per-flag functions (`lv_obj_set_clickable`, `lv_obj_is_hidden`); every object scrolls itself into view on focus by default, which moves lists and tiles under a tap; plain containers are clickable by default.

### Data model: `device/device.json` (excerpt)

```json
{
  "schema": 1,
  "board": { "name": "ESP32-S3-Touch-LCD-3.49", "vendor": "Waveshare", "soc": "ESP32-S3R8",
             "psram_mb": 8, "flash_mb": 16,
             "source": "waveshare wiki", "verified": "2026-10-11" },
  "display": { "panel_w": 172, "panel_h": 640, "landscape_w": 640, "landscape_h": 172,
               "format": "RGB565", "byte_swap": true, "ppi": 190, "mm_w": 86, "mm_h": 23,
               "full_frame_only": true, "flush_ms": null, "flush_ms_source": "task 0.3" },
  "touch": { "type": "capacitive", "points": 1, "period_ms": 30, "wake_source": false,
             "addr": "0x3B", "int_pin_rev": { "V1": null, "V2": "TP_INT" } },
  "buttons": [ { "id": "BOOT", "gpio": 0, "active": "low", "reset_trap": "download mode",
                 "position": null },
               { "id": "PWR", "gpio": 16, "active": "low", "position": null } ],
  "power": { "hold": "TCA9554.P6", "battery_adc": "ADC1_CH3", "divider": 3,
             "states_ma": { "active_backlight": null, "idle_wifi": null, "light_sleep": null } },
  "wifi": { "bands_ghz": [2.4] },
  "budgets": { "internal_ram_kb": null, "psram_kb": 8192, "lvgl_mem_kb": null },
  "fidelity_not_modelled": ["cpu timing", "psram bandwidth", "radio", "brown-out",
                            "flash wear", "audio loudness", "touch feel"],
  "design_rules": [
    { "id": "touch-target", "min_px": 56, "severity": "error",
      "why": "≈7.5 mm at 190 ppi; smaller targets are missed", "exempt_tags": ["list-row"] },
    { "id": "font-min", "min_px": 12, "severity": "error", "why": "≈1.6 mm text" },
    { "id": "contrast", "min_ratio": 4.5, "severity": "error", "why": "IPS 350 cd/m², used outdoors" },
    { "id": "no-momentum", "severity": "error", "why": "full-frame flush makes kinetic scroll judder" },
    { "id": "gradient", "severity": "warning", "why": "RGB565 banding" }
  ]
}
```

`null` marks an unverified value. Unverified values are never used as thresholds (R10.3), and task 0.3 fills them in. The excerpt is the design-time draft: `device/device.json` is the truth, and differs (for example: the flush is 13 ms; touch reports two points and the kit uses the first; the touch interrupt is unusable, kit ADR 0003).

## Error Handling

| Failure | Behaviour |
|---|---|
| A subsystem fails at init (for example, the codec) | `lcd349_board_status` bit set; calls return `LCD349_ERR_UNAVAILABLE`; the product continues |
| `power_off` on USB | Returns `LCD349_ERR_ON_USB` after 1 s (both backends) |
| The emulator build fails | `build` returns parsed compiler errors (file, line, message) and the raw log path |
| An unsupported ESP-IDF component is used in the emulator | A build error naming the component, plus a pointer to the kit API that replaces it (generated table in docs) |
| An instance crashes | The supervisor records the exit status and the last 200 serial lines in `instance.json`; later RPCs return `instance_exited` with them; `power reset` boots it with reason crash |
| An instance stops answering | After 5 s without a `ping` answer the supervisor marks it `hung` and writes a stack sample; an answer clears it |
| The supervisor dies | The product reads end of file on its console, sees its parent gone, and exits; `list` shows the instance as `gone`; `stop` cleans up |
| A control request is malformed or too long | A JSON-RPC error; the instance keeps running |
| `wait` times out | Returns `matched:false` with the last 20 serial lines and a screenshot path |
| The viewer can't open SDL | `lcd349 run` falls back to headless and prints `lcd349 doctor` advice |
| A golden mismatches | The step fails with actual and diff image paths |
| A path outside the project is given | The tool refuses and names the resolved path and the project root |
| Launcher checksum mismatch | Refuses to run; prints the expected and actual hash |

## Threat model

| # | Threat | Mitigation | Residual / accepted |
|---|---|---|---|
| KT1 | Another local user drives an instance | Instance directory 0700, socket 0600, no TCP listener | Root on the same machine (out of scope) |
| KT2 | The agent builds and runs untrusted project code | MCP accepts only lcd349 projects; the docs warn that build and run execute project code (R13.6) | **Accepted:** it's the user's project, and building it is the point |
| KT3 | Path traversal through MCP arguments | Paths resolved with symlinks evaluated and confined to the project root | None |
| KT4 | A tampered `lcd349` binary via the launcher | HTTPS download plus a SHA-256 checksum list shipped inside the (versioned) plugin | Compromise of the plugin repository itself |
| KT5 | Resource exhaustion (huge scale, endless recording, giant trees) | Scale ≤ 4, recording ≤ 60 s, 1 MB tool output cap, 64 KB request cap | None |
| KT6 | Malicious or hallucinated dependency | valvur `check_package` before every addition; `go.sum` committed; exact component versions in the manifest, and `dependencies.lock` committed where it records no local path | Normal residual risk |
| KT7 | Licence contamination from unlicensed vendor code | No Waveshare code; `THIRD_PARTY.md`; review checklist item | None |
| KT8 | Serial pty exposure | Pty slave created 0600, owned by the user | None |

## Testing Strategy

| Layer | What | How |
|---|---|---|
| SDK common | Button debounce and classifier, battery curve, rotation and byte swap (golden byte arrays), tags, error mapping | Unity tests on the `linux` target (`idf.py --preview set-target linux build`, run in CI) |
| Emulator backend | Device model transitions, download mode, Wi-Fi band filter, storage persistence and wipe, flush timing model, control protocol (each method, malformed input, oversize lines), introspection on a known screen, every lint rule with a positive and a negative case | Go integration tests that build `examples/inputs` and a `lint-fixtures` test app for `linux`, then drive them over the socket |
| Viewer | Pure parts (inverse rotation against the kit's `rotate.c`, JSON, layout, scale, touch queue, font); a click and a key reach the product, the tone plays, and the window matches the instance's screenshot pixel for pixel at the backlight level; scale sources; reattaching after a reset | `lcd349-view-tests` (C); `internal/emutest/view_test.go` with SDL's dummy drivers, in CI |
| CLI and MCP | Every subcommand and tool; path validation; instance cleanup on session end; scenario runner and golden diff | `go test ./...`; MCP tested over the go-sdk in-memory transport |
| Plugin | Manifest, marketplace, launcher (PATH hit, download, checksum mismatch) | `claude plugin validate`; shell tests with a local HTTP fixture |
| Generator | device.json → generated files are current | `go run ./internal/devicegen --check` in CI |
| Examples | Both targets build; scenarios and goldens pass headless | CI matrix |
| Hardware | Each subsystem on the board; parity; measurements | `test_apps/board` (Unity, with the `lcd349_harness` component) run by `tools/boardtest`, one subsystem per run (phase 1, D1-6 and D1-12); the guided checkpoint (`boardtest -guide`) for what needs a person |
| Agent success | KN6 | `evals/run.sh` (manual, per release) |

## Deployment and Operation

- **Prerequisites:**
  - ESP-IDF at the version in `VERSIONS` (`lcd349 doctor` checks it);
  - Go, only to build from source;
  - SDL2, for the viewer (Homebrew `sdl2`, or the distro package);
  - on Linux, `libbsd-dev` for the `linux` target.
- **Using the SDK:** in the product's `main/idf_component.yml`:

  ```yaml
  dependencies:
    lcd349:
      git: https://github.com/MaverickHQ/lcd349-kit.git
      path: components/lcd349
      version: v0.1.0
  ```

  During kit development, point `override_path:` at a checkout's `components/lcd349`.
- **Using the agent tooling:** `/plugin install lcd349 --marketplace MaverickHQ/lcd349-kit` (Claude Code 2.1.275 or later). On older versions, `/plugin marketplace add MaverickHQ/lcd349-kit`, then `/plugin install lcd349@lcd349-kit`. Plugin MCP tools are deferred, so they appear as `mcp__plugin_lcd349_lcd349__<tool>` once loaded (spike 0.4).
- **CI:**
  - GitHub Actions on Linux in the `espressif/idf` container at the pinned version: both targets, examples, scenarios, Go tests, generator check, plugin validate.
  - A macOS runner builds the CLI and the viewer.
- **Release:**
  - Tag `vX.Y.Z`.
  - Publish the `lcd349` and `lcd349-view` binaries per platform plus `checksums.txt`.
  - Update `plugin/scripts/checksums.txt` and the `plugin.json` version in the same commit.
  - Add a CHANGELOG entry.
- **Rollback:** consumers pin a tag, and the plugin pins its binary version. Rolling back means pinning the previous tag.
- **The plugin as built (task 8.2):** `plugin.json`'s version, the launcher's `VERSION` and the binary's version are one (a repository check). The launcher accepts `lcd349` on PATH when `lcd349 version` prints `lcd349 <VERSION>`. `checksums.txt` has no entries until a release writes them, so a development plugin needs `lcd349` on PATH, and says so. Tests run the real launcher against a local HTTP fixture: a PATH hit, a PATH binary of another version skipped, a download verified, run and then reused, a tampered cached binary replaced, a checksum mismatch refused with nothing left behind, and no binary at all. During development, `claude --plugin-dir plugin` loads it for one session without installing. The viewer isn't part of the launcher's download yet: with the plugin, a window is built from source on demand (task 10.5 decides whether releases ship it).

## Design notes

- [An agent harness and a Docker runtime](design-note-harness-and-docker.md) (2026-10-17, proposal): build the board layer on existing harnesses rather than a new one, and add a Docker image as a second runtime. Not adopted yet; the web viewer would amend requirement 13.1.
