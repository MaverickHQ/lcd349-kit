package rpc

// Typed results for the instance's methods (requirement 5.3). Fields not
// listed here are still present in the JSON.

// Info is the result of "info".
type Info struct {
	KitVersion    string `json:"kit_version"`
	DeviceJSONSHA string `json:"device_json_sha"`
	PID           int    `json:"pid"`
	InstanceDir   string `json:"instance_dir"`
	DownloadMode  bool   `json:"download_mode"`
	BootReason    string `json:"boot_reason"`
	Board         struct {
		Name            string  `json:"name"`
		Width           int     `json:"width"`
		Height          int     `json:"height"`
		PPI             int     `json:"ppi"`
		PxPerMM         float64 `json:"px_per_mm"`
		TouchPeriodMS   int     `json:"touch_period_ms"`
		FlushMS         int     `json:"flush_ms"`
		FlushMSVerified bool    `json:"flush_ms_verified"`
	} `json:"board"`
	NotModelled []struct {
		ID              string `json:"id"`
		What            string `json:"what"`
		CheckOnHardware string `json:"check_on_hardware"`
	} `json:"not_modelled"`
	Methods   []string       `json:"methods"`
	ExitCodes map[string]int `json:"exit_codes"`
}

// State is the result of "state.get" and "state.set".
type State struct {
	Power     string `json:"power"`
	PowerHold bool   `json:"power_hold"`
	Source    string `json:"source"`
	BoardRev  string `json:"board_rev"`
	Display   struct {
		Backlight int  `json:"backlight"`
		Asleep    bool `json:"asleep"`
		Dark      bool `json:"dark"`
		FrameSeq  int  `json:"frame_seq"`
		Frames    int  `json:"frames"`
		IdleMS    int  `json:"idle_ms"`
	} `json:"display"`
	Battery struct {
		MV  int `json:"mv"`
		Pct int `json:"pct"`
	} `json:"battery"`
	Clock struct {
		Unix int64  `json:"unix"`
		ISO  string `json:"iso"`
	} `json:"clock"`
	Buttons struct {
		Boot bool `json:"boot"`
		Pwr  bool `json:"pwr"`
	} `json:"buttons"`
	WiFi struct {
		State    string `json:"state"`
		Reason   string `json:"reason"`
		SSID     string `json:"ssid"`
		IP       string `json:"ip"`
		Networks []Net  `json:"networks"`
	} `json:"wifi"`
	Memory struct {
		Warnings []string `json:"warnings"`
	} `json:"memory"`
	SerialNext int64 `json:"serial_next"`
}

// Net is a simulated Wi-Fi network.
type Net struct {
	SSID     string  `json:"ssid"`
	Password string  `json:"password,omitempty"`
	Band     float64 `json:"band,omitempty"`
	RSSI     int     `json:"rssi,omitempty"`
	Secure   bool    `json:"secure,omitempty"`
}

// Screenshot is the result of "screenshot".
type Screenshot struct {
	Format    string `json:"format"`
	Width     int    `json:"width"`
	Height    int    `json:"height"`
	FrameSeq  int    `json:"frame_seq"`
	Backlight int    `json:"backlight"`
	Power     string `json:"power"`
	Dark      bool   `json:"dark"`
	Data      []byte `json:"data"` // base64 in JSON
}

// Finding is one ui.lint finding.
type Finding struct {
	Rule     string `json:"rule"`
	Severity string `json:"severity"`
	Path     string `json:"path"`
	Class    string `json:"class"`
	Text     string `json:"text"`
	Message  string `json:"message"`
	Fix      string `json:"fix"`
}

// Lint is the result of "ui.lint".
type Lint struct {
	Errors   int       `json:"errors"`
	Warnings int       `json:"warnings"`
	Findings []Finding `json:"findings"`
}

// Node is one object in "ui.tree".
type Node struct {
	Path        string `json:"path"`
	Class       string `json:"class"`
	X           int    `json:"x"`
	Y           int    `json:"y"`
	W           int    `json:"w"`
	H           int    `json:"h"`
	Visible     bool   `json:"visible"`
	Clickable   bool   `json:"clickable"`
	Interactive bool   `json:"interactive"`
	Text        string `json:"text"`
	Font        *struct {
		Name       string `json:"name"`
		LineHeight int    `json:"line_height"`
	} `json:"font"`
	TextColor string   `json:"text_color"`
	BgColor   string   `json:"bg_color"`
	Tags      []string `json:"tags"`
	Children  []Node   `json:"children"`
	TopLayer  *Node    `json:"top_layer"`
}

// Tree is the result of "ui.tree".
type Tree struct {
	Tree      Node `json:"tree"`
	Count     int  `json:"count"`
	Truncated bool `json:"truncated"`
}

// Wait is the result of "wait".
type Wait struct {
	Matched    bool     `json:"matched"`
	By         string   `json:"by"`
	Line       string   `json:"line"`
	LineNumber int64    `json:"line_number"`
	Text       string   `json:"text"`
	ElapsedMS  int      `json:"elapsed_ms"`
	SerialNext int64    `json:"serial_next"`
	Tail       []string `json:"tail"`
	Screenshot string   `json:"screenshot"`
}

// Touch is the result of "input.touch".
type Touch struct {
	Delivered bool   `json:"delivered"`
	Samples   int    `json:"samples"`
	Reason    string `json:"reason"`
}

// Walk calls fn for n and every descendant, depth first.
func (n *Node) Walk(fn func(*Node)) {
	fn(n)
	for i := range n.Children {
		n.Children[i].Walk(fn)
	}
}
