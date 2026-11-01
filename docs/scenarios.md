# Scenarios

A scenario is a YAML file of steps that starts a fresh headless instance of your product, drives it, and checks it: screenshots against golden images, the design-rule lint, log lines, text on screen and device state. It stops at the first step that fails. Scenarios are how a product proves its screens and flows still work, on your computer and in CI.

The format, every step, how goldens work and how to keep scenarios repeatable are in the skill's reference: [scenarios.md](../plugin/skills/lcd349/references/scenarios.md). The examples' scenarios are good models: [`hello`](../examples/hello/tests/hello.scenario.yaml), [`inputs`](../examples/inputs/tests/) and [`gallery`](../examples/gallery/tests/gallery.scenario.yaml).

## Running them

```
lcd349 scenario tests/home.scenario.yaml                  # builds first, then runs
lcd349 scenario tests/*.scenario.yaml --no-build          # several, using the last build
lcd349 scenario tests/home.scenario.yaml --update-goldens  # accept new or changed screens
lcd349 scenario tests/home.scenario.yaml --keep           # on failure, leave the instance running to inspect
lcd349 scenario tests/home.scenario.yaml --json           # per-step results for scripts
```

The command exits 1 at the first failing step and prints the step, its line, and the paths of the golden, actual and diff images. A screenshot step with no golden writes it and fails, so that someone looks at it. The run stops there, so a new scenario writes one golden per run: check each image, then run again (or run once with `--update-goldens`, and check them all).

## In CI

Scenarios run headless, so any CI that can build the emulator target can run them. On GitHub Actions, in Espressif's container at the kit's ESP-IDF version, with the release's `lcd349` checked against its `SHA256SUMS`:

```yaml
jobs:
  scenarios:
    runs-on: ubuntu-latest
    container: espressif/idf:v5.5.5
    steps:
      - uses: actions/checkout@v4
      - run: apt-get update && apt-get install -y --no-install-recommends libbsd-dev curl
      - name: Scenarios
        shell: bash
        run: |
          . $IDF_PATH/export.sh
          V=0.2.1
          curl -fsSLO https://github.com/MaverickHQ/lcd349-kit/releases/download/v$V/lcd349_${V}_linux_amd64
          curl -fsSLO https://github.com/MaverickHQ/lcd349-kit/releases/download/v$V/SHA256SUMS
          sha256sum -c --ignore-missing SHA256SUMS
          install -m 755 lcd349_${V}_linux_amd64 /usr/local/bin/lcd349
          lcd349 scenario tests/*.scenario.yaml
```

Keep the `*_actual.png`, `*_diff.png` and `*_step*_screen.png` files a failure leaves as CI artifacts: they show what went wrong.
