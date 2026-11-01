package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/color/palette"
	"image/draw"
	"image/gif"
	"image/png"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"syscall"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/MaverickHQ/lcd349-kit/internal/build"
	"github.com/MaverickHQ/lcd349-kit/internal/drive"
	"github.com/MaverickHQ/lcd349-kit/internal/golden"
	"github.com/MaverickHQ/lcd349-kit/internal/instance"
	"github.com/MaverickHQ/lcd349-kit/internal/scenario"
	"github.com/MaverickHQ/lcd349-kit/internal/viewer"
)

// ---- screenshot ----

func (s *Server) screenshot(ctx context.Context, _ *mcp.CallToolRequest, in screenshotIn) (*mcp.CallToolResult, any, error) {
	d, err := s.conn(ctx, in.Instance)
	if err != nil {
		return nil, nil, err
	}
	defer d.Close()
	shot, err := d.Screenshot(ctx, drive.ShotOptions{Raw: in.Raw, Scale: in.Scale})
	if err != nil {
		return nil, nil, err
	}
	pngBytes := golden.Compact(shot.PNG)
	meta := map[string]any{"frame_seq": shot.FrameSeq, "backlight": shot.Backlight, "power": shot.Power, "dark": shot.Dark,
		"width": shot.Width, "height": shot.Height}
	if b, err := d.Board(ctx); err == nil && b.Board.PxPerMM > 0 {
		zoom := float64(max(in.Scale, 1))
		meta["mm"] = []float64{round1(float64(shot.Width) / zoom / b.Board.PxPerMM), round1(float64(shot.Height) / zoom / b.Board.PxPerMM)}
		meta["px_per_mm"] = b.Board.PxPerMM * zoom
	}
	if in.Physical {
		f, note := s.physicalFactor(ctx)
		meta["physical"] = note
		if f > 0 {
			img := resize(shot.Image, f/float64(max(in.Scale, 1)))
			var b bytes.Buffer
			png.Encode(&b, img)
			pngBytes = b.Bytes()
			meta["width"], meta["height"] = img.Bounds().Dx(), img.Bounds().Dy()
		}
	}
	if shot.Dark {
		meta["note"] = "the display is dark (asleep, off or backlight 0), so the screen is black"
	}
	text, _ := json.Marshal(meta)
	return &mcp.CallToolResult{Content: []mcp.Content{
		&mcp.ImageContent{Data: pngBytes, MIMEType: "image/png"},
		&mcp.TextContent{Text: string(text)},
	}}, nil, nil
}

func round1(v float64) float64 { return float64(int(v*10+0.5)) / 10 }

// physicalFactor is display pixels per device pixel at real size, from the
// viewer's view of this computer's display; 0 when it isn't known.
func (s *Server) physicalFactor(ctx context.Context) (float64, string) {
	s.mu.Lock()
	sc := s.scale
	s.mu.Unlock()
	if sc == nil {
		path, err := viewer.Path(discard())
		if err != nil {
			return 0, "not known (" + err.Error() + "); sent at 1:1"
		}
		cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		if sc, err = viewer.PrintScale(cctx, path); err != nil {
			return 0, "not known (" + err.Error() + "); sent at 1:1"
		}
		s.mu.Lock()
		s.scale = sc
		s.mu.Unlock()
	}
	if sc.Source == "none" || sc.Source == "unknown" || sc.Scale <= 0 {
		return 0, "not known here (" + sc.Detail + "); sent at 1:1"
	}
	f := sc.Scale * max(sc.PixelsPerPoint, 1)
	return f, fmt.Sprintf("%.3f display pixels per device pixel, from %s (%s)", f, sc.Source, sc.Detail)
}

// resize scales an image by f with bilinear filtering.
func resize(src image.Image, f float64) image.Image {
	sb := src.Bounds()
	w, h := max(int(float64(sb.Dx())*f+0.5), 1), max(int(float64(sb.Dy())*f+0.5), 1)
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	at := func(x, y int) [3]float64 {
		x = min(max(x, 0), sb.Dx()-1)
		y = min(max(y, 0), sb.Dy()-1)
		r, g, b, _ := src.At(sb.Min.X+x, sb.Min.Y+y).RGBA()
		return [3]float64{float64(r >> 8), float64(g >> 8), float64(b >> 8)}
	}
	for y := 0; y < h; y++ {
		fy := (float64(y)+0.5)/f - 0.5
		y0 := int(fy)
		if fy < 0 {
			y0 = -1
		}
		ty := fy - float64(y0)
		for x := 0; x < w; x++ {
			fx := (float64(x)+0.5)/f - 0.5
			x0 := int(fx)
			if fx < 0 {
				x0 = -1
			}
			tx := fx - float64(x0)
			a, b, c, e := at(x0, y0), at(x0+1, y0), at(x0, y0+1), at(x0+1, y0+1)
			var px [3]uint8
			for i := 0; i < 3; i++ {
				top := a[i]*(1-tx) + b[i]*tx
				bot := c[i]*(1-tx) + e[i]*tx
				px[i] = uint8(top*(1-ty) + bot*ty + 0.5)
			}
			dst.Set(x, y, color.RGBA{px[0], px[1], px[2], 255})
		}
	}
	return dst
}

// ---- ui_tree, with the 1 MB cap (KT5) ----

func (s *Server) uiTree(ctx context.Context, _ *mcp.CallToolRequest, in treeIn) (*mcp.CallToolResult, any, error) {
	d, err := s.conn(ctx, in.Instance)
	if err != nil {
		return nil, nil, err
	}
	defer d.Close()
	p := map[string]any{}
	if in.Path != "" {
		p["path"] = in.Path
	}
	if in.Depth > 0 {
		p["depth"] = in.Depth
	}
	var raw json.RawMessage
	if err := d.Call(ctx, "ui.tree", p, &raw); err != nil {
		return nil, nil, err
	}
	if len(raw) <= s.maxOutput {
		return s.jsonResult(raw, false), nil, nil
	}
	var t map[string]any
	if err := json.Unmarshal(raw, &t); err != nil {
		return nil, nil, err
	}
	root, _ := t["tree"].(map[string]any)
	for depth := treeDepth(root) - 1; depth >= 0; depth-- {
		pruned := prune(root, depth)
		t2 := map[string]any{"tree": pruned, "count": t["count"], "truncated": true,
			"note": fmt.Sprintf("The full tree is %d bytes, over the %s tool output limit, so it stops %d levels down; objects with children_omitted have more. Narrow it with path (an object's path from this tree) or depth.", len(raw), s.limitText(), depth)}
		b, _ := json.Marshal(t2)
		if len(b) <= s.maxOutput {
			return s.jsonResult(json.RawMessage(b), false), nil, nil
		}
	}
	return nil, nil, fmt.Errorf("even the top object is over the %s tool output limit; narrow it with path", s.limitText())
}

func treeDepth(n map[string]any) int {
	kids, _ := n["children"].([]any)
	d := 0
	for _, k := range kids {
		if m, ok := k.(map[string]any); ok {
			d = max(d, treeDepth(m)+1)
		}
	}
	return d
}

// prune copies n down to depth levels; cut nodes say how many children they had.
func prune(n map[string]any, depth int) map[string]any {
	out := map[string]any{}
	for k, v := range n {
		if k != "children" {
			out[k] = v
		}
	}
	kids, _ := n["children"].([]any)
	if len(kids) == 0 {
		return out
	}
	if depth == 0 {
		out["children_omitted"] = len(kids)
		return out
	}
	var cs []any
	for _, k := range kids {
		if m, ok := k.(map[string]any); ok {
			cs = append(cs, prune(m, depth-1))
		}
	}
	out["children"] = cs
	return out
}

// ---- run_scenario ----

func (s *Server) runScenario(ctx context.Context, _ *mcp.CallToolRequest, in scenarioIn) (*mcp.CallToolResult, any, error) {
	proj, err := s.project(in.Project)
	if err != nil {
		return nil, nil, err
	}
	path := in.File
	if !filepath.IsAbs(path) {
		path = filepath.Join(proj.Dir, path)
	}
	if path, err = proj.Inside(path); err != nil {
		return nil, nil, fmt.Errorf("refused: %v", err)
	}
	sc, err := scenario.Load(path)
	if err != nil {
		return nil, nil, err
	}
	var elf string
	if in.Build == nil || *in.Build {
		res, err := s.buildProject(ctx, proj.Dir)
		if err != nil {
			return nil, nil, err
		}
		if !res.OK {
			return s.jsonResult(map[string]any{"build": res, "ran": false}, true), nil, nil
		}
		elf = res.ELF
	} else if elf, err = build.ELF(proj.Dir); err != nil {
		return nil, nil, err
	}
	res, err := scenario.Run(ctx, sc, scenario.Options{Project: proj, ELF: elf, Exe: s.o.Exe, UpdateGoldens: in.UpdateGoldens})
	if err != nil {
		return nil, nil, err
	}
	out := s.jsonResult(res, !res.OK)
	// Show a failing screenshot's evidence: the actual screen and the diff.
	if f := res.Failed(); f != nil {
		var imgs []string
		if f.Golden != nil {
			imgs = append(imgs, f.Golden.Actual, f.Golden.Diff)
		}
		imgs = append(imgs, f.Image)
		for _, p := range imgs {
			if b, err := os.ReadFile(p); p != "" && err == nil && len(b) < s.maxOutput/2 {
				out.Content = append(out.Content, &mcp.ImageContent{Data: golden.Compact(b), MIMEType: "image/png"},
					&mcp.TextContent{Text: "above: " + p})
			}
		}
	}
	return out, nil, nil
}

// ---- record ----

func (s *Server) record(ctx context.Context, _ *mcp.CallToolRequest, in recordIn) (*mcp.CallToolResult, any, error) {
	info, err := s.pick(in.Instance)
	if err != nil {
		return nil, nil, err
	}
	d, err := drive.Connect(ctx, info)
	if err != nil {
		return nil, nil, err
	}
	defer d.Close()
	switch in.Action {
	case "start":
		p := map[string]any{}
		if in.MaxS > 0 {
			p["max_s"] = in.MaxS
		}
		var res map[string]any
		if err := d.Call(ctx, "record.start", p, &res); err != nil {
			return nil, nil, err
		}
		return s.jsonResult(map[string]any{"recording": true, "max_s": res["max_s"]}, false), nil, nil
	case "stop":
		var res struct {
			Dir        string  `json:"dir"`
			Frames     int     `json:"frames"`
			DurationMS float64 `json:"duration_ms"`
		}
		if err := d.Call(ctx, "record.stop", map[string]any{}, &res); err != nil {
			return nil, nil, err
		}
		if res.Frames == 0 {
			return s.jsonResult(map[string]any{"frames": 0, "note": "no frames: nothing on screen changed while recording"}, false), nil, nil
		}
		out, err := recordingsDir(info.Project)
		if err != nil {
			return nil, nil, err
		}
		n, gifPath, err := framesToGIF(res.Dir, out, fmt.Sprintf("%s-%s", info.ID, time.Now().Format("20060102-150405")))
		if err != nil {
			return nil, nil, err
		}
		return s.jsonResult(map[string]any{"gif": gifPath, "frames": n, "duration_ms": res.DurationMS}, false), nil, nil
	}
	return nil, nil, fmt.Errorf("action is start or stop")
}

// recordingsDir is where record writes a GIF: the project's
// build-emu/recordings, or for an instance with no project a directory in
// $TMPDIR only this user can use, checked as the instance roots are.
func recordingsDir(project string) (string, error) {
	if project != "" {
		dir := filepath.Join(project, build.Dir, "recordings")
		return dir, os.MkdirAll(dir, 0o755)
	}
	dir := filepath.Join(os.TempDir(), fmt.Sprintf("lcd349-recordings-%d", os.Getuid()))
	return dir, instance.EnsurePrivateDir(dir)
}

var frameRe = regexp.MustCompile(`^frame-(\d+)-(\d+)\.png$`)

// framesToGIF turns the instance's recorded frames (frame-<n>-<ms>.png) into
// an animated GIF that keeps their timing, in a new file in outDir: name.gif,
// or name-2.gif and so on when that exists. It never writes through a file
// or symlink already there.
func framesToGIF(dir, outDir, name string) (int, string, error) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return 0, "", err
	}
	type frame struct {
		path string
		ms   int
	}
	var frames []frame
	for _, e := range ents {
		if m := frameRe.FindStringSubmatch(e.Name()); m != nil {
			ms, _ := strconv.Atoi(m[2])
			frames = append(frames, frame{filepath.Join(dir, e.Name()), ms})
		}
	}
	sort.Slice(frames, func(i, j int) bool { return frames[i].ms < frames[j].ms })
	var g gif.GIF
	for i, f := range frames {
		b, err := os.ReadFile(f.path)
		if err != nil {
			return 0, "", err
		}
		img, err := png.Decode(bytes.NewReader(b))
		if err != nil {
			return 0, "", err
		}
		pal := image.NewPaletted(img.Bounds(), palette.Plan9)
		draw.FloydSteinberg.Draw(pal, img.Bounds(), img, image.Point{})
		delay := 100 // the last frame: 1 s
		if i+1 < len(frames) {
			delay = max((frames[i+1].ms-f.ms)/10, 2)
		}
		g.Image = append(g.Image, pal)
		g.Delay = append(g.Delay, delay)
	}
	for n := 1; n <= 100; n++ {
		out := filepath.Join(outDir, name+".gif")
		if n > 1 {
			out = filepath.Join(outDir, fmt.Sprintf("%s-%d.gif", name, n))
		}
		fh, err := os.OpenFile(out, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o644)
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		if err != nil {
			return 0, "", err
		}
		err = gif.EncodeAll(fh, &g)
		if cerr := fh.Close(); err == nil {
			err = cerr
		}
		return len(frames), out, err
	}
	return 0, "", fmt.Errorf("no free name for %s.gif in %s", name, outDir)
}
