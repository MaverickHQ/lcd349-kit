package repocheck

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const includeDir = "../../components/lcd349/include"

// C standard library headers (C17).
var cStd = map[string]bool{
	"assert.h": true, "complex.h": true, "ctype.h": true, "errno.h": true, "fenv.h": true,
	"float.h": true, "inttypes.h": true, "iso646.h": true, "limits.h": true, "locale.h": true,
	"math.h": true, "setjmp.h": true, "signal.h": true, "stdalign.h": true, "stdarg.h": true,
	"stdatomic.h": true, "stdbool.h": true, "stddef.h": true, "stdint.h": true, "stdio.h": true,
	"stdlib.h": true, "stdnoreturn.h": true, "string.h": true, "tgmath.h": true, "threads.h": true,
	"time.h": true, "uchar.h": true, "wchar.h": true, "wctype.h": true,
}

var includeRe = regexp.MustCompile(`^\s*#\s*include\s*([<"])([^>"]+)[>"]`)

// TestPublicHeadersIncludeOnlyStdAndLVGL enforces requirement 1.1: public
// headers include only C standard headers, LVGL and each other, so they stay
// framework-neutral and never leak ESP-IDF types.
func TestPublicHeadersIncludeOnlyStdAndLVGL(t *testing.T) {
	bad, err := badIncludes(includeDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range bad {
		t.Errorf("%s; public headers may include only C standard headers, \"lvgl.h\" and other lcd349 headers", b)
	}
}

func TestBadIncludeIsCaught(t *testing.T) {
	dir := t.TempDir()
	h := "#pragma once\n#include <stdint.h>\n#include \"lvgl.h\"\n#include \"esp_err.h\"\n#include <freertos/FreeRTOS.h>\n"
	if err := os.WriteFile(filepath.Join(dir, "lcd349_x.h"), []byte(h), 0o644); err != nil {
		t.Fatal(err)
	}
	bad, err := badIncludes(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(bad) != 2 || !strings.Contains(bad[0], "esp_err.h") || !strings.Contains(bad[1], "freertos/FreeRTOS.h") {
		t.Fatalf("bad = %q", bad)
	}
}

// badIncludes lists "file:line includes X" for every disallowed include.
func badIncludes(dir string) ([]string, error) {
	headers, err := filepath.Glob(filepath.Join(dir, "*.h"))
	if err != nil {
		return nil, err
	}
	if len(headers) == 0 {
		return nil, os.ErrNotExist
	}
	var bad []string
	for _, h := range headers {
		f, err := os.Open(h)
		if err != nil {
			return nil, err
		}
		sc := bufio.NewScanner(f)
		for n := 1; sc.Scan(); n++ {
			m := includeRe.FindStringSubmatch(sc.Text())
			if m == nil {
				continue
			}
			name := m[2]
			ok := (m[1] == "<" && cStd[name]) ||
				(m[1] == `"` && (name == "lvgl.h" || (strings.HasPrefix(name, "lcd349") && strings.HasSuffix(name, ".h") && !strings.Contains(name, "/"))))
			if !ok {
				bad = append(bad, fmt.Sprintf("%s:%d includes %s", filepath.Base(h), n, name))
			}
		}
		f.Close()
	}
	return bad, nil
}

// TestPublicHeadersAreCxxSafe checks every public header that declares
// anything has a #pragma once and an extern "C" block.
func TestPublicHeadersAreCxxSafe(t *testing.T) {
	headers, _ := filepath.Glob(filepath.Join(includeDir, "*.h"))
	for _, h := range headers {
		b, err := os.ReadFile(h)
		if err != nil {
			t.Fatal(err)
		}
		s := string(b)
		if !strings.Contains(s, "#pragma once") {
			t.Errorf("%s: missing #pragma once", filepath.Base(h))
		}
		declares := strings.Contains(s, "typedef") || strings.Contains(s, "(void)") || strings.Contains(s, ");")
		if declares && !strings.Contains(s, `extern "C"`) {
			t.Errorf(`%s: declares API but has no extern "C" block`, filepath.Base(h))
		}
	}
}
