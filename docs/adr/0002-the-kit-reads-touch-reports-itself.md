# The kit reads touch reports itself

The kit reads and decodes the AXS15231B's touch reports itself (the 11-byte read command, then a 14-byte report), instead of using Espressif's `esp_lcd_touch_axs15231b`. On this board the component gets three things wrong, all seen in task 0.3's spike: it treats the controller's "no new report" answer as a lift, it reads a malformed (byte-shifted) frame as a release, and it drops reports with two points, so a held finger shows LVGL a release and a click about every 4 seconds. The decoder is a pure shared module (`axs_touch.c`), tested on the host against frames captured from the board. This amends the parent spec's requirement 2.1.

**Status:** accepted, 2026-10-21 (phase 1's grilling, item 7; design D1-2).

## Consequences

- A touch is released by a lift event, or by three polls in a row with no report (90 ms at 30 ms polling). The controller reports about every 16.8 ms while a finger is down, so 90 ms of silence is over five missed reports.
- A no-report frame or a malformed frame never changes the touch state.
- With two fingers down, the first point is used and the touch stays pressed.
- `esp_lcd_touch` stays in the dependency tree (the panel component pulls it in) but is unused; `THIRD_PARTY.md` says so.
