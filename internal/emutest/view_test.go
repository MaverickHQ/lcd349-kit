package emutest

// Tests for lcd349-view (task 5.1), with SDL's dummy video and audio drivers,
// against examples/inputs. The viewer is built with CMake into viewer/build;
// LCD349_VIEW_BIN names another binary. Without CMake or SDL2 the tests skip,
// unless LCD349_REQUIRE_VIEWER=1 (CI).

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"image/png"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MaverickHQ/lcd349-kit/internal/rpc"
)

const devicePxPerMM = 7.5 // device.json display.px_per_mm

var (
	viewOnce           sync.Once
	viewBin, viewTests string
	viewErr            error
)

func viewer(t *testing.T) (bin, tests string) {
	t.Helper()
	viewOnce.Do(func() {
		if p := os.Getenv("LCD349_VIEW_BIN"); p != "" {
			viewBin, viewTests = p, filepath.Join(filepath.Dir(p), "lcd349-view-tests")
			return
		}
		src, err := filepath.Abs(filepath.Join("..", "..", "viewer"))
		if err != nil {
			viewErr = err
			return
		}
		if _, err := exec.LookPath("cmake"); err != nil {
			viewErr = err
			return
		}
		build := filepath.Join(src, "build")
		for _, args := range [][]string{{"-S", src, "-B", build, "-DVIEW_WERROR=ON"}, {"--build", build}} {
			if out, err := exec.Command("cmake", args...).CombinedOutput(); err != nil {
				viewErr = fmt.Errorf("cmake %s: %v\n%s", strings.Join(args, " "), err, out)
				return
			}
		}
		viewBin, viewTests = filepath.Join(build, "lcd349-view"), filepath.Join(build, "lcd349-view-tests")
	})
	if viewErr != nil {
		if os.Getenv("LCD349_REQUIRE_VIEWER") == "1" {
			t.Fatal(viewErr)
		}
		t.Skipf("no viewer build: %v", viewErr)
	}
	return viewBin, viewTests
}

// viewCmd runs the viewer headless: SDL's dummy drivers, and no calibration
// from the developer's environment.
func viewCmd(ctx context.Context, bin string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, bin, args...)
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "LCD349_VIEW_PPI=") && !strings.HasPrefix(kv, "SDL_") {
			cmd.Env = append(cmd.Env, kv)
		}
	}
	cmd.Env = append(cmd.Env, "SDL_VIDEODRIVER=dummy", "SDL_AUDIODRIVER=dummy")
	return cmd
}

// lastJSON decodes the last line of out that is a JSON object.
func lastJSON(t *testing.T, out []byte, v any) {
	t.Helper()
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.HasPrefix(lines[i], "{") {
			if err := json.Unmarshal([]byte(lines[i]), v); err != nil {
				t.Fatalf("%v in %s", err, lines[i])
			}
			return
		}
	}
	t.Fatalf("no JSON line in:\n%s", out)
}

func TestViewerUnitTests(t *testing.T) {
	_, tests := viewer(t)
	out, err := exec.Command(tests).CombinedOutput()
	if err != nil || !strings.Contains(string(out), " 0 failures") {
		t.Fatalf("%v\n%s", err, out)
	}
}

func TestViewerScale(t *testing.T) {
	bin, _ := viewer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for _, c := range []struct {
		args   []string
		env    string
		scale  float64
		source string
	}{
		{nil, "", 1, "none"}, // the dummy driver has no physical size
		{[]string{"--scale", "2.5"}, "", 2.5, "--scale"},
		{nil, "LCD349_VIEW_PPI=127", 127 / 25.4 / devicePxPerMM, "LCD349_VIEW_PPI"},
		// Physical size at 254 ppi needs 1009 points; the dummy display is 1024
		// wide, so the window shrinks to 95 % of it.
		{nil, "LCD349_VIEW_PPI=254", 1024 * 0.95 / (98.5*devicePxPerMM*(254/25.4/devicePxPerMM) + 24) * (254 / 25.4 / devicePxPerMM), "LCD349_VIEW_PPI"},
	} {
		cmd := viewCmd(ctx, bin, append([]string{"--print-scale"}, c.args...)...)
		if c.env != "" {
			cmd.Env = append(cmd.Env, c.env)
		}
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%v %s: %v\n%s", c.args, c.env, err, out)
		}
		var r struct {
			Scale  float64    `json:"scale"`
			Source string     `json:"source"`
			Detail string     `json:"detail"`
			Window [2]float64 `json:"window"`
		}
		lastJSON(t, out, &r)
		if math.Abs(r.Scale-c.scale) > 0.002 || r.Source != c.source {
			t.Errorf("%v %s: scale %.4f from %q; want %.4f from %q", c.args, c.env, r.Scale, r.Source, c.scale, c.source)
		}
		if shrunk := strings.Contains(r.Detail, "shrunk to fit"); shrunk != (c.env == "LCD349_VIEW_PPI=254") {
			t.Errorf("%v %s: detail %q", c.args, c.env, r.Detail)
		}
		if r.Window[0] < 640*r.Scale {
			t.Errorf("%v %s: window %v is narrower than the screen", c.args, c.env, r.Window)
		}
	}
	for _, args := range [][]string{{"--scale", "0"}, {"--scale", "9"}, {"--scale", "x"}, {}, {"--frobnicate"}} {
		err := viewCmd(ctx, bin, args...).Run()
		if ee, ok := err.(*exec.ExitError); !ok || ee.ExitCode() != 2 {
			t.Errorf("%v: %v; want exit code 2", args, err)
		}
	}
}

// TestViewerSelftest drives the example through the viewer's own input path
// (a click on TAP, then the b key), and checks that the window shows the
// instance's frame: rotated back to landscape and dimmed for the backlight.
func TestViewerSelftest(t *testing.T) {
	bin, _ := viewer(t)
	in := start(t, "")
	in.ready()
	bmp := filepath.Join(t.TempDir(), "self.bmp")
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	out, err := viewCmd(ctx, bin, in.dir, "--scale", "1", "--selftest", bmp).CombinedOutput()
	if err != nil {
		t.Fatalf("selftest: %v\n%s\nserial:\n%s", err, out, tail(in.serial(), 20))
	}
	var r struct {
		Selftest       string  `json:"selftest"`
		TonesReceived  int     `json:"tones_received"`
		TonesStarted   int     `json:"tones_started"`
		Audio          bool    `json:"audio"`
		PixelsPerPoint float64 `json:"pixels_per_point"`
		Screen         [4]int  `json:"screen"`
		Backlight      int     `json:"backlight"`
	}
	lastJSON(t, out, &r)
	if r.Selftest != "pass" || r.PixelsPerPoint != 1 || r.Screen[2] != 640 || r.Screen[3] != 172 {
		t.Fatalf("summary %+v", r)
	}
	in.waitLog(`tap count 1`, 1)
	in.waitLog(`gesture SHORT BOOT 1[0-9]{2} ms`, 1)
	if r.TonesReceived < 1 || (r.Audio && r.TonesStarted < 1) {
		t.Fatalf("the tap's tone wasn't played: %+v", r)
	}

	var s rpc.Screenshot
	in.call("screenshot", nil, &s)
	shot, err := png.Decode(bytes.NewReader(s.Data))
	if err != nil {
		t.Fatal(err)
	}
	win := readBMP(t, bmp)
	if c := win.at(0, 0); c != [3]int{0x1C, 0x1D, 0x20} {
		t.Fatalf("window background %v: the BMP reader is wrong", c)
	}
	// Below the status line (it changes with Wi-Fi state), the window must
	// match the screenshot scaled by the backlight.
	k := float64(r.Backlight) / 100
	bad := 0
	for y := 30; y < 172; y++ {
		for x := 0; x < 640; x++ {
			sr, sg, sb, _ := shot.At(x, y).RGBA()
			want := [3]float64{float64(sr>>8) * k, float64(sg>>8) * k, float64(sb>>8) * k}
			got := win.at(r.Screen[0]+x, r.Screen[1]+y)
			for i := 0; i < 3; i++ {
				if math.Abs(float64(got[i])-want[i]) > 3 {
					if bad < 5 {
						t.Errorf("pixel %d,%d: window %v, screenshot %v at %d%%", x, y, got, want, r.Backlight)
					}
					bad++
					break
				}
			}
		}
	}
	if bad > 0 {
		t.Fatalf("%d pixels differ", bad)
	}
}

// TestViewerReattach: a reset starts a new process; the viewer follows it.
func TestViewerReattach(t *testing.T) {
	bin, _ := viewer(t)
	dir := newDir(t)
	in := start(t, dir)
	in.ready()

	ctx, cancel := context.WithCancel(context.Background())
	cmd := viewCmd(ctx, bin, dir, "--scale", "1", "--mute")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cancel(); cmd.Wait() })
	lines := make(chan string, 64)
	go func() {
		sc := bufio.NewScanner(stdout)
		for sc.Scan() {
			lines <- sc.Text()
		}
		close(lines)
	}()
	expect := func(re string) {
		t.Helper()
		rx := regexp.MustCompile(re)
		deadline := time.After(10 * time.Second)
		for {
			select {
			case l, ok := <-lines:
				if !ok {
					t.Fatalf("viewer exited before %q", re)
				}
				if rx.MatchString(l) {
					return
				}
			case <-deadline:
				t.Fatalf("viewer never printed %q", re)
			}
		}
	}
	expect(fmt.Sprintf(`attached to .* \(pid %d\)`, in.cmd.Process.Pid))
	expect(fmt.Sprintf(`connected to pid %d's control socket`, in.cmd.Process.Pid))

	in.call("power", map[string]any{"action": "reset"}, nil)
	if code := in.waitExit(5 * time.Second); code != exitReset {
		t.Fatalf("reset exit code %d", code)
	}
	in = start(t, dir, "LCD349_BOOT_REASON=software")
	in.ready()
	expect(fmt.Sprintf(`attached to .* \(pid %d\)`, in.cmd.Process.Pid))
	expect(fmt.Sprintf(`connected to pid %d's control socket`, in.cmd.Process.Pid))
}

// bmpImage reads the 32-bit BMPs SDL_SaveBMP writes.
type bmpImage struct {
	w, h   int
	top    bool // rows stored top-down
	stride int
	pix    []byte
}

func (b *bmpImage) at(x, y int) [3]int {
	if !b.top {
		y = b.h - 1 - y
	}
	o := y*b.stride + x*4
	return [3]int{int(b.pix[o+2]), int(b.pix[o+1]), int(b.pix[o])} // B, G, R, A in memory
}

func readBMP(t *testing.T, path string) *bmpImage {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) < 54 || string(raw[:2]) != "BM" || binary.LittleEndian.Uint16(raw[28:]) != 32 {
		t.Fatalf("%s isn't a 32-bit BMP", path)
	}
	off := int(binary.LittleEndian.Uint32(raw[10:]))
	w := int(int32(binary.LittleEndian.Uint32(raw[18:])))
	h := int(int32(binary.LittleEndian.Uint32(raw[22:])))
	b := &bmpImage{w: w, h: h, stride: w * 4}
	if h < 0 {
		b.h, b.top = -h, true
	}
	if off+b.stride*b.h > len(raw) {
		t.Fatalf("%s is truncated", path)
	}
	b.pix = raw[off:]
	return b
}
