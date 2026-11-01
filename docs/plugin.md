# The Claude Code plugin

The `lcd349` plugin gives Claude Code the [skill](../plugin/skills/lcd349/SKILL.md) for this board and the [MCP tools](#the-tools) that drive the emulator. With both, an agent can design, build and verify a UI: it sketches the screens, builds and runs them headless, reads screenshots, the UI tree and the lint, drives touch and buttons, writes a scenario, and hands you the hardware checklist.

## Installing

In Claude Code 2.1.275 or later:

```
/plugin install lcd349 --marketplace MaverickHQ/lcd349-kit
```

On older versions: `/plugin marketplace add MaverickHQ/lcd349-kit`, then `/plugin install lcd349@lcd349-kit`. From a shell: `claude plugin install lcd349 --marketplace MaverickHQ/lcd349-kit`. You also need ESP-IDF (see [getting started](getting-started.md)); `lcd349 doctor` checks it.

The plugin runs the `lcd349` release binary for macOS on Apple silicon or Linux on x86_64, and downloads it the first time ([how](#how-the-plugin-finds-lcd349)). **Intel Macs and Linux on arm64 have no release binary:** build `lcd349` from a checkout of the plugin's version (its tag, such as `v0.2.1`) and put it on PATH, and the plugin uses that instead:

```
go build -o ~/bin/lcd349 ./cmd/lcd349
```

### From a checkout

To work on the kit itself, load the plugin from your checkout for one session. A development plugin has no release binary to download, so put the `lcd349` you built on PATH, and tell `new_project` where the kit is:

```
go build -o ~/bin/lcd349 ./cmd/lcd349
LCD349_KIT_PATH=$PWD/components/lcd349 claude --plugin-dir plugin
```

The installed plugin doesn't need `LCD349_KIT_PATH`: `new_project` pins the kit's release, as `lcd349 new` does.

## What's inside

- **The skill** (`skills/lcd349`): Claude Code starts it by itself when your request or your project calls for it; call it yourself with `/lcd349:lcd349`. Inside: when to use it, the workflow as a checklist, the board's facts and design rules (generated from `device.json`), the button grammar, a never-do list, and references: the device facts, the design guide, recipes (code from `examples/gallery`), the C API (generated from the headers), scenarios, what the emulator doesn't model, the hardware checklist and troubleshooting.
- **The MCP server:** `.mcp.json` runs `scripts/lcd349-launch mcp`.

## The tools

| Tool | Does |
|---|---|
| `device_info` | the board's facts, limits, design rules, and what the emulator doesn't model |
| `new_project` | a product from the template (only inside the session's working directory) |
| `build` | the emulator build; errors with file, line and a hint naming the kit call to use |
| `run`, `stop` | an instance, headless unless `window`; instances stop when the session ends |
| `screenshot` | the screen as an image, with its size in mm; `physical` sizes it as the window shows it |
| `ui_tree`, `ui_lint` | the object tree; the design-rule check, with a fix per finding |
| `touch`, `press` | tap, long press, swipe, raw points; BOOT, PWR or both for a time |
| `wait`, `logs` | for a log line, text or a settled screen; the serial output with a cursor |
| `get_state`, `set_state`, `power` | battery, USB, Wi-Fi networks, clock, board revision (`board_rev`); on, off, reset, download mode |
| `run_scenario` | a scenario, with the actual and diff images of a failure |
| `record` | an animated GIF of what happened |

Plugin tools load on demand: Claude Code finds them when the skill or your request calls for them. Their full names are `mcp__plugin_lcd349_lcd349__<tool>`.

## How the plugin finds `lcd349`

`scripts/lcd349-launch`:

1. runs `lcd349` from PATH if `lcd349 version` reports exactly the plugin's version (it doesn't check that binary's checksum: it's yours);
2. otherwise uses the release binary for your OS and CPU from the plugin's data directory, downloading it over HTTPS if it's missing or doesn't match its checksum;
3. never runs a downloaded binary whose SHA-256 differs from `scripts/checksums.txt`, which ships inside the (versioned) plugin, and says so. Its lines are the `lcd349` lines of the release's `SHA256SUMS`.

The plugin's version, the launcher's and the binary's are always the same. On a platform with no release binary, the launcher stops and says to put `lcd349` of its version on PATH.

The launcher downloads `lcd349`, never `lcd349-view`. Headless runs, the tools' default, don't need the viewer. For a window (`run` with `window`), `lcd349` uses the `lcd349-view` beside it or in `$LCD349_VIEW`, or else builds it once from the sources inside `lcd349` (CMake, Ninja, SDL2 and pkg-config) into `~/.cache/lcd349/`.

## Things to know

- `build` and `run` execute your project's own code, as building any project does: point the tools only at projects you trust. See [security](security.md).
- Tools accept only lcd349 projects (an ESP-IDF project whose `idf_component.yml` depends on lcd349). Scenario and golden paths must be inside the project, symlinks included.
- Every tool result is capped at 1 MB; a larger UI tree comes back trimmed, with a note on narrowing it.
- Remove the plugin with `/plugin uninstall lcd349`; its data directory, with any downloaded binary, goes with it.
