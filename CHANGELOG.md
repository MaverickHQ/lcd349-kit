# Changelog

All notable changes to lcd349-kit are listed here. The SDK follows semantic versioning. Before 1.0, a breaking change to a public header bumps the minor version and is listed under **Breaking** (requirement 1.8).

## v0.2.1 (2026-11-01)

Fixes from a security review of v0.2.0. No public header changed but the version macros, so nothing breaks.

### Security
- **Instance directories are checked on every read, not only when created.** `lcd349 list` and `stop` and the MCP tools read any instance root, so a `/tmp/lcd349-<uid>` planted by another local user could hand `stop` a process to signal, or a tool a socket to talk to. Every read now skips a root or instance directory that isn't a real directory owned by you and closed to others, and reads `instance.json` only as a regular file, up to 4 MB.
- **`stop` removes only instance directories.** An MCP `stop` with `"instance": "./"` removed any directory that held an `instance.json`. A path now names an instance only when it's a direct child of a checked root, symlinks evaluated, and the MCP server resolves a relative one against its workspace.
- **The MCP `record` tool** writes the GIF of an instance with no project into an owner-only `$TMPDIR/lcd349-recordings-<uid>/`, checked as the instance roots are, not a shared folder; and a recording never overwrites, or writes through a symlink at, its name.
- **A request may nest at most 16 arrays and objects deep.** The emulator parses each request recursively, on a 32 KB task: it now refuses a deeper one before parsing it (`-32600`), and `lcd349` refuses to send one; a scenario's `state` `set` is checked as the file is read.
- **The release workflow:** the build, `SHA256SUMS` and the release checks run read-only, for a dry run too. Only a publish job, on a tag, can write; it runs no code from the repository, and publishes only when `dist/` holds exactly `SHA256SUMS` and the four binaries it lists.
- **`tools/publish/export`:**
  - it reads the blobs of a commit (HEAD, or `--commit`), never the working tree, and prints the commit; exporting HEAD, it refuses an allowlisted file that differs from it in the working tree or the index;
  - it compares `--out` with `--repo` as files, so a case variant on a case-insensitive disk is caught, and refuses an `--out` that holds the working copy or is a clone of it. Inside the working copy it takes only a separate git repository the working copy ignores, as `build/_public/lcd349-kit` is;
  - valvur exits 0 when it finds something, so the export reads its `run.json` and goes on only when the status is `clean`. A stale vulnerability database makes the status `inconclusive`, which the export refuses too: update valvur's data first;
  - gitleaks runs on the export's own configuration (its default rules), with an empty ignore file and `--ignore-gitleaks-allow`, whatever `GITLEAKS_CONFIG` says; a `.gitleaksignore` or a valvur configuration in the tree is refused;
  - the home-folder check ignores case and covers Linux and Windows home folders too; an image carrying metadata or text is refused: PNG text, EXIF and time chunks; JPEG EXIF, XMP, IPTC and comments, before or after the image data; GIF comments, text and XMP; and data after an image's end;
  - a path holding a denylisted value is named by its allowlist line, in every message;
  - it removes its staged copy of the tree when it's done, unless valvur's report there refused it.
- The emulator opens its temporary files with `O_EXCL|O_NOFOLLOW`, and `fb.bin` with `O_NOFOLLOW`; `lcd349` reads an instance's reply up to 32 MB, not without bound.

### Fixed
- **`ui.tree` with the path `/` crashed the instance** (a NULL `strcmp` under the UI lock). An empty path, or one of slashes only, is the active screen; a segment that isn't a number is refused.
- **Storage could write a namespace it couldn't read back.** A namespace's file loads up to 64 KB, but any size was saved. A set past 64 KB now returns `LCD349_ERR_NO_MEM` and changes nothing, as a full NVS does on the board.
- **On macOS, a failing host test hung or crashed the run** instead of failing it: Unity's `setjmp` and `longjmp` restored the signal mask and stopped the `linux` target's tick. The host test app builds Unity with `_setjmp` and `_longjmp`. `tools/timeout` limits a command from its parent process (macOS has no `timeout`): it kills the command's whole process group at the limit, passes on TERM, INT and HUP, and exits as GNU `timeout` does.
- **The plugin's launcher** could wait for good on a stalled download, and a TERM left a partial file. curl now has a 30 s connect timeout and a 10-minute limit, and a TERM or INT stops the download at once and removes the partial file.
- **`lcd349 mcp` answered nothing to a client that closed stdin straight after its requests** (`printf … | lcd349 mcp`). The end of stdin now waits until every request read has been answered, or 2 minutes, with or without a newline after the last request.
- **With `TMPDIR=/private/tmp` on a Mac, `lcd349 list` showed every instance twice**, and a command with no instance named found two running: the two instance roots, `$TMPDIR/lcd349-<uid>` and `/tmp/lcd349-<uid>`, were one directory. Each is now read once.
- `lcd349 new --kit-path` and `new_project` write the kit path into `idf_component.yml` as a quoted string, so a path with quotes, backslashes or a newline in it stays itself.

### Changed
- **`lcd349 stop <path>`, `lcd349 logs <path>` and the other commands and MCP tools that take an instance by its path accept only an instance directory inside the current instance roots** (`$TMPDIR/lcd349-<uid>/`, `/tmp/lcd349-<uid>/`, or `LCD349_INSTANCES`). An instance started with another `LCD349_INSTANCES` is named with that variable set, as when it was started.
- `tools/publish/export` takes `--commit`, and needs valvur's status to be `clean` (see Security).
- `golang.org/x/sys` v0.49.0 and `go.yaml.in/yaml/v3` v3.0.6. `x/sys` needs Go 1.26, so building `lcd349` from source does too.
- CI and the release run on `ubuntu-24.04`, not `ubuntu-latest`, and every job has a time limit. Dependabot can't watch the ESP-IDF container image, so it's updated by hand.
- The README lists the board's hardware the kit doesn't cover yet.
- The version strings say `0.2.1`. `plugin/scripts/checksums.txt` has no `lcd349` lines until the release's dry run gives them.

## v0.2.0 (2026-11-01)

The first published version: [the release](https://github.com/MaverickHQ/lcd349-kit/releases/tag/v0.2.0). The repository is public at `github.com/MaverickHQ/lcd349-kit`, CI builds the release binaries, and the Claude Code plugin installs from the repository's marketplace. The SDK is v0.1.0's: no public header changed but the version macros, so nothing breaks. The minor version moves because the published tree isn't the same as the local v0.1.0 tag.

### Added
- **The release, built by CI on a tag** (`.github/workflows/release.yml`): `lcd349` and `lcd349-view` for macOS arm64 and Linux x86_64, `SHA256SUMS`, a build attestation for each binary (check one with `gh attestation verify`), and the GitHub release. The plugin's launcher downloads `lcd349` and runs it only if its SHA-256 matches `plugin/scripts/checksums.txt`, which holds the release's `lcd349` lines.
- **CI on every push and pull request** (`.github/workflows/ci.yml`):
  - on Linux, in the pinned ESP-IDF v5.5.5 container: the host tests, `go vet`, the Go tests with ESP-IDF, the viewer, the examples and the template required, `devicegen --check`, every example's scenarios headless (KN7: 25 s, against 5 minutes), and the build checks for both targets;
  - on macOS arm64: the viewer and the Go tests (KN5);
  - the plugin's manifests (`claude plugin validate`).

  Every action is pinned by its commit SHA and every tool to an exact version. Dependabot watches the actions and the Go modules.
- `lcd349 new <dir>`, with no `--kit-path`, pins this release by its git URL and tag.
- `lcd349` finds the viewer under the release's name, `lcd349-view_<version>_<os>_<arch>`, beside it, as well as `lcd349-view`.
- `tools/publish/export`, which makes each public commit's tree:
  - it copies only the paths an allowlist names;
  - it refuses a tree that holds a value from a private denylist, a home-folder path or a gitleaks finding, naming the file and the line but never the value;
  - it runs a valvur scan and prints the diff against the public clone. It never commits or pushes.
- The README, rewritten for the published kit, with art drawn from the scenario goldens (`tools/media/art.py`). A repository test fails when a golden changes and the art isn't redrawn.
- `SECURITY.md`, `CONTRIBUTING.md` (issues are welcome; pull requests aren't taken yet) and issue forms.
- A guard: the component compiles with `-Werror=frame-larger-than=20480`, so no function of the kit's can put more than 20 KB on a task's stack.

### Fixed
- **The emulator overflowed its main task's stack on every boot after the first.** Loading storage put a 64 KB buffer on the stack (`kv_file.c`), and loading the device model 8 KB (`devmodel.c`). On a Mac the overflow silently overwrote heap memory; on GitHub's Linux runners the instance crashed. Both buffers are on the heap now.
- **`lcd349 scenario` could lose a request while the product powered off.** A `wait` that reached the exiting product came back empty, and a PWR press the product never read counted as done, so the board stayed off. A `wait` is now asked again once the runner follows the product, and a request it never read is sent again.
- **A `wait` for `idle_ms` could count the screen as settled while a change was still waiting to be drawn**, so a screenshot just after a label changed could show the old text. It now also waits until nothing is waiting to be drawn.
- `lcd349 doctor` finds libbsd's headers in Debian and Ubuntu's multiarch folder (`/usr/include/<triplet>/bsd/`), where Ubuntu 24.04 puts them.
- `examples/inputs`' scenarios, and the CLI's and MCP server's tests, wait for the Wi-Fi status text on the screen, not only in the log: the status line redraws once a second, so a screenshot just after the log line could still show "wifi scanning". The `inputs` golden is re-recorded.
- `test_apps/build_check` uses the large app partition, as the examples and the template do: with Wi-Fi, the kit doesn't fit the default 1 MB.
- `tools/soak` builds on Linux.
- The SSH key in a CLI test fixture is one generated for the test.

### Changed
- Go 1.26.10 (the `toolchain` line in `go.mod`, which CI and the release use), and `golang.org/x/sys` v0.44.0 (CVE-2026-39824).
- The version strings say `0.2.0`: `LCD349_VERSION_STR` and `LCD349_VERSION_MAJOR`/`MINOR`/`PATCH`, `lcd349 version`, the plugin's manifest and launcher, `idf_component.yml` and `lcd349-view --version`.
- The docs describe the published version: installing and verifying the release, the plugin's install forms, and building `lcd349` from source where there's no release binary.

### Known problems
- v0.1.0's known problems (below) all still stand. Light sleep is experimental, the board restarted unasked during the checkpoint, the clock stops at a power off on battery, touch may read below the finger, and the screen's size and the current draw aren't measured. The kit's power phase (phase 3) owns most of them.
- Release binaries exist only for macOS arm64 and Linux x86_64. On an Intel Mac or Linux on arm64, build `lcd349` from a checkout of this tag and put it on PATH.
- The Linux `lcd349-view` links the system's SDL2, so it needs SDL2 installed, and it's built on Ubuntu 24.04, so it needs glibc 2.39 or later. The macOS one is built on GitHub's macOS 15 image with no lower minimum set, so it may not run on older macOS versions.

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
