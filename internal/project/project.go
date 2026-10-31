// Package project reads an lcd349 product: an ESP-IDF project whose manifest
// depends on the lcd349 component (requirement 7.5).
package project

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"go.yaml.in/yaml/v3"
)

// Project is an ESP-IDF project directory and what it says about the kit.
type Project struct {
	Dir      string // absolute, symlinks resolved
	Name     string // from project(...)
	Manifest string // the idf_component.yml naming lcd349, or ""
	Kit      *Dep   // the lcd349 dependency, or nil
	// PerTargetLocks: the root CMakeLists.txt sets DEPENDENCIES_LOCK per
	// target, so board and emulator builds don't fight over one lock file.
	PerTargetLocks bool
}

// Dep is the lcd349 entry of a component manifest.
type Dep struct {
	Version      string `yaml:"version"`
	Git          string `yaml:"git"`
	Path         string `yaml:"path"`
	OverridePath string `yaml:"override_path"`
}

var (
	projectRe = regexp.MustCompile(`(?m)^\s*project\(\s*([A-Za-z0-9_.+-]+)`)
	lockRe    = regexp.MustCompile(`(?m)^\s*idf_build_set_property\(\s*DEPENDENCIES_LOCK\s+\S*\$\{IDF_TARGET\}`)
)

// LockLine is what a project's root CMakeLists.txt needs before project().
const LockLine = "idf_build_set_property(DEPENDENCIES_LOCK dependencies.lock.${IDF_TARGET})"

// Load reads the ESP-IDF project in dir. It fails if dir isn't one; a
// project without the kit loads with Kit nil.
func Load(dir string) (*Project, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	if abs, err = filepath.EvalSymlinks(abs); err != nil {
		return nil, err
	}
	cm, err := os.ReadFile(filepath.Join(abs, "CMakeLists.txt"))
	m := projectRe.FindSubmatch(cm)
	if err != nil || m == nil {
		return nil, fmt.Errorf("%s isn't an ESP-IDF project (no CMakeLists.txt with project())", abs)
	}
	p := &Project{Dir: abs, Name: string(m[1]), PerTargetLocks: lockRe.Match(cm)}
	for _, rel := range []string{"main/idf_component.yml", "idf_component.yml"} {
		path := filepath.Join(abs, rel)
		dep, err := kitDep(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if dep != nil {
			p.Manifest, p.Kit = path, dep
			break
		}
	}
	return p, nil
}

// LoadKitProject is Load, refusing a project that doesn't depend on lcd349.
func LoadKitProject(dir string) (*Project, error) {
	p, err := Load(dir)
	if err != nil {
		return nil, err
	}
	if p.Kit == nil {
		return nil, fmt.Errorf("%s is an ESP-IDF project, but neither main/idf_component.yml nor idf_component.yml depends on lcd349", p.Dir)
	}
	return p, nil
}

func kitDep(path string) (*Dep, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var man struct {
		Dependencies map[string]yaml.Node `yaml:"dependencies"`
	}
	if err := yaml.Unmarshal(b, &man); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	for name, node := range man.Dependencies {
		if name != "lcd349" && !strings.HasSuffix(name, "/lcd349") {
			continue
		}
		var d Dep
		if node.Kind == yaml.ScalarNode {
			d.Version = node.Value
		} else if err := node.Decode(&d); err != nil {
			return nil, fmt.Errorf("%s: lcd349: %w", path, err)
		}
		return &d, nil
	}
	return nil, nil
}

// Inside resolves path (symlinks evaluated, relative to the project) and
// returns it if it lies within the project, or explains why not (R7.5, KT3).
func (p *Project) Inside(path string) (string, error) {
	if !filepath.IsAbs(path) {
		path = filepath.Join(p.Dir, path)
	}
	resolved, err := resolveExisting(filepath.Clean(path))
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(p.Dir, resolved)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%s resolves to %s, outside the project %s", path, resolved, p.Dir)
	}
	return resolved, nil
}

// resolveExisting evaluates symlinks in the longest existing prefix of path,
// so a file that doesn't exist yet (a new golden) is checked too.
func resolveExisting(path string) (string, error) {
	var rest []string
	cur := path
	for {
		r, err := filepath.EvalSymlinks(cur)
		if err == nil {
			return filepath.Join(append([]string{r}, rest...)...), nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return "", err
		}
		rest = append([]string{filepath.Base(cur)}, rest...)
		cur = parent
	}
}
