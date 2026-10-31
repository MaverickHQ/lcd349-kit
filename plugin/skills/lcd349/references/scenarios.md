# Scenarios and goldens

A scenario is a YAML file that starts a fresh headless instance, drives it, and checks it: screenshots against goldens, the design rules, log lines, text on screen and device state. It stops at the first failing step. Run one with the `run_scenario` tool, or `lcd349 scenario tests/<name>.scenario.yaml` in a terminal or CI.

## The file

```yaml
# tests/inbox.scenario.yaml
name: inbox
run: { clock: "2026-10-12T09:00:00Z", battery_mv: 3900, usb: true }
steps:
  - wait: { log: "ready" }                # the product logs when its UI is up
  - wait: { idle_ms: 300 }                # the screen has settled
  - shot: goldens/inbox.png               # compare with the golden
  - lint: { max_errors: 0 }
  - touch: { tap: [320, 60] }
  - wait: { log: "opened message 0" }
  - press: { button: boot, ms: 1200 }     # a hold: over the 1 s threshold, with a margin
  - wait: { text: "Approved" }
  - state: { set: { battery: { mv: 3500 } }, expect: { source: usb } }
  - power: reset
  - wait: { log: "ready" }                # after a reset, matches from the new boot's first line
```

`run` options apply to the first boot: `clock` (RFC 3339 or Unix seconds), `wipe` (default `true`: empty storage), `battery_mv`, `usb` (`true`: on USB power; `false`: on battery).

| Step | Parameters | Passes when |
|---|---|---|
| `wait` | `log` (extended regular expression), `text`, `idle_ms`, `timeout_ms` (default 5000) | the line is logged, the text is on screen, or no frame came for `idle_ms` and none is waiting to be drawn |
| `shot` | `golden` (relative to the scenario file), `tolerance_px` (default 0), `raw`, `scale`; or just the path | the screenshot matches the golden |
| `lint` | `max_errors` (default 0), `max_warnings` (default: any); or empty | the screen is within the limits |
| `touch` | one of `tap: [x, y]`, `long_press: [x, y]`, `swipe: {from: [x, y], to: [x, y]}`, `points: [{x, y, down}]`; `ms` | the touch was delivered (it isn't while the display is dark) |
| `press` | `button` (`boot`, `pwr`, `boot+pwr`), `ms` (default 100); or just the button | always, after the release |
| `state` | `set` (partial state, as `set_state` takes it) and/or `expect` (values that must equal `get_state`'s) | every expected value matches within 2 s |
| `power` | `on`, `off`, `reset` or `download` | the new boot answers |
| `run` | the same options as the top-level `run` | a fresh instance answers |

Parsing is strict: a misspelt key or step is an error naming its line, never a silently skipped check.

## Goldens

- A golden is a PNG beside the scenario (`tests/goldens/inbox.png`). The compare is exact at the panel's RGB565 colour depth: colours the panel can't tell apart don't count as differences. `tolerance_px` allows that many differing pixels; keep it at 0 unless something on screen is genuinely variable.
- **A missing golden is written and the step fails.** Look at the new image before you run again: it becomes the truth. `update_goldens` (`--update-goldens`) writes missing and differing goldens and passes; use it only for changes you meant.
- **On a mismatch** the step reports how many pixels differ and where, and writes `<name>_actual.png` and `<name>_diff.png` beside the golden (the golden dimmed, differing pixels in magenta; if the sizes differ, only the actual). `run_scenario` returns both images. A failed `wait` keeps the screen at the time as `<scenario>_step<N>_screen.png`.
- Keep `*_actual.png`, `*_diff.png` and `*_step*_screen.png` out of version control (the template's `.gitignore` does).

## Keeping scenarios deterministic

- **Fix the clock** in `run`, so the status bar's time is the same every run, and keep a scenario short enough that the minute doesn't change.
- **Wait for meaning, never for time.** Wait for the log line the product writes when something happens, or for text on screen. Then `wait: { idle_ms: 300 }` before a `shot`, so the screen has finished redrawing.
- **Log what matters.** Have the product `ESP_LOGI` each event a test needs: "ready", a page change, an approval. Those lines are the scenario's anchors.
- **Mind what changes by itself:** simulated Wi-Fi scanning and giving up, battery level, timers and toasts that expire. Wait until they've settled, set them with `state`, or take the screenshot before they change.
- **Leave margins on timings.** A press's hold time can run a little long on a busy computer: hold well past a threshold (1100-1200 ms for a 1 s hold), and match gestures in log lines without exact milliseconds.
- **`wait` looks back** to the previous step's start, so "press, then wait for its log line" can't miss a line logged during the press. After a `power` step it matches from the new boot.
- **Wake only what is asleep.** A product logs "sleeping" just before it sleeps, and a press in between is missed, as on the board. Between that line and the waking press, add `state: { expect: { power: light_sleep } }`: an `expect` is checked until it holds.
- **A product that switches itself off** (a power-off hold on battery) or resets is followed: the next step talks to the new boot, or to the powered-off device, which answers `state` (`power: "off"`), a dark `shot`, and `wait` for a log line from its last boot. `press: pwr` turns it on again.
- **Every scenario starts fresh:** empty storage (unless `wipe: false`), and a new instance. To check persistence, change a setting, then `power: reset`, then `expect` it.

## Where scenarios live

`tests/*.scenario.yaml` in the project, with goldens in `tests/goldens/`. Goldens and scenario files must be inside the project. A scenario per flow is easier to read and to fix than one long one.
