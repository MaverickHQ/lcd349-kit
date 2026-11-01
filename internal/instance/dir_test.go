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

// plant makes dir/instance.json as another local user could: a directory
// holding a valid record, reachable from a root.
func plant(t *testing.T, dir, id string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, infoFile), []byte(`{"id":"`+id+`","supervisor_pid":1,"pid":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestReadPathsSkipUnsafeRoots: Find, List and Only read only roots and
// instance directories that are real directories, owned by this user and
// closed to everyone else (KT1): a planted root or instance never reaches
// stop's signals or a tool's socket.
func TestReadPathsSkipUnsafeRoots(t *testing.T) {
	base := shortTemp(t)
	elsewhere := filepath.Join(base, "elsewhere")
	plant(t, filepath.Join(elsewhere, "abc"), "abc")
	linkRoot := filepath.Join(base, "link")
	if err := os.Symlink(elsewhere, linkRoot); err != nil {
		t.Fatal(err)
	}
	open := filepath.Join(base, "open")
	plant(t, filepath.Join(open, "def"), "def")
	os.Chmod(open, 0o777)
	good := filepath.Join(base, "good")
	plant(t, filepath.Join(good, "ghi"), "ghi") // a 0700 root, but ghi is open
	os.Chmod(filepath.Join(good, "ghi"), 0o755)
	if err := os.Symlink(filepath.Join(elsewhere, "abc"), filepath.Join(good, "jkl")); err != nil {
		t.Fatal(err)
	}
	useRoots(t, linkRoot, open, good)

	for _, id := range []string{"abc", "def", "ghi", "jkl"} {
		if dir, err := Find(id); err == nil {
			t.Errorf("Find(%s) = %s; want it skipped", id, dir)
		}
	}
	if all, err := List(); err != nil || len(all) != 0 {
		t.Errorf("List() = %d instances, %v; want none", len(all), err)
	}
	if in, err := Only(); err == nil {
		t.Errorf("Only() = %s; want none", in.Dir)
	}
}

// TestReadRefusesALinkedOrHugeRecord: instance.json is opened without
// following a symlink, and refused past a size cap.
func TestReadRefusesALinkedOrHugeRecord(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(t.TempDir(), "x.json")
	os.WriteFile(target, []byte(`{"id":"abc"}`), 0o600)
	if err := os.Symlink(target, filepath.Join(dir, infoFile)); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(dir); err == nil {
		t.Error("Read followed a symlinked instance.json")
	}
	dir = t.TempDir()
	huge := `{"id":"abc","error":"` + strings.Repeat("x", maxInfoSize) + `"}`
	os.WriteFile(filepath.Join(dir, infoFile), []byte(huge), 0o600)
	if _, err := Read(dir); err == nil {
		t.Error("Read accepted an instance.json over the size cap")
	}
}

// TestFindAndStopStayInsideTheRoots: a path names an instance only when it
// is a direct child of a verified root, and Stop never removes anything
// else, whatever instance.json it holds (an MCP stop on "./" removed the
// workspace).
func TestFindAndStopStayInsideTheRoots(t *testing.T) {
	base := shortTemp(t)
	root := filepath.Join(base, "r")
	useRoots(t, root)
	dir, err := Create("abc")
	if err != nil {
		t.Fatal(err)
	}
	writeInfo(t, "abc")
	victim := filepath.Join(base, "victim")
	plant(t, victim, "victim")
	nested := filepath.Join(dir, "deeper")
	plant(t, nested, "deeper")

	for _, ref := range []string{victim, victim + "/", nested, root, base + "/"} {
		if got, err := Find(ref); err == nil {
			t.Errorf("Find(%s) = %s; want it refused", ref, got)
		}
		if err := Stop(ref); err == nil {
			t.Errorf("Stop(%s) succeeded; want it refused", ref)
		}
	}
	if _, err := os.Stat(filepath.Join(victim, infoFile)); err != nil {
		t.Fatalf("Stop removed a directory outside the roots: %v", err)
	}
	if _, err := os.Stat(filepath.Join(nested, infoFile)); err != nil {
		t.Fatalf("Stop removed a directory nested in an instance: %v", err)
	}

	// The instance itself, by its path (through /tmp's symlink on macOS too).
	for _, ref := range []string{dir, dir + "/"} {
		if got, err := Find(ref); err != nil || got != dir {
			t.Errorf("Find(%s) = %s, %v; want %s", ref, got, err, dir)
		}
	}
	if err := Stop(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("Stop left %s: %v", dir, err)
	}
}

// TestRootsThatAreOneDirectory: two roots naming one directory ($TMPDIR
// being /private/tmp on a Mac, say) list each instance once, so Only finds
// the one running instance.
func TestRootsThatAreOneDirectory(t *testing.T) {
	base := shortTemp(t)
	os.Mkdir(filepath.Join(base, "real"), 0o700)
	if err := os.Symlink(filepath.Join(base, "real"), filepath.Join(base, "link")); err != nil {
		t.Fatal(err)
	}
	useRoots(t, filepath.Join(base, "real", "r"), filepath.Join(base, "link", "r"))
	dir, err := Create("abc")
	if err != nil {
		t.Fatal(err)
	}
	writeInfo(t, "abc")
	f, err := lock(dir) // as its supervisor does: the instance is alive
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	all, err := List()
	if err != nil || len(all) != 1 {
		t.Fatalf("List() = %d instances, %v; want 1", len(all), err)
	}
	if in, err := Only(); err != nil || in.Dir != dir {
		t.Fatalf("Only() = %v, %v; want %s", in, err, dir)
	}
}
