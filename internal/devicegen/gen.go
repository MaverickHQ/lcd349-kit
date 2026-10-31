package main

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"text/template"
)

//go:embed templates/*.tmpl templates/skill/*.tmpl
var templateFS embed.FS

// outputs maps each generated file (relative to the repository root) to the
// template that renders it.
var outputs = []struct{ path, tmpl string }{
	{"components/lcd349/include/lcd349_board.h", "board_h.tmpl"},
	{"components/lcd349/src/common/board_info.c", "board_info_c.tmpl"},
	{"components/lcd349/src/emu/defaults.h", "emu_defaults_h.tmpl"},
	{"components/lcd349/src/emu/design_rules.h", "design_rules_h.tmpl"},
	{"plugin/skills/lcd349/references/device.md", "device_md.tmpl"},
	{"plugin/skills/lcd349/references/fidelity.md", "fidelity_md.tmpl"},
	{"docs/hardware.md", "hardware_md.tmpl"},
	{"viewer/device_gen.h", "viewer_device_h.tmpl"},
}

type ruleView struct {
	Rule
	SevC       string // LCD349_SEV_ERROR, ...
	Threshold  string // human form, "" for checks
	ThreshX100 int
	Exempt     string
}

type stateView struct {
	ID, What string
	MA       int // -1 when unmeasured
	Source   string
}

type pending struct{ What, Source string }

type buttonView struct {
	ID       string
	Key      string // the viewer's keyboard shortcut
	HasPos   bool
	X, Y     float64
	Verified bool
	XPx      int // the centre's landscape x: X millimetres from the active area's left
}

type view struct {
	D                    *Device
	SHA                  string
	Rotation             string
	RotationVerified     bool
	FlushMS              int
	FlushVerified        bool
	RefreshHz            int // 0 when device.json has none
	RefreshVerified      bool
	TouchPeriodMS        int
	TouchPeriodVerified  bool
	Curve                [][2]int
	CurveVerified        bool
	Rules                []ruleView
	States               []stateView
	PSRAMKB              int
	InternalKB           int
	LVGLKB               int
	Pending              []pending
	Pins                 []pinView
	ScreenOffset         [2]float64
	ScreenOffsetVerified bool
	Buttons              []buttonView
	ExpanderPins         []pinView // port bits, not GPIOs
	BacklightActiveLow   bool
	BatteryADCUnit       int
	BatteryADCChannel    int
}

type pinView struct {
	Name string
	GPIO int
}

func buildView(d *Device, raw []byte) (*view, error) {
	sum := sha256.Sum256(raw)
	v := &view{D: d, SHA: hex.EncodeToString(sum[:])[:12]}
	var err error
	if v.RotationVerified, _, err = d.Display.Rotation.effective(&v.Rotation); err != nil {
		return nil, err
	}
	if v.FlushVerified, _, err = d.Display.FlushMS.effective(&v.FlushMS); err != nil {
		return nil, err
	}
	if v.RefreshVerified, _, err = d.Display.RefreshHz.effective(&v.RefreshHz); err != nil {
		return nil, err
	}
	if v.TouchPeriodVerified, _, err = d.Touch.PeriodMS.effective(&v.TouchPeriodMS); err != nil {
		return nil, err
	}
	if v.CurveVerified, _, err = d.Power.BatteryCurve.effective(&v.Curve); err != nil {
		return nil, err
	}
	if v.ScreenOffsetVerified, _, err = d.Case.ScreenOffsetMM.effective(&v.ScreenOffset); err != nil {
		return nil, err
	}
	if _, _, err = d.Display.BacklightActiveLow.effective(&v.BacklightActiveLow); err != nil {
		return nil, err
	}
	if _, err = fmt.Sscanf(d.Power.Battery.ADC, "ADC%d_CH%d", &v.BatteryADCUnit, &v.BatteryADCChannel); err != nil {
		return nil, fmt.Errorf("power.battery.adc %q: %w", d.Power.Battery.ADC, err)
	}
	for _, b := range d.Buttons.Items {
		bv := buttonView{ID: b.ID, Key: strings.ToLower(b.ID[:1])}
		var pos [2]float64
		if bv.Verified, bv.HasPos, err = b.PositionMM.effective(&pos); err != nil {
			return nil, err
		}
		bv.X, bv.Y = pos[0], pos[1]
		bv.XPx = int(math.Round(bv.X * d.Display.PxPerMM))
		v.Buttons = append(v.Buttons, bv)
	}
	for _, r := range d.Rules {
		rv := ruleView{Rule: r, SevC: "LCD349_SEV_" + strings.ToUpper(r.Severity), Exempt: strings.Join(r.ExemptTags, ",")}
		if r.Threshold != nil && r.Threshold.Value != nil {
			t := *r.Threshold.Value
			rv.ThreshX100 = int(math.Round(t * 100))
			if r.Threshold.Unit == "ratio" {
				rv.Threshold = strconv.FormatFloat(t, 'f', -1, 64) + ":1"
			} else {
				rv.Threshold = strconv.FormatFloat(t, 'f', -1, 64) + " " + r.Threshold.Unit
			}
		}
		v.Rules = append(v.Rules, rv)
	}
	for _, s := range d.PowerStates {
		sv := stateView{ID: s.ID, What: s.What, MA: -1, Source: s.CurrentMA.Source}
		if s.CurrentMA.IsVerified() {
			var ma float64
			if _, _, err := s.CurrentMA.effective(&ma); err != nil {
				return nil, err
			}
			sv.MA = int(math.Round(ma))
		}
		v.States = append(v.States, sv)
	}
	kb := func(f Fact) (int, error) {
		if !f.IsVerified() {
			return 0, nil
		}
		var n int
		_, _, err := f.effective(&n)
		return n, err
	}
	if v.PSRAMKB, err = kb(d.Budgets.PSRAMKB); err != nil {
		return nil, err
	}
	if v.InternalKB, err = kb(d.Budgets.InternalRAMKB); err != nil {
		return nil, err
	}
	if v.LVGLKB, err = kb(d.Budgets.LVGLMemKB); err != nil {
		return nil, err
	}

	facts := []struct {
		what string
		f    Fact
	}{
		{"Board revision (V1/V2)", d.Board.Revision},
		{"Display rotation in Case B", d.Display.Rotation},
		{"Full-frame flush time", d.Display.FlushMS},
		{"Panel refresh rate", d.Display.RefreshHz},
		{"Backlight polarity", d.Display.BacklightActiveLow},
		{"Touch sampling period", d.Touch.PeriodMS},
		{"Touch: second point ever reported", d.Touch.MultiPoint},
		{"Touch through the closed case", d.Touch.ThroughCase},
		{"Active area position in Case B", d.Case.ScreenOffsetMM},
		{"Battery capacity", d.Power.BatteryCapacityMAh},
		{"Battery discharge curve", d.Power.BatteryCurve},
		{"Audio amp enable pin", d.Audio.AmpEnable},
		{"Speaker connected", d.Audio.SpeakerConnected},
		{"Internal RAM budget", d.Budgets.InternalRAMKB},
		{"LVGL memory budget", d.Budgets.LVGLMemKB},
	}
	for _, b := range d.Buttons.Items {
		facts = append(facts, struct {
			what string
			f    Fact
		}{b.ID + " button position on Case B", b.PositionMM})
	}
	for _, s := range d.PowerStates {
		facts = append(facts, struct {
			what string
			f    Fact
		}{"Current draw: " + s.ID, s.CurrentMA})
	}
	for _, x := range facts {
		if !x.f.IsVerified() {
			v.Pending = append(v.Pending, pending{x.what, x.f.Source})
		}
	}

	for name, gpio := range d.Display.Pins {
		v.Pins = append(v.Pins, pinView{"LCD_" + strings.ToUpper(name), gpio})
	}
	for name, gpio := range d.Touch.Pins {
		v.Pins = append(v.Pins, pinView{"TOUCH_" + strings.ToUpper(name), gpio})
	}
	for _, b := range d.Buttons.Items {
		v.Pins = append(v.Pins, pinView{b.ID, b.GPIO})
	}
	for name, gpio := range d.I2C.Pins {
		v.Pins = append(v.Pins, pinView{"I2C_" + strings.ToUpper(name), gpio})
	}
	for name, gpio := range d.Audio.Codec.I2SPins {
		v.Pins = append(v.Pins, pinView{"I2S_" + strings.ToUpper(name), gpio})
	}
	v.Pins = append(v.Pins, pinView{"EXPANDER_INT", d.Expander.IntGPIO})
	sort.Slice(v.Pins, func(i, j int) bool { return v.Pins[i].Name < v.Pins[j].Name })
	for name, bit := range d.Expander.Pins {
		v.ExpanderPins = append(v.ExpanderPins, pinView{strings.ToUpper(name), bit})
	}
	sort.Slice(v.ExpanderPins, func(i, j int) bool { return v.ExpanderPins[i].GPIO < v.ExpanderPins[j].GPIO })
	return v, nil
}

var funcs = template.FuncMap{
	"cstr":  cstr,
	"upper": strings.ToUpper,
	"cident": func(s string) string {
		return strings.ToUpper(strings.NewReplacer("-", "_", ".", "_").Replace(s))
	},
	"num":  func(f float64) string { return strconv.FormatFloat(f, 'f', -1, 64) },
	"mulf": func(a, b float64) float64 { return a * b },
	"cfloat": func(f float64) string {
		s := strconv.FormatFloat(f, 'f', -1, 64)
		if !strings.Contains(s, ".") {
			s += ".0"
		}
		return s + "f"
	},
	"b2i": func(b bool) int {
		if b {
			return 1
		}
		return 0
	},
	"date": func(p *string) string {
		if p == nil {
			return "unverified"
		}
		return *p
	},
	"mdcell": func(s string) string { return strings.ReplaceAll(s, "|", `\|`) },
	"strp": func(p *string) string {
		if p == nil {
			return ""
		}
		return *p
	},
	"bands": func(b []float64) string {
		var s []string
		for _, x := range b {
			s = append(s, strconv.FormatFloat(x, 'f', -1, 64)+" GHz")
		}
		return strings.Join(s, ", ")
	},
	"last":  func(i, n int) bool { return i == n-1 },
	"deref": func(p *float64) float64 { return *p },
}

// cstr renders s as a C string literal. UTF-8 passes through unchanged.
func cstr(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '\\':
			b.WriteString(`\\`)
		case '"':
			b.WriteString(`\"`)
		case '\n':
			b.WriteString(`\n`)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// Render produces every generated file's contents, keyed by path.
func Render(d *Device, raw []byte) (map[string][]byte, error) {
	v, err := buildView(d, raw)
	if err != nil {
		return nil, err
	}
	t, err := template.New("").Funcs(funcs).ParseFS(templateFS, "templates/*.tmpl")
	if err != nil {
		return nil, err
	}
	out := map[string][]byte{}
	for _, o := range outputs {
		var buf bytes.Buffer
		if err := t.ExecuteTemplate(&buf, o.tmpl, v); err != nil {
			return nil, fmt.Errorf("%s: %w", o.path, err)
		}
		out[o.path] = buf.Bytes()
	}
	return out, nil
}

// Stale lists the generated files under root that are missing or differ.
func Stale(root string, files map[string][]byte) []string {
	var stale []string
	for _, p := range sortedKeys(files) {
		have, err := os.ReadFile(filepath.Join(root, p))
		if err != nil || !bytes.Equal(have, files[p]) {
			stale = append(stale, p)
		}
	}
	return stale
}

// Write writes every generated file under root.
func Write(root string, files map[string][]byte) error {
	for _, rel := range sortedKeys(files) {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(p, files[rel], 0o644); err != nil {
			return err
		}
	}
	return nil
}

func sortedKeys(m map[string][]byte) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
