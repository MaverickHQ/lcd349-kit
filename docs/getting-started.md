# Getting started

This page takes you from nothing to a product running in the emulator, checked by a scenario, and built for the board.

## What you need

- **macOS** on Apple silicon (the main platform) or Intel, or **Linux** x86_64 (arm64 should work too).
- **ESP-IDF** at the version in [`VERSIONS`](../VERSIONS) (see below).
- **Go** 1.26 or later, only to build `lcd349` from source: the release has binaries for macOS on Apple silicon and Linux on x86_64.
- **CMake and Ninja.** On macOS: `brew install cmake ninja`. On Linux: `apt install cmake ninja-build`.
- **SDL2 and pkg-config** for the window: `brew install sdl2 pkg-config`, or `apt install libsdl2-dev pkg-config`. Without them everything works headless.
- **On Linux,** `apt install libbsd-dev` for ESP-IDF's `linux` target.

## ESP-IDF

```
mkdir -p ~/esp/v5.5.6 && cd ~/esp/v5.5.6
git clone -b v5.5.6 --depth 1 --recursive --shallow-submodules https://github.com/espressif/esp-idf.git
cd esp-idf && ./install.sh esp32s3
```

If the install fails with certificate errors (python.org's Python on macOS ships without CA certificates), run `export SSL_CERT_FILE=/etc/ssl/cert.pem` and try again.

`lcd349` finds ESP-IDF by itself (`$IDF_PATH`, then `~/esp/v5.5.6/esp-idf`, `~/esp/esp-idf` and a few others; failing those, another v5.5 release at `~/esp/v5.5.*/esp-idf`, newest first, which `lcd349 doctor` warns about) and sets up its environment, so you don't need `export.sh` for `lcd349` commands. You do for `idf.py`: `. ~/esp/v5.5.6/esp-idf/export.sh`.

## The `lcd349` command

From the release (macOS on Apple silicon shown; on Linux x86_64 use `linux_amd64`), checked against its `SHA256SUMS`:

```
V=0.2.2; A=darwin_arm64
curl -fLO https://github.com/MaverickHQ/lcd349-kit/releases/download/v$V/lcd349_${V}_$A
curl -fLO https://github.com/MaverickHQ/lcd349-kit/releases/download/v$V/SHA256SUMS
shasum -a 256 -c --ignore-missing SHA256SUMS
install -m 755 lcd349_${V}_$A ~/bin/lcd349     # put ~/bin on your PATH
lcd349 doctor
```

`gh attestation verify lcd349_${V}_$A --repo MaverickHQ/lcd349-kit` also shows the binary was built by the repository's release workflow. On an Intel Mac or Linux on arm64, which have no release binary, build it from a checkout instead: `go build -o ~/bin/lcd349 ./cmd/lcd349`.

`doctor` checks ESP-IDF and its version, Python's certificates, the compiler, CMake, Ninja, SDL2 and the viewer, and says how to fix anything missing. `lcd349 help` lists the commands; `lcd349 help <command>` shows a command's flags.

## A first product

```
lcd349 new ~/bar-demo
cd ~/bar-demo
lcd349 run
```

`new` writes an ESP-IDF project (here named `bar_demo`: project names use underscores) with one screen and a counter in `main/main.c`, a scenario in `tests/` with its golden screenshot, and a `CLAUDE.md` for agents. Instances are named from the directory: this one is `bar-demo`. The project pins the kit release of your `lcd349`'s version, by its git URL and tag, in `main/idf_component.yml`; `--kit-path <checkout>/components/lcd349` points it at a kit checkout instead.

`run` builds the emulator version into `build-emu/` (the first build takes a few minutes, mostly compiling LVGL; later builds take seconds), starts it, and opens the window. The terminal shows the product's serial output. Ctrl-C stops it.

## The window

- It shows the screen at its real size, inside an outline of the case. If the size is wrong on your display, `lcd349 doctor` says where the size came from; set `LCD349_VIEW_PPI` to your display's real pixels per inch (or pass `--scale` if you run `lcd349-view` yourself).
- **Touch:** click, or press and drag, on the screen.
- **Buttons:** hold `b` for BOOT and `p` for PWR, or the keys drawn under the case. A button is held for as long as you hold the key.
- **Badges** show the power state, USB or battery, Wi-Fi and backlight. The screen dims with the backlight and goes dark when the product sleeps.
- **Tones** play through your speakers.
- Closing the window doesn't stop the product: `lcd349 view` opens it again. `lcd349 run --headless` runs without one.

## Driving it from a terminal

While an instance runs, these work from another terminal. With one instance running you can leave out its name.

```
lcd349 list                               # instances, state, virtual serial port
lcd349 shot                               # a screenshot, bar-demo.png
lcd349 tree                               # the object tree: sizes, positions, text, fonts
lcd349 lint                               # the board's design rules; exits 1 on errors
lcd349 touch tap 520 86                   # also: long X Y, swipe X1 Y1 X2 Y2, points JSON
lcd349 press boot --ms 1100               # pwr, or boot+pwr for both
lcd349 wait --log "count 1"               # a log line, --text on screen, or --idle ms
lcd349 logs -f                            # the serial output
lcd349 state                              # battery, power, Wi-Fi, clock...
lcd349 state set battery.mv=3500 source=battery
lcd349 power reset                        # on, off, reset, download
lcd349 stop
```

Add `--json` for machine-readable output and `--timings` to see how long things take. `lcd349 run --detach` leaves the instance running in the background.

**The serial port.** `lcd349 list` shows each instance's virtual serial port (`/dev/ttys…` on macOS, `/dev/pts/…` on Linux). Open it with any serial tool, as you would the board's USB port; the product reads lines with `lcd349_console_read_line()` and answers with `printf`.

## Scenarios

A scenario is a YAML script of steps (touch, press, wait, screenshot against a golden, lint, state) that runs headless and fails at the first step that doesn't hold:

```
lcd349 scenario tests/app.scenario.yaml
```

A run that reaches a screenshot with no golden writes it and fails there: look at it, then run again, until every golden exists. See [scenarios](scenarios.md).

## Building for the board

The board build is plain ESP-IDF, in `build/`; the emulator's `build-emu/` is separate, so the two never disturb each other:

```
. ~/esp/v5.5.6/esp-idf/export.sh
idf.py set-target esp32s3 build flash monitor
```

Every SDK call works on the board (revision V2). Light sleep is experimental there: the [changelog](../CHANGELOG.md) lists its known problems.

## Adding the kit to an existing ESP-IDF project

1. Depend on the component in `main/idf_component.yml`:

   ```yaml
   dependencies:
     lcd349:
       git: https://github.com/MaverickHQ/lcd349-kit.git
       path: components/lcd349
       version: v0.2.2
   ```

   During development, use `override_path: /path/to/lcd349-kit/components/lcd349` instead.

2. Keep one component lock file per target. In the root `CMakeLists.txt`, before `project()`:

   ```cmake
   idf_build_set_property(DEPENDENCIES_LOCK dependencies.lock.${IDF_TARGET})
   ```

3. Copy `sdkconfig.defaults` and `sdkconfig.defaults.esp32s3` from `templates/app` (the tick rate, LVGL's colour depth and fonts, and on the board the PSRAM, flash, CPU speed, USB console and a large enough app partition).

4. Call `lcd349_init(NULL)` in `app_main`, build the UI with LVGL under `lcd349_ui_lock()`, and use the `lcd349_*` API for everything physical. Calls to `esp_lcd`, `esp_wifi`, `driver/*` and the like don't exist in the emulator: [this table](emulator-components.md) gives the kit call to use instead.

5. `lcd349 doctor -C <project>` checks the pin and the lock files.

## Next

- [The SDK](sdk.md): threads, the LVGL lock, callbacks, power and sleep.
- The [design guide](../plugin/skills/lcd349/references/design-guide.md) and [recipes](../plugin/skills/lcd349/references/recipes.md) for screens that work on the bar.
- [The plugin](plugin.md), to let Claude Code do the above for you.
