// Package doctor checks what lcd349 needs and says how to fix what's
// missing (requirement 7.3): ESP-IDF, the linux target's prerequisites,
// Python TLS, SDL for the viewer, and a project's kit pin and lock files.
package doctor

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	lcd349kit "github.com/MaverickHQ/lcd349-kit"
	"github.com/MaverickHQ/lcd349-kit/internal/build"
	"github.com/MaverickHQ/lcd349-kit/internal/instance"
	"github.com/MaverickHQ/lcd349-kit/internal/project"
	"github.com/MaverickHQ/lcd349-kit/internal/viewer"
)

// Status of one check.
const (
	OK   = "ok"
	Warn = "warn"
	Fail = "fail"
	Skip = "skip"
)

// Check is one finding: what was checked, the result, and a fix when it isn't ok.
type Check struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Detail string `json:"detail"`
	Fix    string `json:"fix,omitempty"`
}

// Options say what else to check.
type Options struct {
	Project string // a project directory, or "" for none
	// Viewer finds or builds lcd349-view; nil skips the viewer checks.
	Viewer func() (string, error)
}

// Run performs every check.
func Run(ctx context.Context, o Options) []Check {
	var out []Check
	add := func(c Check) { out = append(out, c) }

	idf, idfCheck := checkIDF(ctx)
	add(idfCheck)
	if idf != nil {
		add(checkTLS(ctx, idf))
	}
	for _, c := range checkHostTools(idf) {
		add(c)
	}
	for _, c := range checkViewer(ctx, o.Viewer) {
		add(c)
	}
	if o.Project != "" {
		for _, c := range checkProject(o.Project) {
			add(c)
		}
	}
	add(checkInstances())
	return out
}

func installFix() string {
	return fmt.Sprintf("git clone -b %s --depth 1 --recursive --shallow-submodules https://github.com/espressif/esp-idf.git ~/esp/%s/esp-idf && ~/esp/%s/esp-idf/install.sh esp32s3",
		lcd349kit.IDFVersion, lcd349kit.IDFVersion, lcd349kit.IDFVersion)
}

func checkIDF(ctx context.Context) (*build.IDF, Check) {
	c := Check{Name: "ESP-IDF"}
	found, err := build.Find()
	if err != nil {
		c.Status, c.Detail, c.Fix = Fail, err.Error(), installFix()
		return nil, c
	}
	idf, err := build.Environment(ctx, true)
	if err != nil {
		c.Status, c.Detail = Fail, err.Error()
		c.Fix = fmt.Sprintf("%s/install.sh esp32s3", found.Path)
		return nil, c
	}
	c.Status, c.Detail = OK, fmt.Sprintf("%s at %s", idf.Version, idf.Path)
	if idf.Version != lcd349kit.IDFVersion {
		c.Status = Warn
		c.Detail += fmt.Sprintf("; the kit is pinned to %s and tested only with it", lcd349kit.IDFVersion)
		c.Fix = installFix() + "; then set IDF_PATH to it"
	}
	return idf, c
}

// checkTLS: component downloads fail when Python can't verify certificates,
// as with python.org's Python on macOS before its certificate step (spike 0.1).
func checkTLS(ctx context.Context, idf *build.IDF) Check {
	c := Check{Name: "Python TLS"}
	n, err := build.PythonCACount(ctx, idf.Python, idf.Env)
	switch {
	case err != nil:
		c.Status, c.Detail = Warn, "couldn't ask ESP-IDF's Python: "+err.Error()
	case n > 0 && idf.CertFile != "":
		c.Status = OK
		c.Detail = fmt.Sprintf("Python has no CA certificates of its own; lcd349 sets SSL_CERT_FILE=%s for its builds", idf.CertFile)
		c.Fix = fmt.Sprintf("for idf.py in your own shell: export SSL_CERT_FILE=%s", idf.CertFile)
	case n > 0:
		c.Status, c.Detail = OK, fmt.Sprintf("%d CA certificates", n)
	default:
		c.Status, c.Detail = Fail, "ESP-IDF's Python loads no CA certificates, so component downloads fail"
		c.Fix = "export SSL_CERT_FILE=/etc/ssl/cert.pem (macOS), or run Python's 'Install Certificates.command'"
	}
	return c
}

func checkHostTools(idf *build.IDF) []Check {
	var env []string
	if idf != nil {
		env = idf.Env
	}
	look := func(name string) (string, bool) {
		if env != nil {
			for _, dir := range filepath.SplitList(lookupEnv(env, "PATH")) {
				p := filepath.Join(dir, name)
				if st, err := os.Stat(p); err == nil && !st.IsDir() {
					return p, true
				}
			}
		}
		p, err := exec.LookPath(name)
		return p, err == nil
	}
	var out []Check
	cc := Check{Name: "host C compiler (linux target)"}
	if p, ok := look("cc"); ok {
		cc.Status, cc.Detail = OK, p
	} else {
		cc.Status, cc.Detail = Fail, "no cc on PATH: the linux target builds with the host compiler"
		cc.Fix = map[string]string{"darwin": "xcode-select --install"}[runtime.GOOS]
		if cc.Fix == "" {
			cc.Fix = "install gcc or clang (apt install build-essential)"
		}
	}
	out = append(out, cc)
	for _, tool := range []string{"cmake", "ninja"} {
		c := Check{Name: tool}
		if p, ok := look(tool); ok {
			c.Status, c.Detail = OK, p
		} else {
			c.Status, c.Detail = Fail, tool+" isn't on PATH or in ESP-IDF's tools"
			c.Fix = map[string]string{"darwin": "brew install " + tool}[runtime.GOOS]
			if c.Fix == "" {
				c.Fix = "apt install " + map[string]string{"cmake": "cmake", "ninja": "ninja-build"}[tool]
			}
		}
		out = append(out, c)
	}
	if runtime.GOOS == "linux" {
		c := Check{Name: "libbsd (linux target)"}
		// Debian and Ubuntu's libbsd-dev (Ubuntu 24.04's included) puts the
		// headers in the multiarch folder, /usr/include/<triplet>/bsd/.
		found, _ := filepath.Glob("/usr/include/*/bsd/string.h")
		if _, err := os.Stat("/usr/include/bsd/string.h"); err == nil {
			found = append([]string{"/usr/include/bsd/string.h"}, found...)
		}
		if len(found) > 0 {
			c.Status, c.Detail = OK, found[0]
		} else {
			c.Status, c.Detail, c.Fix = Fail, "the linux target needs libbsd's headers", "apt install libbsd-dev"
		}
		out = append(out, c)
	}
	return out
}

func checkViewer(ctx context.Context, find func() (string, error)) []Check {
	sdl := Check{Name: "SDL2 (viewer)"}
	switch {
	case exec.Command("pkg-config", "--exists", "sdl2").Run() == nil:
		v, _ := exec.Command("pkg-config", "--modversion", "sdl2").Output()
		sdl.Status, sdl.Detail = OK, "SDL2 "+strings.TrimSpace(string(v))
	default:
		sdl.Status, sdl.Detail = Warn, "SDL2 isn't found (pkg-config sdl2): instances run headless only"
		sdl.Fix = map[string]string{"darwin": "brew install sdl2 pkg-config"}[runtime.GOOS]
		if sdl.Fix == "" {
			sdl.Fix = "apt install libsdl2-dev pkg-config"
		}
	}
	out := []Check{sdl}
	if find == nil {
		return out
	}
	v := Check{Name: "viewer"}
	path, err := find()
	if err != nil {
		v.Status, v.Detail, v.Fix = Warn, err.Error(), "lcd349 run --headless works without it"
		return append(out, v)
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	sc, err := viewer.PrintScale(ctx, path)
	switch {
	case err == nil:
		v.Status = OK
		v.Detail = fmt.Sprintf("%s; physical size: %.3f points per device pixel, from %s (%s)", path, sc.Scale, sc.Source, sc.Detail)
		if sc.Source == "nominal dpi" || sc.Source == "unknown" {
			v.Status = Warn
			v.Fix = "the window may not be at physical size: set LCD349_VIEW_PPI to the display's real pixels per inch"
		}
	case strings.Contains(err.Error(), "no window"):
		v.Status, v.Detail = Warn, path+": no window can open here, so lcd349 run falls back to headless"
		v.Fix = "run from a desktop session (on Linux, with DISPLAY or WAYLAND_DISPLAY set)"
	default:
		v.Status, v.Detail = Warn, fmt.Sprintf("%s --print-scale failed: %v", path, err)
	}
	return append(out, v)
}

func checkProject(dir string) []Check {
	p, err := project.Load(dir)
	if err != nil {
		return []Check{{Name: "project", Status: Fail, Detail: err.Error(), Fix: "-C names an ESP-IDF project directory; lcd349 new makes one"}}
	}
	pin := Check{Name: "kit pin", Status: OK}
	switch {
	case p.Kit == nil:
		pin.Status, pin.Detail = Fail, p.Dir+" doesn't depend on lcd349"
		pin.Fix = "add lcd349 to main/idf_component.yml (see the README's \"Using the SDK\"), or start from lcd349 new"
	case p.Kit.OverridePath != "":
		kitDir := p.Kit.OverridePath
		if !filepath.IsAbs(kitDir) {
			kitDir = filepath.Join(filepath.Dir(p.Manifest), kitDir)
		}
		v := headerVersion(kitDir)
		pin.Detail = fmt.Sprintf("override_path %s (kit %s; this lcd349 is %s)", kitDir, v, lcd349kit.Version)
		if v != lcd349kit.Version {
			pin.Status = Warn
			pin.Fix = "use the lcd349 binary built from that kit checkout, or point override_path at a matching kit"
		}
	default:
		want := "v" + strings.TrimSuffix(lcd349kit.Version, "-dev")
		pin.Detail = fmt.Sprintf("version %q (this lcd349 is %s)", p.Kit.Version, lcd349kit.Version)
		if strings.TrimPrefix(p.Kit.Version, "v") != strings.TrimPrefix(want, "v") {
			pin.Status = Warn
			pin.Fix = fmt.Sprintf("pin lcd349 to %s in %s, or use the lcd349 release that matches the pin", want, p.Manifest)
		}
	}
	locks := Check{Name: "per-target lock files", Status: OK, Detail: "dependencies.lock.${IDF_TARGET}"}
	if !p.PerTargetLocks {
		locks.Status = Warn
		locks.Detail = "one dependencies.lock is shared by the board and emulator builds, and flips its target on every switch"
		locks.Fix = "add this line before project() in " + filepath.Join(p.Dir, "CMakeLists.txt") + ": " + project.LockLine
	}
	return []Check{pin, locks}
}

// headerVersion reads LCD349_VERSION_STR from a component directory.
func headerVersion(dir string) string {
	b, err := os.ReadFile(filepath.Join(dir, "include", "lcd349_core.h"))
	if err != nil {
		return "unknown"
	}
	const key = `#define LCD349_VERSION_STR "`
	i := strings.Index(string(b), key)
	if i < 0 {
		return "unknown"
	}
	rest := string(b[i+len(key):])
	return rest[:strings.IndexByte(rest, '"')]
}

func checkInstances() Check {
	c := Check{Name: "instances", Status: OK}
	all, err := instance.List()
	if err != nil {
		c.Status, c.Detail = Warn, err.Error()
		return c
	}
	var gone []string
	for _, in := range all {
		if !in.Alive() {
			gone = append(gone, in.ID)
		}
	}
	c.Detail = fmt.Sprintf("%d, under %s", len(all), strings.Join(instance.Roots(), " or "))
	if len(gone) > 0 {
		c.Status = Warn
		c.Detail = fmt.Sprintf("%s left behind by a supervisor that died", strings.Join(gone, ", "))
		c.Fix = "lcd349 stop --all"
	}
	return c
}

func lookupEnv(env []string, key string) string {
	for i := len(env) - 1; i >= 0; i-- {
		if k, v, ok := strings.Cut(env[i], "="); ok && k == key {
			return v
		}
	}
	return ""
}
