// Command export makes the public repository's next tree from the working
// copy (phase 5 requirement 3, herdr-pager ADR 0010). It copies only the
// files git tracks that the allowlist names.
//
//	go run ./tools/publish/export --repo <working copy> --allowlist <file> --denylist <file> --out <public clone>
//
// The allowlist has one path per line, relative to the working copy. A path
// ending in / names every tracked file under it; `src -> dest` copies src to
// dest instead of to the same path; # starts a comment line. The denylist,
// a private file outside every repository, has one value per line.
//
// It refuses an export holding a denylisted value, a home-folder path or a
// gitleaks finding, naming the file and the line but never the value, and
// then nothing reaches --out. Otherwise valvur scans the export, the export
// replaces everything in --out but a clone's .git, and the diff against the
// clone's HEAD is printed. It never commits or pushes: the owner reviews the
// diff, then commits and pushes it.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
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
	if err := fl.Parse(args); err != nil {
		return 2
	}
	if *repo == "" || *allowlist == "" || *denylist == "" || *out == "" || fl.NArg() > 0 {
		fmt.Fprintln(stderr, "usage: export --repo <working copy> --allowlist <file> --denylist <file> --out <public clone>")
		return 2
	}
	rules, err := parseAllowlist(*allowlist)
	if err != nil {
		fmt.Fprintln(stderr, "export:", err)
		return 1
	}
	files, err := collect(*repo, rules, stderr)
	if err != nil {
		fmt.Fprintln(stderr, "export:", err)
		return 1
	}
	deny, err := readDenylist(*denylist)
	if err != nil {
		fmt.Fprintln(stderr, "export:", err)
		return 1
	}
	clone, err := checkOut(*repo, *out)
	if err != nil {
		fmt.Fprintln(stderr, "export:", err)
		return 1
	}

	// The export is staged and scanned before anything reaches --out.
	stage, err := os.MkdirTemp("", "lcd349-export-")
	if err != nil {
		fmt.Fprintln(stderr, "export:", err)
		return 1
	}
	report := filepath.Join(stage, ".security-scan")
	defer func() {
		if _, err := os.Stat(report); err != nil {
			os.RemoveAll(stage)
		}
	}()
	if err := write(stage, files); err != nil {
		fmt.Fprintln(stderr, "export:", err)
		return 1
	}
	problems, err := refusals(stage, files, deny)
	if err != nil {
		fmt.Fprintln(stderr, "export:", err)
		return 1
	}
	if len(problems) > 0 {
		for _, p := range problems {
			fmt.Fprintln(stderr, "export:", p)
		}
		fmt.Fprintf(stderr, "export: refused (%d problems); nothing was written to %s\n", len(problems), *out)
		return 1
	}
	if err := scanTree(stage, stderr); err != nil {
		fmt.Fprintf(stderr, "export: valvur: %v; its report is in %s; nothing was written to %s\n", err, report, *out)
		return 1
	}

	if err := empty(*out); err != nil {
		fmt.Fprintln(stderr, "export:", err)
		return 1
	}
	if err := write(*out, files); err != nil {
		fmt.Fprintln(stderr, "export:", err)
		return 1
	}
	fmt.Fprintf(stdout, "exported %d files to %s\n", len(files), *out)
	if _, err := os.Stat(report); err == nil {
		fmt.Fprintf(stdout, "valvur's report is in %s\n", report)
	}
	if !clone {
		fmt.Fprintf(stdout, "%s isn't a git clone, so there's no diff\n", *out)
		return 0
	}
	if err := diff(*out, stdout, stderr); err != nil {
		fmt.Fprintln(stderr, "export:", err)
		return 1
	}
	return 0
}

// checkOut says whether --out is a git clone. It refuses the working copy
// itself, and a folder that's neither empty nor a clone, because the export
// replaces everything in --out but a clone's .git.
func checkOut(repo, out string) (clone bool, err error) {
	if err := os.MkdirAll(out, 0o755); err != nil {
		return false, err
	}
	r, err := filepath.EvalSymlinks(repo)
	if err != nil {
		return false, err
	}
	o, err := filepath.EvalSymlinks(out)
	if err != nil {
		return false, err
	}
	if r == o {
		return false, fmt.Errorf("--out is the working copy")
	}
	entries, err := os.ReadDir(out)
	if err != nil {
		return false, err
	}
	clone = slices.ContainsFunc(entries, func(e os.DirEntry) bool { return e.Name() == ".git" })
	if !clone && len(entries) > 0 {
		return false, fmt.Errorf("%s is neither empty nor a git clone", out)
	}
	return clone, nil
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

// file is one file of the export: its path in the working copy, its path in
// the public tree, the allowlist line that names it, and its content.
type file struct {
	src, dest, where string
	exec             bool
	body             []byte
}

// name is how a problem names the file: by its path in the working copy.
func (f file) name() string {
	if f.src == f.dest {
		return f.src
	}
	return f.src + " (exported as " + f.dest + ")"
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

// collect reads the tracked files the rules name. A rule that names no
// tracked file is only a warning: the file may come with a later merge.
func collect(repo string, rules []rule, stderr io.Writer) ([]file, error) {
	ls, err := exec.Command("git", "-C", repo, "ls-files", "-z").Output()
	if err != nil {
		return nil, fmt.Errorf("listing the tracked files in %s: %v", repo, err)
	}
	tracked := strings.Split(strings.TrimSuffix(string(ls), "\x00"), "\x00")
	var files []file
	from := map[string]string{}
	for _, r := range rules {
		n := 0
		for _, f := range tracked {
			var dest string
			switch {
			case strings.HasSuffix(r.src, "/") && strings.HasPrefix(f, r.src):
				dest = r.dest + strings.TrimPrefix(f, r.src)
			case f == r.src:
				dest = r.dest
			default:
				continue
			}
			n++
			if prev, ok := from[dest]; ok {
				return nil, fmt.Errorf("%s: %s and %s both go to %s", r.where, prev, f, dest)
			}
			from[dest] = f
			p := filepath.Join(repo, filepath.FromSlash(f))
			info, err := os.Lstat(p)
			if err != nil {
				return nil, err
			}
			if !info.Mode().IsRegular() {
				return nil, fmt.Errorf("%s: not a regular file", f)
			}
			body, err := os.ReadFile(p)
			if err != nil {
				return nil, err
			}
			files = append(files, file{f, dest, r.where, info.Mode()&0o111 != 0, body})
		}
		if n == 0 {
			fmt.Fprintf(stderr, "export: warning: %s: %s names no tracked file\n", r.where, r.src)
		}
	}
	return files, nil
}

// refusals are the reasons not to export: the denylisted values and
// home-folder paths, then gitleaks's findings in the staged export.
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
			name = f.name()
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

// homePrefix starts a home-folder path on the Mac. It's split so that this
// file passes its own export.
const homePrefix = "/" + "Users/"

// check finds the denylisted values and home-folder paths in the export. A
// problem names the file and the line, or for a path the allowlist line, and
// never the value.
func check(files []file, deny []denied) []string {
	var problems []string
	for _, f := range files {
		paths := strings.ToLower(f.src + "\n" + f.dest)
		bad := false
		for _, d := range deny {
			if strings.Contains(paths, d.value) {
				problems = append(problems, fmt.Sprintf("%s: a path it copies holds the value on denylist line %d", f.where, d.line))
				bad = true
			}
		}
		if bad {
			continue
		}
		for n, line := range strings.Split(string(f.body), "\n") {
			lower := strings.ToLower(line)
			for _, d := range deny {
				if strings.Contains(lower, d.value) {
					problems = append(problems, fmt.Sprintf("%s:%d: holds the value on denylist line %d", f.name(), n+1, d.line))
				}
			}
			if strings.Contains(line, homePrefix) {
				problems = append(problems, fmt.Sprintf("%s:%d: holds a home-folder path", f.name(), n+1))
			}
		}
	}
	return problems
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
	cmd := exec.Command("gitleaks", "dir", ".", "--no-banner", "--redact", "--log-level", "error",
		"--report-format", "json", "--report-path", report, "--exit-code", "0")
	cmd.Dir = dir
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
