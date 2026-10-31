// Package scenario runs *.scenario.yaml files (requirement 7.7, design
// §Scenario format): it starts a headless instance, runs the steps in order,
// and stops at the first failing one, with diff images for screenshots.
package scenario

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/MaverickHQ/lcd349-kit/internal/drive"
)

// Scenario is one parsed file.
type Scenario struct {
	Name  string     `yaml:"name"`
	Run   RunOptions `yaml:"run"`
	Steps []Step     `yaml:"-"`
	File  string     `yaml:"-"`
}

// RunOptions start the instance: a fixed clock, empty storage, a battery
// level and power source make screenshots repeatable.
type RunOptions struct {
	Clock     string `yaml:"clock" json:"clock,omitempty"`
	Wipe      *bool  `yaml:"wipe" json:"wipe,omitempty"` // default true: scenarios start from empty storage
	BatteryMV int    `yaml:"battery_mv" json:"battery_mv,omitempty"`
	USB       *bool  `yaml:"usb" json:"usb,omitempty"`
}

// Step is one step: exactly one field is set.
type Step struct {
	Line  int          `yaml:"-"`
	Kind  string       `yaml:"-"`
	Run   *RunOptions  `yaml:"run"`
	Press *Press       `yaml:"press"`
	Touch *drive.Touch `yaml:"touch"`
	Wait  *Wait        `yaml:"wait"`
	Shot  *Shot        `yaml:"shot"`
	Lint  *Lint        `yaml:"lint"`
	State *State       `yaml:"state"`
	Power string       `yaml:"power"`
}

// Press holds a button (boot, pwr or boot+pwr) for MS milliseconds.
type Press struct {
	Button string `yaml:"button"`
	MS     int    `yaml:"ms"`
}

// Wait waits for a serial line (an extended regular expression), text on
// screen, or the display staying idle; TimeoutMS defaults to 5000.
type Wait struct {
	Log       string `yaml:"log"`
	Text      string `yaml:"text"`
	IdleMS    int    `yaml:"idle_ms"`
	TimeoutMS int    `yaml:"timeout_ms"`
}

// Shot compares a screenshot with a golden image, relative to the scenario
// file. TolerancePx differing pixels are allowed (default 0).
type Shot struct {
	Golden      string `yaml:"golden"`
	TolerancePx int    `yaml:"tolerance_px"`
	Raw         bool   `yaml:"raw"`
	Scale       int    `yaml:"scale"`
}

// Lint fails when the screen has more errors (default 0) or warnings
// (default: any number) than allowed.
type Lint struct {
	MaxErrors   *int `yaml:"max_errors"`
	MaxWarnings *int `yaml:"max_warnings"`
}

// State sets device state (state.set's object) and/or checks it: every
// value in Expect must equal the same path in state.get.
type State struct {
	Set    map[string]any `yaml:"set"`
	Expect map[string]any `yaml:"expect"`
}

var kinds = []string{"run", "press", "touch", "wait", "shot", "lint", "state", "power"}

// Load reads and checks a scenario file. Its path is made absolute, since
// goldens are found relative to it.
func Load(path string) (*Scenario, error) {
	path, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	s, err := Parse(b)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	s.File = path
	return s, nil
}

// Parse checks a scenario strictly: unknown keys are errors, so a typo
// can't silently skip a check.
func Parse(b []byte) (*Scenario, error) {
	var doc struct {
		Name  string      `yaml:"name"`
		Run   RunOptions  `yaml:"run"`
		Steps []yaml.Node `yaml:"steps"`
	}
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	if err := dec.Decode(&doc); err != nil {
		return nil, err
	}
	if len(doc.Steps) == 0 {
		return nil, errors.New("no steps")
	}
	s := &Scenario{Name: doc.Name, Run: doc.Run}
	for i, n := range doc.Steps {
		st, err := parseStep(&n)
		if err != nil {
			return nil, fmt.Errorf("line %d, step %d: %w", n.Line, i+1, err)
		}
		s.Steps = append(s.Steps, st)
	}
	return s, nil
}

func parseStep(n *yaml.Node) (Step, error) {
	st := Step{Line: n.Line}
	if n.Kind != yaml.MappingNode || len(n.Content) != 2 {
		return st, fmt.Errorf("a step is one of %s, as `- kind: {…}`", strings.Join(kinds, ", "))
	}
	key, val := n.Content[0].Value, n.Content[1]
	st.Kind = key
	strict := func(out any) error {
		var b bytes.Buffer
		enc := yaml.NewEncoder(&b)
		if err := enc.Encode(val); err != nil {
			return err
		}
		dec := yaml.NewDecoder(&b)
		dec.KnownFields(true)
		return dec.Decode(out)
	}
	var err error
	switch key {
	case "run":
		st.Run = &RunOptions{}
		err = strict(st.Run)
	case "press":
		st.Press = &Press{}
		if val.Kind == yaml.ScalarNode {
			st.Press.Button = val.Value
		} else {
			err = strict(st.Press)
		}
		if err == nil {
			_, err = drive.PressParams(st.Press.Button, st.Press.MS)
		}
	case "touch":
		st.Touch = &drive.Touch{}
		if err = strict(st.Touch); err == nil {
			err = st.Touch.Validate()
		}
	case "wait":
		st.Wait = &Wait{}
		if err = strict(st.Wait); err == nil && st.Wait.Log == "" && st.Wait.Text == "" && st.Wait.IdleMS == 0 {
			err = errors.New("wait needs log, text or idle_ms")
		}
	case "shot":
		st.Shot = &Shot{}
		if val.Kind == yaml.ScalarNode {
			st.Shot.Golden = val.Value
		} else {
			err = strict(st.Shot)
		}
		if err == nil && st.Shot.Golden == "" {
			err = errors.New("shot needs a golden path")
		}
	case "lint":
		st.Lint = &Lint{}
		if val.Kind != yaml.ScalarNode || val.Value != "" {
			err = strict(st.Lint)
		}
	case "state":
		st.State = &State{}
		if err = strict(st.State); err == nil && st.State.Set == nil && st.State.Expect == nil {
			err = errors.New("state needs set or expect")
		}
	case "power":
		st.Power = val.Value
		switch st.Power {
		case "on", "off", "reset", "download":
		default:
			err = fmt.Errorf("power is on, off, reset or download, not %q", st.Power)
		}
	default:
		err = fmt.Errorf("unknown step %q; steps are %s", key, strings.Join(kinds, ", "))
	}
	return st, err
}

// Describe is a short label for a step, for progress and failures.
func (st Step) Describe() string {
	switch {
	case st.Press != nil:
		if st.Press.MS > 0 {
			return fmt.Sprintf("press %s %d ms", st.Press.Button, st.Press.MS)
		}
		return "press " + st.Press.Button
	case st.Touch != nil:
		t := st.Touch
		switch {
		case t.Tap != nil:
			return fmt.Sprintf("touch tap %d,%d", t.Tap[0], t.Tap[1])
		case t.LongPress != nil:
			return fmt.Sprintf("touch long_press %d,%d", t.LongPress[0], t.LongPress[1])
		case t.Swipe != nil:
			return fmt.Sprintf("touch swipe %d,%d to %d,%d", t.Swipe.From[0], t.Swipe.From[1], t.Swipe.To[0], t.Swipe.To[1])
		}
		return fmt.Sprintf("touch %d points", len(t.Points))
	case st.Wait != nil:
		var p []string
		if st.Wait.Log != "" {
			p = append(p, fmt.Sprintf("log %q", st.Wait.Log))
		}
		if st.Wait.Text != "" {
			p = append(p, fmt.Sprintf("text %q", st.Wait.Text))
		}
		if st.Wait.IdleMS > 0 {
			p = append(p, fmt.Sprintf("idle %d ms", st.Wait.IdleMS))
		}
		return "wait " + strings.Join(p, ", ")
	case st.Shot != nil:
		return "shot " + st.Shot.Golden
	case st.Power != "":
		return "power " + st.Power
	}
	return st.Kind
}
