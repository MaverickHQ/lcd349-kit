# The kit drives the power latch by register

On battery, the board stays on only while the TCA9554 expander's P6 is asserted. Espressif's `esp_io_expander_tca9554` component resets the expander in its constructor, which releases P6 and powers the board off on any warm restart. So the kit writes the expander's registers itself: the output level first, then the direction, as the first thing `lcd349_init` does, and nothing in the kit ever resets the expander. This amends the parent spec's requirement 2.1, which named the component.

**Status:** accepted, 2026-10-21 (phase 1's grilling, item 6; design D1-1).

## Consequences

- The kit owns a small register helper (`src/hw/expander.c`) that the power and audio code share: P6 (power), P1 (backlight enable) and P7 (the amplifier).
- `THIRD_PARTY.md` drops `esp_io_expander_tca9554`.
