# The emulator

The emulator runs your product's own code on your computer. It builds the unchanged project for ESP-IDF's `linux` target, where FreeRTOS tasks run as threads, with the kit's emulator backend in place of the board's drivers. LVGL, FreeRTOS, `esp_event`, NVS and logging behave as on the board; the screen, touch, buttons, power, battery, storage and Wi-Fi are modelled from [`device.json`](../device/device.json).

```
 your project ── lcd349 build ──► build-emu/<name>.elf   (always headless)
                                     │  writes frames to fb.bin (shared memory)
                                     │  answers JSON-RPC on control.sock
                                     │  reads and writes its console on a pty
 lcd349 supervise (one per instance) ┘  restarts it, relays the console to a
                                        virtual serial port, records crashes and hangs
 lcd349-view ── maps fb.bin, sends touch and buttons over control.sock
 lcd349 CLI, lcd349 mcp ── talk to control.sock
```

## Instances

`lcd349 run` builds the project, starts an instance, and (unless `--headless`) opens the window. Options set the first boot:

| Option | Effect |
|---|---|
| `--clock 2026-10-12T09:00:00Z` | the clock starts at this time (RFC 3339 or Unix seconds), so screens are repeatable |
| `--wipe` | storage starts empty |
| `--battery-mv 3700`, `--usb` / `--battery` | the battery's voltage, and whether USB powers the board |
| `--storage <dir>` | keep storage in a directory of yours, across instances |
| `--detach` | return once the instance answers, and leave it running |
| `--id <id>` | the instance's name (default: from the project) |

First-boot settings apply only to the first boot: a reset keeps the clock, storage and battery the instance has reached since, as the board would.

Each instance has a directory under `$TMPDIR/lcd349-<uid>/<id>/` (owner-only), holding `control.sock`, `fb.bin`, `instance.json` (its state, for `lcd349 list`), `serial.log` (rotated to `serial.log.1` at 10 MB), `devstate.json` (the simulated world: battery, power source, board revision, clock, Wi-Fi networks), `storage/`, `viewer.log`, and for a detached instance `supervisor.log`. `lcd349 stop` removes it.

### What happens when the product…

- **resets** (`lcd349 power reset`): the supervisor starts it again with boot reason "software". Storage and the simulated world are kept. A product's own `esp_restart()` ends the emulator program with exit code 0, which the supervisor records as a crash.
- **resets with BOOT held** (`lcd349 power download`): it starts in ROM download mode, showing "DOWNLOAD MODE"; the product doesn't run until the next reset.
- **powers off** (on battery): the instance shows OFF and waits. Pressing PWR (`lcd349 press pwr`, the window's PWR key) or `lcd349 power on` turns it on.
- **crashes:** the supervisor keeps the exit status and the last 200 serial lines (`lcd349 list --json`), and every request answers with them until you `lcd349 power reset` (boot reason "crash") or stop it. Segfaults print a backtrace.
- **stops answering for 5 s:** the instance is marked hung and a stack sample is written to `hang-<pid>.txt` in its directory (`sample` on macOS; `gdb` on Linux, or only each thread's wait channel without it). It isn't killed: `lcd349 stop` does that.

## The window

`lcd349-view` maps the instance's framebuffer and shows it at physical size inside the case outline, with the BOOT and PWR keys and status badges. Click or drag to touch; hold `b` or `p` (or the drawn keys) for the buttons. It follows the instance across resets, plays tones, and dims with the backlight. Its physical size comes from the display's real dimensions (CoreGraphics on macOS) or its DPI; `lcd349 doctor` shows which, and `LCD349_VIEW_PPI` corrects it (or `--scale`, when you run `lcd349-view <instance dir>` yourself). The CLI uses `$LCD349_VIEW`, or the `lcd349-view` beside it: the release's `lcd349-view_<version>_<os>_<arch>`, downloaded into the same directory, is found under that name too. Without one, it builds the viewer from its own copy of the sources the first time it's needed, into `~/.cache/lcd349/`.

## The serial port

The product's console (stdin and stdout) is relayed to a pseudo-terminal, owner-only, whose path `lcd349 list` shows. A host tool opens it like the board's USB port: raw bytes, no echo. Output nobody reads is dropped oldest first, as on a USB port with no host, so the product never stalls. Everything is also in `serial.log`.

## The control protocol

Each instance answers JSON-RPC 2.0, one message per line, on `<instance dir>/control.sock`. The CLI, the MCP server and the viewer all use it, and so can your own tools:

```
printf '{"jsonrpc":"2.0","id":1,"method":"state.get"}\n' | nc -U "$TMPDIR/lcd349-$(id -u)/bar-demo/control.sock"
```

| Method | Does |
|---|---|
| `ping`, `info` | liveness; the board's facts, the fidelity list, the methods and exit codes |
| `state.get`, `state.set` | get: power, battery, USB or battery, board revision, clock, Wi-Fi, memory; set: battery, USB or battery, board revision, clock, Wi-Fi networks in range |
| `power` | `on`, `off`, `reset` (`boot_held: true` for download mode) |
| `screenshot` | PNG (`raw` for the panel's portrait image, `scale` 1-4), or raw `rgb565be` |
| `ui.tree`, `ui.lint` | the LVGL object tree; the design-rule check |
| `input.press` | `button` (`boot`, `pwr`) or `chord`, with `ms`, or `state` `down`/`up` for a hold |
| `input.touch` | `tap`, `long_press`, `swipe` or `points`, delivered as samples at the touch period |
| `wait` | for a log line (`log`, an extended regular expression), `text` on screen, or `idle_ms` without a frame |
| `audio.events` | the tones played since a sequence number |
| `record.start`, `record.stop` | PNG frames of everything drawn, for `max_s` seconds (default 10, at most 60) or 600 frames, whichever comes first |

Lines over 64 KB and malformed requests get JSON-RPC errors; the instance carries on. There is no network listener anywhere: only this owner-only Unix socket.

## Environment variables

For `lcd349`:

| Variable | Effect |
|---|---|
| `IDF_PATH` | the ESP-IDF to use (otherwise `lcd349` looks in the usual places) |
| `LCD349_INSTANCES` | where instance directories go (tests use it) |
| `LCD349_VIEW` | the `lcd349-view` binary to use |
| `LCD349_VIEW_PPI` | your display's pixels per inch, when the window's size is wrong |
| `LCD349_KIT_PATH` | the kit's `components/lcd349`, for the MCP server's `new_project` |
| `XDG_CACHE_HOME` | where the ESP-IDF environment and the viewer build are cached |

The product reads its configuration from `LCD349_*` variables, which the supervisor sets; you need them only to run an emulator program by hand (`LCD349_INSTANCE_DIR=<dir> build-emu/<name>.elf`). They include `LCD349_CLOCK`, `LCD349_WIPE`, `LCD349_BATTERY_MV`, `LCD349_SOURCE`, `LCD349_STORAGE_DIR`, `LCD349_BOOT_HELD` and `LCD349_BOOT_REASON`. `LCD349_FLUSH_MS` overrides the modelled frame time for experiments.

## Limits

- What the emulator doesn't model is listed in the [README](../README.md#what-the-emulator-doesnt-model) and checked off on the board with the [hardware checklist](../plugin/skills/lcd349/references/hardware-checklist.md).
- ESP-IDF components for the board's hardware don't exist in the emulator; [this table](emulator-components.md) gives the kit call to use instead.
- Timing follows the board's frame time and touch period, but the CPU runs at your computer's speed.
- When something goes wrong, see [troubleshooting](../plugin/skills/lcd349/references/troubleshooting.md).
