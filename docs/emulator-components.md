<!-- Generated from internal/build/components.go by `go test ./internal/build -update`. Don't edit. -->

# ESP-IDF components in the emulator

The emulator builds your product for ESP-IDF's `linux` target, which lacks the board's hardware drivers. Products use the kit API for everything physical, so the same source builds for both targets. When a build uses one of these components, `lcd349 build` names the replacement.

In ESP-IDF v5.5.5 a missing component shows in one of three ways: it doesn't resolve (`Failed to resolve component`), its header is missing (`'driver/gpio.h' file not found`), or it compiles and its functions don't link (undefined symbols).

| Component | Headers | Use instead |
|---|---|---|
| `esp_lcd` | `esp_lcd_panel_ops.h`, `esp_lcd_panel_io.h`, `esp_lcd_axs15231b.h` | Draw with LVGL on `lcd349_display()`; the kit drives the panel, rotation and byte order. |
| `esp_lcd_touch` | `esp_lcd_touch.h` | `lcd349_touch_indev()` for LVGL, or `lcd349_touch_subscribe()` for raw samples. |
| `esp_driver_gpio` | `driver/gpio.h` | Buttons: `lcd349_buttons_subscribe()`, `lcd349_button_is_down()` and the classifier. The board has no other free pins the kit exposes. |
| `esp_driver_ledc` | `driver/ledc.h` | Backlight: `lcd349_backlight_set()`. |
| `esp_driver_i2c` | `driver/i2c_master.h`, `driver/i2c.h` | The kit owns the I2C bus: `lcd349_rtc_get()`/`lcd349_rtc_set()`, `lcd349_power_hold()`/`lcd349_power_off()`, touch through `lcd349_touch_subscribe()`. |
| `esp_driver_i2s` | `driver/i2s_std.h`, `driver/i2s.h` | `lcd349_tone()`, `lcd349_tone_seq()`, `lcd349_tone_stop()`. |
| `esp_codec_dev` | `esp_codec_dev.h` | `lcd349_tone()`, `lcd349_tone_seq()`, `lcd349_tone_stop()`. |
| `esp_adc` | `esp_adc/adc_oneshot.h`, `esp_adc/adc_cali.h` | Battery: `lcd349_battery_read()`. |
| `esp_wifi` | `esp_wifi.h` | `lcd349_wifi_set_networks()`, `lcd349_wifi_start()`, `lcd349_wifi_status()`, `lcd349_wifi_subscribe()`. In the emulator, sockets use the host's network. |
| `esp_pm` | `esp_pm.h`, `esp_sleep.h` | `lcd349_sleep_until_button()`, `lcd349_display_sleep()`, `lcd349_power_off()`. |
| `esp_driver_usb_serial_jtag` | `driver/usb_serial_jtag.h` | Write with `printf` or `ESP_LOG`; read lines with `lcd349_console_read_line()`. |
| `esp_driver_uart` | `driver/uart.h` | The console is USB: `printf`/`ESP_LOG` out, `lcd349_console_read_line()` in. |
| `esp_timer` | — | `lcd349_time_us()`, `lcd349_time_ms()`; FreeRTOS timers (`xTimerCreate`) for periodic work. |
| `esp_psram` | `esp_psram.h` | `lcd349_alloc(size, LCD349_MEM_PSRAM)` and `lcd349_mem_stats()`. |

These work in the emulator as on the board: FreeRTOS, `esp_event`, `esp_log`, `nvs_flash` (prefer `lcd349_kv_*`: per instance, and cleared by `--wipe`), and BSD sockets.
