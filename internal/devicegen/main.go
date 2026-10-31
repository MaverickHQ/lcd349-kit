// Command devicegen generates the SDK constants, emulator defaults, lint
// thresholds, skill references and hardware docs from device/device.json
// (requirement 10.2), and the skill's API reference and recipes from the
// public headers and examples/gallery (requirement 8.3).
//
//	go run ./internal/devicegen            regenerate
//	go run ./internal/devicegen --check    fail if any generated file is stale
package main

import (
	"flag"
	"fmt"
	"os"
)

func main() {
	checkOnly := flag.Bool("check", false, "report stale generated files and exit 1 instead of writing")
	root := flag.String("root", ".", "repository root")
	flag.Parse()

	d, raw, err := Load(*root)
	if err != nil {
		fmt.Fprintln(os.Stderr, "devicegen:", err)
		os.Exit(1)
	}
	files, err := Render(d, raw)
	if err != nil {
		fmt.Fprintln(os.Stderr, "devicegen:", err)
		os.Exit(1)
	}
	skill, err := RenderSkill(*root, d, raw, files)
	if err != nil {
		fmt.Fprintln(os.Stderr, "devicegen:", err)
		os.Exit(1)
	}
	for p, b := range skill {
		files[p] = b
	}
	if *checkOnly {
		if stale := Stale(*root, files); len(stale) > 0 {
			for _, p := range stale {
				fmt.Fprintln(os.Stderr, "stale:", p)
			}
			fmt.Fprintln(os.Stderr, "devicegen: generated files are out of date; run: go run ./internal/devicegen")
			os.Exit(1)
		}
		return
	}
	if err := Write(*root, files); err != nil {
		fmt.Fprintln(os.Stderr, "devicegen:", err)
		os.Exit(1)
	}
}
