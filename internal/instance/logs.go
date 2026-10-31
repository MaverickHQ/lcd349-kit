package instance

import (
	"bufio"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// LogLine is one serial line, numbered from the instance's start across log
// rotation.
type LogLine struct {
	N    int    `json:"n"`
	Text string `json:"text"`
}

// ReadLogs returns the serial lines numbered since and later, from
// serial.log.1 and serial.log, and the number the next line will get.
func ReadLogs(in *Info, since int) ([]LogLine, int, error) {
	n := in.LogLinesBefore
	var out []LogLine
	for _, name := range []string{"serial.log.1", "serial.log"} {
		f, err := os.Open(filepath.Join(in.Dir, name))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, 0, err
		}
		r := bufio.NewReaderSize(f, 64<<10)
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				break // a partial last line is left for next time
			}
			n++
			if n >= since {
				out = append(out, LogLine{n, strings.TrimRight(line, "\r\n")})
			}
		}
		f.Close()
	}
	return out, n + 1, nil
}
