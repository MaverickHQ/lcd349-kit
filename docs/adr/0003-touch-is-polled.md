# Touch is polled

Touch is polled every 30 ms, and only while the display is awake and lit; the kit uses no touch interrupt. The touch controller's interrupt reaches the TCA9554 expander's P0, not a GPIO, and the expander's own interrupt line (GPIO42) fires on every change of any of its inputs. The panel's TE signal is on P5 and changes about 122 times a second, so GPIO42 is always busy and can't serve as a cheap touch signal. The controller makes a report about every 16.8 ms, and 20 ms was the shortest period with no false lifts; 30 ms leaves room for scheduling jitter. This amends the parent spec's requirement 2.7.

**Status:** accepted, 2026-10-21 (phase 1's grilling, item 7; design D1-4).

## Consequences

- Touch never wakes the board, which the parent spec's requirement 1.5 wants anyway (pocket safety).
- `device.json` records the touch interrupt as unusable, and the panel's refresh rate (61 Hz) as a fact.
