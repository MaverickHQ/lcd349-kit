package main

import (
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
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
	git(t, repo, "commit", "-qm", "fixture")
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

// scans records what the fake valvur scanned: each tree, and where it was.
type scans struct {
	trees []map[string]string
	dirs  []string
}

// fakeScanners stands in for gitleaks, which needn't be installed where the
// tests run, and for valvur, which runs through uvx. Neither finds anything:
// the fake valvur writes a run.json whose status is clean, as valvur does,
// and records the trees it scanned.
func fakeScanners(t *testing.T) *scans {
	t.Helper()
	realSecrets, realTree := scanSecrets, scanTree
	scanned := &scans{}
	scanSecrets = func(string) ([]finding, error) { return nil, nil }
	scanTree = func(dir string, _ io.Writer) error {
		scanned.trees = append(scanned.trees, tree(t, dir))
		scanned.dirs = append(scanned.dirs, dir)
		writeFile(t, dir, ".security-scan/run.json", `{"status": "clean", "status_reason": "nothing was found"}`)
		return nil
	}
	t.Cleanup(func() { scanSecrets, scanTree = realSecrets, realTree })
	return scanned
}

// export runs the export and returns its exit code, stdout and stderr.
func export(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := run(args, &stdout, &stderr)
	return code, stdout.String(), stderr.String()
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
		{name: "home-folder path in capitals", value: "/" + "USERS/Someone/src"},
		{name: "Linux home-folder path", value: "/" + "home/someone/src"},
		{name: "Windows home-folder path", value: `C:\` + `Users\someone\src`},
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
	if len(scanned.trees) != 1 || !maps.Equal(scanned.trees[0], want) {
		t.Errorf("valvur scanned %v, want the export %v once", scanned.trees, want)
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

// TestExportRefusesOutHoldingOrInsideTheRepo: the export empties --out, so
// it refuses an --out that is the working copy by another spelling (a case
// variant on a case-insensitive disk), holds it, lies inside it as a plain
// folder or as a repository the working copy doesn't ignore, or is a clone
// of it; each refusal says why, and leaves both as they were.
func TestExportRefusesOutHoldingOrInsideTheRepo(t *testing.T) {
	fakeScanners(t)
	base := t.TempDir()
	// A clone of the public repository whose working tree holds the working copy.
	holder := filepath.Join(base, "holder")
	os.Mkdir(holder, 0o755)
	git(t, holder, "init", "-q")
	writeFile(t, holder, "README.md", "public\n")
	git(t, holder, "add", "-A")
	git(t, holder, "commit", "-qm", "public")
	repo := filepath.Join(holder, "kit")
	os.Mkdir(repo, 0o755)
	git(t, repo, "init", "-q")
	writeFile(t, repo, "README.md", "the kit\n")
	writeFile(t, repo, ".gitignore", "build/\n")
	git(t, repo, "add", "-A")
	git(t, repo, "commit", "-qm", "private")
	writeFile(t, repo, "notes.txt", "untracked, the owner's\n")
	cfg := t.TempDir()
	allowlist := writeFile(t, cfg, "publish.allowlist", "README.md\n")
	denylist := writeFile(t, cfg, "denylist.txt", "a-private-value\n")
	ownClone := filepath.Join(base, "own-clone")
	git(t, base, "clone", "-q", repo, ownClone)
	// Inside the working copy: an ignored plain folder, and a separate
	// repository it doesn't ignore.
	plain := filepath.Join(repo, "build", "plain")
	os.MkdirAll(plain, 0o755)
	tracked := filepath.Join(repo, "public")
	os.MkdirAll(tracked, 0o755)
	git(t, tracked, "init", "-q")

	outs := map[string]struct{ out, why string }{
		"a clone holding the working copy":             {holder, "holds the working copy"},
		"a plain folder inside the working copy":       {plain, "isn't a git repository of its own"},
		"a repository the working copy doesn't ignore": {tracked, "doesn't ignore"},
		"a new folder inside the working copy":         {filepath.Join(repo, "build", "new"), "isn't a git repository of its own"},
		"a clone of the working copy":                  {ownClone, "it's a clone of the working copy"},
	}
	variant := filepath.Join(holder, "KIT")
	if a, err := os.Stat(variant); err == nil {
		if b, _ := os.Stat(repo); os.SameFile(a, b) {
			outs["the working copy in other capitals"] = struct{ out, why string }{variant, "is the working copy"}
		}
	}
	for name, c := range outs {
		t.Run(name, func(t *testing.T) {
			before, beforeOut := tree(t, repo), map[string]string{}
			if _, err := os.Stat(c.out); err == nil {
				beforeOut = tree(t, c.out)
			}
			code, stdout, stderr := export(t, "--repo", repo, "--allowlist", allowlist, "--denylist", denylist, "--out", c.out)
			if code == 0 || !strings.Contains(stderr, c.why) {
				t.Fatalf("exit %d, want a refusal because --out %s\nstdout:\n%s\nstderr:\n%s", code, c.why, stdout, stderr)
			}
			if got := tree(t, repo); !maps.Equal(got, before) {
				t.Errorf("the working copy changed to %v\nwant %v", got, before)
			}
			if _, err := os.Stat(c.out); err == nil {
				if got := tree(t, c.out); !maps.Equal(got, beforeOut) {
					t.Errorf("--out changed to %v\nwant %v", got, beforeOut)
				}
			}
		})
	}
}

// TestExportIntoAnIgnoredCloneInsideTheRepo: the documented flow, a public
// clone kept in the working copy's ignored build/ (--out
// build/_public/lcd349-kit), exports, and again; the working copy is left
// as it was.
func TestExportIntoAnIgnoredCloneInsideTheRepo(t *testing.T) {
	fakeScanners(t)
	repo := fixtureRepo(t, map[string]string{"README.md": "the kit\n", ".gitignore": "build/\n"})
	writeFile(t, repo, "notes.txt", "untracked, the owner's\n")
	public := filepath.Join(repo, "build", "_public", "kit")
	os.MkdirAll(public, 0o755)
	git(t, public, "init", "-q")
	writeFile(t, public, "README.md", "the kit, old\n")
	git(t, public, "add", "-A")
	git(t, public, "commit", "-qm", "v0.1")
	cfg := t.TempDir()
	allowlist := writeFile(t, cfg, "publish.allowlist", "README.md\n")
	denylist := writeFile(t, cfg, "denylist.txt", "a-private-value\n")
	for i := 0; i < 2; i++ {
		code, stdout, stderr := export(t, "--repo", repo, "--allowlist", allowlist, "--denylist", denylist, "--out", public)
		if code != 0 {
			t.Fatalf("export %d: exit %d\nstdout:\n%s\nstderr:\n%s", i+1, code, stdout, stderr)
		}
		if got := tree(t, public); !maps.Equal(got, map[string]string{"README.md": "the kit\n"}) {
			t.Fatalf("export %d: the clone holds %v", i+1, got)
		}
		if b, err := os.ReadFile(filepath.Join(repo, "notes.txt")); err != nil || string(b) != "untracked, the owner's\n" {
			t.Fatalf("export %d: the working copy's untracked file: %q, %v", i+1, b, err)
		}
	}
}

// TestExportGatesOnValvurStatus: valvur exits 0 when it finds problems, so
// the export reads its run.json and goes on only when the status is clean.
// A refusal keeps the report and names it; a scan that wrote no report
// names none, and leaves nothing staged behind.
func TestExportGatesOnValvurStatus(t *testing.T) {
	for _, status := range []string{"findings", "inconclusive", ""} {
		t.Run("status "+status, func(t *testing.T) {
			scanned := fakeScanners(t)
			scanTree = func(dir string, _ io.Writer) error {
				scanned.dirs = append(scanned.dirs, dir)
				if status != "" {
					writeFile(t, dir, ".security-scan/run.json", `{"status": "`+status+`", "status_reason": "the reason"}`)
				}
				return nil
			}
			repo := fixtureRepo(t, map[string]string{"README.md": "the kit\n"})
			cfg := t.TempDir()
			allowlist := writeFile(t, cfg, "publish.allowlist", "README.md\n")
			denylist := writeFile(t, cfg, "denylist.txt", "a-private-value\n")
			out := t.TempDir()
			code, stdout, stderr := export(t, "--repo", repo, "--allowlist", allowlist, "--denylist", denylist, "--out", out)
			if code == 0 {
				t.Fatalf("exit 0, want a refusal\nstdout:\n%s\nstderr:\n%s", stdout, stderr)
			}
			if got := tree(t, out); len(got) != 0 {
				t.Errorf("--out holds %d files after valvur's status %q", len(got), status)
			}
			t.Cleanup(func() { os.RemoveAll(scanned.dirs[0]) })
			report := filepath.Join(scanned.dirs[0], ".security-scan")
			if status != "" {
				if !strings.Contains(stderr, status) || !strings.Contains(stderr, "the reason") || !strings.Contains(stderr, report) {
					t.Errorf("stderr doesn't give the status, its reason and the report:\n%s", stderr)
				}
				if _, err := os.Stat(filepath.Join(report, "run.json")); err != nil {
					t.Errorf("the report is gone: %v", err)
				}
			} else {
				if strings.Contains(stderr, report) {
					t.Errorf("stderr names a report valvur never wrote:\n%s", stderr)
				}
				if _, err := os.Stat(scanned.dirs[0]); !os.IsNotExist(err) {
					t.Errorf("the staged export is still in %s", scanned.dirs[0])
				}
			}
		})
	}
}

// TestExportCleansUpItsStage: a successful export leaves no staged copy of
// the tree behind in the temporary folder.
func TestExportCleansUpItsStage(t *testing.T) {
	scanned := fakeScanners(t)
	repo := fixtureRepo(t, map[string]string{"README.md": "the kit\n"})
	cfg := t.TempDir()
	allowlist := writeFile(t, cfg, "publish.allowlist", "README.md\n")
	denylist := writeFile(t, cfg, "denylist.txt", "a-private-value\n")
	code, stdout, stderr := export(t, "--repo", repo, "--allowlist", allowlist, "--denylist", denylist, "--out", t.TempDir())
	if code != 0 {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	if _, err := os.Stat(scanned.dirs[0]); !os.IsNotExist(err) {
		t.Errorf("the staged export is still in %s", scanned.dirs[0])
	}
}

// TestExportReadsTheCommit: the export is made from a commit's blobs (HEAD,
// or --commit), never the working tree, and prints the commit. Exporting
// HEAD, it refuses an allowlisted file whose working copy differs from the
// commit, and reads nothing through a folder replaced by a symlink.
func TestExportReadsTheCommit(t *testing.T) {
	fakeScanners(t)
	repo := fixtureRepo(t, map[string]string{"README.md": "v1\n", "docs/a.md": "a, v1\n", "private.md": "v1\n"})
	first := strings.TrimSpace(git(t, repo, "rev-parse", "HEAD"))
	writeFile(t, repo, "README.md", "v2\n")
	git(t, repo, "commit", "-qam", "v2")
	second := strings.TrimSpace(git(t, repo, "rev-parse", "HEAD"))
	cfg := t.TempDir()
	allowlist := writeFile(t, cfg, "publish.allowlist", "README.md\ndocs/\n")
	denylist := writeFile(t, cfg, "denylist.txt", "a-private-value\n")
	args := func(out string, more ...string) []string {
		return append([]string{"--repo", repo, "--allowlist", allowlist, "--denylist", denylist, "--out", out}, more...)
	}

	// A private file's changes don't matter; HEAD is exported and named.
	writeFile(t, repo, "private.md", "edited\n")
	out := t.TempDir()
	code, stdout, stderr := export(t, args(out)...)
	if code != 0 {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	if !strings.Contains(stdout, second) {
		t.Errorf("stdout doesn't name the commit %s:\n%s", second, stdout)
	}
	if got := tree(t, out); got["README.md"] != "v2\n" {
		t.Errorf("README.md = %q, want HEAD's", got["README.md"])
	}

	// An allowlisted file edited, or a new one staged, refuses HEAD's export.
	for name, edit := range map[string]func(){
		"edited": func() { writeFile(t, repo, "docs/a.md", "a, edited\n") },
		"staged": func() { writeFile(t, repo, "docs/new.md", "new\n"); git(t, repo, "add", "docs/new.md") },
	} {
		t.Run(name, func(t *testing.T) {
			edit()
			t.Cleanup(func() { git(t, repo, "reset", "-q", "--hard"); os.Remove(filepath.Join(repo, "docs", "new.md")) })
			out := t.TempDir()
			code, stdout, stderr := export(t, args(out)...)
			if code == 0 {
				t.Fatalf("exit 0, want a refusal\nstdout:\n%s\nstderr:\n%s", stdout, stderr)
			}
			if !strings.Contains(stderr, "docs/") || !strings.Contains(stderr, second) {
				t.Errorf("stderr doesn't name the file and the commit:\n%s", stderr)
			}
			if got := tree(t, out); len(got) != 0 {
				t.Errorf("--out holds %d files after a refusal", len(got))
			}
		})
	}

	// An older commit, named, is exported from its blobs, even with docs/
	// replaced by a symlink to other files.
	elsewhere := t.TempDir()
	writeFile(t, elsewhere, "a.md", "planted\n")
	os.RemoveAll(filepath.Join(repo, "docs"))
	if err := os.Symlink(elsewhere, filepath.Join(repo, "docs")); err != nil {
		t.Fatal(err)
	}
	out = t.TempDir()
	code, stdout, stderr = export(t, args(out, "--commit", first[:12])...)
	if code != 0 {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	want := map[string]string{"README.md": "v1\n", "docs/a.md": "a, v1\n"}
	if got := tree(t, out); !maps.Equal(got, want) {
		t.Errorf("--out holds %v\nwant %v", got, want)
	}
	if !strings.Contains(stdout, first) {
		t.Errorf("stdout doesn't name the commit %s:\n%s", first, stdout)
	}
	// HEAD, with docs/ a symlink, is refused.
	if code, stdout, stderr := export(t, args(t.TempDir())...); code == 0 {
		t.Errorf("exit 0 with docs/ a symlink, want a refusal\nstdout:\n%s\nstderr:\n%s", stdout, stderr)
	}
}

// TestExportRefusesSymlinks: a tracked symlink is never exported.
func TestExportRefusesSymlinks(t *testing.T) {
	fakeScanners(t)
	repo := fixtureRepo(t, map[string]string{"docs/a.md": "a\n"})
	if err := os.Symlink("../private.md", filepath.Join(repo, "docs", "link.md")); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "add", "-A")
	git(t, repo, "commit", "-qm", "a link")
	cfg := t.TempDir()
	allowlist := writeFile(t, cfg, "publish.allowlist", "docs/\n")
	denylist := writeFile(t, cfg, "denylist.txt", "a-private-value\n")
	out := t.TempDir()
	code, stdout, stderr := export(t, "--repo", repo, "--allowlist", allowlist, "--denylist", denylist, "--out", out)
	if code == 0 || !strings.Contains(stderr, "docs/link.md") {
		t.Fatalf("exit %d, want a refusal naming docs/link.md\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	if got := tree(t, out); len(got) != 0 {
		t.Errorf("--out holds %d files after a refusal", len(got))
	}
}

// pngWith returns a small PNG with extra chunks inserted after IHDR.
func pngWith(t *testing.T, chunks ...string) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewGray(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	raw := b.Bytes()
	ihdrEnd := 8 + 4 + 4 + 13 + 4
	var out bytes.Buffer
	out.Write(raw[:ihdrEnd])
	for _, typ := range chunks {
		data := []byte("Comment\x00a note")
		binary.Write(&out, binary.BigEndian, uint32(len(data)))
		out.WriteString(typ)
		out.Write(data)
		binary.Write(&out, binary.BigEndian, crc32.ChecksumIEEE(append([]byte(typ), data...)))
	}
	out.Write(raw[ihdrEnd:])
	return out.Bytes()
}

// jpegWith returns a small JPEG with a segment (marker, payload) inserted
// after SOI.
func jpegWith(t *testing.T, marker byte, payload string) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := jpeg.Encode(&b, image.NewGray(image.Rect(0, 0, 2, 2)), nil); err != nil {
		t.Fatal(err)
	}
	raw := b.Bytes()
	if marker == 0 {
		return raw
	}
	var out bytes.Buffer
	out.Write(raw[:2])
	out.Write([]byte{0xFF, marker})
	binary.Write(&out, binary.BigEndian, uint16(len(payload)+2))
	out.WriteString(payload)
	out.Write(raw[2:])
	return out.Bytes()
}

// jpegEndingWith returns a small JPEG with a segment (marker, payload)
// inserted after its scan, before its end (EOI).
func jpegEndingWith(t *testing.T, marker byte, payload string) []byte {
	t.Helper()
	raw := jpegWith(t, 0, "")
	var out bytes.Buffer
	out.Write(raw[:len(raw)-2])
	out.Write([]byte{0xFF, marker})
	binary.Write(&out, binary.BigEndian, uint16(len(payload)+2))
	out.WriteString(payload)
	out.Write(raw[len(raw)-2:])
	return out.Bytes()
}

// gifWith returns a small GIF with extension blocks (label, data) inserted
// before its image.
func gifWith(t *testing.T, exts ...[2]string) []byte {
	t.Helper()
	var b bytes.Buffer
	img := image.NewPaletted(image.Rect(0, 0, 2, 2), color.Palette{color.Black, color.White})
	if err := gif.Encode(&b, img, nil); err != nil {
		t.Fatal(err)
	}
	raw := b.Bytes()
	at := 13
	if raw[10]&0x80 != 0 {
		at += 3 << (raw[10]&7 + 1)
	}
	var out bytes.Buffer
	out.Write(raw[:at])
	for _, e := range exts {
		out.Write([]byte{0x21, e[0][0], byte(len(e[1]))})
		out.WriteString(e[1])
		out.WriteByte(0)
	}
	out.Write(raw[at:])
	return out.Bytes()
}

// TestExportRefusesImageMetadata: an image carrying metadata or text (a
// PNG's text, EXIF or time chunks; a JPEG's EXIF, XMP or comment, before
// or after its scan, or data after its end; a GIF's comment or text) is
// refused by name; plain images pass.
func TestExportRefusesImageMetadata(t *testing.T) {
	bad := map[string][]byte{
		"text.png":         pngWith(t, "tEXt"),
		"ztext.png":        pngWith(t, "zTXt"),
		"itext.png":        pngWith(t, "iTXt"),
		"exif.png":         pngWith(t, "eXIf"),
		"time.png":         pngWith(t, "tIME"),
		"exif.jpg":         jpegWith(t, 0xE1, "Exif\x00\x00II*\x00"),
		"xmp.jpg":          jpegWith(t, 0xE1, "http://ns.adobe.com/xap/1.0/\x00<x:xmpmeta/>"),
		"comment.jpg":      jpegWith(t, 0xFE, "a note"),
		"late-comment.jpg": jpegEndingWith(t, 0xFE, "a note"),
		"late-exif.jpg":    jpegEndingWith(t, 0xE1, "Exif\x00\x00II*\x00"),
		"trailing.jpg":     append(jpegWith(t, 0, ""), "a note"...),
		"comment.gif":      gifWith(t, [2]string{"\xFE", "a note"}),
		"text.gif":         gifWith(t, [2]string{"\x01", "a note"}),
		"trailing.gif":     append(gifWith(t), "a note"...),
	}
	good := map[string][]byte{
		"plain.png": pngWith(t, "pHYs"), "plain.jpg": jpegWith(t, 0, ""),
		"plain.gif": gifWith(t, [2]string{"\xF9", "\x04\x00\x00\x00\x00"}),
	}
	for name, body := range bad {
		t.Run(name, func(t *testing.T) {
			fakeScanners(t)
			files := map[string]string{"img/" + name: string(body)}
			for g, b := range good {
				files["img/"+g] = string(b)
			}
			repo := fixtureRepo(t, files)
			cfg := t.TempDir()
			allowlist := writeFile(t, cfg, "publish.allowlist", "img/\n")
			denylist := writeFile(t, cfg, "denylist.txt", "a-private-value\n")
			out := t.TempDir()
			code, stdout, stderr := export(t, "--repo", repo, "--allowlist", allowlist, "--denylist", denylist, "--out", out)
			if code == 0 {
				t.Fatalf("exit 0, want a refusal\nstdout:\n%s\nstderr:\n%s", stdout, stderr)
			}
			if !strings.Contains(stderr, "img/"+name) {
				t.Errorf("stderr doesn't name img/%s:\n%s", name, stderr)
			}
			for g := range good {
				if strings.Contains(stderr, "img/"+g) {
					t.Errorf("stderr refuses the plain image img/%s:\n%s", g, stderr)
				}
			}
		})
	}
}

// TestExportScanIgnoresTheTreesOwnConfig: the exported tree can't configure
// its own scans. gitleaks runs on the export's own configuration, with no
// gitleaks:allow comments or GITLEAKS_CONFIG; and an export holding a
// configuration the export can't override (a .gitleaksignore, which
// gitleaks reads from the tree whatever it's told, or valvur's
// .security-scan.toml) is refused.
func TestExportScanIgnoresTheTreesOwnConfig(t *testing.T) {
	if _, err := exec.LookPath("gitleaks"); err != nil {
		t.Skip("gitleaks isn't on the PATH")
	}
	fakeScanners(t)
	scanSecrets = gitleaks
	secret := "ghp_" + randomToken(t, 36)
	permissive := "title = \"all\"\n[allowlist]\npaths = ['''.*''']\n"
	repo := fixtureRepo(t, map[string]string{
		"README.md":      "the kit\n",
		"docs/notes.txt": "first\nsecond\ntoken = \"" + secret + "\" # gitleaks" + ":allow\n",
		".gitleaks.toml": permissive,
	})
	cfg := t.TempDir()
	t.Setenv("GITLEAKS_CONFIG", writeFile(t, cfg, "permissive.toml", permissive))
	t.Setenv("GITLEAKS_CONFIG_TOML", permissive)
	allowlist := writeFile(t, cfg, "publish.allowlist", "README.md\ndocs/\n.gitleaks.toml\n")
	denylist := writeFile(t, cfg, "denylist.txt", "a-private-value\n")
	out := t.TempDir()
	code, stdout, stderr := export(t, "--repo", repo, "--allowlist", allowlist, "--denylist", denylist, "--out", out)
	if code == 0 || !strings.Contains(stderr, "docs/notes.txt:3: gitleaks finds a secret") {
		t.Fatalf("exit %d, want gitleaks's finding at docs/notes.txt:3\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	if strings.Contains(stdout+stderr, secret) {
		t.Errorf("the output prints the secret")
	}

	for _, name := range []string{".gitleaksignore", "sub/.gitleaksignore", "sub/.security-scan.toml", ".security-scan/run.json"} {
		repo = fixtureRepo(t, map[string]string{"README.md": "the kit\n", name: "docs/notes.txt:github-pat:3\n"})
		allowlist = writeFile(t, cfg, "publish.allowlist", "README.md\n"+name+"\n")
		code, stdout, stderr = export(t, "--repo", repo, "--allowlist", allowlist, "--denylist", denylist, "--out", out)
		if code == 0 || !strings.Contains(stderr, name+": configures a scan of the export") {
			t.Errorf("exit %d, want a refusal naming %s\nstdout:\n%s\nstderr:\n%s", code, name, stdout, stderr)
		}
	}
}

// TestExportNeverPrintsADenylistedPath: a path holding a denylisted value
// is named by its allowlist line, in every message: a warning, a refusal
// and a scanner's finding.
func TestExportNeverPrintsADenylistedPath(t *testing.T) {
	fakeScanners(t)
	value := "owner-" + randomToken(t, 12)
	scanSecrets = func(string) ([]finding, error) {
		return []finding{{file: "docs/" + value + ".md", line: 1, rule: "a-rule"}}, nil
	}
	repo := fixtureRepo(t, map[string]string{"docs/" + value + ".md": "a doc\n", "README.md": "the kit\n"})
	cfg := t.TempDir()
	allowlist := writeFile(t, cfg, "publish.allowlist", "README.md\ndocs/\nnotes/"+value+".txt\n")
	denylist := writeFile(t, cfg, "denylist.txt", value+"\n")
	out := t.TempDir()
	code, stdout, stderr := export(t, "--repo", repo, "--allowlist", allowlist, "--denylist", denylist, "--out", out)
	if code == 0 {
		t.Fatalf("exit 0, want a refusal\nstdout:\n%s\nstderr:\n%s", stdout, stderr)
	}
	if strings.Contains(stdout+stderr, value) {
		t.Errorf("the output prints the value:\n%s%s", stdout, stderr)
	}
	for _, line := range []string{"publish.allowlist:2", "publish.allowlist:3"} {
		if !strings.Contains(stderr, line) {
			t.Errorf("stderr doesn't name %s:\n%s", line, stderr)
		}
	}
}
