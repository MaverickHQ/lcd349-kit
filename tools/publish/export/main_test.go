package main

import (
	"bytes"
	"crypto/rand"
	"errors"
	"io"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// git runs git in dir, isolated from the Mac's own git configuration, and
// returns its output.
func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.invalid",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.invalid")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

// writeFile writes body to dir/name, making its folders, and returns the path.
func writeFile(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// fixtureRepo makes a working copy in which git tracks files.
func fixtureRepo(t *testing.T, files map[string]string) string {
	t.Helper()
	repo := t.TempDir()
	git(t, repo, "init", "-q")
	for name, body := range files {
		writeFile(t, repo, name, body)
	}
	git(t, repo, "add", "-A")
	return repo
}

// tree reads every file under dir, except a clone's .git, by its slash path.
func tree(t *testing.T, dir string) map[string]string {
	t.Helper()
	got := map[string]string{}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && d.Name() == ".git" {
			return filepath.SkipDir
		}
		if d.IsDir() {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		got[filepath.ToSlash(rel)] = string(b)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// fakeScanners stands in for gitleaks, which needn't be installed where the
// tests run, and for valvur, which runs through uvx. Neither finds anything;
// the fake valvur returns the trees it scanned.
func fakeScanners(t *testing.T) *[]map[string]string {
	t.Helper()
	realSecrets, realTree := scanSecrets, scanTree
	scanned := &[]map[string]string{}
	scanSecrets = func(string) ([]finding, error) { return nil, nil }
	scanTree = func(dir string, _ io.Writer) error {
		*scanned = append(*scanned, tree(t, dir))
		return nil
	}
	t.Cleanup(func() { scanSecrets, scanTree = realSecrets, realTree })
	return scanned
}

// TestExportCopiesOnlyAllowlistedPaths (S5K-4): only the tracked files the
// allowlist names reach --out, a mapped one at its destination. An untracked
// file under a listed folder, and a file that only shares a listed folder's
// prefix, stay behind.
func TestExportCopiesOnlyAllowlistedPaths(t *testing.T) {
	fakeScanners(t)
	repo := fixtureRepo(t, map[string]string{
		"README.md":     "the kit\n",
		"src/a.go":      "package a\n",
		"src/sub/b.go":  "package sub\n",
		"srcfile.go":    "package private\n",
		"docs/guide.md": "a guide\n",
		"docs/notes.md": "private notes\n",
		"RUNLOG.md":     "the runner's log\n",
	})
	writeFile(t, repo, "src/untracked.txt", "build output\n")
	cfg := t.TempDir()
	allowlist := writeFile(t, cfg, "publish.allowlist",
		"# The public paths\nREADME.md\nsrc/\n\ndocs/guide.md -> guide/index.md\n")
	denylist := writeFile(t, cfg, "denylist.txt", "a-private-value\n")
	out := t.TempDir()

	var stdout, stderr bytes.Buffer
	code := run([]string{"--repo", repo, "--allowlist", allowlist, "--denylist", denylist, "--out", out}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, &stdout, &stderr)
	}
	want := map[string]string{
		"README.md":      "the kit\n",
		"src/a.go":       "package a\n",
		"src/sub/b.go":   "package sub\n",
		"guide/index.md": "a guide\n",
	}
	if got := tree(t, out); !maps.Equal(got, want) {
		t.Fatalf("--out holds %v\nwant %v", got, want)
	}
}

// randomToken returns n random letters and digits.
func randomToken(t *testing.T, n int) string {
	t.Helper()
	const chars = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	for i := range b {
		b[i] = chars[int(b[i])%len(chars)]
	}
	return string(b)
}

// TestExportRefusesDenylistedValue (S5K-4): a tracked file holding a
// denylisted value, a home-folder path or a secret gitleaks finds is refused
// with a non-zero exit. The message names the file and the line; neither
// stdout nor stderr holds the value; nothing reaches --out. The planted
// values are built at run time, so this file itself holds none of them and
// passes its own export.
func TestExportRefusesDenylistedValue(t *testing.T) {
	cases := []struct {
		name, value  string
		realGitleaks bool
	}{
		{name: "denylisted value", value: "owner-" + randomToken(t, 12)},
		{name: "home-folder path", value: "/" + "Users/someone/src/kit"},
		{name: "gitleaks finding", value: "ghp_" + randomToken(t, 36), realGitleaks: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fakeScanners(t)
			if c.realGitleaks {
				if _, err := exec.LookPath("gitleaks"); err != nil {
					t.Skip("gitleaks isn't on the PATH")
				}
				scanSecrets = gitleaks
			}
			repo := fixtureRepo(t, map[string]string{
				"README.md":      "the kit\n",
				"docs/notes.txt": "first\nsecond\ntoken = \"" + c.value + "\"\nfourth\n",
			})
			cfg := t.TempDir()
			allowlist := writeFile(t, cfg, "publish.allowlist", "README.md\ndocs/\n")
			deny := "an-unrelated-value\n"
			if c.name == "denylisted value" {
				deny += c.value + "\n"
			}
			denylist := writeFile(t, cfg, "denylist.txt", deny)
			out := t.TempDir()

			var stdout, stderr bytes.Buffer
			code := run([]string{"--repo", repo, "--allowlist", allowlist, "--denylist", denylist, "--out", out}, &stdout, &stderr)
			if code == 0 {
				t.Fatalf("exit 0, want a refusal\nstdout:\n%s\nstderr:\n%s", &stdout, &stderr)
			}
			if !strings.Contains(stderr.String(), "docs/notes.txt:3") {
				t.Errorf("stderr doesn't name docs/notes.txt:3:\n%s", &stderr)
			}
			if strings.Contains(stdout.String()+stderr.String(), c.value) {
				t.Errorf("the output prints the value")
			}
			if got := tree(t, out); len(got) != 0 {
				t.Errorf("--out holds %d files after a refusal", len(got))
			}
		})
	}
}

// TestExportPrintsDiffAndNeverCommits (S5K-4): when --out is a clone of the
// public repository, its working tree becomes the export, a file the export
// no longer holds included; valvur scans the exported tree; the diff against
// the clone's HEAD is printed; and the clone's HEAD and index are unchanged.
func TestExportPrintsDiffAndNeverCommits(t *testing.T) {
	scanned := fakeScanners(t)
	repo := fixtureRepo(t, map[string]string{
		"README.md": "the kit, new\n",
		"src/a.go":  "package a\n",
		"RUNLOG.md": "the runner's log\n",
	})
	cfg := t.TempDir()
	allowlist := writeFile(t, cfg, "publish.allowlist", "README.md\nsrc/\n")
	denylist := writeFile(t, cfg, "denylist.txt", "a-private-value\n")

	public := fixtureRepo(t, map[string]string{
		"README.md": "the kit, old\n",
		"stale.md":  "no longer exported\n",
	})
	git(t, public, "commit", "-qm", "v0.1")
	out := filepath.Join(t.TempDir(), "clone")
	git(t, filepath.Dir(out), "clone", "-q", public, out)
	head := git(t, out, "rev-parse", "HEAD")
	index, err := os.ReadFile(filepath.Join(out, ".git", "index"))
	if err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	code := run([]string{"--repo", repo, "--allowlist", allowlist, "--denylist", denylist, "--out", out}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, &stdout, &stderr)
	}
	want := map[string]string{"README.md": "the kit, new\n", "src/a.go": "package a\n"}
	if got := tree(t, out); !maps.Equal(got, want) {
		t.Errorf("the clone holds %v\nwant %v", got, want)
	}
	if len(*scanned) != 1 || !maps.Equal((*scanned)[0], want) {
		t.Errorf("valvur scanned %v, want the export %v once", *scanned, want)
	}
	for _, line := range []string{"-the kit, old", "+the kit, new", "+package a", "-no longer exported"} {
		if !strings.Contains(stdout.String(), "\n"+line+"\n") {
			t.Errorf("the diff lacks %q\nstdout:\n%s", line, &stdout)
		}
	}
	if got := git(t, out, "rev-parse", "HEAD"); got != head {
		t.Errorf("HEAD moved from %s to %s", head, got)
	}
	if got, err := os.ReadFile(filepath.Join(out, ".git", "index")); err != nil || !bytes.Equal(got, index) {
		t.Errorf("the clone's index changed (%v)", err)
	}
}

// TestExportRefusesUnsafeOut: the export empties --out, so it refuses an --out
// that's the working copy, or a folder that's neither empty nor a git clone,
// and leaves both as they were.
func TestExportRefusesUnsafeOut(t *testing.T) {
	fakeScanners(t)
	repo := fixtureRepo(t, map[string]string{"README.md": "the kit\n"})
	cfg := t.TempDir()
	allowlist := writeFile(t, cfg, "publish.allowlist", "README.md\n")
	denylist := writeFile(t, cfg, "denylist.txt", "a-private-value\n")
	folder := t.TempDir()
	writeFile(t, folder, "keep.txt", "the owner's file\n")

	for name, out := range map[string]string{"the working copy": repo, "a folder": folder} {
		t.Run(name, func(t *testing.T) {
			before := tree(t, out)
			var stdout, stderr bytes.Buffer
			code := run([]string{"--repo", repo, "--allowlist", allowlist, "--denylist", denylist, "--out", out}, &stdout, &stderr)
			if code == 0 {
				t.Fatalf("exit 0, want a refusal\nstdout:\n%s\nstderr:\n%s", &stdout, &stderr)
			}
			if got := tree(t, out); !maps.Equal(got, before) {
				t.Errorf("--out changed to %v\nwant %v", got, before)
			}
		})
	}
}

// TestExportRefusesSecretScannerFinding: a secret the scanner finds in a
// mapped file is refused by its working copy's path and line, with nothing
// written to --out. It covers the refusal where gitleaks isn't installed.
func TestExportRefusesSecretScannerFinding(t *testing.T) {
	fakeScanners(t)
	scanSecrets = func(string) ([]finding, error) {
		return []finding{{file: "guide/index.md", line: 2, rule: "a-rule"}}, nil
	}
	repo := fixtureRepo(t, map[string]string{"docs/guide.md": "a guide\na secret\n"})
	cfg := t.TempDir()
	allowlist := writeFile(t, cfg, "publish.allowlist", "docs/guide.md -> guide/index.md\n")
	denylist := writeFile(t, cfg, "denylist.txt", "a-private-value\n")
	out := t.TempDir()

	var stdout, stderr bytes.Buffer
	code := run([]string{"--repo", repo, "--allowlist", allowlist, "--denylist", denylist, "--out", out}, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("exit 0, want a refusal\nstdout:\n%s\nstderr:\n%s", &stdout, &stderr)
	}
	want := "docs/guide.md (exported as guide/index.md):2: gitleaks finds a secret (rule a-rule)"
	if !strings.Contains(stderr.String(), want) {
		t.Errorf("stderr lacks %q:\n%s", want, &stderr)
	}
	if got := tree(t, out); len(got) != 0 {
		t.Errorf("--out holds %d files after a refusal", len(got))
	}
}

// TestExportStopsWhenValvurFails: a valvur run that fails stops the export
// before anything reaches --out.
func TestExportStopsWhenValvurFails(t *testing.T) {
	fakeScanners(t)
	scanTree = func(string, io.Writer) error { return errors.New("exit status 1") }
	repo := fixtureRepo(t, map[string]string{"README.md": "the kit\n"})
	cfg := t.TempDir()
	allowlist := writeFile(t, cfg, "publish.allowlist", "README.md\n")
	denylist := writeFile(t, cfg, "denylist.txt", "a-private-value\n")
	out := t.TempDir()

	var stdout, stderr bytes.Buffer
	code := run([]string{"--repo", repo, "--allowlist", allowlist, "--denylist", denylist, "--out", out}, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("exit 0, want a failure\nstdout:\n%s\nstderr:\n%s", &stdout, &stderr)
	}
	if got := tree(t, out); len(got) != 0 {
		t.Errorf("--out holds %d files after valvur failed", len(got))
	}
}
