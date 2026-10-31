# Measurements

The kit's speed and stability budgets (requirements §Non-functional), measured in the emulator and, [below](#on-the-board), on the board. A FAIL blocks a release.

**Host:** Apple M1 MacBook (8 GB), macOS 26.6.2, ESP-IDF v5.5.5, kit 0.1.0-dev. **Date:** 2026-10-17.

| Budget | Measured | Result |
|---|---|---|
| **KN1** screenshot ≤ 300 ms | 66 ms median, 67 ms max as a tool call; 75 ms median, 82 ms max for `lcd349 shot` end to end | PASS |
| **KN1** input to next frame ≤ 200 ms | a tap: 36 ms median, 42 ms max; a button press: 15 ms median, 128 ms max | PASS |
| **KN1** incremental emulator build ≤ 20 s | 0.96 s median, 1.9 s max | PASS |
| **KN2** instance start to first frame ≤ 3 s | 86 ms median, 530 ms max (the first, cold start) | PASS |
| **KN7** all examples' scenarios in CI ≤ 5 min | not measured: CI hasn't run yet. Locally the four example scenarios take 23 s | pending |
| **KN9** 8 hours, no crash, < 10 % memory growth | the 8-hour rerun (2026-10-23, after the first run's three faults were fixed, below): 8 h 0 m with no crash or hang, 50,015 rounds. The product's resident memory grew 4.5 % (6,368 KB median in the hour after the warm-up, 6,656 KB in the last hour); the supervisor's fell 18.4 % | PASS |

## How

**KN1 and KN2:** `go run ./tools/measure`, which prints JSON. It builds the CLI and the examples, then:

- **Start-up (5 runs):** prepares an instance of `examples/inputs`, starts its supervisor, and times until the instance's framebuffer shows a frame from the new process. The instance answers its control socket at the same moment, to within a millisecond.
- **Screenshots (20 runs each):** as an MCP tool call does it (connect, read the board's facts, take the screenshot), and as `lcd349 shot` does it, timed from starting the process to the PNG on disk.
- **Input to next frame (20 runs each):** on `examples/hello`, which draws only in answer to input, so the next frame is the input's. The time runs from sending the request to the framebuffer's frame counter moving: for a tap, the touch-down label; for a press, the button-down label, after the 10 ms debounce. The one slow press (128 ms) is an outlier the other 19 (13-20 ms) don't share.
- **Incremental build (20 runs):** touches `examples/inputs/main/main.c` and runs the emulator build, which recompiles that file and relinks. The first build of a new project, which compiles LVGL, takes about 100 s and isn't part of this budget.

**KN9:** `go run ./tools/soak -duration 8h`, on one instance of `examples/inputs`. Each round taps, waits for the log line, presses BOOT, swipes, takes a screenshot, reads the UI tree, lints, and changes the battery voltage. Every 25th round opens a new connection, every 50th changes the Wi-Fi networks in range, and every 60th sleeps and wakes the board. It samples the product's and the supervisor's resident memory every minute into `samples.csv`. Resident memory on macOS jitters by a megabyte or more between samples at this size, so growth compares medians: the hour after a 10-minute warm-up against the last hour. It stops early, and fails, on any crash or hang.

**What the first KN9 run found (2026-10-17).** It stopped at 33 minutes with the product's log silent while the product kept running. Parallel 20- and 30-minute runs, with a debug build that logs LVGL's input events and a heartbeat that writes past stdio, separated three faults:

- **The log went silent for good.** In the emulator, the FreeRTOS tick is a signal installed without `SA_RESTART`. Under load the console pipe fills, a task's `printf` waits in `write()`, and a tick landing then makes the write fail. That sets stdout's error flag, and macOS stdio drops everything printed to the stream afterwards. Fixed: the tick restarts interrupted calls (the board never interrupts a write), and the console capture clears an error flag if one is ever set, logging `output restored`. Under a load average above 100, the old build failed 4 of 4 runs within 3 minutes; the fixed build passed 4 of 4 ten-minute runs, and the safety net never fired.
- **A tap just after the UI changed was lost.** LVGL hit-tests against the layout of the last refresh, so a touch right after a change found the wrong object, then saw the finger "dragged" onto the right one, which never clicks. The soak's first tap, 5 ms after "ready", hit it. Fixed: the kit lays out before each touch read, on the board too; a host test reproduces the race.
- **The soak raced the product.** It pressed PWR as soon as the product logged "sleeping", which can be before the board sleeps, so the press was missed (as it would be on the board). The soak now waits for `light_sleep`, and a scenario `state` step's `expect` is checked until it holds, for up to 2 s.

**The 8-hour rerun (2026-10-23).** Phase 1 ran it detached (`go run ./tools/soak -detach -duration 8h -elf … -out …`) from 07:47 to 15:47 BST, on a copy of `examples/inputs`'s emulator build from after phase 1's frame-path, generated-file and power-source changes (task 1.4b), so later builds couldn't change it. That build predates the checkpoint guide `inputs` gained afterwards, which runs only when asked. It ran 50,015 rounds and 746,068 frames with no failure, and took 482 memory samples. The product started at 3,744 KB and peaked at 8,240 KB. Its summary is in `.kiro/specs/phase-1-kit-mvp/soak/summary.json`.

## On the board

**Board:** Waveshare ESP32-S3-Touch-LCD-3.49, revision V2, ESP-IDF v5.5.5, kit 0.1.0-dev. **Date:** 2026-10-23, phase 1's guided checkpoint.

| Budget | Measured | Result |
|---|---|---|
| **KN3** every static screen of `gallery` matches the viewer at physical scale | the owner, with the board beside `lcd349 view`: every page matches in layout, colour and text, with nothing clipped | PASS |
| **KN4** the emulator's modelled flush within ±20 % of the board's | the board: 13.29 ms mean over 100 full-frame flushes. The emulator models device.json's `flush_ms`, 13 ms: 2.2 % under the board | PASS |
| **P1-N1** full-frame flush ≤ 15 ms mean, ≤ 16 ms p95 | the same run: 13.29 ms mean, 13.30 ms p95 (13.27–13.34 ms) | PASS |
| **P1-N2** no false release in 10 s of a held finger, polling every 30 ms | held 10 s on the first try with no release: 301 samples with the finger down, one every 33 ms | PASS |

### The checkpoint

The owner followed the on-device guide in `examples/inputs`, driven by `go run ./tools/boardtest -guide -C examples/inputs` (log `guide.log`). The guide logs each step's result as an `@R {"guide":…}` line, keeps it in storage through resets and power offs, and prints every result again at the end. Every item on the board checkpoint's list (the parent spec's task 3.5) passed:

| Item | Guide step | What the log shows | Result |
|---|---|---|---|
| Landscape text the right way up, in the right colours | 2, the picture | the owner answered YES to "Is this text upright, each colour as named?" over four named swatches | PASS |
| Touch events in the log | 1, the corners; 3, the hold | a touch down and up in each corner, and the held finger | PASS |
| Button events in the log | 4, BOOT then PWR | BOOT held 132 ms, then PWR 75 ms, with no other presses | PASS |
| The tone is audible | 5, the tone | played once (`LCD349_OK`); the owner answered YES | PASS |
| A plausible battery reading | 7, the battery; 13 | 4,085 mV and 86 % on USB; 4,070 mV and 84 % on battery | PASS |
| Wi-Fi joins a 2.4 GHz network | 8, Wi-Fi | connected in 5.5 s on channel 10, at −67 dBm, with an address | PASS |
| On USB, a power off returns `ON_USB` | 9 | `LCD349_ERR_ON_USB` after 1,003 ms | PASS |
| On battery, a 5 s PWR hold powers off | 10, unplug; 13, the power off | on battery, PWR held 5,289 ms and let go, `lcd349_power_off` called, and the power went: the next boot's reason was power-on, and the guide's mark in RTC memory was gone | PASS |
| A line sent over USB reaches `lcd349_console_read_line` | 6, ping | `@P ping` answered with `@P pong` in 1 ms, at the first try | PASS |

The guide logged 12 of its 14 steps as PASS. What it found beyond the list:

- **Touch position (step 1, FAIL in the guide's own check).** The top-left touch read at (4, 57). The guide counts a corner's touch only within the outer third of the height, which ends at y 56, so that corner missed by a pixel; the other three landed in theirs. The owner counted the step for "touch events in the log", since a touch registered in every corner. Each corner touch read below its dot, whose centre is 32 px in from both edges: 25 px for top left, 5 for top right, 31 for bottom right and 23 for bottom left. The held finger in step 3 read 1 px below its dot. Whether that is where the finger landed or an offset in the panel's touch is open.
- **Light sleep (steps 11 and 12; task 7.3's open item, not on the list).** BOOT woke the board after 24.1 s (PASS). In step 12, the wake after 4.6 s read as BOOT, not PWR (FAIL). Going to sleep logs `E sleep: sleep_cpu_configure(236): Failed to enable CPU power down during light sleep.` The owner saw the screen flash off and straight back on instead of going dark. Both go to the kit's power phase (phase 3).
- **Restarts.** The board restarted three times when nothing asked it to: twice in step 4, after BOOT and before any PWR press was logged, and once asleep in step 12. Each boot gave the reason power-on, with the RTC still keeping time, and each came while the guide was waiting for PWR. The guide resumed each time. These go to phase 3 too.
- **The RTC.** The first boot after step 13's power off logged `W lcd349_rtc: the clock stopped (lost power)`, so the clock's time didn't survive a power off on battery.
- **The lit width (step 14).** The white screen was shown, but the owner didn't measure it, so X-03 stays open.
- **`examples/hello`** wasn't flashed: the guide in `inputs` covers every item on the list.

### How

**KN4 and P1-N1:** `go run ./tools/boardtest -only display`, during the checkpoint's preparation (log `prep-board-display.log`). The board test `test_flush_within_budget` makes 100 changes to a screen, each a new solid colour, and the hardware backend times each full-frame flush from entry until both strip buffers are free again. Its result line is `@R {"flush":{"n":100,"mean_us":13291,"p95_us":13296,"min_us":13274,"max_us":13336}}`. The display lane's four runs of the same test (task 3.3), in its own worktree, gave means of 12.92–12.93 ms.

**P1-N2:** the guide's step 3 counts the kit's touch samples with the finger down, and fails a try on any release in the 10 s.

