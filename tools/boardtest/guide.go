package main

import (
	"encoding/json"
	"regexp"
	"strings"
	"time"
)

// The guided checkpoint (seam S1-5): with -guide, boardtest runs an app's
// on-device guide (examples/inputs/main/guide.c) instead of Unity tests.
//   - It sends "@P guide" ("@P guide restart" with -restart, "@P guide from
//     <step>" with -from) until the board answers with an "@R {"guide":…}"
//     line. It sends "@P guide" again whenever the port comes back or the
//     app starts again (the kit's start-up line), so a guide interrupted by
//     a reset or a power off resumes.
//   - It answers the guide's "@Q" requests as it does for tests: ping, and
//     the Wi-Fi file's networks.
//   - It waits as long as the run may last for the port to come back, while
//     the owner has USB unplugged.
//   - It ends at the guide's "done" line: exit 0 if every step passed, 1 if
//     one didn't; or at its "error" line (exit 2).

type guideLine struct {
	Guide *struct {
		Error   string       `json:"error"`
		Summary *guideResult `json:"summary"`
		Done    bool         `json:"done"`
		Passed  int          `json:"passed"`
		Of      int          `json:"of"`
	} `json:"guide"`
}

type guideResult struct {
	Step   string `json:"step"`
	Index  int    `json:"index"`
	Of     int    `json:"of"`
	Result string `json:"result"`
}

// How often the guide is asked for until the board answers.
const guideAskEvery = 2 * time.Second

// kitUpRe is the kit's start-up line (src/common/core.c), which lcd349_init
// logs on the board and in the emulator alike.
var kitUpRe = regexp.MustCompile(`lcd349: lcd349 \S+ up \(device\.json `)

// guide runs the guide, asking for it first with cmd.
func (s *session) guide(path string, timeout time.Duration, cmd string) int {
	p := &serial{path: path, logf: s.logf, stop: make(chan struct{}), wait: timeout, reopened: make(chan struct{}, 1)}
	if err := p.open(); err != nil {
		s.logf("%v", err)
		return 2
	}
	defer p.close()
	lines, fail := make(chan string, 256), make(chan error, 1)
	go p.read(lines, fail)

	answered := false
	ask := func() {
		s.out.line("> " + cmd)
		if err := p.send(cmd); err != nil {
			s.logf("sending %q: %v", cmd, err)
		}
	}
	ask()
	tick := time.NewTicker(guideAskEvery)
	defer tick.Stop()
	deadline := time.After(timeout)
	var results []guideResult
	for {
		select {
		case line := <-lines:
			s.out.line(line)
			if strings.HasPrefix(line, "@Q ") {
				if err := s.answer(strings.TrimSpace(line[3:]), p); err != nil {
					s.logf("%v", err)
					return 2
				}
				continue
			}
			if kitUpRe.MatchString(line) {
				answered = false // the app started again on the same link: ask again
				continue
			}
			var g guideLine
			if !strings.HasPrefix(line, "@R {") || json.Unmarshal([]byte(line[3:]), &g) != nil || g.Guide == nil {
				continue
			}
			if g.Guide.Error != "" {
				s.logf("the guide refused %q: %s", cmd, g.Guide.Error)
				return 2
			}
			if !answered {
				answered, cmd = true, "@P guide" // from now on, only ever resume
			}
			if g.Guide.Summary != nil {
				results = append(results, *g.Guide.Summary)
			}
			if g.Guide.Done {
				return s.guideVerdict(results, g.Guide.Passed, g.Guide.Of)
			}
		case <-tick.C:
			if !answered {
				ask()
			}
		case <-p.reopened:
			answered = false
			ask()
		case err := <-fail:
			s.logf("%v", err)
			return 2
		case <-deadline:
			s.logf("timed out after %s: the guide didn't finish (run boardtest -guide again to resume it)", timeout)
			return 2
		}
	}
}

func (s *session) guideVerdict(results []guideResult, passed, of int) int {
	failed := 0
	for _, r := range results {
		s.logf("step %d of %d, %s: %s", r.Index, r.Of, r.Step, r.Result)
		if r.Result != "PASS" {
			failed++
		}
	}
	verdict, code := "PASS", 0
	if failed > 0 || len(results) == 0 || passed != of {
		verdict, code = "FAIL", 1
	}
	s.logf("%s: %d of %d steps passed (%s)", verdict, passed, of, s.what)
	return code
}
