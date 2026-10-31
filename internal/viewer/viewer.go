// Package viewer finds lcd349-view, building it from the sources embedded in
// the lcd349 binary when no release binary ships beside it, and reads its
// physical scale.
package viewer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"

	lcd349kit "github.com/MaverickHQ/lcd349-kit"
)

// Path finds lcd349-view: $LCD349_VIEW; then beside this binary, as
// lcd349-view or under the name a release ships it as (releaseName); then the
// cache, building it there from the embedded sources the first time (needs
// CMake and SDL2; progress goes to w).
func Path(w io.Writer) (string, error) {
	if p := os.Getenv("LCD349_VIEW"); p != "" {
		if _, err := os.Stat(p); err != nil {
			return "", fmt.Errorf("LCD349_VIEW=%s: %w", p, err)
		}
		return p, nil
	}
	if exe, err := executable(); err == nil {
		for _, name := range []string{"lcd349-view", releaseName()} {
			p := filepath.Join(filepath.Dir(exe), name)
			if st, err := os.Stat(p); err == nil && !st.IsDir() {
				return p, nil
			}
		}
	}
	return buildViewer(w)
}

// releaseName is the viewer's release asset for this version, OS and CPU,
// lcd349-view_<version>_<os>_<arch>, as .github/workflows/release.yml names it.
func releaseName() string {
	return fmt.Sprintf("lcd349-view_%s_%s_%s", lcd349kit.Version, runtime.GOOS, runtime.GOARCH)
}

// executable is os.Executable; tests stand an lcd349 in their own directory.
var executable = os.Executable

// CacheDir is $XDG_CACHE_HOME/lcd349 or ~/.cache/lcd349.
func CacheDir() (string, error) {
	if x := os.Getenv("XDG_CACHE_HOME"); x != "" {
		return filepath.Join(x, "lcd349"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".cache", "lcd349"), nil
}

// viewerSourceHash identifies the embedded viewer sources, so a changed
// viewer is rebuilt even within one version.
func viewerSourceHash() string {
	h := sha256.New()
	fs.WalkDir(lcd349kit.ViewerSource, ".", func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			b, _ := lcd349kit.ViewerSource.ReadFile(p)
			fmt.Fprintf(h, "%s %d\n", p, len(b))
			h.Write(b)
		}
		return nil
	})
	return hex.EncodeToString(h.Sum(nil))[:12]
}

func buildViewer(w io.Writer) (string, error) {
	cache, err := CacheDir()
	if err != nil {
		return "", err
	}
	root := filepath.Join(cache, "viewer", lcd349kit.Version+"-"+viewerSourceHash())
	bin := filepath.Join(root, "build", "lcd349-view")
	if _, err := os.Stat(bin); err == nil {
		return bin, nil
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return "", err
	}
	// One build at a time: two `lcd349 run`s may start together.
	lk, err := os.OpenFile(filepath.Join(root, ".lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return "", err
	}
	defer lk.Close()
	if err := syscall.Flock(int(lk.Fd()), syscall.LOCK_EX); err != nil {
		return "", err
	}
	if _, err := os.Stat(bin); err == nil {
		return bin, nil
	}
	cmake, err := exec.LookPath("cmake")
	if err != nil {
		return "", errors.New("the viewer needs CMake and SDL2 to build (lcd349 doctor shows how to install them); use --headless without them")
	}
	src := filepath.Join(root, "src")
	os.RemoveAll(src)
	sub, _ := fs.Sub(lcd349kit.ViewerSource, "viewer")
	if err := os.CopyFS(src, sub); err != nil {
		return "", err
	}
	fmt.Fprintf(w, "lcd349: building the viewer once, into %s\n", root)
	var log strings.Builder
	for _, args := range [][]string{
		{"-S", src, "-B", filepath.Join(root, "build"), "-DCMAKE_BUILD_TYPE=Release", "-DVIEW_VERSION=" + lcd349kit.Version},
		{"--build", filepath.Join(root, "build"), "--target", "lcd349-view"},
	} {
		cmd := exec.Command(cmake, args...)
		cmd.Stdout, cmd.Stderr = &log, &log
		if err := cmd.Run(); err != nil {
			os.WriteFile(filepath.Join(root, "build.log"), []byte(log.String()), 0o600)
			hint := ""
			if strings.Contains(log.String(), "sdl2") || strings.Contains(log.String(), "SDL2") {
				hint = " SDL2 is missing: lcd349 doctor shows how to install it."
			}
			return "", fmt.Errorf("building the viewer failed (%v); the log is %s.%s Use --headless meanwhile",
				err, filepath.Join(root, "build.log"), hint)
		}
	}
	return bin, nil
}

// Scale is the viewer's physical scale: window points per device pixel, and
// window pixels per point, as `lcd349-view --print-scale` reports them.
type Scale struct {
	Scale          float64 `json:"scale"`
	Source         string  `json:"source"`
	Detail         string  `json:"detail"`
	PixelsPerPoint float64 `json:"pixels_per_point"`
}

// PrintScale runs `path --print-scale`. Exit code 3 means no display.
func PrintScale(ctx context.Context, path string) (*Scale, error) {
	cmd := exec.CommandContext(ctx, path, "--print-scale")
	b, err := cmd.Output()
	if err != nil {
		if cmd.ProcessState != nil && cmd.ProcessState.ExitCode() == 3 {
			return nil, errors.New("no window can open here")
		}
		return nil, err
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	var s Scale
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &s); err != nil {
		return nil, fmt.Errorf("unreadable --print-scale output: %w", err)
	}
	return &s, nil
}
