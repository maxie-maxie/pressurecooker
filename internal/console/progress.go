package console

import (
	"bufio"
	"io"
	"strconv"
	"strings"
)

// ParseProgress reads FFmpeg -progress key=value lines and emits one Event per block.
func ParseProgress(r io.Reader, onEvent func(Event)) error {
	var ev Event
	var haveUS bool
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		k, v, ok := strings.Cut(strings.TrimSpace(sc.Text()), "=")
		if !ok {
			continue
		}
		v = strings.TrimSpace(v)
		switch k {
		case "out_time_us", "out_time_ms": // both are microseconds in FFmpeg
			if n, err := strconv.ParseInt(v, 10, 64); err == nil && n >= 0 {
				ev.Timeline, haveUS = float64(n)/1e6, true
			}
		case "out_time":
			if !haveUS {
				if s, ok := parseClock(v); ok {
					ev.Timeline = s
				}
			}
		case "speed":
			ev.Speed = 0
			if f, err := strconv.ParseFloat(strings.TrimSuffix(v, "x"), 64); err == nil && f > 0 {
				ev.Speed = f
			}
		case "progress":
			ev.End = v == "end"
			if v == "end" || v == "continue" {
				onEvent(ev)
				ev, haveUS = Event{}, false
			}
		}
	}
	return sc.Err()
}

func parseClock(v string) (float64, bool) {
	parts := strings.Split(v, ":")
	if len(parts) != 3 || strings.HasPrefix(v, "-") {
		return 0, false
	}
	var total float64
	for _, p := range parts {
		f, err := strconv.ParseFloat(p, 64)
		if err != nil || f < 0 {
			return 0, false
		}
		total = total*60 + f
	}
	return total, true
}
