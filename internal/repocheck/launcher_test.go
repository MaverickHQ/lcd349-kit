package repocheck

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"

	lcd349kit "github.com/MaverickHQ/lcd349-kit"
)

const pluginDir = "../../plugin"

// TestPluginVersionsAgree: the plugin's version, the launcher's and the
// binary's are one (requirements 9.2, 9.3).
func TestPluginVersionsAgree(t *testing.T) {
	var manifest struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	}
	b, err := os.ReadFile(filepath.Join(pluginDir, ".claude-plugin", "plugin.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &manifest); err != nil {
		t.Fatal(err)
	}
	launcher, _ := os.ReadFile(filepath.Join(pluginDir, "scripts", "lcd349-launch"))
	m := regexp.MustCompile(`(?m)^VERSION="([^"]+)"`).FindSubmatch(launcher)
	if manifest.Name != "lcd349" || manifest.Version != lcd349kit.Version || m == nil || string(m[1]) != lcd349kit.Version {
		t.Fatalf("plugin %q %q, launcher %q, kit %q", manifest.Name, manifest.Version, m, lcd349kit.Version)
	}
	var mcp struct {
		Servers map[string]struct {
			Command string   `json:"command"`
			Args    []string `json:"args"`
		} `json:"mcpServers"`
	}
	b, _ = os.ReadFile(filepath.Join(pluginDir, ".mcp.json"))
	if err := json.Unmarshal(b, &mcp); err != nil {
		t.Fatal(err)
	}
	s := mcp.Servers["lcd349"]
	if s.Command != "${CLAUDE_PLUGIN_ROOT}/scripts/lcd349-launch" || len(s.Args) != 1 || s.Args[0] != "mcp" {
		t.Fatalf(".mcp.json: %+v", mcp)
	}
	var market struct {
		Plugins []struct {
			Name, Source string
		} `json:"plugins"`
	}
	b, _ = os.ReadFile("../../.claude-plugin/marketplace.json")
	if err := json.Unmarshal(b, &market); err != nil {
		t.Fatal(err)
	}
	if len(market.Plugins) != 1 || market.Plugins[0].Name != "lcd349" || market.Plugins[0].Source != "./plugin" {
		t.Fatalf("marketplace: %+v", market)
	}
}

// launcher is a copy of the plugin's launcher with its own checksums.txt.
type launcher struct {
	t      *testing.T
	script string
	data   string // CLAUDE_PLUGIN_DATA
	path   string // PATH: only system tools, plus fake binaries
	asset  string
}

func newLauncher(t *testing.T, sums map[string][]byte) *launcher {
	t.Helper()
	dir := t.TempDir()
	src, err := os.ReadFile(filepath.Join(pluginDir, "scripts", "lcd349-launch"))
	if err != nil {
		t.Fatal(err)
	}
	l := &launcher{t: t, script: filepath.Join(dir, "scripts", "lcd349-launch"), data: filepath.Join(dir, "data")}
	os.MkdirAll(filepath.Dir(l.script), 0o755)
	os.WriteFile(l.script, src, 0o755)
	arch := map[string]string{"amd64": "amd64", "arm64": "arm64"}[runtime.GOARCH]
	l.asset = fmt.Sprintf("lcd349_%s_%s_%s", lcd349kit.Version, runtime.GOOS, arch)
	var b strings.Builder
	b.WriteString("# test checksums\n")
	for name, content := range sums {
		sum := sha256.Sum256(content)
		fmt.Fprintf(&b, "%s  %s\n", hex.EncodeToString(sum[:]), strings.ReplaceAll(name, "ASSET", l.asset))
	}
	os.WriteFile(filepath.Join(dir, "scripts", "checksums.txt"), []byte(b.String()), 0o644)
	l.path = filepath.Join(dir, "fakebin") + ":/usr/bin:/bin"
	os.MkdirAll(filepath.Join(dir, "fakebin"), 0o755)
	return l
}

// fakePathBinary puts an `lcd349` on PATH that reports version.
func (l *launcher) fakePathBinary(version string) {
	dir := strings.SplitN(l.path, ":", 2)[0]
	script := fmt.Sprintf("#!/bin/sh\nif [ \"$1\" = version ]; then echo \"lcd349 %s\"; exit 0; fi\necho \"from PATH: $*\"\n", version)
	os.WriteFile(filepath.Join(dir, "lcd349"), []byte(script), 0o755)
}

func (l *launcher) run(base string, env ...string) (string, string, int) {
	cmd := exec.Command(l.script, "mcp", "--x")
	cmd.Env = append([]string{"PATH=" + l.path, "HOME=" + l.t.TempDir()}, env...)
	if base != "" {
		cmd.Env = append(cmd.Env, "LCD349_RELEASE_BASE="+base)
	}
	var o, e bytes.Buffer
	cmd.Stdout, cmd.Stderr = &o, &e
	err := cmd.Run()
	code := 0
	if err != nil {
		code = cmd.ProcessState.ExitCode()
	}
	return o.String(), e.String(), code
}

// release serves one asset and counts downloads.
func release(t *testing.T, name string, content []byte) (*httptest.Server, *atomic.Int32) {
	var hits atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.TrimPrefix(r.URL.Path, "/") != name {
			http.NotFound(w, r)
			return
		}
		hits.Add(1)
		w.Write(content)
	}))
	t.Cleanup(s.Close)
	return s, &hits
}

var good = []byte("#!/bin/sh\necho \"downloaded: $*\"\n")

func TestLauncherUsesPathBinaryOfItsVersion(t *testing.T) {
	l := newLauncher(t, nil)
	l.fakePathBinary(lcd349kit.Version)
	out, errOut, code := l.run("")
	if code != 0 || out != "from PATH: mcp --x\n" {
		t.Fatalf("exit %d, out %q, err %q", code, out, errOut)
	}
}

// TestLauncherDownloadsAndVerifies: a PATH binary of another version is
// skipped; the release binary is downloaded, verified, run, and then reused.
func TestLauncherDownloadsAndVerifies(t *testing.T) {
	l := newLauncher(t, map[string][]byte{"ASSET": good})
	l.fakePathBinary("0.0.1")
	srv, hits := release(t, l.asset, good)
	env := "CLAUDE_PLUGIN_DATA=" + l.data
	for i := 0; i < 2; i++ {
		out, errOut, code := l.run(srv.URL, env)
		if code != 0 || out != "downloaded: mcp --x\n" {
			t.Fatalf("run %d: exit %d, out %q, err %q", i+1, code, out, errOut)
		}
	}
	if hits.Load() != 1 {
		t.Fatalf("%d downloads, want 1: the verified binary should be reused", hits.Load())
	}
	// A cached binary that no longer matches is replaced, not run.
	bin := filepath.Join(l.data, "bin", l.asset)
	os.WriteFile(bin, []byte("#!/bin/sh\necho tampered\n"), 0o755)
	out, _, code := l.run(srv.URL, env)
	if code != 0 || out != "downloaded: mcp --x\n" || hits.Load() != 2 {
		t.Fatalf("after tampering: exit %d, out %q, %d downloads", code, out, hits.Load())
	}
}

// TestLauncherRefusesChecksumMismatch (requirement 13.5).
func TestLauncherRefusesChecksumMismatch(t *testing.T) {
	l := newLauncher(t, map[string][]byte{"ASSET": good})
	srv, _ := release(t, l.asset, []byte("#!/bin/sh\necho evil\n"))
	out, errOut, code := l.run(srv.URL, "CLAUDE_PLUGIN_DATA="+l.data)
	if code == 0 || strings.Contains(out, "evil") || !strings.Contains(errOut, "checksum mismatch") || !strings.Contains(errOut, "refusing") {
		t.Fatalf("exit %d, out %q, err %q", code, out, errOut)
	}
	ents, _ := os.ReadDir(filepath.Join(l.data, "bin"))
	if len(ents) != 0 {
		t.Fatalf("files left after a refused download: %v", ents)
	}
}

func TestLauncherWithoutBinaryExplains(t *testing.T) {
	l := newLauncher(t, nil)
	_, errOut, code := l.run("")
	if code == 0 || !strings.Contains(errOut, "on PATH") {
		t.Fatalf("no PATH binary and no plugin data: exit %d, err %q", code, errOut)
	}
	// A development plugin (no checksums) says how to proceed.
	_, errOut, code = l.run("", "CLAUDE_PLUGIN_DATA="+l.data)
	if code == 0 || !strings.Contains(errOut, "development plugin") {
		t.Fatalf("no checksum: exit %d, err %q", code, errOut)
	}
}
