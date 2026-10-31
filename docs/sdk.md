# The SDK

`components/lcd349` is an ESP-IDF component. Products include `lcd349.h`, use LVGL 9 for their UI, and call the kit for everything physical. The full API, generated from the headers, is in [sdk-api.md](sdk-api.md); this page explains how the pieces fit.

## One source, two targets

The same product source builds for the board (`esp32s3`) and for the emulator (ESP-IDF's `linux` target). The kit has a shared core (argument checks, the LVGL port with rotation and byte order, the button debounce and gesture classifier, the battery curve) and one backend per target. Products never test the target: anything that differs between the board and the emulator belongs in the kit. The kit's argument checks run before either backend, so both reject exactly the same inputs.

## Starting up

```c
void app_main(void)
{
    lcd349_err_t err = lcd349_init(NULL);         /* NULL: the default configuration */
    ESP_LOGI(TAG, "lcd349_init: %s, status 0x%03x", lcd349_err_str(err), (unsigned)lcd349_board_status());
    if (!lcd349_display()) {
        return;                                   /* no display: nothing to show */
    }
    lcd349_ui_lock();
    /* build the UI on lv_screen_active() */
    lcd349_ui_unlock();
}
```

`lcd349_init` asserts the board's power hold first, then brings up the display, touch, buttons, battery, audio, clock and storage, and starts the kit's tasks. A subsystem that fails doesn't stop the others: its bit is set in `lcd349_board_status()` and its calls return `LCD349_ERR_UNAVAILABLE`, so a product can carry on without, say, audio. Wi-Fi starts only with `lcd349_wifi_start()`.

## Threads and the LVGL lock

The kit runs two tasks that call your code:

- **`lcd349_ui`** runs LVGL: rendering, input, timers and LVGL event callbacks.
- **`lcd349_evt`** delivers the kit's own callbacks: buttons, touch samples, Wi-Fi changes and power-source changes.

The rules:

- Touch LVGL only from LVGL callbacks and timers (which run on `lcd349_ui` with the lock held), or between `lcd349_ui_lock()` and `lcd349_ui_unlock()` (recursive). `lcd349_ui_call(fn, ctx)` runs a function on `lcd349_ui`.
- Kit callbacks run on `lcd349_evt`: keep them short, and hand work to your own task through a queue.
- Never block inside an LVGL or kit callback, and never wait on your own mutex while holding the LVGL lock if another task can take them in the other order.

The `linux` target switches tasks preemptively at the FreeRTOS tick, and nothing protects the C library's own locks there. So in the emulator the kit's tasks run at `app_main`'s priority, and a product task above priority 1 that shares `malloc` or `printf` with lower ones can, rarely, hang an instance. The supervisor reports a hang with a stack sample.

## Display

- `lcd349_display()` is the LVGL display, landscape. The kit renders into a buffer, rotates it for the portrait panel and swaps the colour bytes; both the board and the emulator receive identical panel bytes.
- The panel takes only full frames, so every change redraws the whole screen. Change things at once rather than animating, and set labels only when their text changes.
- `lcd349_backlight_set()` and `lcd349_display_sleep()`; while the display sleeps nothing renders, and waking redraws everything.
- `lcd349_ui_tag(obj, "list-row")` marks a dense full-width row, which the lint lets be shorter than the touch target. `lcd349_ui_tag(obj, "ignore:<rule>")` silences one rule for one object: use it only for deliberate exceptions.
- Size UI with `LCD349_LCD_W`, `LCD349_LCD_H`, `LCD349_MM_TO_PX(mm)` and the design rules' `LCD349_RULE_*` constants from `lcd349_board.h`.

## Touch and buttons

- `lcd349_touch_indev()` is LVGL's input device, so LVGL widgets just work. `lcd349_touch_subscribe()` delivers the raw samples, in landscape coordinates. Touch is one point, sampled only while the display is on, and never wakes the board.
- `lcd349_buttons_subscribe()` delivers debounced down and up edges of BOOT and PWR, stamped with the press time. `lcd349_button_is_down()` reads one.
- The classifier (`lcd349_classifier_*`) is pure code you feed with edges and tick regularly: it reports short presses, holds at each threshold you configure, hold ends, and chords (both buttons). `lcd349_classifier_hold_permille()` drives a progress bar. The [recipes](../plugin/skills/lcd349/references/recipes.md) show the usual loop.
- The press that wakes the board from sleep should only wake it: ignore gestures until both buttons are up. Holding BOOT through a reset enters download mode, so never design a boot-time combination with BOOT.

## Power, sleep and battery

- `lcd349_power_off()` releases the power hold. On battery the board turns off; on USB it can't, and the call returns `LCD349_ERR_ON_USB` after a short check, so tell the user.
- `lcd349_power_source()` says whether the board runs on USB or battery (or can't tell), checked once a second; `lcd349_power_on_source_change()` calls you on a change. On the board, USB means a USB host is connected, so a charger with no data reads as battery.
- `lcd349_sleep_until_button(timeout_ms, &woke_by)` puts the display to sleep and enters light sleep until a button press or the timeout, then wakes the display if it was on. On the board, light sleep is experimental (the [changelog](../CHANGELOG.md) lists its known problems), and needs the component's Kconfig option `LCD349_LIGHT_SLEEP` (on by default); with it off, the call returns `LCD349_ERR_UNAVAILABLE`.
- `lcd349_battery_read(&mv, &pct)` reads the cell voltage and a percentage from the discharge curve.

## Audio, clock, storage, Wi-Fi and the console

- **Tones:** `lcd349_tone(hz, ms, volume)`, or `lcd349_tone_seq()` for a short melody; a new tone replaces the one playing.
- **Clock:** `lcd349_rtc_get()` and `lcd349_rtc_set()`, in UTC Unix seconds. `lcd349_time_ms()` is the time since start.
- **Storage:** `lcd349_kv_*` stores strings and blobs by namespace and key (names up to 15 bytes, typed as NVS is). It survives resets and power-off; in the emulator `--wipe` clears it.
- **Wi-Fi:** give `lcd349_wifi_set_networks()` up to `LCD349_WIFI_MAX_NETWORKS` networks in priority order, then `lcd349_wifi_start()`. While disconnected the kit scans and joins the best visible one; `lcd349_wifi_subscribe()` reports changes. The radio is 2.4 GHz only. In the emulator, sockets use your computer's network, so wait for `LCD349_WIFI_CONNECTED` before connecting, as you must on the board.
- **The console:** write with `printf` or `ESP_LOG`; read lines a host tool sends over USB with `lcd349_console_read_line()`, from your own task.

## Memory

Allocate with `lcd349_alloc(size, LCD349_MEM_PSRAM)` (or `_INTERNAL`, or `_ANY`) and free with `lcd349_free()`. `lcd349_mem_stats()` reports what the kit has counted; the emulator compares it with the board's budgets and warns in `lcd349 state`.

## Errors

Every call returns an `lcd349_err_t` or a documented value, and never aborts on a hardware error. `lcd349_err_str()` names an error.

## Versions

The component follows semantic versioning. Before 1.0, a breaking change to a public header bumps the minor version and is listed under **Breaking** in [CHANGELOG.md](../CHANGELOG.md).
