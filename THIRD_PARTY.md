# Third-party software

lcd349-kit is licensed under Apache-2.0 (`LICENSE`). It uses the following third-party software. Before adding to this list, run valvur `check_package` (requirement 13.3).

## ESP-IDF component (`components/lcd349`), resolved by the ESP-IDF component manager

| Component | Version | Licence | Used for |
|---|---|---|---|
| ESP-IDF | v5.5.5 (see `VERSIONS`) | Apache-2.0 | Build system, FreeRTOS, drivers, the `linux` host target |
| `lvgl/lvgl` | 9.6.0 | MIT | UI library. Products use it directly. |
| LVGL's bundled Montserrat fonts | with LVGL | OFL-1.1 (font), MIT (LVGL conversion) | Default fonts |

Hardware backend components (`esp32s3` only), added in phase 1 (the parent spec's task group 3):

| Component | Version | Licence | Used for |
|---|---|---|---|
| `espressif/esp_lcd_axs15231b` | 2.1.1 | Apache-2.0 | The panel. Its touch driver isn't used: the kit reads touch reports itself (kit ADR 0002). |
| `espressif/esp_codec_dev` | 1.6.2 | Apache-2.0 | The ES8311 codec, for tones (added in phase 1, task 1.2) |
| `espressif/esp_lcd_touch` (required by `esp_lcd_axs15231b`) | 1.2.1 | Apache-2.0 | Present but unused: it comes with `esp_lcd_axs15231b`, and the kit calls none of it |
| `espressif/cmake_utilities` (required by `esp_lcd_axs15231b`) | 0.5.3 | Apache-2.0 | Build helpers for `esp_lcd_axs15231b` |

`espressif/esp_io_expander_tca9554` was planned and is not used: its constructor resets the expander, which releases the power latch on battery, so the kit writes the TCA9554's registers itself (kit ADR 0001).

## Go (`lcd349` CLI and MCP server); exact versions are in `go.sum`

| Module | Licence |
|---|---|
| `github.com/modelcontextprotocol/go-sdk` | Apache-2.0 (with MIT for earlier contributions; see its LICENSE) |
| `github.com/google/jsonschema-go` | MIT |
| `github.com/yosida95/uritemplate/v3` | BSD-3-Clause |
| `github.com/segmentio/encoding`, `github.com/segmentio/asm` | MIT |
| `github.com/golang-jwt/jwt/v5` | MIT |
| `golang.org/x/oauth2`, `x/time`, `x/tools`, `x/sync`, `x/sys` | BSD-3-Clause |
| `go.yaml.in/yaml/v3` (the YAML organisation's continuation of `gopkg.in/yaml.v3`; added in task 6.2: scenario files and `idf_component.yml`) | MIT and Apache-2.0 (see its LICENSE) |

## Viewer (`viewer/`)

| Library | Licence | How |
|---|---|---|
| SDL2 (on macOS, Homebrew's `sdl2-compat` over SDL3) | zlib | Dynamically linked; not redistributed |

## Not used

Waveshare's demo repository for this board (github.com/waveshareteam/ESP32-S3-Touch-LCD-3.49) has no licence. No code from it is in this repository. Facts read from it are cited in `device/device.json`.
