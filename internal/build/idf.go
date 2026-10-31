// Package build runs ESP-IDF for lcd349: it finds the pinned ESP-IDF,
// captures its environment without a shell, builds a project's emulator
// target into build-emu/, and turns compiler output into diagnostics
// (design §Instances, serial and the lcd349 CLI: Build).
package build

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	lcd349kit "github.com/MaverickHQ/lcd349-kit"
)

// IDF is an ESP-IDF installation and the environment idf.py runs in.
type IDF struct {
	Path    string `json:"path"`    // IDF_PATH
	Version string `json:"version"` // "v5.5.5", from tools/cmake/version.cmake
	Python  string `json:"python"`  // the ESP-IDF Python environment's interpreter
	// Exports are the variables ESP-IDF's environment sets, as captured
	// (PATH keeps a literal $PATH for this process's own); Env is the
	// environment idf.py runs in.
	Exports []string `json:"exports"`
	Env     []string `json:"-"`
	// CertFile is the SSL_CERT_FILE added because Python had no CA
	// certificates (python.org builds on macOS; spike 0.1), or "".
	CertFile string `json:"cert_file,omitempty"`
}

// Candidates lists where ESP-IDF is looked for: $IDF_PATH, then the usual
// install directories for the pinned version.
func Candidates() []string {
	var c []string
	if p := os.Getenv("IDF_PATH"); p != "" {
		c = append(c, p)
	}
	if home, err := os.UserHomeDir(); err == nil {
		c = append(c,
			filepath.Join(home, "esp", lcd349kit.IDFVersion, "esp-idf"),
			filepath.Join(home, "esp", "esp-idf-"+lcd349kit.IDFVersion),
			filepath.Join(home, "esp", "esp-idf"))
	}
	return append(c, "/opt/esp/idf") // the espressif/idf container
}

// Find returns the first ESP-IDF among Candidates, without its environment.
func Find() (*IDF, error) {
	var tried []string
	for _, p := range Candidates() {
		v, err := ReadVersion(p)
		if err != nil {
			tried = append(tried, p)
			continue
		}
		return &IDF{Path: p, Version: v}, nil
	}
	return nil, fmt.Errorf("ESP-IDF isn't installed where lcd349 looks (%s); install %s (lcd349 doctor shows how) or set IDF_PATH",
		strings.Join(tried, ", "), lcd349kit.IDFVersion)
}

var versionRe = regexp.MustCompile(`set\(IDF_VERSION_(MAJOR|MINOR|PATCH) (\d+)\)`)

// ReadVersion reads ESP-IDF's version from tools/cmake/version.cmake.
func ReadVersion(idfPath string) (string, error) {
	b, err := os.ReadFile(filepath.Join(idfPath, "tools", "cmake", "version.cmake"))
	if err != nil {
		return "", err
	}
	parts := map[string]string{}
	for _, m := range versionRe.FindAllSubmatch(b, -1) {
		parts[string(m[1])] = string(m[2])
	}
	if len(parts) != 3 {
		return "", errors.New("unreadable version.cmake")
	}
	return fmt.Sprintf("v%s.%s.%s", parts["MAJOR"], parts["MINOR"], parts["PATCH"]), nil
}

// Environment finds ESP-IDF and its environment, from the cache when it is
// still valid. refresh ignores the cache (lcd349 doctor).
func Environment(ctx context.Context, refresh bool) (*IDF, error) {
	idf, err := Find()
	if err != nil {
		return nil, err
	}
	cache := cachePath(idf.Path)
	if !refresh {
		if c, err := readCache(cache); err == nil && c.Path == idf.Path && c.Version == idf.Version && c.valid() {
			c.Env = c.environ()
			return c, nil
		}
	}
	exports, err := exportEnv(ctx, idf.Path)
	if err != nil {
		return nil, err
	}
	idf.Exports = exports
	if pyenv := lookup(exports, "IDF_PYTHON_ENV_PATH"); pyenv != "" {
		idf.Python = filepath.Join(pyenv, "bin", "python")
	}
	if idf.Python == "" {
		return nil, fmt.Errorf("ESP-IDF at %s has no Python environment; run its install.sh", idf.Path)
	}
	idf.Exports = append(idf.Exports, "IDF_PATH="+idf.Path)
	if cert := certFileNeeded(ctx, idf.Python); cert != "" {
		idf.CertFile = cert
		idf.Exports = append(idf.Exports, "SSL_CERT_FILE="+cert)
	}
	writeCache(cache, idf)
	idf.Env = idf.environ()
	return idf, nil
}

// environ is this process's environment with ESP-IDF's variables applied.
func (c *IDF) environ() []string {
	ex := make([]string, len(c.Exports))
	for i, kv := range c.Exports {
		if strings.HasPrefix(kv, "PATH=") {
			kv = strings.ReplaceAll(kv, "$PATH", os.Getenv("PATH"))
		}
		ex[i] = kv
	}
	return merge(os.Environ(), ex)
}

// exportEnv runs ESP-IDF's own exporter in its key-value form (no shell)
// and returns the variables it sets.
func exportEnv(ctx context.Context, idfPath string) ([]string, error) {
	py, err := hostPython()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, py, filepath.Join(idfPath, "tools", "idf_tools.py"), "export", "--format", "key-value")
	// The exporter leaves out variables the environment already has right, as
	// when export.sh was sourced for this ESP-IDF. Those are taken from the
	// environment below; for another ESP-IDF's, it must print its own.
	env := os.Environ()
	sameIDF := os.Getenv("IDF_PATH") == idfPath
	if !sameIDF {
		env = without(env, selfExported)
	}
	cmd.Env = merge(env, []string{"IDF_PATH=" + idfPath})
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("ESP-IDF's environment export failed (%v): %s; run %s/install.sh",
			err, strings.TrimSpace(errb.String()), idfPath)
	}
	var vars []string
	sc := bufio.NewScanner(&out)
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), "=")
		if !ok || k == "" || k == "IDF_DEACTIVATE_FILE_PATH" {
			continue
		}
		vars = append(vars, k+"="+v)
	}
	if sameIDF {
		for _, k := range selfExported {
			if lookup(vars, k) == "" && os.Getenv(k) != "" {
				vars = append(vars, k+"="+os.Getenv(k))
			}
		}
	}
	return vars, nil
}

// selfExported are the variables idf_tools.py export prints only when the
// environment doesn't already have them.
var selfExported = []string{"IDF_PYTHON_ENV_PATH", "ESP_IDF_VERSION", "OPENOCD_SCRIPTS", "ESP_ROM_ELF_DIR"}

func without(env, keys []string) []string {
	var out []string
	for _, kv := range env {
		k, _, _ := strings.Cut(kv, "=")
		drop := false
		for _, d := range keys {
			drop = drop || k == d
		}
		if !drop {
			out = append(out, kv)
		}
	}
	return out
}

// hostPython finds a Python 3.9 or later to run idf_tools.py, as ESP-IDF's
// export.sh does.
func hostPython() (string, error) {
	for _, name := range []string{"python3", "python"} {
		p, err := exec.LookPath(name)
		if err != nil {
			continue
		}
		if exec.Command(p, "-c", "import sys; sys.exit(sys.version_info < (3, 9))").Run() == nil {
			return p, nil
		}
	}
	return "", errors.New("Python 3.9 or later isn't on PATH; ESP-IDF needs it")
}

// certFileNeeded returns /etc/ssl/cert.pem when Python has no CA
// certificates of its own and SSL_CERT_FILE isn't set, so component
// downloads can verify TLS.
func certFileNeeded(ctx context.Context, python string) string {
	if os.Getenv("SSL_CERT_FILE") != "" || runtime.GOOS != "darwin" {
		return ""
	}
	if n, err := PythonCACount(ctx, python, nil); err == nil && n == 0 {
		if _, err := os.Stat("/etc/ssl/cert.pem"); err == nil {
			return "/etc/ssl/cert.pem"
		}
	}
	return ""
}

// PythonCACount reports how many CA certificates python's default TLS
// context loads.
func PythonCACount(ctx context.Context, python string, env []string) (int, error) {
	cmd := exec.CommandContext(ctx, python, "-c", "import ssl; print(ssl.create_default_context().cert_store_stats()['x509_ca'])")
	if env != nil {
		cmd.Env = env
	}
	out, err := cmd.Output()
	if err != nil {
		return 0, err
	}
	var n int
	_, err = fmt.Sscan(string(out), &n)
	return n, err
}

func lookup(env []string, key string) string {
	for i := len(env) - 1; i >= 0; i-- {
		if k, v, ok := strings.Cut(env[i], "="); ok && k == key {
			return v
		}
	}
	return ""
}

// merge returns base with every variable in over replacing or added.
func merge(base, over []string) []string {
	keys := map[string]bool{}
	for _, kv := range over {
		k, _, _ := strings.Cut(kv, "=")
		keys[k] = true
	}
	var out []string
	for _, kv := range base {
		k, _, _ := strings.Cut(kv, "=")
		if !keys[k] {
			out = append(out, kv)
		}
	}
	return append(out, over...)
}

func cachePath(idfPath string) string {
	base := os.Getenv("XDG_CACHE_HOME")
	if base == "" {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".cache")
	}
	h := sha256.Sum256([]byte(idfPath))
	return filepath.Join(base, "lcd349", "idf-env-"+hex.EncodeToString(h[:6])+".json")
}

func readCache(path string) (*IDF, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var c IDF
	return &c, json.Unmarshal(b, &c)
}

func writeCache(path string, idf *IDF) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return
	}
	b, _ := json.MarshalIndent(idf, "", "  ")
	os.WriteFile(path, b, 0o600)
}

// valid checks that the cached interpreter and tool directories still exist.
func (c *IDF) valid() bool {
	if _, err := os.Stat(c.Python); err != nil {
		return false
	}
	for _, dir := range filepath.SplitList(lookup(c.Exports, "PATH")) {
		if strings.Contains(dir, ".espressif") {
			if _, err := os.Stat(dir); err != nil {
				return false
			}
		}
	}
	return true
}
