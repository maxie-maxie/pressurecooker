package console

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestFormatDuration(t *testing.T) {
	for d, want := range map[time.Duration]string{
		0: "[00:00]", 65 * time.Second: "[01:05]", 125*time.Minute + 4*time.Second: "[125:04]", -time.Second: "[00:00]",
	} {
		if got := FormatDuration(d); got != want {
			t.Errorf("%v: got %s want %s", d, got, want)
		}
	}
}

func parse(t *testing.T, s string) []Event {
	var evs []Event
	if err := ParseProgress(strings.NewReader(s), func(e Event) { evs = append(evs, e) }); err != nil {
		t.Fatal(err)
	}
	return evs
}

func TestParseProgress(t *testing.T) {
	evs := parse(t, "speed=N/A\nout_time=N/A\nout_time_us=-5\nprogress=continue\n"+
		"progress=continue\nspeed=1.25x\nout_time_ms=2500000\nout_time=00:00:09.000000\nframe=3\nprogress=continue\n"+
		"out_time=00:01:02.500000\nspeed= 2x\nprogress=end\nout_time_us=1\n")
	if len(evs) != 4 {
		t.Fatalf("got %d events", len(evs))
	}
	if evs[0].Timeline != 0 || evs[0].Speed != 0 || evs[0].End {
		t.Errorf("ev0 %+v", evs[0])
	}
	if evs[1] != (Event{}) {
		t.Errorf("ev1 %+v", evs[1])
	}
	if evs[2].Timeline != 2.5 || evs[2].Speed != 1.25 || evs[2].End {
		t.Errorf("ev2 %+v", evs[2])
	}
	if evs[3].Timeline != 62.5 || evs[3].Speed != 2 || !evs[3].End {
		t.Errorf("ev3 %+v", evs[3])
	}
}

func TestEventQuiet(t *testing.T) {
	var b bytes.Buffer
	w := New(&b, false)
	for i := 0; i < 3; i++ {
		w.Event(Event{Stage: "Pass 1/2", Timeline: float64(i), Duration: 10})
	}
	w.Event(Event{Stage: "Pass 2/2"})
	w.Event(Event{Trial: 2, Trials: 5})
	if got, want := b.String(), "Pass 1/2\nPass 2/2\ntrial 2/5\n"; got != want {
		t.Errorf("got %q want %q", got, want)
	}
}

func TestEventTerminalAlwaysOn(t *testing.T) {
	var b bytes.Buffer
	w := New(&b, true)
	w.color = false
	base := time.Unix(0, 0)
	now := base
	w.now = func() time.Time { return now }
	w.Event(Event{Stage: "Pass 1/2", Timeline: 5, Duration: 10, Speed: 2})
	now = base.Add(65 * time.Second)
	w.Event(Event{Stage: "Pass 1/2", Timeline: 12, Duration: 10, End: true})
	want := "\rPass 1/2 [##########----------]  50% elapsed [00:00] eta [00:02]" +
		"\rPass 1/2 [####################] 100% elapsed [01:05]\n"
	if b.String() != want {
		t.Errorf("got %q want %q", b.String(), want)
	}
	if strings.Contains(b.String(), "\x1b") {
		t.Error("ansi in plain output")
	}
}

func TestEventTrialNoPercent(t *testing.T) {
	var b bytes.Buffer
	w := New(&b, true)
	w.color = false
	w.Event(Event{Trial: 1, Trials: 4})
	if got := b.String(); strings.Contains(got, "%") || !strings.HasPrefix(got, "\rtrial 1/4 ") {
		t.Errorf("got %q", got)
	}
}

var diags = []Diagnostic{{"error", "bad"}, {"warning", "w1"}, {"warning", "w2"}}

func TestFooterPlain(t *testing.T) {
	var b bytes.Buffer
	New(&b, false).Footer(diags)
	if want := "warning: w1\nwarning: w2\nerror: bad\n"; b.String() != want {
		t.Errorf("got %q", b.String())
	}
}

func TestFooterNoColorEnv(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	var b bytes.Buffer
	New(&b, true).Footer(diags)
	if strings.Contains(b.String(), "\x1b[") || !strings.HasSuffix(b.String(), "error: bad\n") {
		t.Errorf("got %q", b.String())
	}
}

func TestFooterColor(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	// os.Unsetenv via Setenv cleanup is not possible; emulate by constructing directly.
	var b bytes.Buffer
	w := &Writer{out: &b, color: true}
	w.Footer(diags)
	want := "\x1b[33mwarning: w1\x1b[0m\n\x1b[33mwarning: w2\x1b[0m\n\x1b[31merror: bad\x1b[0m\n"
	if b.String() != want {
		t.Errorf("got %q", b.String())
	}
}

func TestPromptPlainAndColor(t *testing.T) {
	var plain bytes.Buffer
	New(&plain, false).Prompt("Continue? [y/N]")
	if got, want := plain.String(), "Continue? [y/N]"; got != want {
		t.Fatalf("plain prompt: got %q want %q", got, want)
	}

	var colored bytes.Buffer
	w := &Writer{out: &colored, color: true, terminal: true, now: time.Now}
	w.Prompt("Continue? [y/N]")
	if got, want := colored.String(), yellow+"Continue? [y/N]"+reset; got != want {
		t.Fatalf("color prompt: got %q want %q", got, want)
	}
}

func TestPromptEndsProgressAndStaysOnOneLine(t *testing.T) {
	var b bytes.Buffer
	w := New(&b, true)
	w.color = false
	w.Event(Event{Stage: "Encode", Timeline: 1, Duration: 2})
	w.Prompt("First\r\nSecond\nThird\r  ")
	if got := b.String(); !strings.HasSuffix(got, "\nFirst; Second; Third;") {
		t.Fatalf("got %q", got)
	}
}

func TestFooterCollapsesNewlines(t *testing.T) {
	var b bytes.Buffer
	New(&b, false).Footer([]Diagnostic{
		{Severity: "error", Message: "bad\rline  "},
		{Severity: "warning", Message: "one\r\ntwo\nthree\t  "},
	})
	if got, want := b.String(), "warning: one; two; three\nerror: bad; line\n"; got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestResultGolden(t *testing.T) {
	var b bytes.Buffer
	New(&b, false).Result(Result{
		Path: "out.mp4", Container: "mp4", Codec: "h264", Encoder: "libx264", RateControl: "crf 23",
		Dimensions: "1280x720", Audio: "aac 128k", TargetFit: "none", Bytes: 19450000, FPS: 29.97,
		Elapsed: 125*time.Minute + 4*time.Second, Metric: "vmaf", MetricVersion: "v2.3.1",
	})
	want := `Output:       out.mp4
Size:         19.45 MB (19450000 bytes)
Format:       mp4 / h264 / libx264
Quality:      crf 23
Video:        1280x720 @ 29.97 fps
Audio:        aac 128k
Target fit:   none
Metric:       vmaf v2.3.1
Elapsed:      [125:04]
`
	if b.String() != want {
		t.Errorf("got\n%s", b.String())
	}
	for _, bad := range []string{"=", "budget", "formula", "\x1b"} {
		if strings.Contains(b.String(), bad) {
			t.Errorf("contains %q", bad)
		}
	}
}

func TestResultNoMetric(t *testing.T) {
	var b bytes.Buffer
	New(&b, false).Result(Result{})
	if strings.Contains(b.String(), "Metric") {
		t.Error("metric line without metric")
	}
}
