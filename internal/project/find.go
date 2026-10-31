package project

import (
	"fmt"
	"path/filepath"
)

// Enclosing finds the lcd349 project containing path, walking up from it.
func Enclosing(path string) (*Project, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	for dir := filepath.Dir(abs); ; dir = filepath.Dir(dir) {
		if p, err := LoadKitProject(dir); err == nil {
			return p, nil
		}
		if filepath.Dir(dir) == dir {
			return nil, fmt.Errorf("%s isn't inside an lcd349 project; -C names the project", abs)
		}
	}
}
