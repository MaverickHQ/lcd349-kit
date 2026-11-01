// Command export makes the public repository's next tree from a commit of
// the working copy (phase 5 requirement 3, herdr-pager ADR 0010). It copies
// only the files of that commit that the allowlist names, from the commit's
// blobs, never from the working tree.
//
//	go run ./tools/publish/export --repo <working copy> --allowlist <file> --denylist <file> --out <public clone> [--commit <commit>]
//
// The commit is HEAD unless --commit names another; the export prints it.
// Exporting HEAD, it refuses an allowlisted file that differs from HEAD in
// the working tree or the index. The allowlist has one path per line,
// relative to the working copy. A path ending in / names every file under
// it; `src -> dest` copies src to dest instead of to the same path; #
// starts a comment line. The denylist, a private file outside every
// repository, has one value per line.
//
// It refuses an export holding a denylisted value, a home-folder path, an
// image carrying metadata or text, a scanner's configuration, or a gitleaks
// finding (gitleaks runs on the export's configuration, never the tree's),
// naming the file and the line but never the value, nor a path holding one;
// and then nothing reaches --out. Otherwise valvur scans the export, and
// unless its status is clean, nothing reaches --out either. Then the export
// replaces everything in --out but a clone's .git, and the diff against the
// clone's HEAD is printed. It never commits or pushes: the owner reviews the
// diff, then commits and pushes it.
package main

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

func run(args []string, stdout, stderr io.Writer) int {
	fl := flag.NewFlagSet("export", flag.ContinueOnError)
	fl.SetOutput(stderr)
	repo := fl.String("repo", "", "the working copy")
	allowlist := fl.String("allowlist", "", "the allowlist (publish.allowlist)")
	denylist := fl.String("denylist", "", "the owner's denylist, outside every repository")
	out := fl.String("out", "", "a clone of the public repository")
	commit := fl.String("commit", "HEAD", "the commit to export")
	if err := fl.Parse(args); err != nil {
		return 2
	}
	if *repo == "" || *allowlist == "" || *denylist == "" || *out == "" || *commit == "" || fl.NArg() > 0 {
		fmt.Fprintln(stderr, "usage: export --repo <working copy> --allowlist <file> --denylist <file> --out <public clone> [--commit <commit>]")
		return 2
	}
	fail := func(err error) int {
		fmt.Fprintln(stderr, "export:", err)
		return 1
	}
	// The denylist comes first, so that no message names a path holding a
	// value on it.
	deny, err := readDenylist(*denylist)
	if err != nil {
		return fail(err)
	}
	rules, err := parseAllowlist(*allowlist)
	if err != nil {
		return fail(err)
	}
	sha, head, err := resolveCommit(*repo, *commit)
	if err != nil {
		return fail(err)
	}
	fmt.Fprintf(stdout, "exporting commit %s\n", sha)
	clone, err := checkOut(*repo, *out, sha)
	if err != nil {
		return fail(err)
	}
	if sha == head {
		dirty, err := dirtyFiles(*repo, sha, rules, deny)
		if err != nil {
			return fail(err)
		}
		if len(dirty) > 0 {
			for _, d := range dirty {
				fmt.Fprintln(stderr, "export:", d)
			}
			fmt.Fprintf(stderr, "export: refused: commit or stash those changes, or name a --commit; nothing was written to %s\n", *out)
			return 1
		}
	}
	files, err := collect(*repo, sha, rules, deny, stderr)
	if err != nil {
		return fail(err)
	}

	// The export is staged and scanned before anything reaches --out. The
	// stage goes when the export ends, unless it holds a report of valvur's
	// that refused it.
	stage, err := os.MkdirTemp("", "lcd349-export-")
	if err != nil {
		return fail(err)
	}
	keep := false
	defer func() {
		if !keep {
			os.RemoveAll(stage)
		}
	}()
	report := filepath.Join(stage, ".security-scan")
	if err := write(stage, files); err != nil {
		return fail(err)
	}
	problems, err := refusals(stage, files, deny)
	if err != nil {
		return fail(err)
	}
	if len(problems) > 0 {
		for _, p := range problems {
			fmt.Fprintln(stderr, "export:", p)
		}
		fmt.Fprintf(stderr, "export: refused (%d problems); nothing was written to %s\n", len(problems), *out)
		return 1
	}
	scanErr := scanTree(stage, stderr)
	if _, err := os.Stat(report); err != nil {
		if scanErr == nil {
			scanErr = errors.New("it wrote no report")
		}
		fmt.Fprintf(stderr, "export: valvur: %v; nothing was written to %s\n", scanErr, *out)
		return 1
	}
	status, reason, err := valvurStatus(report)
	if scanErr == nil && err == nil && status != "clean" {
		scanErr = fmt.Errorf("its status is %q, not clean: %s", status, reason)
	}
	if scanErr == nil {
		scanErr = err
	}
	if scanErr != nil {
		keep = true
		fmt.Fprintf(stderr, "export: valvur: %v; its report is in %s; nothing was written to %s\n", scanErr, report, *out)
		return 1
	}
	fmt.Fprintf(stdout, "valvur: clean (%s)\n", reason)

	if err := empty(*out); err != nil {
		return fail(err)
	}
	if err := write(*out, files); err != nil {
		return fail(err)
	}
	fmt.Fprintf(stdout, "exported %d files to %s\n", len(files), *out)
	if !clone {
		fmt.Fprintf(stdout, "%s isn't a git clone, so there's no diff\n", *out)
		return 0
	}
	if err := diff(*out, stdout, stderr); err != nil {
		return fail(err)
	}
	return 0
}

// resolveCommit returns the commit that commit names in repo, and HEAD's.
func resolveCommit(repo, commit string) (sha, head string, err error) {
	rev := func(name string) (string, error) {
		out, err := exec.Command("git", "-C", repo, "rev-parse", "--verify", "--quiet", "--end-of-options", name+"^{commit}").Output()
		if err != nil {
			return "", fmt.Errorf("%s names no commit in %s", name, repo)
		}
		return strings.TrimSpace(string(out)), nil
	}
	if sha, err = rev(commit); err != nil {
		return "", "", err
	}
	if head, err = rev("HEAD"); err != nil {
		return "", "", err
	}
	return sha, head, nil
}

// dirtyFiles names each allowlisted file that differs from commit sha in
// the working tree or the index, so that HEAD's export is what the owner
// sees.
func dirtyFiles(repo, sha string, rules []rule, deny []denied) ([]string, error) {
	out, err := exec.Command("git", "-C", repo, "diff", "--name-only", "-z", "--no-renames", "--no-ext-diff", "--no-textconv", sha, "--").Output()
	if err != nil {
		return nil, fmt.Errorf("comparing the working copy with %s: %v", sha, err)
	}
	var dirty []string
	for _, f := range strings.Split(strings.TrimSuffix(string(out), "\x00"), "\x00") {
		if f == "" {
			continue
		}
		for _, r := range rules {
			if _, ok := r.match(f); ok {
				dirty = append(dirty, fmt.Sprintf("%s: differs from commit %s in the working copy", label(f, r.where, deny), sha))
				break
			}
		}
	}
	return dirty, nil
}

// checkOut says whether --out is a git clone. The export replaces
// everything in --out but a clone's .git, so it refuses an --out that is
// the working copy (by any spelling: files are compared, not names) or
// holds it; a folder that's neither empty nor a clone; and a clone holding
// the exported commit, which is a clone of the working copy rather than of
// the public repository. Inside the working copy, it takes only a separate
// repository the working copy ignores, such as build/_public/lcd349-kit.
func checkOut(repo, out, sha string) (clone bool, err error) {
	r, err := os.Stat(repo)
	if err != nil {
		return false, err
	}
	o, err := physical(out)
	if err != nil {
		return false, err
	}
	for p := o; ; p = filepath.Dir(p) {
		if st, err := os.Stat(p); err == nil && os.SameFile(st, r) {
			if p == o {
				return false, fmt.Errorf("--out is the working copy")
			}
			if err := nestedClone(repo, o); err != nil {
				return false, fmt.Errorf("--out is inside the working copy, and %v", err)
			}
			break
		}
		if filepath.Dir(p) == p {
			break
		}
	}
	if ost, err := os.Stat(out); err == nil {
		pr, err := filepath.EvalSymlinks(repo)
		if err != nil {
			return false, err
		}
		for p := pr; ; p = filepath.Dir(p) {
			if st, err := os.Stat(p); err == nil && os.SameFile(st, ost) {
				return false, fmt.Errorf("--out holds the working copy")
			}
			if filepath.Dir(p) == p {
				break
			}
		}
	}
	if err := os.MkdirAll(out, 0o755); err != nil {
		return false, err
	}
	entries, err := os.ReadDir(out)
	if err != nil {
		return false, err
	}
	clone = slices.ContainsFunc(entries, func(e os.DirEntry) bool { return e.Name() == ".git" })
	if !clone && len(entries) > 0 {
		return false, fmt.Errorf("%s is neither empty nor a git clone", out)
	}
	if clone && exec.Command("git", "-C", out, "cat-file", "-e", sha+"^{commit}").Run() == nil {
		return false, fmt.Errorf("%s holds commit %s: it's a clone of the working copy, not of the public repository", out, sha)
	}
	return clone, nil
}

// nestedClone checks that out, a folder inside the working copy repo, is the
// top of a git repository of its own that the working copy ignores, as a
// public clone in its build/ is.
func nestedClone(repo, out string) error {
	gitPath := func(dir string, args ...string) (os.FileInfo, error) {
		b, err := exec.Command("git", append([]string{"-C", dir, "rev-parse", "--path-format=absolute"}, args...)...).Output()
		if err != nil {
			return nil, err
		}
		return os.Stat(strings.TrimSpace(string(b)))
	}
	ost, err := os.Stat(out)
	if err != nil {
		return errors.New("it isn't a git repository of its own")
	}
	top, err := gitPath(out, "--show-toplevel")
	if err != nil || !os.SameFile(top, ost) {
		return errors.New("it isn't a git repository of its own")
	}
	outGit, err := gitPath(out, "--git-common-dir")
	if err != nil {
		return errors.New("it isn't a git repository of its own")
	}
	if repoGit, err := gitPath(repo, "--git-common-dir"); err != nil || os.SameFile(outGit, repoGit) {
		return errors.New("it isn't a git repository of its own: it shares the working copy's")
	}
	b, err := exec.Command("git", "-C", repo, "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return err
	}
	repoTop, err := filepath.EvalSymlinks(strings.TrimSpace(string(b)))
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(repoTop, out)
	if err != nil || !filepath.IsLocal(rel) {
		return errors.New("it isn't a git repository of its own")
	}
	if exec.Command("git", "-C", repoTop, "check-ignore", "-q", "--", filepath.ToSlash(rel)).Run() != nil {
		return errors.New("the working copy doesn't ignore it: put it in an ignored folder, such as build/")
	}
	return nil
}

// physical returns p with its symlinks evaluated, for a p that may not
// exist yet: its nearest existing folder, evaluated, joined to the rest.
func physical(p string) (string, error) {
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	rest := ""
	for {
		r, err := filepath.EvalSymlinks(abs)
		if err == nil {
			return filepath.Join(r, rest), nil
		}
		if !errors.Is(err, fs.ErrNotExist) || filepath.Dir(abs) == abs {
			return "", err
		}
		rest = filepath.Join(filepath.Base(abs), rest)
		abs = filepath.Dir(abs)
	}
}

// empty removes everything in out but a clone's .git, so that a file the
// export no longer holds goes too.
func empty(out string) error {
	entries, err := os.ReadDir(out)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.Name() != ".git" {
			if err := os.RemoveAll(filepath.Join(out, e.Name())); err != nil {
				return err
			}
		}
	}
	return nil
}

// diff prints the clone's working tree against its HEAD. The tree is staged
// in a throwaway index, so the clone's own index and HEAD never change; the
// export never commits or pushes.
func diff(out string, stdout, stderr io.Writer) error {
	tmp, err := os.MkdirTemp("", "lcd349-index-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	env := append(os.Environ(), "GIT_INDEX_FILE="+filepath.Join(tmp, "index"))
	add := exec.Command("git", "-C", out, "add", "--all", "--force", ".")
	add.Env = env
	if b, err := add.CombinedOutput(); err != nil {
		return fmt.Errorf("git add, into a throwaway index: %v\n%s", err, b)
	}
	d := exec.Command("git", "--no-pager", "-C", out, "diff", "--cached", "--no-color", "--no-ext-diff", "--no-textconv", "--patch-with-stat")
	d.Env, d.Stdout, d.Stderr = env, stdout, stderr
	if err := d.Run(); err != nil {
		return fmt.Errorf("git diff: %v", err)
	}
	return nil
}

// rule is one allowlist line. A src ending in / names a folder, and its dest
// does too.
type rule struct {
	where     string
	src, dest string
}

// match returns where f goes in the public tree, if the rule names it.
func (r rule) match(f string) (string, bool) {
	switch {
	case strings.HasSuffix(r.src, "/") && strings.HasPrefix(f, r.src):
		return r.dest + strings.TrimPrefix(f, r.src), true
	case f == r.src:
		return r.dest, true
	}
	return "", false
}

// file is one file of the export: its path in the working copy, its path in
// the public tree, the allowlist line that names it, how messages name it,
// and its content.
type file struct {
	src, dest, where, label string
	exec                    bool
	body                    []byte
}

// label is how a message names a file: by its path in the working copy, and
// where it's exported to when that differs; or, when either path holds a
// denylisted value, by the allowlist line that names it.
func label(src, where string, deny []denied, dest ...string) string {
	all := src
	for _, d := range dest {
		all += "\n" + d
	}
	if holds(all, deny) >= 0 {
		return "a file " + where + " names, whose path holds a denylisted value"
	}
	if len(dest) > 0 && dest[0] != src {
		return src + " (exported as " + dest[0] + ")"
	}
	return src
}

// holds returns the denylist line of the first value s holds, or -1.
func holds(s string, deny []denied) int {
	lower := strings.ToLower(s)
	for _, d := range deny {
		if strings.Contains(lower, d.value) {
			return d.line
		}
	}
	return -1
}

func parseAllowlist(name string) ([]rule, error) {
	b, err := os.ReadFile(name)
	if err != nil {
		return nil, err
	}
	var rules []rule
	for i, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		src, dest, mapped := strings.Cut(line, "->")
		src, dest = strings.TrimSpace(src), strings.TrimSpace(dest)
		if !mapped {
			dest = src
		}
		where := fmt.Sprintf("%s:%d", name, i+1)
		if !clean(src) || !clean(dest) || strings.HasSuffix(src, "/") != strings.HasSuffix(dest, "/") ||
			strings.SplitN(dest, "/", 2)[0] == ".git" {
			return nil, fmt.Errorf("%s: want a relative path, a folder ending in /, or src -> dest of the same kind", where)
		}
		rules = append(rules, rule{where, src, dest})
	}
	return rules, nil
}

// clean says whether p is a relative slash path in its shortest form, such
// as a/b or a/b/, and not the working copy itself.
func clean(p string) bool {
	s := strings.TrimSuffix(p, "/")
	return s != "" && s != "." && path.Clean(s) == s && filepath.IsLocal(s)
}

// collect reads the files of commit sha that the rules name, from its
// blobs. A rule that names no file is only a warning: the file may come
// with a later commit. A symlink or a submodule is refused.
func collect(repo, sha string, rules []rule, deny []denied, stderr io.Writer) ([]file, error) {
	ls, err := exec.Command("git", "-C", repo, "ls-tree", "-r", "-z", "--full-tree", sha).Output()
	if err != nil {
		return nil, fmt.Errorf("listing the files of %s in %s: %v", sha, repo, err)
	}
	type entry struct{ mode, oid string }
	tree := map[string]entry{}
	var paths []string
	for _, rec := range strings.Split(strings.TrimSuffix(string(ls), "\x00"), "\x00") {
		meta, p, ok := strings.Cut(rec, "\t")
		fields := strings.Fields(meta)
		if !ok || len(fields) != 3 {
			continue
		}
		tree[p] = entry{fields[0], fields[2]}
		paths = append(paths, p)
	}
	var files []file
	var oids []string
	from := map[string]string{}
	for _, r := range rules {
		n := 0
		for _, f := range paths {
			dest, ok := r.match(f)
			if !ok {
				continue
			}
			n++
			name := label(f, r.where, deny, dest)
			if prev, ok := from[dest]; ok {
				return nil, fmt.Errorf("%s: %s and %s go to the same place", r.where, prev, name)
			}
			from[dest] = name
			e := tree[f]
			if e.mode != "100644" && e.mode != "100755" {
				return nil, fmt.Errorf("%s: not a regular file (git mode %s)", name, e.mode)
			}
			files = append(files, file{src: f, dest: dest, where: r.where, label: name, exec: e.mode == "100755"})
			oids = append(oids, e.oid)
		}
		if n == 0 {
			what := r.src
			if holds(r.src, deny) >= 0 {
				what = "it"
			}
			fmt.Fprintf(stderr, "export: warning: %s: %s names no file in %s\n", r.where, what, sha)
		}
	}
	bodies, err := readBlobs(repo, oids)
	if err != nil {
		return nil, err
	}
	for i := range files {
		files[i].body = bodies[i]
	}
	return files, nil
}

// readBlobs reads the blobs oids name, in order, with one git cat-file.
func readBlobs(repo string, oids []string) ([][]byte, error) {
	if len(oids) == 0 {
		return nil, nil
	}
	cmd := exec.Command("git", "-C", repo, "cat-file", "--batch")
	cmd.Stdin = strings.NewReader(strings.Join(oids, "\n") + "\n")
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("reading the blobs: %v", err)
	}
	r := bufio.NewReader(bytes.NewReader(out))
	bodies := make([][]byte, 0, len(oids))
	for _, oid := range oids {
		header, err := r.ReadString('\n')
		fields := strings.Fields(header)
		if err != nil || len(fields) != 3 || fields[0] != oid || fields[1] != "blob" {
			return nil, fmt.Errorf("git cat-file: unexpected %q for %s", strings.TrimSpace(header), oid)
		}
		size, err := strconv.Atoi(fields[2])
		if err != nil {
			return nil, fmt.Errorf("git cat-file: %v", err)
		}
		body := make([]byte, size+1)
		if _, err := io.ReadFull(r, body); err != nil || body[size] != '\n' {
			return nil, fmt.Errorf("git cat-file: a short blob %s", oid)
		}
		bodies = append(bodies, body[:size])
	}
	return bodies, nil
}

// refusals are the reasons not to export: the denylisted values, home-folder
// paths, images carrying metadata and scanner configurations, then
// gitleaks's findings in the staged export.
func refusals(stage string, files []file, deny []denied) ([]string, error) {
	problems := check(files, deny)
	found, err := scanSecrets(stage)
	if err != nil {
		return nil, err
	}
	byDest := map[string]file{}
	for _, f := range files {
		byDest[f.dest] = f
	}
	for _, s := range found {
		name := s.file
		if f, ok := byDest[s.file]; ok {
			name = f.label
		} else if holds(name, deny) >= 0 {
			name = "a file whose path holds a denylisted value"
		}
		problems = append(problems, fmt.Sprintf("%s:%d: gitleaks finds a secret (rule %s)", name, s.line, s.rule))
	}
	return problems, nil
}

// scanTree runs valvur on an exported tree; the tests fake it.
var scanTree = valvur

// valvur scans dir as its Claude Code plugin runs it: through uvx, at a
// pinned version. It writes its report into dir/.security-scan/.
func valvur(dir string, w io.Writer) error {
	cmd := exec.Command("uvx", "--from", "valvur==1.3.1", "valvur", "scan", dir)
	cmd.Stdout, cmd.Stderr = w, w
	return cmd.Run()
}

// valvurStatus reads the status valvur's run.json gives, and its reason.
// valvur exits 0 whatever it finds, so only a clean status lets the export
// through.
func valvurStatus(report string) (status, reason string, err error) {
	b, err := os.ReadFile(filepath.Join(report, "run.json"))
	if err != nil {
		return "", "", fmt.Errorf("its run.json: %v", err)
	}
	var run struct {
		Status string `json:"status"`
		Reason string `json:"status_reason"`
	}
	if err := json.Unmarshal(b, &run); err != nil {
		return "", "", fmt.Errorf("its run.json: %v", err)
	}
	return run.Status, run.Reason, nil
}

// denied is one denylist value, lower-cased, and its line in the denylist.
type denied struct {
	line  int
	value string
}

// readDenylist reads one value per line; blank lines and lines starting with
// # are skipped. Matching ignores case.
func readDenylist(name string) ([]denied, error) {
	b, err := os.ReadFile(name)
	if err != nil {
		return nil, err
	}
	var deny []denied
	for i, line := range strings.Split(string(b), "\n") {
		v := strings.TrimSpace(line)
		if v == "" || strings.HasPrefix(v, "#") {
			continue
		}
		deny = append(deny, denied{i + 1, strings.ToLower(v)})
	}
	if len(deny) == 0 {
		return nil, fmt.Errorf("%s names no values", name)
	}
	return deny, nil
}

// homePath matches a home-folder path, in any case: /Users/<name> on a Mac,
// /home/<name> on Linux, or C:\Users\<name> on Windows. A path that a
// tilde starts, such as ~/bin, is the reader's own and passes.
var homePath = regexp.MustCompile(`(?i)(^|[^a-z0-9._-])(/users/|/home/|\\users\\)[a-z0-9._-]`)

// scanConfigs are the files that would configure a scan of the export and
// that the export can't override: gitleaks reads a .gitleaksignore at the
// tree's root whatever ignore path it's given, and valvur reads
// .security-scan.toml (as it writes .security-scan/). The export refuses
// them anywhere in the tree.
var scanConfigs = []string{".gitleaksignore", ".security-scan.toml"}

// check finds the denylisted values, home-folder paths, images carrying
// metadata and scanner configurations in the export. A problem names the
// file and the line, or for a path the allowlist line, and never the value.
func check(files []file, deny []denied) []string {
	var problems []string
	for _, f := range files {
		if line := holds(f.src+"\n"+f.dest, deny); line >= 0 {
			problems = append(problems, fmt.Sprintf("%s: a path it copies holds the value on denylist line %d", f.where, line))
			continue
		}
		parts := strings.Split(f.dest, "/")
		if slices.Contains(parts, ".security-scan") || slices.Contains(scanConfigs, parts[len(parts)-1]) {
			problems = append(problems, fmt.Sprintf("%s: configures a scan of the export", f.label))
		}
		if why := imageMetadata(f.body); why != "" {
			problems = append(problems, fmt.Sprintf("%s: an image carrying %s; strip it", f.label, why))
		}
		for n, line := range strings.Split(string(f.body), "\n") {
			lower := strings.ToLower(line)
			for _, d := range deny {
				if strings.Contains(lower, d.value) {
					problems = append(problems, fmt.Sprintf("%s:%d: holds the value on denylist line %d", f.label, n+1, d.line))
				}
			}
			if homePath.MatchString(line) {
				problems = append(problems, fmt.Sprintf("%s:%d: holds a home-folder path", f.label, n+1))
			}
		}
	}
	return problems
}

// imageMetadata says what metadata or text an image carries, or "" for
// none: a PNG's text, EXIF or time chunks; a JPEG's EXIF, XMP, IPTC or
// comment, or any APPn after its first scan; a GIF's comment or text
// extensions or XMP; XMP in a WebP; and data after any of their ends.
func imageMetadata(b []byte) string {
	switch {
	case bytes.HasPrefix(b, []byte("\x89PNG\r\n\x1a\n")):
		return pngMetadata(b)
	case bytes.HasPrefix(b, []byte{0xFF, 0xD8}):
		return jpegMetadata(b)
	case bytes.HasPrefix(b, []byte("GIF87a")), bytes.HasPrefix(b, []byte("GIF89a")):
		return gifMetadata(b)
	case bytes.HasPrefix(b, []byte("RIFF")) && len(b) > 12 && string(b[8:12]) == "WEBP":
		if bytes.Contains(b, []byte("<x:xmpmeta")) {
			return "XMP"
		}
	}
	return ""
}

func pngMetadata(b []byte) string {
	for i := 8; ; {
		if i+12 > len(b) {
			return "a damaged PNG chunk"
		}
		n := int(binary.BigEndian.Uint32(b[i:]))
		typ := string(b[i+4 : i+8])
		end := i + 12 + n
		if n < 0 || end > len(b) || end < i {
			return "a damaged PNG chunk"
		}
		switch typ {
		case "tEXt", "zTXt", "iTXt", "eXIf", "tIME":
			return "a PNG " + typ + " chunk"
		case "IEND":
			if end != len(b) {
				return "data after the PNG's end"
			}
			return ""
		}
		i = end
	}
}

// jpegMetadata walks a JPEG's segments, and the entropy-coded data of each
// scan (where 0xFF is followed by 0x00 or a restart marker), to its end.
func jpegMetadata(b []byte) string {
	scanned, inScan := false, false
	for i := 2; i < len(b); {
		if b[i] != 0xFF {
			if inScan {
				i++
				continue
			}
			return "a damaged JPEG segment"
		}
		if i+1 >= len(b) {
			break
		}
		m := b[i+1]
		switch {
		case m == 0xFF:
			i++ // fill
			continue
		case m == 0x00 && inScan, m >= 0xD0 && m <= 0xD7 && inScan:
			i += 2 // a stuffed 0xFF, a restart marker
			continue
		case m == 0x01 || m >= 0xD0 && m <= 0xD7:
			i += 2
			continue
		case m == 0xD9:
			if i+2 != len(b) {
				return "data after the JPEG's end"
			}
			return ""
		}
		if i+4 > len(b) {
			break
		}
		n := int(binary.BigEndian.Uint16(b[i+2:]))
		if n < 2 || i+2+n > len(b) {
			return "a damaged JPEG segment"
		}
		seg := b[i+4 : i+2+n]
		switch {
		case m == 0xE1 && bytes.HasPrefix(seg, []byte("Exif\x00")):
			return "JPEG EXIF"
		case m == 0xE1:
			return "JPEG XMP"
		case m == 0xED:
			return "JPEG IPTC"
		case m == 0xFE:
			return "a JPEG comment"
		case m >= 0xE0 && m <= 0xEF && scanned:
			return "a JPEG APP segment after the image data"
		}
		inScan = m == 0xDA
		scanned = scanned || inScan
		i += 2 + n
	}
	return "a damaged JPEG: no end"
}

// gifMetadata walks a GIF's blocks to its trailer.
func gifMetadata(b []byte) string {
	if len(b) < 13 {
		return "a damaged GIF"
	}
	i := 13
	if b[10]&0x80 != 0 {
		i += 3 << (b[10]&7 + 1)
	}
	// subBlocks returns the index after the data sub-blocks at j.
	subBlocks := func(j int) int {
		for j < len(b) {
			n := int(b[j])
			j++
			if n == 0 {
				return j
			}
			j += n
		}
		return -1
	}
	for i < len(b) {
		switch b[i] {
		case 0x21: // an extension
			if i+2 > len(b) {
				return "a damaged GIF"
			}
			switch label := b[i+1]; {
			case label == 0xFE:
				return "a GIF comment"
			case label == 0x01:
				return "a GIF text extension"
			case label == 0xFF && bytes.HasPrefix(b[i+2:], []byte("\x0bXMP DataXMP")):
				return "XMP"
			}
			if i = subBlocks(i + 2); i < 0 {
				return "a damaged GIF"
			}
		case 0x2C: // an image
			if i+10 > len(b) {
				return "a damaged GIF"
			}
			packed := b[i+9]
			i += 10
			if packed&0x80 != 0 {
				i += 3 << (packed&7 + 1)
			}
			if i++; i > len(b) { // the LZW code size
				return "a damaged GIF"
			}
			if i = subBlocks(i); i < 0 {
				return "a damaged GIF"
			}
		case 0x3B: // the trailer
			if i+1 != len(b) {
				return "data after the GIF's end"
			}
			return ""
		default:
			return "a damaged GIF"
		}
	}
	return "a damaged GIF: no end"
}

// finding is one secret a scanner found: the file in the exported tree, its
// line and the scanner's rule.
type finding struct {
	file string
	line int
	rule string
}

// scanSecrets runs gitleaks on an exported tree; the tests fake it.
var scanSecrets = gitleaks

// gitleaksConfig is gitleaks's own default rules. The export passes it,
// an empty ignore file and --ignore-gitleaks-allow, and clears
// GITLEAKS_CONFIG, so that the exported tree can't configure its own scan
// with a .gitleaks.toml or gitleaks:allow comments (check refuses a
// .gitleaksignore, which gitleaks reads from the tree regardless).
const gitleaksConfig = "[extend]\nuseDefault = true\n"

// gitleaks scans dir, redacted, and reads its JSON report. gitleaks prints
// nothing but its errors.
func gitleaks(dir string) ([]finding, error) {
	dir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return nil, err
	}
	tmp, err := os.MkdirTemp("", "lcd349-gitleaks-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)
	report := filepath.Join(tmp, "report.json")
	config := filepath.Join(tmp, "gitleaks.toml")
	ignore := filepath.Join(tmp, "gitleaksignore")
	if err := os.WriteFile(config, []byte(gitleaksConfig), 0o600); err != nil {
		return nil, err
	}
	if err := os.WriteFile(ignore, nil, 0o600); err != nil {
		return nil, err
	}
	cmd := exec.Command("gitleaks", "dir", ".", "--no-banner", "--redact", "--log-level", "error",
		"--config", config, "--gitleaks-ignore-path", ignore, "--ignore-gitleaks-allow",
		"--report-format", "json", "--report-path", report, "--exit-code", "0")
	cmd.Dir = dir
	cmd.Env = slices.DeleteFunc(os.Environ(), func(kv string) bool {
		return strings.HasPrefix(kv, "GITLEAKS_CONFIG=") || strings.HasPrefix(kv, "GITLEAKS_CONFIG_TOML=")
	})
	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("gitleaks: %v\n%s", err, bytes.TrimSpace(out))
	}
	b, err := os.ReadFile(report)
	if err != nil {
		return nil, fmt.Errorf("gitleaks wrote no report: %v", err)
	}
	var rep []struct {
		File      string
		StartLine int
		RuleID    string
	}
	if err := json.Unmarshal(b, &rep); err != nil {
		return nil, fmt.Errorf("gitleaks's report: %v", err)
	}
	var found []finding
	for _, r := range rep {
		f := r.File
		if filepath.IsAbs(f) {
			if f, err = filepath.Rel(dir, f); err != nil {
				return nil, err
			}
		}
		found = append(found, finding{filepath.ToSlash(filepath.Clean(f)), r.StartLine, r.RuleID})
	}
	return found, nil
}

func write(out string, files []file) error {
	for _, f := range files {
		p := filepath.Join(out, filepath.FromSlash(f.dest))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		mode := os.FileMode(0o644)
		if f.exec {
			mode = 0o755
		}
		if err := os.WriteFile(p, f.body, mode); err != nil {
			return err
		}
	}
	return nil
}
