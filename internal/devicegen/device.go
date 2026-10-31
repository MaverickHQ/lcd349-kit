package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/google/jsonschema-go/jsonschema"
)

// Fact is a value with its own provenance. Value is null until verified;
// Assumed is a stand-in that the SDK and emulator may use, never a lint
// threshold (requirement 10.3).
type Fact struct {
	Value        json.RawMessage `json:"value"`
	Assumed      json.RawMessage `json:"assumed,omitempty"`
	AssumedBasis string          `json:"assumed_basis,omitempty"`
	Options      []string        `json:"options,omitempty"`
	Source       string          `json:"source"`
	Verified     *string         `json:"verified"`
}

func isNull(m json.RawMessage) bool {
	return len(m) == 0 || string(bytes.TrimSpace(m)) == "null"
}

// IsVerified reports whether the fact has a measured or documented value.
func (f Fact) IsVerified() bool { return !isNull(f.Value) && f.Verified != nil }

// effective decodes the verified value, or else the assumed one, into dst.
// It returns whether the decoded value is verified, and false/nil error with
// ok=false when neither is present.
func (f Fact) effective(dst any) (verified, ok bool, err error) {
	switch {
	case !isNull(f.Value):
		return true, true, json.Unmarshal(f.Value, dst)
	case !isNull(f.Assumed):
		return false, true, json.Unmarshal(f.Assumed, dst)
	}
	return false, false, nil
}

type Provenance struct {
	Source   string  `json:"source"`
	Verified *string `json:"verified"`
}

type Device struct {
	Schema int `json:"schema"`
	Board  struct {
		Name        string `json:"name"`
		Vendor      string `json:"vendor"`
		VariantNote string `json:"variant_note"`
		SoC         string `json:"soc"`
		CPUMHz      int    `json:"cpu_mhz"`
		PSRAMMB     int    `json:"psram_mb"`
		FlashMB     int    `json:"flash_mb"`
		Provenance
		Revision Fact `json:"revision"`
	} `json:"board"`
	I2C struct {
		Pins map[string]int `json:"pins"`
		Note string         `json:"note"`
		Provenance
	} `json:"i2c"`
	Expander struct {
		Chip    string         `json:"chip"`
		I2CAddr string         `json:"i2c_addr"`
		IntGPIO int            `json:"int_gpio"`
		Pins    map[string]int `json:"pins"` // signal -> port bit
		Provenance
	} `json:"expander"`
	Display struct {
		Controller    string         `json:"controller"`
		Bus           string         `json:"bus"`
		Pins          map[string]int `json:"pins"`
		PanelW        int            `json:"panel_w"`
		PanelH        int            `json:"panel_h"`
		LandscapeW    int            `json:"landscape_w"`
		LandscapeH    int            `json:"landscape_h"`
		Format        string         `json:"format"`
		ByteSwap      bool           `json:"byte_swap"`
		FullFrameOnly bool           `json:"full_frame_only"`
		HWRotation    bool           `json:"hw_rotation"`
		DiagonalIn    float64        `json:"diagonal_in"`
		PPI           int            `json:"ppi"`
		PxPerMM       float64        `json:"px_per_mm"`
		MMW           float64        `json:"mm_w"`
		MMH           float64        `json:"mm_h"`
		Provenance
		Rotation           Fact `json:"rotation"`
		FlushMS            Fact `json:"flush_ms"`
		RefreshHz          Fact `json:"refresh_hz"`
		BacklightActiveLow Fact `json:"backlight_active_low"`
	} `json:"display"`
	Case struct {
		Name string  `json:"name"`
		MMW  float64 `json:"mm_w"`
		MMH  float64 `json:"mm_h"`
		MMD  float64 `json:"mm_d"`
		Provenance
		ScreenOffsetMM Fact `json:"screen_offset_mm"`
	} `json:"case"`
	Touch struct {
		Type       string             `json:"type"`
		Controller string             `json:"controller"`
		I2CAddr    string             `json:"i2c_addr"`
		Pins       map[string]int     `json:"pins"`
		Points     int                `json:"points"`
		WakeSource bool               `json:"wake_source"`
		IntPin     map[string]*string `json:"int_pin"`
		Provenance
		PeriodMS    Fact `json:"period_ms"`
		MultiPoint  Fact `json:"multi_point"`
		ThroughCase Fact `json:"through_case"`
	} `json:"touch"`
	Buttons struct {
		Provenance
		Items []struct {
			ID         string  `json:"id"`
			GPIO       int     `json:"gpio"`
			Active     string  `json:"active"`
			ResetTrap  *string `json:"reset_trap"`
			PositionMM Fact    `json:"position_mm"`
		} `json:"items"`
	} `json:"buttons"`
	Power struct {
		Hold struct {
			Expander string `json:"expander"`
			Pin      string `json:"pin"`
			Note     string `json:"note"`
		} `json:"hold"`
		Battery struct {
			ADC       string  `json:"adc"`
			Divider   float64 `json:"divider"`
			Chemistry string  `json:"chemistry"`
			NominalV  float64 `json:"nominal_v"`
		} `json:"battery"`
		Provenance
		BatteryCapacityMAh Fact `json:"battery_capacity_mah"`
		BatteryCurve       Fact `json:"battery_curve"`
	} `json:"power"`
	PowerStates []struct {
		ID        string `json:"id"`
		What      string `json:"what"`
		CurrentMA Fact   `json:"current_ma"`
	} `json:"power_states"`
	Audio struct {
		DAC     string `json:"dac"`
		ADC     string `json:"adc"`
		Mics    int    `json:"mics"`
		Speaker string `json:"speaker"`
		Codec   struct {
			Chip    string         `json:"chip"`
			I2CAddr string         `json:"i2c_addr"`
			I2SPins map[string]int `json:"i2s_pins"`
			Provenance
		} `json:"codec"`
		Provenance
		AmpEnable        Fact `json:"amp_enable"`
		SpeakerConnected Fact `json:"speaker_connected"`
	} `json:"audio"`
	RTC  I2CChip `json:"rtc"`
	IMU  I2CChip `json:"imu"`
	WiFi struct {
		BandsGHz []float64 `json:"bands_ghz"`
		Note     string    `json:"note"`
		Provenance
	} `json:"wifi"`
	Kit struct {
		BacklightDefaultPct int `json:"backlight_default_pct"`
		ButtonDebounceMS    int `json:"button_debounce_ms"`
		PowerOffUSBCheckMS  int `json:"power_off_usb_check_ms"`
		WiFiMaxNetworks     int `json:"wifi_max_networks"`
		WiFiScanIntervalS   int `json:"wifi_scan_interval_s"`
		AudioSampleRateHz   int `json:"audio_sample_rate_hz"`
		EmulatorBatteryMV   int `json:"emulator_battery_mv"`
		Provenance
	} `json:"kit"`
	Budgets struct {
		PSRAMKB       Fact `json:"psram_kb"`
		InternalRAMKB Fact `json:"internal_ram_kb"`
		LVGLMemKB     Fact `json:"lvgl_mem_kb"`
	} `json:"budgets"`
	Fidelity []struct {
		ID              string `json:"id"`
		What            string `json:"what"`
		CheckOnHardware string `json:"check_on_hardware"`
	} `json:"fidelity_not_modelled"`
	Rules []Rule `json:"design_rules"`
}

type I2CChip struct {
	Chip    string `json:"chip"`
	I2CAddr string `json:"i2c_addr"`
	Provenance
}

type Rule struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`
	Severity  string `json:"severity"`
	Threshold *struct {
		Value *float64 `json:"value"`
		Unit  string   `json:"unit"`
	} `json:"threshold"`
	Why        string   `json:"why"`
	Fix        string   `json:"fix"`
	ExemptTags []string `json:"exempt_tags"`
	Provenance
}

// Load reads device.json and its schema from root/device, validates the file
// against the schema, decodes it, and applies the checks a schema can't
// express.
func Load(root string) (*Device, []byte, error) {
	raw, err := os.ReadFile(filepath.Join(root, "device", "device.json"))
	if err != nil {
		return nil, nil, err
	}
	schema, err := os.ReadFile(filepath.Join(root, "device", "device.schema.json"))
	if err != nil {
		return nil, nil, err
	}
	d, err := Parse(raw, schema)
	return d, raw, err
}

// Parse validates raw against schema, decodes it and checks it.
func Parse(raw, schema []byte) (*Device, error) {
	if err := validateSchema(raw, schema); err != nil {
		return nil, fmt.Errorf("device.json does not match device.schema.json: %w", err)
	}
	var d Device
	dec := json.NewDecoder(bytes.NewReader(raw))
	if err := dec.Decode(&d); err != nil {
		return nil, fmt.Errorf("device.json: %w", err)
	}
	if err := check(&d); err != nil {
		return nil, err
	}
	return &d, nil
}

func validateSchema(raw, schemaRaw []byte) error {
	var s jsonschema.Schema
	if err := json.Unmarshal(schemaRaw, &s); err != nil {
		return fmt.Errorf("schema: %w", err)
	}
	rs, err := s.Resolve(nil)
	if err != nil {
		return fmt.Errorf("schema: %w", err)
	}
	var inst any
	if err := json.Unmarshal(raw, &inst); err != nil {
		return err
	}
	return rs.Validate(inst)
}

// check enforces the rules a JSON schema can't express.
func check(d *Device) error {
	var errs []error
	add := func(format string, a ...any) { errs = append(errs, fmt.Errorf(format, a...)) }

	if d.Display.LandscapeW != d.Display.PanelH || d.Display.LandscapeH != d.Display.PanelW {
		add("display: landscape %dx%d is not the panel %dx%d rotated", d.Display.LandscapeW, d.Display.LandscapeH, d.Display.PanelW, d.Display.PanelH)
	}
	var rot string
	if _, ok, err := d.Display.Rotation.effective(&rot); err != nil || !ok || (rot != "cw90" && rot != "ccw90") {
		add("display.rotation: needs a value or an assumed value of cw90 or ccw90")
	}
	var n int
	if _, ok, err := d.Display.FlushMS.effective(&n); err != nil || !ok || n <= 0 {
		add("display.flush_ms: needs a positive value or assumed value (the emulator's timing model)")
	}
	if _, ok, err := d.Display.RefreshHz.effective(&n); err != nil || (ok && n <= 0) {
		add("display.refresh_hz: must be a positive number of hertz")
	}
	if _, ok, err := d.Touch.PeriodMS.effective(&n); err != nil || !ok || n <= 0 {
		add("touch.period_ms: needs a positive value or assumed value")
	}
	var off [2]float64
	if _, ok, err := d.Case.ScreenOffsetMM.effective(&off); err != nil || !ok {
		add("case.screen_offset_mm: needs a value or an assumed value of [x, y] millimetres")
	} else if off[0] < 0 || off[1] < 0 || off[0]+d.Display.MMW > d.Case.MMW || off[1]+d.Display.MMH > d.Case.MMH {
		add("case.screen_offset_mm: the %gx%g mm active area at [%g, %g] doesn't fit the %gx%g mm case", d.Display.MMW, d.Display.MMH, off[0], off[1], d.Case.MMW, d.Case.MMH)
	}
	if d.Touch.WakeSource {
		add("touch.wake_source: touch is never a wake source (requirement 1.5)")
	}
	if want := fmt.Sprintf("P%d", d.Expander.Pins["sys_en"]); d.Power.Hold.Expander != d.Expander.Chip || d.Power.Hold.Pin != want {
		add("power.hold.pin: the hold is %s %s, but the expander's SYS_EN is %s %s", d.Power.Hold.Expander, d.Power.Hold.Pin, d.Expander.Chip, want)
	}
	var backlightLow bool
	if _, ok, err := d.Display.BacklightActiveLow.effective(&backlightLow); err != nil || !ok {
		add("display.backlight_active_low: needs a value or an assumed value of true or false")
	}

	seen := map[string]bool{}
	for _, b := range d.Buttons.Items {
		if seen["btn:"+b.ID] {
			add("buttons: duplicate id %s", b.ID)
		}
		seen["btn:"+b.ID] = true
		var pos [2]float64
		if _, ok, err := b.PositionMM.effective(&pos); err != nil {
			add("buttons[%s].position_mm: must be [x, y] millimetres from the active area's top-left: %v", b.ID, err)
		} else if ok && (pos[0] < -d.Case.MMW || pos[0] > d.Case.MMW || pos[1] < -d.Case.MMH || pos[1] > d.Case.MMH) {
			add("buttons[%s].position_mm: [%g, %g] is outside the case", b.ID, pos[0], pos[1])
		}
	}
	if !seen["btn:BOOT"] || !seen["btn:PWR"] {
		add("buttons: both BOOT and PWR are required")
	}
	for _, s := range d.PowerStates {
		if seen["ps:"+s.ID] {
			add("power_states: duplicate id %s", s.ID)
		}
		seen["ps:"+s.ID] = true
	}
	for _, f := range d.Fidelity {
		if seen["fid:"+f.ID] {
			add("fidelity_not_modelled: duplicate id %s", f.ID)
		}
		seen["fid:"+f.ID] = true
	}

	var curve [][2]int
	if _, ok, err := d.Power.BatteryCurve.effective(&curve); err != nil || !ok {
		add("power.battery_curve: needs a value or an assumed value: %v", err)
	} else if err := checkCurve(curve); err != nil {
		add("power.battery_curve: %v", err)
	}

	for _, r := range d.Rules {
		if seen["rule:"+r.ID] {
			add("design_rules: duplicate id %s", r.ID)
		}
		seen["rule:"+r.ID] = true
		if err := checkRule(r); err != nil {
			add("design_rules[%s]: %v", r.ID, err)
		}
	}
	return errors.Join(errs...)
}

func checkCurve(c [][2]int) error {
	if len(c) < 2 {
		return errors.New("needs at least two points")
	}
	if c[0][1] != 100 || c[len(c)-1][1] != 0 {
		return errors.New("must run from 100 % down to 0 %")
	}
	for i := 1; i < len(c); i++ {
		if c[i][0] >= c[i-1][0] || c[i][1] >= c[i-1][1] {
			return fmt.Errorf("points must strictly decrease in both millivolts and percent (at index %d)", i)
		}
	}
	return nil
}

// checkRule refuses thresholds that would make lint enforce an unverified
// number (requirement 10.3).
func checkRule(r Rule) error {
	if r.Severity == "off" {
		return nil
	}
	if r.Verified == nil {
		return errors.New("an unverified rule can't be enforced; set severity to off until it is verified")
	}
	if r.Kind == "check" {
		if r.Threshold != nil {
			return errors.New("a check rule takes no threshold")
		}
		return nil
	}
	if r.Threshold == nil || r.Threshold.Value == nil {
		if r.Severity == "error" {
			return errors.New("severity error needs a verified threshold, but the threshold is null")
		}
		return errors.New("a threshold rule needs a threshold value, or severity off")
	}
	if *r.Threshold.Value <= 0 {
		return errors.New("threshold must be positive")
	}
	want := map[string]string{"min_size": "px", "min_font": "px", "min_contrast": "ratio"}[r.Kind]
	if r.Threshold.Unit != want {
		return fmt.Errorf("kind %s takes unit %s, not %s", r.Kind, want, r.Threshold.Unit)
	}
	return nil
}
