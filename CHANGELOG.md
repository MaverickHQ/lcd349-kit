# Changelog

All notable changes to lcd349-kit are listed here. The SDK follows semantic versioning. Before 1.0, a breaking change to a public header bumps the minor version and is listed under **Breaking** (requirement 1.8).

## v0.1.0 (2026-10-23)

The first version that runs on the board. It's a local tag for herdr-pager to pin, not a release: nothing is published.

**On the board** (the Waveshare ESP32-S3-Touch-LCD-3.49, revision V2, with ESP-IDF v5.5.5), a product built from the same source as its emulator build now runs. Every SDK call works there: power and the power source, the display and backlight, touch, buttons, the battery, tones, the clock, storage, the USB console and Wi-Fi. Light sleep also works, but is **experimental** (below). `test_apps/board` tests each subsystem on the board: 23 tests, run one subsystem at a time, all passing. The guided checkpoint passed every item of the board checklist: the picture the right way up in the right colours, touch and button events, the tone, the battery, Wi-Fi, power off on USB and on battery, and a line from the host.

**Measured** (details in `docs/measurements.md`; KN6 from the eval runs of 2026-10-17):
- **The full-frame flush on the board:** 13.29 ms mean and 13.30 ms p95 over 100 flushes, against at most 15 ms and 16 ms (P1-N1): PASS.
- **KN4:** the emulator models device.json's 13 ms flush, 2.2 % under the board (the limit is ±20 %): PASS.
- **KN6:** all 5 agent-evaluation sessions passed (4 needed), each costing $1.39–1.96: PASS.
- **KN9:** the emulator ran 8 hours (50,015 rounds) with no crash or hang, and the product's resident memory grew 4.5 % (the limit is 10 %): PASS.
- **P1-N2:** a finger held still for 10 s, polled every 30 ms, was never released (301 samples with the finger down): PASS.
- **KN3:** every static `gallery` screen on the board matches the viewer at physical scale (by the owner's eye): PASS.

### Known problems
- **Light sleep is experimental.** From a fresh boot, `lcd349_sleep_until_button` light-sleeps and wakes on its timer with Wi-Fi still associated, and on BOOT. These problems remain, and the kit's power phase (phase 3) owns them:
  - **It may not sleep after other work.** In a full board run, after the storage, power and clock tests, the chip stayed awake through the whole wait. The call still returns after its timeout with the right result, so a product behaves correctly but saves no power.
  - **The screen comes straight back on:** it flashes off and on instead of staying dark.
  - **A PWR wake was misread:** on battery, a PWR press during sleep was read as a BOOT wake.
  - **Each sleep logs an error:** `sleep_cpu_configure(236): Failed to enable CPU power down during light sleep`.
  - **The tick runs fast:** FreeRTOS ticks run about 7 % ahead of `esp_timer` across a sleep.
  - **The console drops:** on USB, a product loses its console at its first sleep.
- **Unexplained restarts:** during the checkpoint, the board restarted three times that nothing asked for. Each came while the board waited for a PWR press, once while it was asleep, and each boot gave the reason power-on. Phase 3 looks into them with the power model.
- **The clock doesn't survive a power off on battery.** The next boot logs `the clock stopped (lost power)`, and the time has to be set again.
- **Touch may read a little below the finger.** In the checkpoint, each corner's touch read 5–31 px below its dot; the held finger read 1 px below. It's still open whether that is where the finger landed or an offset in the panel.
- **The screen's physical size isn't measured yet.** device.json keeps 86 × 23 mm; Waveshare's drawing gives 84.00 × 22.58 mm. The viewer's physical scale and the design rules' millimetre thresholds use 86 mm until someone measures the lit width with a ruler.
- **The current draw isn't measured yet** (it needs a USB meter, in phase 3).
- **The version strings still say `0.1.0-dev`** at this tag: `LCD349_VERSION_STR`, `lcd349 version`, the plugin's manifest and launcher, and `lcd349-view --version`. Only `idf_component.yml` says `0.1.0`.

### Fixed
- A clone couldn't build `cmd/lcd349`: the `build/` ignore rule also matched the Go package `internal/build`, which is now committed. The examples' `dependencies.lock*` files are no longer committed: they recorded the kit's absolute path, so a clone built the original checkout's kit.
- The control socket answers a client that has finished sending (`printf ... | nc -U control.sock`) before closing; before, the reply was dropped. On Linux, a client that disconnects while its reply is being written no longer ends the instance with SIGPIPE.
- `lcd349 scenario` with a relative path from outside the project wrote goldens under a doubled path.
- An instance's log could go silent for good while the product carried on (found by the soak test, and by an agent in the plugin evaluation). In the emulator, the FreeRTOS tick (a signal) interrupted a console write that was waiting on a full pipe; the failed write set stdout's error flag, and macOS then drops everything printed to the stream. The tick now restarts interrupted calls, as the board never interrupts a write, and the console capture clears a stream's error flag if one is ever set, saying so in the log.
- The serial port relay: the supervisor reads every pty with a blocking thread instead of the runtime's poller, and output for a serial port nobody reads is discarded oldest first, as documented (it was newest first). The product puts each line where `wait` sees it before writing it to the console, so a slow console can't hide lines from `wait`.
- A tap that arrived just after the UI changed (a new screen, a moved object) could be lost: LVGL hit-tested objects where they were before the change, then saw the finger "dragged" onto the right one, which never clicks. The kit now brings the layout up to date before each touch read, on the board and in the emulator. Found by the soak test (the first tap 5 ms after "ready").
- A scenario `state` step's `expect` is checked until it holds, for up to 2 s, so a state the product reaches a moment after a log line (such as `light_sleep` after "sleeping") no longer fails the step on a busy computer.
- `go run ./internal/devicegen` needed a second run after a device.json change: the API reference read `lcd349_board.h` from disk before the same run rewrote it.
- `ui.lint`: an object counts as interactive only through an input event handler; the no-momentum rule skips objects a drag can't reach (such as a truncated label); objects under a modal backdrop aren't compared with the modal's for overlap; the top layer is laid out before linting and before `ui.tree`.

### Changed
- The shared LVGL port hands the backend the landscape frame (`be_panel_flush`), and each backend rotates it with the same code: the board in strips while the previous strip is sent, the emulator all at once. A host test proves the two give the same bytes, so the emulator's framebuffer is unchanged. Products see no difference.
- The examples' and the template's `sdkconfig.defaults.esp32s3` set what the kit needs on the board: the large single-app partition (the kit with Wi-Fi doesn't fit the default), the USB Serial/JTAG console, 240 MHz, QIO flash at 80 MHz, PSRAM at 80 MHz and performance optimisation. A product made earlier with `lcd349 new` needs the same lines.
- Dependencies: the kit drives the TCA9554 expander by register instead of through `esp_io_expander_tca9554`, which resets the expander and so releases the power latch on battery (kit ADR 0001). It reads touch reports itself instead of through `esp_lcd_axs15231b`'s touch driver (kit ADR 0002). `esp_codec_dev` is a dependency on the board only. See `THIRD_PARTY.md`.
- device.json's touch facts: the controller reports up to two points and the kit uses the first; the touch interrupt (expander P0) is recorded as unusable, because the expander's interrupt line fires on every change of any input, the panel's TE included. Touch is polled on both board revisions (kit ADR 0003).
- `docs/measurements.md`: KN9 passes (the 8-hour soak), and the board's checkpoint results are recorded (KN3, KN4, P1-N1, P1-N2).
- device.json holds the hardware measurements from task 0.3:
  - The full-frame flush time is 13 ms (was assumed 25), so the emulator's modelled flush is faster.
  - The touch sampling period (30 ms), two-point touch reports, the rotation (cw90), the board revision (V2), the amplifier enable pin, the speaker and touch through the case are verified.
  - Case B's active-area offset and the BOOT and PWR positions come from Waveshare's drawing, so the viewer draws both buttons on the case's top edge.

### Added
- The hardware backend (`esp32s3`), with a test on the board for each subsystem:
  - power: the latch (expander P6) and the backlight enable (P1) are held by register as the first thing `lcd349_init` does; power off releases the latch, and returns `LCD349_ERR_ON_USB` on USB;
  - the display: the AXS15231B panel over QSPI, each full frame rotated in strips into two DMA buffers while the previous strip is on the bus; the backlight (LEDC, active low);
  - touch: the kit's own report reader, polled while the screen is lit. A frame with no new report keeps the touch, a malformed frame changes nothing, a lift or three silent reads release, and with two fingers the first is kept;
  - buttons (BOOT and PWR, both wake sources for light sleep), the battery (ADC with calibration), tones (the ES8311 through `esp_codec_dev`, with the amplifier on only while a tone plays), the RTC (PCF85063), storage (NVS), the serial console (USB Serial/JTAG), Wi-Fi (station, up to four saved networks, scans while disconnected, events, modem power save), and light sleep woken by a button or a timer.
- `lcd349_power_source()` and `lcd349_power_on_source_change()`: whether the board runs on USB or battery (or can't tell), checked once a second, with a callback on a change. On the board, USB means a USB host is connected, so a charger with no data reads as battery. New API, so no break.
- Kconfig `LCD349_LIGHT_SLEEP` (default on; never on the `linux` target) turns on power management and tickless idle, which `lcd349_sleep_until_button` needs. With it off, the call returns `LCD349_ERR_UNAVAILABLE`.
- `lcd349_board.h`: `LCD349_BUTTON_PWR_X_PX` and `LCD349_BUTTON_BOOT_X_PX`, each button's centre as a landscape x, generated from device.json, so a product can put a hint under a button.
- device.json: the panel's refresh rate, the battery's capacity, and the memory budgets measured on the board: the internal heap free after `lcd349_init` (Wi-Fi included), and the LVGL heap the board build configures. The emulator's memory report (in the full state) now warns past each.
- `test_apps/board`: a Unity app that tests the SDK on the board, and `tools/boardtest`, which builds, flashes and runs it, one subsystem or all. It answers the board's requests, sending Wi-Fi credentials from a file it reads only if its mode is 0600, and never prints them. It follows the board across restarts and USB drops, and takes a lock, so runs on one board queue. `-C` runs another project's board checks, built with the `lcd349_harness` component; `-guide` runs a product's on-device guide.
- `examples/inputs`: an on-device guide for checking a board by hand (the corners, a held finger, the buttons, the tone, a line from the host, the battery, Wi-Fi, power off, light sleep and a white screen to measure). The host starts it with `@P guide`, and it resumes after a reset or a power off.
- `tools/soak -detach`: runs the soak in its own process session, keeping the Mac awake while it runs.
- Repository scaffold, `device/device.json` with its schema and generator, the plugin skeleton and launcher.
- The SDK's public API and shared core, with host tests.
- The emulator backend (`linux` target): framebuffer file, timing, input, sleep and power, battery, Wi-Fi, storage, audio events, download mode, and the JSON-RPC control socket with the UI tree and lint. `examples/inputs`.
- `lcd349-view`: the emulated device at physical size in Case B, with mouse touch, held buttons, status badges and tones.
- device.json `case` (Case B outline; the screen's position in it is assumed until measured).
- Control method `input.press` takes `state` `down`/`up` for a hold; a client's held buttons are released when it disconnects.
- `fb.bin` header: the panel's rotation and byte order, in formerly reserved bytes.
- `lcd349_console.h`: `lcd349_console_read_line()` reads lines a host tool sends over the serial console (the board's USB port, or the instance's virtual serial port). New API, so no break.
- The `lcd349` CLI: `doctor`, `new`, `build`, `run` (with `--detach`, `--headless` and first-boot options), `list`, `stop`, `logs`, `view`, `power`, `shot`, `tree`, `lint`, `press`, `touch`, `wait`, `state` and `scenario`.
- Instances run under a supervisor: a virtual serial port (owner-only pty), `serial.log` with rotation, restarts on reset and download mode, power on from off, crash records with the last 200 serial lines, and hang reports with a stack sample.
- Scenario files with golden screenshots (RGB565-exact, with actual and diff images on a mismatch), and `templates/app` for `lcd349 new`.
- `docs/emulator-components.md`: the ESP-IDF components the emulator lacks, and their kit replacements.
- `lcd349 mcp`: 17 MCP tools over stdio (`device_info`, `new_project`, `build`, `run`, `stop`, `screenshot` with image content, `ui_tree`, `ui_lint`, `press`, `touch`, `logs`, `wait`, `get_state`, `set_state`, `power`, `run_scenario`, `record` as a GIF), with paths confined to lcd349 projects and the workspace, a 1 MB output cap, and the server's instances stopped when the session ends.
- The `lcd349` skill: SKILL.md (workflow, button grammar, never-do list, tools) with the board facts and design rules generated from `device.json`; references for the device, fidelity, design guide, recipes (quoted from `examples/gallery`), the C API (from the headers), scenarios, the hardware checklist and troubleshooting.
- `examples/gallery`: status bar, list, detail card, menu tiles, confirm dialog, toast, hold to confirm and setup screen, with a scenario and goldens.
- `examples/hello`, and scenarios with goldens for `examples/inputs` (touch and buttons; battery, Wi-Fi, sleep and power-off on battery).
- Documentation: getting started, the SDK, the emulator, scenarios, the plugin, security, framework adapters and measurements, with a link check; the README's fidelity list and `docs/sdk-api.md` are generated.
- `tools/measure` (KN1, KN2), `tools/soak` (KN9) and `tools/eval` (KN6; `evals/run.sh` runs it).
- Scenarios follow a product that powers off, resets or crashes between or during steps; while a device is off, `wait {log}` searches its last boot's lines.
- `lcd349_board.h`: `LCD349_RULE_TOUCH_TARGET_PX`, `LCD349_RULE_FONT_MIN_PX` and `LCD349_RULE_CONTRAST_X100`, the design rules' thresholds.
- The plugin at the kit's version, with launcher tests.
- Emulator: `LCD349_BATTERY_MV` and `LCD349_SOURCE` first-boot settings; a crash's last output now always reaches the console (ESP-IDF's crash handlers exit without draining it).
