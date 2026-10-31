# Troubleshooting

## Building

- **"Failed to resolve component 'esp_lcd'"**, **"'driver/gpio.h' file not found"**, or **undefined symbols** such as `i2c_new_master_bus` or `esp_light_sleep_start`: the product uses an ESP-IDF component the emulator lacks. The error's hint names the kit call to use instead; the full table is `docs/emulator-components.md` in the kit. The fix is never a target `#if`: use the `lcd349_*` API, which works on both.
- **The first build takes a few minutes:** it downloads and compiles LVGL. Later builds are incremental (seconds).
- **Component downloads fail with certificate errors:** Python can't verify TLS. `lcd349 doctor` says so; set `SSL_CERT_FILE=/etc/ssl/cert.pem` (macOS) for `idf.py` in your own shell. The tools set it for their own builds.
- **`dependencies.lock` keeps changing target:** the project shares one lock file between the board and emulator builds. Add `idf_build_set_property(DEPENDENCIES_LOCK dependencies.lock.${IDF_TARGET})` before `project()` in the root `CMakeLists.txt` (the template has it).
- **Something else:** `lcd349 doctor` checks ESP-IDF, the compiler, CMake, Ninja, SDL and the project's kit pin, and says how to fix each.

## Running

- **"instance_exited" / the instance crashed:** the answer carries the last serial lines. A segfault prints a backtrace; an `abort()` or failed `assert` prints "ERROR: Aborted" after the product's last line. Fix the code, `build` and `run` again; or `power` reset (with `power: reset` the boot reason is "crash", as on the board after a panic).
- **The instance is "hung":** it stopped answering for 5 s. The state names a stack sample of every thread. Usual causes: blocking inside an LVGL or kit callback; waiting on your own mutex while holding `lcd349_ui_lock()` (or the reverse); a loop without `vTaskDelay`; `malloc` or `printf` from a task above priority 1 that shares them with lower ones (an emulator limit; see `fidelity.md`).
- **The screen is black:** the display is asleep or off, or the backlight is 0 (`get_state` shows `dark`). Or the UI was built on a screen that isn't loaded, or outside `lcd349_ui_lock()`.
- **"the device is off":** the product powered off (on battery). `press` PWR or `power` on.
- **A touch is refused:** the display is dark, and touch never wakes the board. Press a button to wake it first, as a person would.
- **A tap does nothing:** check `ui_tree` at that point. The object may not be clickable; an overlay on the top layer (a dialog backdrop, a toast panel) may be taking the tap; or "scroll on focus" moved the list under the finger. Plain LVGL containers are clickable by default: make holders non-clickable.
- **Instances pile up or are "gone":** `lcd349 list` shows them; `lcd349 stop --all` cleans up. Instances the MCP server starts stop when the session ends.

## Checking

- **`wait` timed out:** the answer has the last serial lines and the screen's path. A `log` pattern is an extended regular expression (escape `.` `(` `[`); `text` is plain text, not a pattern: it matches part of one visible label, case included. If `idle_ms` never settles, something redraws all the time: a timer, a label set every tick, an animation.
- **A golden mismatch:** look at `_diff.png`. Common causes besides a real change: the clock moved on (fix it in `run`), Wi-Fi or battery state changed, a toast or timer was mid-way, a press held a little longer. Update goldens only for intended changes.
- **Lint findings:** each finding has the object's path and a fix. Dense full-width list rows may be shorter than the touch target when tagged `list-row` (`lcd349_ui_tag(row, "list-row")`). An `ignore:<rule>` tag silences a finding for one object: use it only for a deliberate exception, with a comment saying why.
- **Contrast fails on text that looks fine:** contrast is measured after colours are reduced to RGB565 and composited over every ancestor's background. A semi-transparent panel changes the colour behind the text.
- **`clipped-text` on a label:** give it a fixed width and `LV_LABEL_LONG_MODE_DOTS` (with a fixed height for several lines), or wrap it where the height fits every line, or shorter text.
- **`no-momentum`:** call `lv_obj_set_scroll_momentum(obj, false)` on containers that scroll.
- **`overlap`:** two click areas share pixels. Move them apart, or put the upper one on a modal with a full-screen clickable backdrop.

## On the board

- **Flashing doesn't start:** hold BOOT while connecting the USB cable (download mode), release it, then flash. Reconnect to run the product.
- **It looks different from the emulator:** compare with `fidelity.md` and the assumed values in `hardware-checklist.md`; tell the kit's maintainers if a board fact differs, so `device.json` can be corrected.
