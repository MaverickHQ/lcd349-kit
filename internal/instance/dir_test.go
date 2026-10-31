package instance

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func useRoots(t *testing.T, roots ...string) {
	t.Helper()
	testRoots = roots
	t.Cleanup(func() { testRoots = nil })
}

func shortTemp(t *testing.T) string {
	t.Helper()
	d, err := os.MkdirTemp("/tmp", "l349u-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(d) })
	return d
}

func TestNewID(t *testing.T) {
	useRoots(t, filepath.Join(shortTemp(t), "r"))
	cases := map[string]string{
		"inputs": "inputs", "My App!": "my-app", "": "app", "---": "app",
		"a-very-long-project-name": "a-very-lo",
	}
	for base, want := range cases {
		if got := NewID(base); got != want {
			t.Errorf("NewID(%q) = %q, want %q", base, got, want)
		}
	}
	if _, err := Create("inputs"); err != nil {
		t.Fatal(err)
	}
	writeInfo(t, "inputs")
	if got := NewID("inputs"); got != "inputs-2" {
		t.Errorf("second id = %q, want inputs-2", got)
	}
	if got := NewID("a-very-long-project-name"); len(got) > MaxID || !ValidID(got) {
		t.Errorf("id %q is invalid or longer than %d", got, MaxID)
	}
}

func writeInfo(t *testing.T, id string) {
	t.Helper()
	dir, err := Find(id)
	if err == nil {
		return
	}
	for _, r := range Roots() {
		if _, err := os.Stat(filepath.Join(r, id)); err == nil {
			dir = filepath.Join(r, id)
		}
	}
	if err := (&Info{ID: id, Dir: dir}).write(); err != nil {
		t.Fatal(err)
	}
}

// TestCreateFallsBackForLongSocketPaths: a root that makes control.sock's
// path longer than macOS allows is skipped for the next one.
func TestCreateFallsBackForLongSocketPaths(t *testing.T) {
	base := shortTemp(t)
	long := filepath.Join(base, strings.Repeat("x", 90))
	short := filepath.Join(base, "s")
	useRoots(t, long, short)
	dir, err := Create("abc")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(dir) != short {
		t.Fatalf("created %s, want it under %s", dir, short)
	}
	for _, d := range []string{short, dir} {
		st, _ := os.Stat(d)
		if st.Mode().Perm() != 0o700 {
			t.Errorf("%s mode %v, want 0700", d, st.Mode().Perm())
		}
	}
	if _, err := Create("Bad_ID"); err == nil {
		t.Error("Create accepted an invalid id")
	}
}

// TestRootMustBeOwnDirectory: a symlink planted at the root is refused (KT1).
func TestRootMustBeOwnDirectory(t *testing.T) {
	base := shortTemp(t)
	target := filepath.Join(base, "elsewhere")
	os.Mkdir(target, 0o777)
	link := filepath.Join(base, "root")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	useRoots(t, link)
	if _, err := Create("abc"); err == nil || !strings.Contains(err.Error(), "owned by you") {
		t.Fatalf("Create under a symlinked root: %v", err)
	}
}

func TestSerialLogRotatesAndKeepsLastLines(t *testing.T) {
	dir := t.TempDir()
	var rotated []int
	l, err := openSerialLog(dir, nil, func(before int) { rotated = append(rotated, before) })
	if err != nil {
		t.Fatal(err)
	}
	l.limit = 100
	for i := 1; i <= 30; i++ {
		l.Write([]byte(strings.Repeat("x", 9) + "\n")) // 10 bytes a line
	}
	l.Write([]byte("partial"))
	l.close()
	cur, _ := os.ReadFile(filepath.Join(dir, "serial.log"))
	prev, _ := os.ReadFile(filepath.Join(dir, "serial.log.1"))
	if len(cur) > 100 || len(prev) != 100 {
		t.Fatalf("serial.log %d bytes, serial.log.1 %d bytes; want <= 100 and 100", len(cur), len(prev))
	}
	// Rotated before lines 11 and 21 and before the partial line; each
	// rotation drops the old serial.log.1 (10 lines).
	if len(rotated) != 3 || rotated[1] != 10 || rotated[2] != 20 {
		t.Fatalf("rotations reported %v, want [0 10 20]", rotated)
	}
	last := l.last(5)
	if len(last) != 5 || last[4] != strings.Repeat("x", 9) {
		t.Fatalf("last lines %q", last)
	}
	if n := len(l.last(500)); n != 30 {
		t.Fatalf("last(500) gave %d lines, want all 30", n)
	}
}
