# Security

lcd349-kit runs on your computer and runs code you give it. This page says what it does there and what to trust.

## It runs your project's code

`lcd349 build`, `lcd349 run` and `lcd349 scenario`, and the MCP tools `build`, `run` and `run_scenario`, compile your project and execute it, with your user's rights. That's the point of an emulator, and it means what building any project means: **use them only on projects you trust.** This holds for agents too: point the MCP server only at projects you'd build yourself. An agent can write code into a project and then run it; review what it wrote as you would any change.

## What it doesn't do

- **No network listeners.** The emulator, the CLI and the MCP server never open a TCP or UDP port. Each instance answers only on a Unix socket in a directory only you can open (mode 0700; the socket is 0600).
- **No shells.** Every program the kit starts (`idf.py`, your product, the viewer, CMake) gets a fixed argument list; nothing you or an agent types is passed through a shell.
- **No new network access for your product.** In the emulator your product's sockets use your computer's network, as any program you run would. The simulated Wi-Fi only decides when the product believes it's connected.

## Files it creates

- Instance directories under `$TMPDIR/lcd349-<uid>/` (or `/tmp/lcd349-<uid>/`), owner-only. The kit refuses, or skips when listing, a directory there that isn't yours, is open to other users or is a symlink, and reads `instance.json` only as a regular file. An instance named by a path must be an instance directory there. `lcd349 stop` removes an instance's directory, and never anything else.
- Each instance's virtual serial port is a pseudo-terminal owned by you with mode 0600, so other users can't read or write it.
- A cache in `~/.cache/lcd349/`: ESP-IDF's environment and the viewer built from the kit's sources.
- Your project's `build-emu/` (the emulator build, kept apart from the board's `build/`), with the MCP `record` tool's GIFs in `build-emu/recordings/`, and goldens, actual and diff images and failed-`wait` screens in `tests/`. A recording of an instance with no project goes to `$TMPDIR/lcd349-recordings-<uid>/`, owner-only and checked as the instance directories are. A recording never overwrites a file, or writes through a symlink, already at its name.

## The MCP server's limits

- A `project` argument must resolve, symlinks evaluated, to an ESP-IDF project that depends on lcd349; anything else is refused with the reason.
- Scenario and golden paths must lie inside that project; `new_project` writes only inside the session's working directory.
- Results are capped at 1 MB, recordings at 60 s (or 600 frames), and screenshot zoom at 4. Each instance's control socket refuses request lines over 64 KB, and requests nesting more than 16 arrays and objects deep, whoever sends them.
- The instances it starts stop when the session ends, and with the server if it's killed.

## The plugin's binary

The plugin's launcher runs `lcd349` from your PATH only if it reports the plugin's exact version; it trusts that binary as it is, checking only the version. Otherwise it downloads the release binary over HTTPS into the plugin's data directory and runs it only if its SHA-256 matches the list that ships inside the plugin (`checksums.txt`: the `lcd349` lines of the release's `SHA256SUMS`). It downloads `lcd349` only, never `lcd349-view`. A compromise of the kit's repository itself is outside what that can catch.

Each release binary, `lcd349-view` included, also has a GitHub build attestation: `gh attestation verify <file> --repo MaverickHQ/lcd349-kit` shows it was built by the repository's release workflow. The [README](../README.md#quick-start-for-people) shows the whole check for a binary you install yourself.

## Dependencies

Every dependency is checked before it's added, and the Go modules' and ESP-IDF components' versions are locked: `go.sum`, the exact component versions in the component's manifest, and the test apps' `dependencies.lock.*`. The examples' lock files aren't committed, because they record the kit's absolute path. `THIRD_PARTY.md` lists them with their licences. No code comes from Waveshare's demo repository, which has no licence.

## Reporting a problem

Open an issue. For anything sensitive, don't: report it privately from the repository's [Security tab](https://github.com/MaverickHQ/lcd349-kit/security) instead.
