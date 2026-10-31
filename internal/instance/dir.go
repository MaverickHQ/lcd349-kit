// Package instance runs emulator instances: their directories, the
// supervisor that starts the product and restarts it on resets, the serial
// pseudo-terminal relay, and stopping and listing (design §Instances, serial
// and the lcd349 CLI).
package instance

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"
)

// MaxID is the longest instance id. Ids are short because socket paths are
// (104 bytes on macOS).
const MaxID = 12

// maxSocketPath leaves room under macOS's 104-byte sun_path.
const maxSocketPath = 100

// testRoots replaces Roots in tests.
var testRoots []string

var idRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,11}$`)

// ValidID reports whether id can name an instance.
func ValidID(id string) bool { return idRe.MatchString(id) }

// Roots lists the directories holding instance directories, preferred first:
// $TMPDIR/lcd349-$UID, then /tmp/lcd349-$UID for when $TMPDIR is too long
// for a socket path. LCD349_INSTANCES replaces both (tests and CI).
func Roots() []string {
	if testRoots != nil {
		return testRoots
	}
	if r := os.Getenv("LCD349_INSTANCES"); r != "" {
		return []string{filepath.Clean(r)}
	}
	name := fmt.Sprintf("lcd349-%d", os.Getuid())
	r := []string{filepath.Join(os.TempDir(), name)}
	if t := filepath.Join("/tmp", name); t != r[0] {
		r = append(r, t)
	}
	return r
}

// ensureRoot creates root (0700) if needed and checks that it is a real
// directory owned by this user, so another local user can't plant it (KT1).
func ensureRoot(root string) error {
	if err := os.Mkdir(root, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	st, err := os.Lstat(root)
	if err != nil {
		return err
	}
	sys, ok := st.Sys().(*syscall.Stat_t)
	if !st.IsDir() || st.Mode()&os.ModeSymlink != 0 || !ok || int(sys.Uid) != os.Getuid() {
		return fmt.Errorf("%s isn't a directory owned by you; remove it or set LCD349_INSTANCES", root)
	}
	if st.Mode().Perm() != 0o700 {
		return os.Chmod(root, 0o700)
	}
	return nil
}

// Create makes the directory for a new instance called id (mode 0700) under
// the first root that keeps its control socket path short enough.
func Create(id string) (string, error) {
	if !ValidID(id) {
		return "", fmt.Errorf("instance id %q: use 1-%d lower-case letters, digits and dashes", id, MaxID)
	}
	if dir, err := Find(id); err == nil {
		return "", fmt.Errorf("instance %s already exists (%s); stop it first", id, dir)
	}
	for _, root := range Roots() {
		dir := filepath.Join(root, id)
		if len(filepath.Join(dir, "control.sock")) > maxSocketPath {
			continue
		}
		if err := ensureRoot(root); err != nil {
			return "", err
		}
		if err := os.Mkdir(dir, 0o700); err != nil {
			return "", err
		}
		return dir, nil
	}
	return "", fmt.Errorf("no instance root keeps %s/control.sock under %d bytes; set LCD349_INSTANCES to a short directory", id, maxSocketPath)
}

// NewID picks a free id from base (usually the project name): base itself,
// then base-2, base-3 and so on.
func NewID(base string) string {
	b := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			return r
		case r >= 'A' && r <= 'Z':
			return r - 'A' + 'a'
		}
		return '-'
	}, base)
	b = strings.Trim(b, "-")
	if b == "" {
		b = "app"
	}
	if len(b) > MaxID-3 {
		b = strings.TrimRight(b[:MaxID-3], "-")
	}
	for n := 1; ; n++ {
		id := b
		if n > 1 {
			id = fmt.Sprintf("%s-%d", b, n)
		}
		if _, err := Find(id); err != nil {
			return id
		}
	}
}

// ErrNotFound means no instance has that id or directory.
var ErrNotFound = errors.New("no such instance")

// Find returns the directory of the instance ref names: an id, or a path to
// an instance directory.
func Find(ref string) (string, error) {
	if strings.ContainsRune(ref, '/') {
		dir, err := filepath.Abs(ref)
		if err != nil {
			return "", err
		}
		if _, err := os.Stat(filepath.Join(dir, infoFile)); err != nil {
			return "", fmt.Errorf("%s: %w (no %s)", dir, ErrNotFound, infoFile)
		}
		return dir, nil
	}
	if !ValidID(ref) {
		return "", fmt.Errorf("%q: %w", ref, ErrNotFound)
	}
	for _, root := range Roots() {
		dir := filepath.Join(root, ref)
		if _, err := os.Stat(filepath.Join(dir, infoFile)); err == nil {
			return dir, nil
		}
	}
	return "", fmt.Errorf("%s: %w; lcd349 list shows the instances", ref, ErrNotFound)
}

// List returns every instance in the roots, oldest first.
func List() ([]*Info, error) {
	var out []*Info
	for _, root := range Roots() {
		ents, err := os.ReadDir(root)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		for _, e := range ents {
			if !e.IsDir() || !ValidID(e.Name()) {
				continue
			}
			info, err := Read(filepath.Join(root, e.Name()))
			if err != nil {
				continue // being created or removed
			}
			out = append(out, info)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Started.Before(out[j].Started) })
	return out, nil
}

// Only returns the single instance that is running, for commands where the
// instance may be left out.
func Only() (*Info, error) {
	all, err := List()
	if err != nil {
		return nil, err
	}
	var live []*Info
	for _, in := range all {
		if in.Alive() {
			live = append(live, in)
		}
	}
	switch len(live) {
	case 1:
		return live[0], nil
	case 0:
		return nil, errors.New("no instance is running; start one with lcd349 run")
	}
	ids := make([]string, len(live))
	for i, in := range live {
		ids[i] = in.ID
	}
	return nil, fmt.Errorf("%d instances are running (%s); name one", len(live), strings.Join(ids, ", "))
}
