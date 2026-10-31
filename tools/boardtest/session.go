package main

import (
	"bufio"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/MaverickHQ/lcd349-kit/internal/pty"
)

// ---- The port, reopened when the USB link drops ----

type serial struct {
	path     string
	logf     func(string, ...any)
	stop     chan struct{}
	wait     time.Duration // how long the port may stay away; 0 means 30 s
	reopened chan struct{} // if not nil, told after each reopen
	mu       sync.Mutex
	f        *os.File
}

func (p *serial) open() error {
	f, err := pty.OpenSerial(p.path)
	if err != nil {
		return err
	}
	p.mu.Lock()
	p.f = f
	p.mu.Unlock()
	return nil
}

func (p *serial) stopped() bool {
	select {
	case <-p.stop:
		return true
	default:
		return false
	}
}

func (p *serial) close() {
	close(p.stop)
	p.mu.Lock()
	if p.f != nil {
		p.f.Close()
	}
	p.mu.Unlock()
}

func (p *serial) send(line string) error {
	p.mu.Lock()
	f := p.f
	p.mu.Unlock()
	if f == nil {
		return errors.New("the port is closed")
	}
	_, err := io.WriteString(f, line+"\n")
	return err
}

// read passes each line on. When the link drops (a reboot, light sleep, an
// unplugged cable) it reopens the port, for up to 30 s or p.wait, and
// carries on.
func (p *serial) read(lines chan<- string, fail chan<- error) {
	wait := p.wait
	if wait == 0 {
		wait = 30 * time.Second
	}
	for {
		p.mu.Lock()
		rd := bufio.NewReader(p.f)
		p.mu.Unlock()
		for {
			s, err := rd.ReadString('\n')
			if s = strings.TrimRight(s, "\r\n"); s != "" {
				select {
				case lines <- s:
				case <-p.stop:
					return
				}
			}
			if err != nil {
				break
			}
		}
		if p.stopped() {
			return
		}
		p.logf("the USB link dropped; reopening %s", p.path)
		p.mu.Lock()
		p.f.Close()
		p.f = nil
		p.mu.Unlock()
		said := time.Now()
		for deadline := time.Now().Add(wait); ; time.Sleep(200 * time.Millisecond) {
			if p.stopped() {
				return
			}
			if err := p.open(); err == nil {
				p.logf("reopened %s", p.path)
				if p.reopened != nil {
					select {
					case p.reopened <- struct{}{}:
					default:
					}
				}
				break
			} else if time.Now().After(deadline) {
				fail <- fmt.Errorf("the board's port didn't come back within %s: %v", wait, err)
				return
			}
			if time.Since(said) >= time.Minute {
				p.logf("still waiting for %s to come back (plug the board in)", p.path)
				said = time.Now()
			}
		}
	}
}

// ---- Masking: the Wi-Fi credentials never reach the output ----

type masker struct {
	mu      sync.Mutex
	w       io.Writer
	secrets [][2]string // text, replacement; longest first
}

func (m *masker) add(secret, label string) {
	if secret == "" {
		return
	}
	j, _ := json.Marshal(secret)
	forms := []string{secret, string(j[1 : len(j)-1]), hex.EncodeToString([]byte(secret)), strings.ToUpper(hex.EncodeToString([]byte(secret)))}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, f := range forms {
		m.secrets = append(m.secrets, [2]string{f, label})
	}
	sort.SliceStable(m.secrets, func(i, k int) bool { return len(m.secrets[i][0]) > len(m.secrets[k][0]) })
}

func (m *masker) line(s string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, sec := range m.secrets {
		s = strings.ReplaceAll(s, sec[0], sec[1])
	}
	fmt.Fprintln(m.w, s)
}

// ---- The Wi-Fi file ----

type network struct {
	SSID     string `json:"ssid"`
	Password string `json:"password"`
}

// readWifi reads test-wifi.toml's [[network]] tables (ssid and password,
// basic or literal strings), refusing the file unless it's a regular file of
// mode 0600. Its errors never quote the file.
func readWifi(path string) ([]network, error) {
	st, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("the Wi-Fi file %s: %w", path, err)
	}
	if !st.Mode().IsRegular() || st.Mode().Perm() != 0o600 {
		return nil, fmt.Errorf("refusing the Wi-Fi file %s: its mode is %v, and it must be a file of mode 0600 (chmod 600 it)", path, st.Mode())
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("the Wi-Fi file %s: %w", path, err)
	}
	var nets []network
	for i, raw := range strings.Split(string(b), "\n") {
		line := strings.TrimSpace(raw)
		switch {
		case line == "" || strings.HasPrefix(line, "#"):
			continue
		case line == "[[network]]":
			nets = append(nets, network{})
			continue
		}
		key, val, ok := strings.Cut(line, "=")
		if !ok || len(nets) == 0 {
			return nil, fmt.Errorf("the Wi-Fi file %s, line %d: expected [[network]] or key = \"value\"", path, i+1)
		}
		v, err := tomlString(strings.TrimSpace(val))
		if err != nil {
			return nil, fmt.Errorf("the Wi-Fi file %s, line %d: %v", path, i+1, err)
		}
		switch strings.TrimSpace(key) {
		case "ssid":
			nets[len(nets)-1].SSID = v
		case "password":
			nets[len(nets)-1].Password = v
		}
	}
	var ok []network
	for _, n := range nets {
		if n.SSID != "" {
			ok = append(ok, n)
		}
	}
	if len(ok) == 0 {
		return nil, fmt.Errorf("the Wi-Fi file %s has no [[network]] with an ssid", path)
	}
	return ok, nil
}

func tomlString(v string) (string, error) {
	if len(v) >= 2 && v[0] == '\'' {
		end := strings.IndexByte(v[1:], '\'')
		if end < 0 {
			return "", errors.New("unterminated literal string")
		}
		return v[1 : 1+end], nil
	}
	if len(v) < 2 || v[0] != '"' {
		return "", errors.New("not a string")
	}
	var sb strings.Builder
	for i := 1; i < len(v); i++ {
		switch c := v[i]; {
		case c == '"':
			return sb.String(), nil
		case c == '\\' && i+1 < len(v):
			i++
			switch v[i] {
			case 'n':
				sb.WriteByte('\n')
			case 't':
				sb.WriteByte('\t')
			default:
				sb.WriteByte(v[i])
			}
		default:
			sb.WriteByte(c)
		}
	}
	return "", errors.New("unterminated string")
}

// ---- The harness protocol (lcd349_harness.h) ----

type runLine struct {
	Tag    string `json:"tag"`
	Index  int    `json:"index"`
	Stage  int    `json:"stage"`
	Stages int    `json:"stages"`
	Name   string `json:"name"`
}

type session struct {
	out      *masker
	logf     func(string, ...any)
	wifiPath string
	what     string // for the verdict line
	tag      string // the plan's tag, "*" for all

	pending  string // the command for the board's next "@Q cmd"
	lastSent string
	sentAt   time.Time
	last     *runLine // the test running since the last command
	progress bool     // a test started since the last command
	done     bool

	summaries, tests, failures int
	failed                     []string // Unity FAIL lines, and tests the board restarted in
}

var (
	summaryRe = regexp.MustCompile(`^(\d+) Tests (\d+) Failures (\d+) Ignored`)
	failRe    = regexp.MustCompile(`^[^:]+:\d+:.*:FAIL(:|$)`)
)

func (s *session) run(path string, timeout time.Duration) int {
	p := &serial{path: path, logf: s.logf, stop: make(chan struct{})}
	if err := p.open(); err != nil {
		s.logf("%v", err)
		return 2
	}
	defer p.close()
	lines, fail := make(chan string, 256), make(chan error, 1)
	go p.read(lines, fail)
	deadline := time.After(timeout)
	for {
		select {
		case line := <-lines:
			s.out.line(line)
			if err := s.handle(line, p); err != nil {
				s.logf("%v", err)
				return 2
			}
			if s.done {
				return s.verdict()
			}
		case err := <-fail:
			s.logf("%v", err)
			return 2
		case <-deadline:
			s.logf("timed out after %s waiting for the board", timeout)
			return 2
		}
	}
}

func (s *session) handle(line string, p *serial) error {
	started := s.lastSent != ""
	switch {
	case strings.HasPrefix(line, "@R {"):
		var r struct {
			Boot *struct{ Reason string } `json:"boot"`
			Run  *runLine                 `json:"run"`
			Done *bool                    `json:"done"`
		}
		if json.Unmarshal([]byte(line[3:]), &r) != nil {
			return nil
		}
		switch {
		case r.Boot != nil:
			s.boot(r.Boot.Reason)
		case r.Run != nil && started:
			s.last, s.progress = r.Run, true
		case r.Done != nil && started && s.pending == "":
			s.done = true
		}
	case line == "@Q cmd":
		switch {
		case s.pending != "":
			s.lastSent, s.pending = s.pending, ""
		case started && !s.progress && time.Since(s.sentAt) > 5*time.Second:
			// still asking: the command was lost (sent while the board reset)
		default:
			return nil
		}
		s.sentAt, s.progress, s.last = time.Now(), false, nil
		s.out.line("> " + s.lastSent)
		return p.send(s.lastSent)
	case strings.HasPrefix(line, "@Q "):
		return s.answer(strings.TrimSpace(line[3:]), p)
	case started:
		if m := summaryRe.FindStringSubmatch(line); m != nil {
			n, _ := strconv.Atoi(m[1])
			f, _ := strconv.Atoi(m[2])
			s.summaries, s.tests, s.failures = s.summaries+1, s.tests+n, s.failures+f
		} else if failRe.MatchString(line) {
			s.failed = append(s.failed, line)
		}
	}
	return nil
}

// boot decides what the board gets after it restarts in the middle of a run.
func (s *session) boot(reason string) {
	if s.lastSent == "" || s.done || s.pending != "" {
		return // nothing sent yet, or already waiting to send
	}
	l := s.last
	s.last = nil
	switch {
	case l == nil:
		s.pending = s.lastSent // nothing ran: the reset came first
	case l.Stage < l.Stages && reason != "crash" && reason != "brownout":
		s.pending = fmt.Sprintf("@P resume %s %d %d", l.Tag, l.Index, l.Stage+1)
	default:
		s.failed = append(s.failed, fmt.Sprintf("%s (stage %d of %d): the board restarted (%s)", l.Name, l.Stage, l.Stages, reason))
		s.tests, s.failures = s.tests+1, s.failures+1
		s.pending = fmt.Sprintf("@P resume %s %d 1", l.Tag, l.Index+1)
	}
}

// answer replies to a test's request: "wifi [n]" gets the n-th network of
// test-wifi.toml; anything else gets its first word back.
func (s *session) answer(req string, p *serial) error {
	word, rest, _ := strings.Cut(req, " ")
	if word != "wifi" {
		s.out.line("> @P " + word)
		return p.send("@P " + word)
	}
	n := 1
	if rest != "" {
		var err error
		if n, err = strconv.Atoi(strings.TrimSpace(rest)); err != nil || n < 1 {
			return fmt.Errorf("the board asked for Wi-Fi network %q", rest)
		}
	}
	nets, err := readWifi(s.wifiPath)
	if err != nil {
		return err
	}
	for _, net := range nets {
		s.out.add(net.Password, "[password]")
		if len(net.SSID) >= 4 {
			s.out.add(net.SSID, "[ssid]")
		}
	}
	if n > len(nets) {
		return fmt.Errorf("the board asked for Wi-Fi network %d; the Wi-Fi file has %d", n, len(nets))
	}
	var b strings.Builder
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(nets[n-1]); err != nil {
		return err
	}
	line := "@P wifi " + strings.TrimSpace(b.String())
	s.out.add(line[len("@P wifi "):], "[network]")
	s.out.line(fmt.Sprintf("> @P wifi [network %d of the Wi-Fi file]", n))
	return p.send(line)
}

func (s *session) verdict() int {
	for _, f := range s.failed {
		s.logf("FAIL %s", f)
	}
	if s.summaries == 0 {
		s.logf("the board reported no Unity summary")
		return 2
	}
	if s.tests == 0 {
		s.logf("no test ran (%s): is the tag right?", s.what)
		return 2
	}
	verdict, code := "PASS", 0
	if s.failures > 0 {
		verdict, code = "FAIL", 1
	}
	s.logf("%s: %s, %s (%s)", verdict, plural(s.tests, "test"), plural(s.failures, "failure"), s.what)
	return code
}

func plural(n int, what string) string {
	if n == 1 {
		return "1 " + what
	}
	return fmt.Sprintf("%d %ss", n, what)
}
