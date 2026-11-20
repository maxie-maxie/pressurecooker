// Package console owns all terminal output: stage lines, progress, results and diagnostics.
package console

import (
	"fmt"
	"io"
	"os"
	"strings"
	"time"
	"unicode"
)

type Event struct {
	Stage                     string
	Timeline, Duration, Speed float64 // seconds; speed is multiplier
	Trial, Trials             int
	End                       bool // FFmpeg progress end; not publication completion
}

type Diagnostic struct {
	Severity string
	Message  string
}

type Result struct {
	Path, Container, Codec, Encoder, RateControl, Dimensions, Audio, TargetFit string
	Bytes                                                                      int64
	FPS                                                                        float64
	Elapsed                                                                    time.Duration
	Metric, MetricVersion                                                      string
}

type Writer struct {
	out      io.Writer
	terminal bool
	color    bool
	stage    string
	started  time.Time
	open     bool // an unterminated progress line is on screen
	now      func() time.Time
}

const (
	yellow = "\x1b[33m"
	red    = "\x1b[31m"
	reset  = "\x1b[0m"
)

// New builds a Writer; color needs a terminal and no NO_COLOR variable.
func New(out io.Writer, terminal bool) *Writer {
	w := &Writer{out: out, terminal: terminal, now: time.Now}
	if terminal {
		w.color = true
		if _, set := os.LookupEnv("NO_COLOR"); set {
			w.color = false
		}
		if w.color {
			if f, ok := out.(*os.File); ok && !enableVT(f) {
				w.color = false
			}
		}
	}
	return w
}

func (w *Writer) endProgress() {
	if w.open {
		fmt.Fprintln(w.out)
		w.open = false
	}
}

func (w *Writer) Line(s string) {
	w.endProgress()
	fmt.Fprintln(w.out, s)
}

func stageLabel(ev Event) string {
	if ev.Trial > 0 && ev.Trials > 0 {
		return fmt.Sprintf("trial %d/%d", ev.Trial, ev.Trials)
	}
	return ev.Stage
}

func (w *Writer) Event(ev Event) {
	label := stageLabel(ev)
	changed := label != w.stage
	if changed {
		w.stage = label
		w.started = w.now()
	}
	if !w.terminal {
		if changed && label != "" {
			w.Line(label)
		}
		return
	}
	line := w.progressLine(label, ev)
	clr := "\r"
	if w.color {
		clr = "\r\x1b[K"
	}
	if !ev.End {
		if changed {
			w.endProgress()
		}
		fmt.Fprint(w.out, clr+line)
		w.open = true
		return
	}
	fmt.Fprint(w.out, clr)
	w.open = false
	fmt.Fprintln(w.out, line)
}

func (w *Writer) progressLine(label string, ev Event) string {
	elapsed := w.now().Sub(w.started)
	var b strings.Builder
	if label != "" {
		b.WriteString(label + " ")
	}
	if ev.Duration > 0 {
		p := ev.Timeline / ev.Duration
		p = min(max(p, 0), 1)
		if ev.End {
			p = 1
		}
		n := int(p*20 + 0.5)
		fmt.Fprintf(&b, "[%s%s] %3d%% ", strings.Repeat("#", n), strings.Repeat("-", 20-n), int(p*100+0.5))
	}
	b.WriteString("elapsed " + FormatDuration(elapsed))
	if ev.Duration > 0 && ev.Speed > 0 && !ev.End {
		rem := max(ev.Duration-ev.Timeline, 0) / ev.Speed
		b.WriteString(" eta " + FormatDuration(time.Duration(rem*float64(time.Second))))
	}
	return b.String()
}

func (w *Writer) Result(r Result) {
	w.endProgress()
	mb := float64(r.Bytes) / 1e6
	fmt.Fprintf(w.out, "Output:       %s\n", r.Path)
	fmt.Fprintf(w.out, "Size:         %.2f MB (%d bytes)\n", mb, r.Bytes)
	fmt.Fprintf(w.out, "Format:       %s / %s / %s\n", r.Container, r.Codec, r.Encoder)
	fmt.Fprintf(w.out, "Quality:      %s\n", r.RateControl)
	fmt.Fprintf(w.out, "Video:        %s @ %s fps\n", r.Dimensions, trimFloat(r.FPS))
	fmt.Fprintf(w.out, "Audio:        %s\n", r.Audio)
	fmt.Fprintf(w.out, "Target fit:   %s\n", r.TargetFit)
	if r.Metric != "" {
		m := r.Metric
		if r.MetricVersion != "" {
			m += " " + r.MetricVersion
		}
		fmt.Fprintf(w.out, "Metric:       %s\n", m)
	}
	fmt.Fprintf(w.out, "Elapsed:      %s\n", FormatDuration(r.Elapsed))
}

func trimFloat(f float64) string {
	return strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.3f", f), "0"), ".")
}

// Footer prints warnings first, then errors, so the last line is an error when one exists.
func (w *Writer) Footer(diags []Diagnostic) {
	w.endProgress()
	for _, sev := range []string{"warning", "error"} {
		for _, d := range diags {
			if strings.ToLower(d.Severity) != sev {
				continue
			}
			line := sev + ": " + oneLine(d.Message)
			if w.color {
				c := yellow
				if sev == "error" {
					c = red
				}
				line = c + line + reset
			}
			fmt.Fprintln(w.out, line)
		}
	}
}

func oneLine(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "; ")
	s = strings.NewReplacer("\n", "; ", "\r", "; ").Replace(s)
	return strings.TrimRightFunc(s, unicode.IsSpace)
}

// FormatDuration renders [mm:ss] with unbounded minutes.
func FormatDuration(d time.Duration) string {
	s := int64(max(d, 0) / time.Second)
	return fmt.Sprintf("[%02d:%02d]", s/60, s%60)
}

// Prompt writes a single yellow prompt line with no trailing newline.
func (w *Writer) Prompt(question string) {
	w.endProgress()
	question = oneLine(question)
	if w.color {
		fmt.Fprint(w.out, yellow+question+reset)
		return
	}
	fmt.Fprint(w.out, question)
}
