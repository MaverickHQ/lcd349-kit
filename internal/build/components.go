package build

import (
	"fmt"
	"regexp"
	"strings"
)

// Unsupported is one ESP-IDF component the emulator (the linux target)
// lacks, how a build failure shows it, and the kit API that replaces it.
// ESP-IDF v5.5.6 fails these three ways: the component doesn't resolve
// (esp_lcd, esp_wifi, esp_adc), its header is missing (driver/gpio.h), or
// it compiles and its functions don't link (I2C, USB Serial/JTAG, sleep).
type Unsupported struct {
	Component string   // as in REQUIRES
	Headers   []string // headers a product would include
	Symbols   []string // function name prefixes
	Use       string   // the kit replacement
}

// UnsupportedComponents is the table behind build hints and the generated
// docs/emulator-components.md.
var UnsupportedComponents = []Unsupported{
	{"esp_lcd", []string{"esp_lcd_panel_ops.h", "esp_lcd_panel_io.h", "esp_lcd_axs15231b.h"}, []string{"esp_lcd_panel_", "esp_lcd_new_"},
		"Draw with LVGL on `lcd349_display()`; the kit drives the panel, rotation and byte order."},
	{"esp_lcd_touch", []string{"esp_lcd_touch.h"}, []string{"esp_lcd_touch_"},
		"`lcd349_touch_indev()` for LVGL, or `lcd349_touch_subscribe()` for raw samples."},
	{"esp_driver_gpio", []string{"driver/gpio.h"}, []string{"gpio_"},
		"Buttons: `lcd349_buttons_subscribe()`, `lcd349_button_is_down()` and the classifier. The board has no other free pins the kit exposes."},
	{"esp_driver_ledc", []string{"driver/ledc.h"}, []string{"ledc_"},
		"Backlight: `lcd349_backlight_set()`."},
	{"esp_driver_i2c", []string{"driver/i2c_master.h", "driver/i2c.h"}, []string{"i2c_"},
		"The kit owns the I2C bus: `lcd349_rtc_get()`/`lcd349_rtc_set()`, `lcd349_power_hold()`/`lcd349_power_off()`, touch through `lcd349_touch_subscribe()`."},
	{"esp_driver_i2s", []string{"driver/i2s_std.h", "driver/i2s.h"}, []string{"i2s_"},
		"`lcd349_tone()`, `lcd349_tone_seq()`, `lcd349_tone_stop()`."},
	{"esp_codec_dev", []string{"esp_codec_dev.h"}, []string{"esp_codec_dev_"},
		"`lcd349_tone()`, `lcd349_tone_seq()`, `lcd349_tone_stop()`."},
	{"esp_adc", []string{"esp_adc/adc_oneshot.h", "esp_adc/adc_cali.h"}, []string{"adc_oneshot_", "adc_cali_"},
		"Battery: `lcd349_battery_read()`."},
	{"esp_wifi", []string{"esp_wifi.h"}, []string{"esp_wifi_"},
		"`lcd349_wifi_set_networks()`, `lcd349_wifi_start()`, `lcd349_wifi_status()`, `lcd349_wifi_subscribe()`. In the emulator, sockets use the host's network."},
	{"esp_pm", []string{"esp_pm.h", "esp_sleep.h"}, []string{"esp_pm_", "esp_light_sleep_", "esp_deep_sleep", "esp_sleep_"},
		"`lcd349_sleep_until_button()`, `lcd349_display_sleep()`, `lcd349_power_off()`."},
	{"esp_driver_usb_serial_jtag", []string{"driver/usb_serial_jtag.h"}, []string{"usb_serial_jtag_"},
		"Write with `printf` or `ESP_LOG`; read lines with `lcd349_console_read_line()`."},
	{"esp_driver_uart", []string{"driver/uart.h"}, []string{"uart_"},
		"The console is USB: `printf`/`ESP_LOG` out, `lcd349_console_read_line()` in."},
	{"esp_timer", nil, []string{"esp_timer_"},
		"`lcd349_time_us()`, `lcd349_time_ms()`; FreeRTOS timers (`xTimerCreate`) for periodic work."},
	{"esp_psram", []string{"esp_psram.h"}, []string{"esp_psram_"},
		"`lcd349_alloc(size, LCD349_MEM_PSRAM)` and `lcd349_mem_stats()`."},
}

var (
	notFoundRe = regexp.MustCompile(`'([^']+)' file not found|([\w/.]+\.h): No such file`)

	symbolRe = regexp.MustCompile(`undefined symbol (\S+)`)
)

// HintFor returns the kit replacement when a build error names a component
// the emulator lacks, or "".
func HintFor(msg string) string {
	if m := resolveRe.FindStringSubmatch(msg); m != nil {
		for _, u := range UnsupportedComponents {
			if u.Component == m[1] {
				return u.hint("isn't available in the emulator")
			}
		}
		if m[1] == "driver" {
			return "the legacy `driver` component isn't available in the emulator: require the specific esp_driver_* component, and use the kit API for board hardware (docs/emulator-components.md)"
		}
	}
	if m := notFoundRe.FindStringSubmatch(msg); m != nil {
		h := m[1] + m[2]
		for _, u := range UnsupportedComponents {
			for _, uh := range u.Headers {
				if h == uh {
					return u.hint("has no " + h + " in the emulator")
				}
			}
		}
	}
	if m := symbolRe.FindStringSubmatch(msg); m != nil {
		for _, u := range UnsupportedComponents {
			for _, p := range u.Symbols {
				if strings.HasPrefix(m[1], p) {
					return u.hint("compiles but doesn't run in the emulator (" + m[1] + " is missing)")
				}
			}
		}
	}
	return ""
}

func (u Unsupported) hint(why string) string {
	return fmt.Sprintf("%s %s. Use the kit instead: %s", u.Component, why, strings.ReplaceAll(u.Use, "`", ""))
}

// ComponentsDoc renders docs/emulator-components.md from the table.
func ComponentsDoc() string {
	var b strings.Builder
	b.WriteString("<!-- Generated from internal/build/components.go by `go test ./internal/build -update`. Don't edit. -->\n\n")
	b.WriteString("# ESP-IDF components in the emulator\n\n")
	b.WriteString("The emulator builds your product for ESP-IDF's `linux` target, which lacks the board's hardware drivers. ")
	b.WriteString("Products use the kit API for everything physical, so the same source builds for both targets. ")
	b.WriteString("When a build uses one of these components, `lcd349 build` names the replacement.\n\n")
	b.WriteString("In ESP-IDF v5.5.6 a missing component shows in one of three ways: it doesn't resolve (`Failed to resolve component`), ")
	b.WriteString("its header is missing (`'driver/gpio.h' file not found`), or it compiles and its functions don't link (undefined symbols).\n\n")
	b.WriteString("| Component | Headers | Use instead |\n|---|---|---|\n")
	for _, u := range UnsupportedComponents {
		hs := make([]string, len(u.Headers))
		for i, h := range u.Headers {
			hs[i] = "`" + h + "`"
		}
		headers := strings.Join(hs, ", ")
		if headers == "" {
			headers = "—"
		}
		fmt.Fprintf(&b, "| `%s` | %s | %s |\n", u.Component, headers, u.Use)
	}
	b.WriteString("\nThese work in the emulator as on the board: FreeRTOS, `esp_event`, `esp_log`, `nvs_flash` (prefer `lcd349_kv_*`: per instance, and cleared by `--wipe`), and BSD sockets.\n")
	return b.String()
}
