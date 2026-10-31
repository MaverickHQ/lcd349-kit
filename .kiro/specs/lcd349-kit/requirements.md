# Requirements Document

## Introduction

lcd349-kit is a reusable development kit for the Waveshare ESP32-S3-Touch-LCD-3.49 (the "B" listing is the version with Case B and a Li-Po battery). The board has:

- a 640×172 capacitive touch "bar" display;
- two usable buttons;
- a speaker, battery, RTC and IMU;
- an ESP32-S3R8 with 8 MB PSRAM.

The kit has four deliverables:

1. **Board SDK:** an ESP-IDF component (`lcd349`) with a small C API for everything on the board, and two backends, hardware and emulator.
2. **Emulator:** runs a product built from the **same sources** for ESP-IDF's `linux` target, as a faithful model of the device, limits included. It runs either in a window at physical size or headless, and is driven over a local control socket.
3. **`lcd349`:** one Go binary that is both a CLI and an MCP server. It builds, runs, screenshots, inspects, lints, injects input, changes device state and runs scenarios.
4. **Claude Code plugin:** the `lcd349` skill plus the MCP server, so that an agent knows the device's features and limits and can design, build and verify an interactive UI in the emulator.

**Consumers:** herdr-pager (its desk pager builds against this kit's local `v0.1.0` tag) and one more project whose stack isn't decided yet (see Requirement 16 and the decision gate in §Scope).

Terms:

- **Product:** an ESP-IDF project that depends on the `lcd349` component.
- **Backend:** the hardware or emulator implementation behind the SDK API.
- **Instance:** one running emulated device.
- **Scenario:** a scripted sequence of steps run against an instance.
- **Golden:** a reference screenshot.
- **Lint:** the UI checker driven by the device's design rules.
- **device.json:** the single machine-readable description of the board.

## Requirements

### Requirement 1: Board SDK API

**User Story:** As a product developer, I want one small, documented C API for everything on the board, so that my product code doesn't care whether it runs on the board or in the emulator.

#### Acceptance Criteria

1. THE SDK SHALL be an ESP-IDF component named `lcd349`. Its public headers SHALL include only C standard headers and LVGL 9 headers, and SHALL be usable from C++ (`extern "C"`).
2. THE API SHALL cover:
   - initialisation and status;
   - display: an LVGL display in landscape 640×172, backlight 0–100 %, display sleep and wake;
   - touch: an LVGL pointer input, plus a subscription to raw first-point samples;
   - buttons: BOOT and PWR down and up events with millisecond timestamps, plus an optional classifier (short, holds at configurable thresholds, chord);
   - power: hold, power off, sleep until a button press, wake cause;
   - battery: millivolts and an estimated percentage;
   - audio: tone (frequency, duration, volume), tone sequences, stop;
   - RTC: get and set UTC;
   - key-value storage: namespaced get, set and erase of strings and blobs;
   - Wi-Fi station: up to 4 saved networks in priority order, start, scan, status events with SSID and RSSI;
   - a monotonic microsecond clock;
   - UI tags for lint (Requirement 6);
   - the board's static description (`lcd349_board_info`), generated from device.json.
3. WHEN `lcd349_init` runs THEN it SHALL assert the power hold before initialising the display or anything slow.
4. EVERY API call SHALL return an `lcd349_err_t` or a documented value, and SHALL NOT abort on a hardware error. A subsystem that failed or is absent SHALL be reported by `lcd349_board_status`, and its calls SHALL return `LCD349_ERR_UNAVAILABLE`.
5. Touch SHALL NEVER be a wake source, and touch SHALL be read only while the backlight is on.
6. Products SHALL do network I/O through BSD sockets, which both backends provide. The SDK SHALL NOT wrap sockets.
7. Callbacks SHALL run in a documented kit task and SHALL NOT be called from interrupt context.
8. THE API SHALL follow semantic versioning. Before 1.0, a breaking change to a public header SHALL bump the minor version and be listed in `CHANGELOG.md`.

### Requirement 2: Hardware backend

**User Story:** As a product developer, I want the hardware backend to bring the board up correctly and safely, so that I never fight pin maps, power latches or panel quirks.

#### Acceptance Criteria

1. THE hardware backend SHALL use only licensed code:
   - `espressif/esp_lcd_axs15231b` (Apache-2.0) for the panel;
   - `espressif/esp_codec_dev` for the ES8311 codec;
   - ESP-IDF's own drivers;
   - the kit's own code for the TCA9554 expander and the touch reports.

   It SHALL contain no code copied from Waveshare's demo repository, which has no licence.

   (Amended 2026-10-23 by phase 1, `.kiro/specs/phase-1-kit-mvp/`, design D1-1 and D1-2.) The kit writes the expander's registers itself (kit ADR 0001): `esp_io_expander_tca9554`'s constructor resets the expander, which releases the power latch (P6) on battery on any warm restart. The kit also reads and decodes the AXS15231B touch reports itself (kit ADR 0002): the component's touch driver (`esp_lcd_touch_axs15231b`) treats "no new report" as a lift, reads a malformed frame as a release, and drops two-point reports, and the board showed all three.
2. THE display SHALL be driven with full-frame flushes and software rotation to landscape, because the AXS15231B panel is reported to need both.
3. Buttons SHALL be BOOT = GPIO0 and PWR = GPIO16, active-low and debounced. The docs SHALL warn that holding BOOT through a reset enters the ROM download mode.
4. Power off SHALL release TCA9554 P6. IF the board is still running 1 second later (on USB power) THEN `lcd349_power_off` SHALL return `LCD349_ERR_ON_USB`.
5. Battery SHALL be read on ADC1 channel 3 through the ×3 divider, averaged, and mapped to a percentage with a Li-Po discharge curve.
6. Sleep SHALL use light sleep with wake on BOOT or PWR, with Wi-Fi modem power save on.
7. THE board revision (V1/V2) SHALL be a Kconfig option. Touch SHALL be polled every `touch.period_ms` (30 ms), on both revisions, and only while the display is awake and lit; the touch interrupt SHALL NOT be used. (Amended 2026-10-23 by phase 1, design D1-4 and kit ADR 0003, which replace "On V2, the touch interrupt MAY replace polling while the screen is on". The touch interrupt reaches the expander's P0, and the expander's interrupt line (GPIO42) fires on every change of any expander input, including the panel's TE on P5, about 122 times a second.)

### Requirement 3: Emulator backend and device model

**User Story:** As a product developer, I want my unchanged product to run in an emulator that behaves like the board, limits included, so that what works there works on the device.

#### Acceptance Criteria

1. WHEN a product is built with `lcd349 build` THEN it SHALL compile from the same sources for ESP-IDF's `linux` target, with the emulator backend replacing the hardware backend. No product source file SHALL need a target check.
2. **Display:**
   - The emulator SHALL hold a panel-native 172×640 RGB565 framebuffer.
   - It SHALL receive exactly the bytes the hardware backend would send to the panel, including rotation and byte order.
   - It SHALL present the framebuffer in landscape.
   - The backlight level SHALL dim the presented image; 0 SHALL present black.
3. **Display timing:** each flush SHALL complete only after the flush time measured on hardware (device.json, from task 0.3), so animation runs at the board's speed (KN4).
4. **Touch:** the emulator SHALL deliver one touch point, sampled every 30 ms, only while the backlight is on. Touch SHALL never wake the device.
5. **Buttons:** the emulator SHALL model BOOT and PWR. WHEN an instance is reset with BOOT held THEN the emulator SHALL show "DOWNLOAD MODE" and not start the product, as the board would.
6. **Power:**
   - The emulator SHALL model the power hold, the USB or battery source, and light sleep, which pauses rendering until a button press.
   - Powering off on battery SHALL stop the instance and present "OFF".
   - On USB, `power_off` SHALL return `LCD349_ERR_ON_USB`.
7. **Battery:** the voltage SHALL be settable. An optional drain model SHALL use the per-state current draw in device.json.
8. **Audio:** tones SHALL be recorded as events (time, frequency, duration, volume) and SHALL be played on the host's audio output when a viewer is attached.
9. **Wi-Fi:**
   - The emulator SHALL simulate networks with an SSID, band, RSSI and password.
   - 5 GHz networks SHALL be invisible to scans and connections, because the ESP32-S3 is 2.4 GHz only.
   - Connecting, failing and dropping SHALL raise the same status events as the hardware backend.
   - Sockets SHALL use the host's network.
10. **Storage:** each instance SHALL have file-backed storage that persists across resets and is cleared by `--wipe`.
11. **RTC:** the clock SHALL start from the host clock, or from a fixed `--clock` value for deterministic screenshots.
12. **Fidelity list:** THE emulator SHALL report what it doesn't model, from device.json:
    - CPU speed and cycle timing;
    - cache and PSRAM bandwidth;
    - radio behaviour;
    - brown-outs;
    - flash wear;
    - audio loudness;
    - touch feel.
13. **Memory:** THE emulator SHALL report LVGL memory use and the largest allocations made through `heap_caps_malloc`. It SHALL warn when either exceeds the board's budgets in device.json.

### Requirement 4: Window mode (viewer)

**User Story:** As a developer, I want to see and use the emulated device on my screen at its real size, so that I judge layouts as they'll look in the hand.

#### Acceptance Criteria

1. `lcd349 run` SHALL open a viewer window showing the device at physical size by default, using the host display's DPI. `--scale` SHALL override it.
2. THE viewer SHALL draw:
   - the case outline;
   - both buttons at their physical positions from device.json;
   - state badges for power source, battery, Wi-Fi and sleep.
3. THE mouse SHALL act as the finger. Keys `b` and `p`, and clicks on the drawn buttons, SHALL press BOOT and PWR, held for as long as they are held.
4. THE viewer SHALL be a separate process attached to a headless instance. Products SHALL never link SDL.

### Requirement 5: Headless mode and control socket

**User Story:** As an agent or a CI job, I want to drive an instance without a window, so that I can test and iterate automatically.

#### Acceptance Criteria

1. `lcd349 run --headless` SHALL start an instance with no viewer and a control socket.
2. THE control socket SHALL be a Unix-domain socket in a per-user instance directory with mode 0700, and the socket file SHALL have mode 0600. Neither the emulator nor the CLI SHALL open a TCP or UDP listener.
3. THE control protocol (JSON-RPC 2.0, one message per line) SHALL offer:
   - `info` and `state.get`;
   - `state.set` (battery, power source, Wi-Fi networks, board revision, clock);
   - `power` (on, reset, reset with BOOT held, off);
   - `screenshot`, `ui.tree` and `ui.lint`;
   - `input.press` and `input.touch`;
   - `wait`;
   - `audio.events`;
   - `record.start` and `record.stop`.
4. `screenshot` SHALL return a PNG of the landscape image as presented, or of the raw panel framebuffer with `raw:true`, optionally at physical scale.
5. `input.touch` SHALL accept tap, long press, swipe and raw point sequences, and SHALL deliver them as timed samples at the 30 ms touch period. `input.press` SHALL accept a button with a hold duration, or a chord.
6. `wait` SHALL return when any of these happens:
   - a serial line matches a regular expression;
   - a given text is on screen;
   - the display has been idle (no flush) for a given time;
   - the timeout expires.
7. THE instance's serial console SHALL be exposed as a pseudo-terminal whose path is in the instance's `instance.json`, so host tools (for example `pager-bridge provision`) can talk to the product as they would over USB. Serial output SHALL also be written to the instance's `serial.log`.
8. A malformed request SHALL get a JSON-RPC error and SHALL NOT crash the instance. Lines over 64 KB SHALL be rejected.

### Requirement 6: UI introspection and lint

**User Story:** As an agent designing a UI, I want the screen as structured data and a checker that knows the device's limits, so that I can fix problems I can't see in a screenshot.

#### Acceptance Criteria

1. `ui.tree` SHALL return the active screen's LVGL object tree. Each object SHALL have:
   - its class, coordinates and size;
   - visibility, and its clickable and scrollable flags;
   - its text, if it is a label;
   - its font and font size;
   - its text and background colours;
   - its tags;
   - a stable path.
2. `ui.lint` SHALL check the screen against the design rules in device.json and return findings, each with a severity, rule id, object path and fix hint. The rules SHALL cover:
   - clickable objects smaller than the minimum touch target (default 56 × 56 px), except objects tagged `list-row`;
   - text below the minimum font size (default 12 px);
   - text below the minimum contrast ratio against its background (default 4.5:1);
   - text clipped by or overflowing its object;
   - glyphs missing from the font for the text shown;
   - objects outside the 640×172 screen;
   - overlapping clickable objects;
   - scroll momentum (kinetic scrolling) enabled;
   - gradients (a warning, because they band in RGB565).
3. Lint rules SHALL be data in device.json (`design_rules`), each with an id, a threshold and a rationale that the skill also uses.
4. A product SHALL be able to tag objects (`list-row`, `ignore:<rule-id>`) with an SDK call, so lint exceptions are explicit and reviewable.

### Requirement 7: `lcd349` CLI and MCP server

**User Story:** As a developer or an agent, I want one tool that builds, runs and drives the emulator, with the same features over MCP, so that people, scripts and agents share one interface.

#### Acceptance Criteria

1. THE CLI SHALL be one Go binary with the subcommands `doctor`, `new`, `build`, `run`, `stop`, `list`, `shot`, `tree`, `lint`, `press`, `touch`, `logs`, `wait`, `state`, `scenario` and `mcp`.
2. `lcd349 build -C <project>` SHALL build the emulator target in a separate build directory and sdkconfig under `<project>/build-emu/`, so it never disturbs the project's board build. It SHALL run `idf.py` with a fixed argument list and no shell.
3. `lcd349 doctor` SHALL check the following and print a fix for each failure:
   - the ESP-IDF version;
   - the `linux` target's prerequisites;
   - SDL, for the viewer;
   - the kit version the project pins.
4. `lcd349 mcp` SHALL run an MCP server over stdio. Its tools SHALL be `device_info`, `new_project`, `build`, `run`, `stop`, `screenshot` (returning image content), `ui_tree`, `ui_lint`, `press`, `touch`, `logs`, `wait`, `get_state`, `set_state`, `run_scenario` and `record`.
5. MCP tools SHALL accept a project path only if it is a directory holding an ESP-IDF project that depends on the `lcd349` component. Every other path, including golden and scenario paths outside that project, SHALL be refused with an explanation.
6. THE MCP server SHALL track the instances it starts and SHALL stop them when the MCP session ends.
7. `lcd349 scenario <file>` SHALL run a YAML scenario headless and exit non-zero on the first failing step, printing the step and the path of a diff image. Steps are run, press, touch, wait, shot (with a golden compare), lint and state.
8. Golden compares SHALL be pixel-exact unless a step sets a tolerance. WHEN a golden is missing THEN it SHALL be written and the step SHALL fail, unless `--update-goldens` is given.

### Requirement 8: Claude skill

**User Story:** As a developer working with Claude, I want a skill that knows this board, its limits and how to design for it, so that an agent can design, build and verify an interactive UI with little guidance.

#### Acceptance Criteria

1. THE skill SHALL include:
   - device facts and limits;
   - a design guide for a 640×172 touch bar with two buttons;
   - the SDK API reference;
   - UI recipes: status bar, list, detail card, horizontal menu tiles, confirm dialog, toast, hold-to-confirm and setup screen;
   - the emulator's fidelity list;
   - a hardware verification checklist.
2. THE skill SHALL define the workflow:
   1. scaffold with `new_project`;
   2. sketch the layout as a 640×172 wireframe with sizes;
   3. build and run;
   4. check with `screenshot`, `ui_tree` and `ui_lint`;
   5. fix and repeat;
   6. write a scenario with goldens;
   7. hand the hardware checklist to the user.
3. THE skill's device facts and thresholds SHALL be generated from device.json and never copied by hand.
4. SKILL.md SHALL stay under 500 lines, with detail in reference files loaded on demand. Its `description` plus `when_to_use` SHALL stay under 1,536 characters.
5. THE skill SHALL tell the agent to verify on hardware everything the fidelity list says the emulator doesn't model.

### Requirement 9: Plugin and distribution

**User Story:** As a developer, I want to install the kit's agent tooling with two commands and consume the SDK by version, so that adopting the kit takes minutes.

#### Acceptance Criteria

1. THE repository SHALL contain a Claude Code marketplace (`.claude-plugin/marketplace.json`) listing the `lcd349` plugin from the `plugin/` subdirectory. Users SHALL be able to install with `/plugin install lcd349 --marketplace MaverickHQ/lcd349-kit` (Claude Code 2.1.275 or later), or with `/plugin marketplace add MaverickHQ/lcd349-kit` followed by `/plugin install lcd349@lcd349-kit`.
2. THE plugin SHALL bundle the skill and an MCP server entry whose command is a launcher script. The launcher SHALL:
   - use `lcd349` from PATH if its version matches the plugin's;
   - otherwise download the release binary for the host OS and architecture into `${CLAUDE_PLUGIN_DATA}`;
   - verify the download's SHA-256 against a checksum list shipped inside the plugin before running it.
3. EACH release SHALL publish:
   - the SDK as a git tag that `idf_component.yml` can pin;
   - `lcd349` and the viewer for macOS arm64 and Linux x86_64, with SHA-256 checksums (the owner's decision, 2026-10-30, grilling item 25; it was macOS arm64/x86_64 and Linux x86_64/arm64);
   - a plugin version equal to the binary version.
4. `claude plugin validate` SHALL pass in CI.
5. THE repository SHALL be licensed Apache-2.0. Third-party licences (LVGL MIT, fonts OFL, Espressif components Apache-2.0) SHALL be listed in `THIRD_PARTY.md`.

### Requirement 10: Device knowledge as data

**User Story:** As a maintainer, I want every fact about the board in one checked file, so that the SDK, emulator, lint, skill and docs can never disagree.

#### Acceptance Criteria

1. `device/device.json`, validated by `device/device.schema.json`, SHALL be the single source of:
   - board facts: pins, sizes, pixel density, memory, buttons and their physical positions, touch, audio, power;
   - budgets;
   - the fidelity list;
   - per-state current draw;
   - the display flush time;
   - the design rules.
2. A generator SHALL produce the following from it, and CI SHALL fail when a generated file is stale:
   - the skill's device and fidelity references;
   - the SDK's `lcd349_board_info` constants;
   - the emulator's defaults;
   - the lint thresholds;
   - the docs' hardware page.
3. EVERY fact SHALL carry a source (datasheet, Waveshare wiki, or a measurement task) and a verification date. A fact marked unverified SHALL NOT be used as a lint threshold.

### Requirement 11: Templates and examples

**User Story:** As a developer, I want a working starting point and reference examples, so that an agent or I can start from something that already passes lint and tests.

#### Acceptance Criteria

1. `lcd349 new <dir> --name <app>` SHALL scaffold a product from `templates/app` with:
   - an ESP-IDF project that pins the kit;
   - a `main` with init and one screen;
   - one scenario with one golden;
   - a `CLAUDE.md` pointing at the skill.
2. THE examples SHALL include:
   - `hello`: one screen, with buttons and touch logged;
   - `gallery`: every recipe at real size;
   - `inputs`: visualises touch samples, button timings, battery, Wi-Fi and power states.
3. EVERY example and the template SHALL build for both targets in CI and SHALL have scenarios with goldens that pass headless.

### Requirement 12: Hardware parity

**User Story:** As a developer, I want evidence that the emulator matches the board, so that I can trust it for design work.

#### Acceptance Criteria

1. WHEN `gallery` runs on the board and in the emulator THEN their static screens SHALL match visually (a human checkpoint in the MVP; byte-exact through hardware-in-the-loop in Phase 1.5).
2. THE flush time, touch sampling period, idle current and per-state current SHALL be measured on hardware and recorded in device.json with dates.

### Requirement 13: Security and hygiene

**User Story:** As a maintainer, I want the kit to be safe to run on a developer's machine and clean to publish, so that adopting it carries no hidden risk.

#### Acceptance Criteria

1. Neither the emulator nor the CLI SHALL open a network listener. Control SHALL be through local Unix sockets only.
2. NO subprocess SHALL be started through a shell. `idf.py`, the product binary and the viewer SHALL get fixed argument lists, and MCP-supplied paths SHALL be validated (7.5).
3. BEFORE any dependency is added (Go module, ESP-IDF component or vendored code), valvur `check_package` SHALL be run. A flagged package SHALL NOT be added without the maintainer's approval.
4. NO code SHALL be copied from Waveshare's demo repository. Facts taken from it SHALL cite it.
5. THE launcher SHALL verify a binary's SHA-256 before running it, and SHALL refuse to run it on a mismatch.
6. THE docs SHALL state that `build` and `run` execute the project's own code, so the MCP server should be pointed only at trusted projects.

### Requirement 14: Hardware in the loop (Phase 1.5)

**User Story:** As an agent, I want the same screenshot and input tools against the real board, so that I can verify a design on hardware without a person describing the screen.

#### Acceptance Criteria

1. WHERE a product is built with `CONFIG_LCD349_HIL=y` THE board SHALL accept control requests over its USB serial console as prefixed lines, supporting `info`, `screenshot` (framebuffer readback), `input.press`, `input.touch` and `wait`.
2. THE CLI and the MCP tools SHALL target a board with `--device <serial port>`, using the same commands.
3. Screenshots of `gallery`'s static screens from the board and the emulator SHALL be byte-identical.

### Requirement 15: QEMU tier (Phase 2)

**User Story:** As a developer, I want to run the real `esp32s3` binary under emulation, so that I can test code the `linux` target can't host.

#### Acceptance Criteria

1. THE kit SHALL offer a QEMU backend that runs the real `esp32s3` binary in Espressif's QEMU with the `esp_lcd_qemu_rgb` virtual framebuffer, with input and control over a virtual UART, if a spike shows it is feasible.

### Requirement 16: Framework adapters (Phase 2, gated)

**User Story:** As the maintainer of the second consuming project, I want the kit to support my stack, so that I can use it without moving to ESP-IDF C.

#### Acceptance Criteria

1. WHEN the second consuming project's stack is known THEN the kit SHALL add at most one adapter for it, chosen at the decision gate in §Scope:
   - **Arduino or PlatformIO:** a C++ wrapper header, an example using Arduino as an ESP-IDF component, and a PlatformIO example;
   - **ESPHome:** a board package (YAML) for the board, using ESPHome's own SDL host display for desktop work.

## Non-Functional Requirements

| ID | Requirement | Budget | How it is measured |
|---|---|---|---|
| KN1 | Agent loop speed | `screenshot` ≤ 300 ms. Input applied and the next frame presented ≤ 200 ms. Incremental emulator `build` after a one-file change ≤ 20 s on an M-series Mac. | `lcd349 --timings` over 20 runs of each |
| KN2 | Start-up | Instance start to first frame ≤ 3 s | `lcd349 run --headless --timings`, 5 runs |
| KN3 | Pixel fidelity | Static screens match the board: human visual check in the MVP; byte-identical in Phase 1.5 (14.3) | Checkpoint 10.1; HIL compare later |
| KN4 | Timing fidelity | Modelled flush time within ±20 % of the hardware measurement | Compare instance logs with the task 0.3 measurement |
| KN5 | Platforms | macOS arm64 (primary); Linux x86_64 (CI, headless). macOS x86_64 and Linux arm64 best effort. | CI matrix |
| KN6 | Agent success | In 5 fresh Claude Code sessions with only the plugin installed and a one-paragraph brief for a 3-screen app (list, detail, menu; buttons and touch), at least 4 produce a project that builds for both targets, passes lint with no errors and passes its own scenario, with no human help | `evals/run.sh`; transcripts and results in `evals/results/` |
| KN7 | CI time | All examples' scenarios run in ≤ 5 minutes on GitHub Actions Linux | CI timing |
| KN8 | Skill size | SKILL.md ≤ 500 lines; `description` + `when_to_use` ≤ 1,536 characters | A CI check |
| KN9 | Stability | An instance runs an 8-hour scripted scenario loop with no crash and less than 10 % growth in resident memory | Soak script sampling RSS every minute |

## Scope

### MVP (v0.1.0)

Requirements 1–13 and KN1–KN9, with KN3 checked by a person. The SDK covers display, touch, buttons, power, battery, audio tones, RTC, storage and Wi-Fi.

### Phase 1.5

- Requirement 14 (hardware in the loop).
- SDK additions: IMU, microphone capture, SD card.

### Phase 2

- Requirement 15 (QEMU tier).
- Requirement 16 (one framework adapter, gated).
- Publish the component to the Espressif Component Registry.

### Roadmap (explicitly deferred — do NOT build)

- Other boards.
- A visual UI editor.
- LVGL XML hot reload (evaluate first: licence, maturity, agent value).
- Windows hosts.

### Decision gate: the second project's stack

The kit stays focused on ESP-IDF + LVGL 9 in C until the second project's stack is known. Three rules keep the door open at no extra cost:

- Public headers stay framework-neutral (1.1).
- The control protocol, MCP tools and lint act on the device model, not on the product's framework.
- The device knowledge (device.json and the design guide) is usable by any stack.

When the second project picks its stack:

| Stack | Action |
|---|---|
| ESP-IDF + LVGL in C | Nothing; it's supported. |
| Arduino or PlatformIO | Requirement 16 Arduino adapter. A sketch that uses only the kit API runs in the emulator. Arbitrary Arduino libraries won't, and the docs say so. |
| ESPHome | Requirement 16 ESPHome board package. ESPHome's own SDL host display covers desktop layout work; this kit's emulator is not extended to run ESPHome. |
| MicroPython, Rust or other | Out of scope. Device knowledge and the design guide still apply. |

### Cut list (drop in this order if the build runs into trouble)

1. Viewer polish: the case outline and state badges (4.2). A plain window remains.
2. Host audio playback (3.8 playback clause). Tone events remain.
3. Battery drain model (3.7 drain clause). A settable voltage remains.
4. Recording (`record.start`/`record.stop`, the `record` tool).
5. The `gallery` example (11.2). `hello` and `inputs` remain, and checkpoint 10.1 uses `inputs`.

Never cut:

- same-source builds (3.1);
- the control socket with screenshot, tree, lint and input (Requirements 5 and 6);
- device.json as the single source (Requirement 10);
- the skill (Requirement 8);
- the fidelity list (3.12).

**Abort threshold:** the kit fails if neither the `linux` target (design D1) nor its fallback can run an unchanged product with the emulator backend on macOS arm64.

## Assumptions and RECON

Recon performed 2026-10-12 on the host (macOS 26.6.2, arm64) and from published sources:

- **VERIFIED — host:** Go 1.26.4; Homebrew SDL3 3.4.12 and sdl2-compat 2.32.70 are installed; ESP-IDF isn't installed; the kit's folder didn't exist before this spec.
- **VERIFIED — ESP-IDF `linux` target** (ESP-IDF "Running ESP-IDF Applications on Host"):
  - It is experimental, and the FreeRTOS simulator is a "preview target".
  - Each FreeRTOS task runs as a pthread, in single-core mode.
  - Scheduling is described as cooperative ("a context switch happens only when a task interacts with the FreeRTOS kernel"). **CORRECTED (task group 4, 2026-10-13):** the v5.5.5 port is preemptive (`configUSE_PREEMPTION` 1, time slicing; the SIGALRM tick switches tasks anywhere), and libc locks aren't protected. See design §Threading on the `linux` target.
  - Timing granularity is the tick period.
  - Blocking FreeRTOS primitives can't be called from threads not created through FreeRTOS.
  - It "currently also works on macOS".
  - The root CMakeLists sets `set(COMPONENTS main)`.
  - Its component table lists simulation support for esp_event, nvs_flash, log, lwip and esp_netif. esp_timer and driver are mock-only. esp_lcd and esp_wifi aren't listed, so the emulator backend must replace them.
- **VERIFIED — `espressif/esp_lcd_axs15231b`:** Apache-2.0. It drives the AXS15231B panel and its touch controller over QSPI, SPI or I80 (plus MIPI-DSI in 2.1.x). The latest version seen is 2.1.1.
- **VERIFIED (community source, not official):** an AXS15231B QSPI panel on another board only works with full-frame LVGL refresh, has no hardware rotation, and some batches ignore window-address commands (atomic14's Guition JC3248W535 write-up).
- **VERIFIED — Espressif QEMU:** supports ESP32-S3 and offers a virtual framebuffer; `espressif/esp_lcd_qemu_rgb` supports RGB565 and ARGB8888; `idf.py qemu --graphics` shows it.
- **VERIFIED — LVGL 9.5:** `LV_USE_TEST_SCREENSHOT_COMPARE` compares the screen with a reference PNG using lodepng (`LV_USE_LODEPNG`), and the test module includes `lv_test_indev.h` for input emulation (its function names are unconfirmed).
- **VERIFIED — MCP:** the official Go SDK `github.com/modelcontextprotocol/go-sdk` has a stdio transport. v1.7.0 was current at recon; task 0.4 pinned **v1.8.0** (2026-09-04, the latest stable, with the same dependencies).
- **VERIFIED — Claude Code plugins** (official docs, via a docs lookup):
  - Layout: `.claude-plugin/plugin.json`, `skills/<name>/SKILL.md`, and `.mcp.json` at the plugin root.
  - Variables: `${CLAUDE_PLUGIN_ROOT}` and `${CLAUDE_PLUGIN_DATA}`.
  - Marketplace: `.claude-plugin/marketplace.json` with relative plugin sources.
  - Installation copies the whole plugin directory into a cache, so the plugin lives in `plugin/`, not the repo root.
  - Platform-specific binaries have no documented pattern.
  - claude.ai and Cowork won't install a plugin with a top-level `bin/`.
  - SKILL.md should stay under 500 lines, and `description` + `when_to_use` is truncated at 1,536 characters.
- **VERIFIED — ESPHome** has an SDL host display platform and an `axs15231` touchscreen platform (relevant only to Requirement 16).
- **VERIFIED — board facts:** from herdr-pager's recon (Waveshare wiki and demo repo, read for facts only); they seed device.json.
  - BOOT = GPIO0 and PWR = GPIO16, both active-low.
  - Power hold = TCA9554 P6; backlight = GPIO8.
  - LCD on QSPI (CS 9, PCLK 10, D0–D3 11–14, RST 21).
  - Touch: capacitive, I2C 0x3B (SCL 18 / SDA 17); the demo reads only the first point; no touch interrupt on V1.
  - Battery on ADC1 channel 3 through a ×3 divider; Case B ships with a 3.7 V Li-Po.
  - Other chips: RTC 0x51, IMU 0x6B, ES8311 codec.
  - About 190 pixels per inch, so the active area is about 86 × 23 mm and 1 mm ≈ 7.5 px.
  - Wi-Fi is 2.4 GHz only.
- **VERIFIED — toolchain pin (task 0.1, 2026-10-12):** ESP-IDF **v5.5.5** satisfies all four constraints. arduino-esp32 3.3.12 (latest stable) is built on v5.5.5 and allows ≥5.3 and <6.2; `esp_lcd_axs15231b` 2.1.1 needs ≥5.3; `esp_codec_dev` 1.6.2 needs ≥4.0; LVGL 9.6.0 has no constraint. `hello_world` built for `esp32s3`, and a FreeRTOS app built and ran on the `linux` target on this Mac. Recorded in `/VERSIONS`; evidence in `spikes/emu/RESULTS.md`.
- **VERIFIED — emulator architecture (task 0.2, 2026-10-12): design D1 as written.**
  - LVGL 9.6.0 builds unmodified for `linux`.
  - A FreeRTOS-task control server works on a Unix socket (owner-only file modes).
  - A file-backed framebuffer works with a seqlock.
  - Host BSD sockets work directly from FreeRTOS tasks, without lwIP.
  - `heap_caps_malloc` works, but `heap_caps_get_free_size` returns UINT32_MAX, so the kit counts allocations through `lcd349_alloc`.
  - A separate SDL2 viewer completed the click → frame loop.
  - Timings: ping 1 ms, screenshot 37 ms, touch release → frame 6 ms median.
  - An idle instance uses 2–4 % CPU and about 1.1 MB.
- **VERIFIED — side-by-side builds (task 0.2, 2026-10-12):** `-B build-emu -D SDKCONFIG=build-emu/sdkconfig` leaves the board build untouched, so 7.2 holds as written. Projects need per-target lock files (`DEPENDENCIES_LOCK dependencies.lock.${IDF_TARGET}`); otherwise the shared lock file flips target.
- **CORRECTED — host display DPI (task group 5, 2026-10-13):** SDL's display DPI isn't physical with Homebrew's sdl2-compat on macOS: it reports 96 × the content scale (192 on a 2× Retina panel), while CoreGraphics gives 286.9 mm across 1440 points (127 points per inch). The viewer uses CoreGraphics on macOS and flags nominal DPI values elsewhere (design §Viewer). The dummy driver reports 96.
- **VERIFIED — instances and the serial port on macOS (task 6.1, 2026-10-13):**
  - A pty's terminal settings reset when its last descriptor closes, so raw mode only survives host tools opening and closing the port if the supervisor holds a descriptor.
  - When a session leader exits, macOS revokes its controlling terminal; giving the product its pty as controlling terminal cut the relay after the first reset. The product gets its own session without one.
  - ESP-IDF's `linux` `main()` installs SIGABRT and SIGSEGV handlers that print and call `_exit(1)`: product crashes exit with code 1, not by signal, and skip atexit handlers, so the kit drains its serial pipe from its own handler first.
  - Start to first frame through `lcd349 run --headless`: 0.48 s (KN2 budget 3 s). An emulator build with nothing to do: 0.9 s.
- **VERIFIED — unsupported components on the `linux` target (task 6.2, 2026-10-13):** besides those that don't resolve (esp_lcd, esp_wifi, esp_adc, esp_pm, esp_driver_i2s, driver, …), `esp_driver_gpio` resolves but has no `driver/gpio.h` (so `driver/ledc.h` fails too), `esp_driver_uart` has no header, and I2C, USB Serial/JTAG and `esp_light_sleep_start` compile but don't link. `docs/emulator-components.md` lists the kit replacement for each.
- **VERIFIED — LVGL 9.6 behaviour that matters on this panel (task 8.1, 2026-10-13):** every object has "scroll on focus" set, so a tap focuses it and scrolls it into view (lists and tile rows moved under a tap); plain containers are clickable; labels are scrollable, and a truncated multi-line label has a scroll range; `lv_obj_add_flag`/`lv_obj_has_flag` are deprecated in favour of per-flag functions.
- **VERIFIED — board bring-up, first part (task 0.3, 2026-10-18; `spikes/hw/RESULTS.md`):**
  - ESP32-S3 revision v0.2, 8 MB octal PSRAM, 16 MB flash; every expected chip answers on the main I2C bus (SDA 47 / SCL 48) and touch on its own bus (SDA 17 / SCL 18).
  - The backlight is active low. The panel needs only DCS sleep-out and display-on. Over QSPI it takes no row address, so frames go out whole (2.1 holds).
  - Full-frame flush: 13.0 ms with strip rotation into DMA bounce buffers; 75.7 ms with the kit's whole-frame rotation (PSRAM, column reads). The hardware backend must rotate in strips.
  - Touch reports come in landscape units, rotated 180° from the kit's landscape. The controller reports about every 16.8 ms, and answers "no new report" (0x30 bytes) in between, which isn't a lift: 20 ms is the shortest stable period; 30 ms is recorded. About 1 read in 130–200 is malformed. Two fingers are reported as two points with IDs.
  - Amp enable: TCA9554 P7, high = on (Waveshare's demo; not yet confirmed by sound). `esp_io_expander_tca9554`'s constructor resets the expander, which would release P6 on battery.
  - The rest, the same day: revision V2 (the touch interrupt reaches TCA9554 P0, inferred; the case can't be opened); the speaker is connected and P7 enables its amplifier (heard with P7 high, silent with P7 low); the image is upright with cw90 when the buttons edge is at the top; touch works through the case (Case B leaves the glass open; tested through a screen protector); the panel refreshes at 61 Hz (TE on P5 every 16.41 ms). Case B's active-area offset and button positions come from Waveshare's drawing (about 0.5 mm). That drawing gives an active area of 84.00 × 22.58 mm, a 0.1313 mm pitch on both axes (about 194 PPI), against device.json's 86 × 23 mm (190 PPI) from the diagonal; it's unresolved until measured.
- **RECON NEEDED (board, task 0.3, the rest):** battery capacity (the owner will confirm it); current draw per power state (needs a USB power meter, with the battery unplugged); optionally the active area's width with a ruler. All results go into device.json.
- **VERIFIED — plugin launcher (task 0.4, 2026-10-12):**
  - A launcher script works as a plugin's MCP `command` (`${CLAUDE_PLUGIN_ROOT}/scripts/lcd349-launch`).
  - `${CLAUDE_PLUGIN_DATA}` is set in the server's environment (`~/.claude/plugins/data/lcd349-lcd349-kit`), is writable, and binaries placed there run.
  - Install copies only `plugin/`, keeping the executable bit.
  - Image content returned by the tool reached the model, which read the screen correctly.
  - The PATH-only branch isn't needed. Evidence: `spikes/mcp/RESULTS.md`.
