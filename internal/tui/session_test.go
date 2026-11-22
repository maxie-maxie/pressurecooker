package tui

import (
	"bytes"
	"context"
	"errors"
	"io"
	"math"
	"os"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/maxie-maxie/pressurecooker/internal/config"
)

func sessionFixture(mode Mode) Session {
	p := config.FactoryPreset()
	return Session{Mode: mode, Preset: p, Settings: p.Settings()}
}

func sessionKey(kind tea.KeyType) tea.KeyMsg { return tea.KeyMsg{Type: kind} }

func sessionType(s string) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

func sessionFeed(m *sessionModel, messages ...tea.Msg) {
	for _, msg := range messages {
		m.Update(msg)
	}
}

func sessionReach(t *testing.T, m *sessionModel, key string) {
	t.Helper()
	for attempts := 0; m.key() != key; attempts++ {
		if attempts >= len(m.steps) || m.status != statusRunning {
			t.Fatalf("cannot reach %s from %s: %s", key, m.key(), m.msg)
		}
		m.Update(sessionKey(tea.KeyEnter))
	}
}

func sessionEdit(t *testing.T, m *sessionModel, key, value string) {
	t.Helper()
	if m.key() == "review" {
		want := 0
		for i, field := range m.steps {
			if field == key {
				want = i + 1
			}
		}
		if want == 0 {
			t.Fatalf("unknown field %s", key)
		}
		for m.cur != want {
			m.Update(sessionKey(tea.KeyDown))
		}
		m.Update(sessionKey(tea.KeyEnter))
	} else {
		sessionReach(t, m, key)
	}
	sessionFeed(m, sessionKey(tea.KeyCtrlU), sessionType(value), sessionKey(tea.KeyEnter))
}

func sessionConfirm(m *sessionModel) {
	for m.cur != 0 {
		m.Update(sessionKey(tea.KeyUp))
	}
	m.Update(sessionKey(tea.KeyEnter))
}

func TestSessionStepsAndUnchangedPrefill(t *testing.T) {
	for _, mode := range []Mode{ModeSetup, ModePreset} {
		t.Run(map[Mode]string{ModeSetup: "setup", ModePreset: "preset"}[mode], func(t *testing.T) {
			m := newSessionModel(sessionFixture(mode), SessionOptions{})
			want := []string{"start", "codec", "container", "preset", "quality", "audio", "fps", "resolution", "hw", "target", "review"}
			if mode == ModePreset {
				want = []string{"label", "description", "codec", "container", "preset", "quality", "audio", "fps", "resolution", "review"}
			}
			if !reflect.DeepEqual(m.steps, want) {
				t.Fatalf("steps %v", m.steps)
			}
			for i, key := range want {
				if m.key() != key || !strings.Contains(m.View(), "step "+fmtStep(i+1, len(want))) {
					t.Fatalf("missing step %d: %s", i, m.View())
				}
				m.Update(sessionKey(tea.KeyEnter))
			}
			got, err := sessionOutcome(m)
			if err != nil || got == nil || !reflect.DeepEqual(got.Settings, sessionFixture(mode).Settings) || len(got.Edited) != 0 {
				t.Fatalf("unchanged prefill: %+v, %v", got, err)
			}
		})
	}
}

func fmtStep(current, total int) string {
	return strconv.Itoa(current) + "/" + strconv.Itoa(total)
}

func TestSessionStartPresetSelection(t *testing.T) {
	one, two := config.FactoryPreset(), config.FactoryPreset()
	two.ID, two.Label, two.Codec, two.Speed, two.Quality = 2, "Fast H264", "h264", "fast", "small"
	s := sessionFixture(ModeSetup)
	s.Preset, s.Settings = two, two.Settings()
	s.Settings.HW = "off"
	n := int64(12_500_000)
	s.TargetBytes = &n
	m := newSessionModel(s, SessionOptions{Presets: []config.Preset{one, two}})
	if m.cur != 1 || !strings.Contains(m.View(), "preset Fast H264") {
		t.Fatalf("preselection: %s", m.View())
	}
	sessionFeed(m, sessionKey(tea.KeyUp), sessionKey(tea.KeyEnter))
	if m.work.Preset.ID != 1 || m.work.Settings.Codec != "av1" || m.work.Settings.Preset != "8" || m.work.Settings.HW != "off" || *m.work.TargetBytes != n {
		t.Fatalf("start-from values: %+v", m.work)
	}
	if !m.editedFields()["codec"] || !m.editedFields()["preset"] || !m.editedFields()["quality"] || m.editedFields()["target"] {
		t.Fatalf("edited fields: %v", m.editedFields())
	}
	sessionFeed(m, sessionKey(tea.KeyShiftTab), sessionKey(tea.KeyDown), sessionKey(tea.KeyEnter))
	if len(m.editedFields()) != 0 {
		t.Fatalf("reverted preset: %v", m.editedFields())
	}
	s.Preset.ID = 99
	fallback := newSessionModel(s, SessionOptions{Presets: []config.Preset{one, two}})
	if fallback.cur != 0 || fallback.work.Preset.ID != one.ID || fallback.work.Settings.Codec != s.Settings.Codec {
		t.Fatal("missing preset should select the first and keep prefilled settings")
	}
}

func TestSessionBackAndEditedTracking(t *testing.T) {
	s := sessionFixture(ModeSetup)
	s.Edited = map[string]bool{"fps": true, "audio": false}
	m := newSessionModel(s, SessionOptions{})
	sessionReach(t, m, "codec")
	sessionFeed(m, sessionKey(tea.KeyDown), sessionKey(tea.KeyEnter))
	if m.key() != "container" || !m.editedFields()["codec"] {
		t.Fatal("codec selection did not apply")
	}
	sessionFeed(m, sessionKey(tea.KeyLeft), sessionKey(tea.KeyUp), sessionKey(tea.KeyEnter))
	if m.editedFields()["codec"] || !m.editedFields()["fps"] || s.Edited["codec"] {
		t.Fatalf("reverting should keep only initial edits: %v", m.editedFields())
	}
	sessionFeed(m, sessionKey(tea.KeyShiftTab), sessionKey(tea.KeyShiftTab), sessionKey(tea.KeyShiftTab))
	if m.idx != 0 {
		t.Fatal("back moved past the first step")
	}
	p := newSessionModel(sessionFixture(ModePreset), SessionOptions{})
	sessionFeed(p, sessionKey(tea.KeyEnter), sessionKey(tea.KeyLeft))
	if p.key() != "description" {
		t.Fatal("Left should not leave a text field")
	}
	p.Update(sessionKey(tea.KeyShiftTab))
	if p.key() != "label" {
		t.Fatal("Shift+Tab must go back from a text field")
	}
	sessionReach(t, m, "review")
	m.Update(sessionKey(tea.KeyLeft))
	if m.key() != "target" {
		t.Fatal("review back should reach size")
	}
}

func TestSessionQualityAudioAndCaps(t *testing.T) {
	m := newSessionModel(sessionFixture(ModeSetup), SessionOptions{})
	sessionEdit(t, m, "quality", "28")
	if m.work.Settings.CRF == nil || *m.work.Settings.CRF != 28 || m.work.Settings.Quality != "" || !m.editedFields()["quality"] || !m.editedFields()["crf"] {
		t.Fatalf("raw CRF: %+v", m.work.Settings)
	}
	m.Update(sessionKey(tea.KeyShiftTab))
	sessionFeed(m, sessionKey(tea.KeyDown), sessionKey(tea.KeyEnter))
	if m.work.Settings.CRF != nil || m.work.Settings.Quality != "balanced" || m.editedFields()["quality"] || m.editedFields()["crf"] {
		t.Fatal("named quality must clear CRF and restore the base")
	}
	sessionEdit(t, m, "audio", "73")
	if m.work.Settings.AudioSource || m.work.Settings.AudioKbps != 73 || !m.editedFields()["audio"] {
		t.Fatal("numeric audio must disable source audio")
	}
	m.Update(sessionKey(tea.KeyShiftTab))
	sessionFeed(m, sessionKey(tea.KeyDown), sessionKey(tea.KeyUp), sessionKey(tea.KeyEnter))
	if !m.work.Settings.AudioSource || m.editedFields()["audio"] {
		t.Fatal("source audio must ignore the inactive bitrate")
	}
	sessionEdit(t, m, "fps", "29.97")
	sessionEdit(t, m, "resolution", "1920x1080")
	if *m.work.Settings.FPS != 29.97 || m.work.Settings.Resolution != "1920x1080" {
		t.Fatal("custom caps were lost")
	}
	sessionReach(t, m, "review")
	sessionEdit(t, m, "fps", "source")
	sessionEdit(t, m, "resolution", "source")
	if m.key() != "review" || m.work.Settings.FPS != nil || m.work.Settings.Resolution != "" {
		t.Fatal("review edits must return to review with source caps")
	}
}

func TestSessionSizeModesAndRange(t *testing.T) {
	n := int64(12_500_000)
	s := sessionFixture(ModeSetup)
	s.TargetBytes = &n
	m := newSessionModel(s, SessionOptions{})
	sessionReach(t, m, "target")
	if m.text != "12.5" {
		t.Fatalf("size prefill: %q", m.text)
	}
	m.Update(sessionKey(tea.KeyEnter))
	if *m.work.TargetBytes != n || m.editedFields()["target"] {
		t.Fatal("Enter should keep the prefilled size")
	}
	sessionEdit(t, m, "target", "")
	if m.work.TargetBytes != nil || !m.editedFields()["target"] || !strings.Contains(m.View(), "Size: Conversion") {
		t.Fatal("empty size should select Conversion")
	}
	sessionEdit(t, m, "target", "12.5")
	if *m.work.TargetBytes != n || m.editedFields()["target"] || !strings.Contains(m.View(), "Default (12.5 MB)") {
		t.Fatal("decimal size should select Default and clear a reverted edit")
	}
	for _, test := range []struct {
		input string
		want  int64
	}{
		{"0.0000006", 1}, {"20.125", 20_125_000}, {"9223372036854.775807", math.MaxInt64},
	} {
		sessionEdit(t, m, "target", test.input)
		if m.key() != "review" || *m.work.TargetBytes != test.want {
			t.Fatalf("size %s: %+v, %s", test.input, m.work.TargetBytes, m.msg)
		}
	}
}

func TestSessionInvalidInputStaysOnField(t *testing.T) {
	for _, test := range []struct {
		key    string
		values []string
	}{
		{"codec", []string{"vp9"}}, {"container", []string{"avi"}}, {"preset", []string{"14", "quick"}},
		{"quality", []string{"NaN", "Inf", "0", "64"}}, {"audio", []string{"0", "-1", "1.5", "abc"}},
		{"fps", []string{"0", "-1", "1001", "NaN", "Inf"}},
		{"resolution", []string{"641x480", "0x720", "16386x1080", "abc"}},
		{"hw", []string{"nvenc"}}, {"target", []string{"0", "-1", "NaN", "Inf", "1e1000", "1/2", "0.0000001", "9223372036854.775808"}},
	} {
		t.Run(test.key, func(t *testing.T) {
			m := newSessionModel(sessionFixture(ModeSetup), SessionOptions{})
			sessionReach(t, m, test.key)
			before := cloneSession(m.work)
			for _, value := range test.values {
				sessionEdit(t, m, test.key, value)
				if m.key() != test.key || m.msg == "" || !reflect.DeepEqual(before, m.work) {
					t.Fatalf("%q should reprompt without changing values: %+v (%s)", value, m.work, m.msg)
				}
			}
		})
	}
}

func TestSessionPresetLabelAndDescription(t *testing.T) {
	s := sessionFixture(ModePreset)
	s.Preset.Label, s.Preset.Description = "", ""
	m := newSessionModel(s, SessionOptions{})
	for _, bad := range []string{"", " ", strings.Repeat("ก", 41)} {
		sessionEdit(t, m, "label", bad)
		if m.key() != "label" || m.msg == "" {
			t.Fatal("invalid label advanced")
		}
	}
	label := strings.Repeat("ก", 40)
	sessionEdit(t, m, "label", label)
	sessionEdit(t, m, "description", strings.Repeat("ก", 121))
	if m.key() != "description" || m.msg == "" {
		t.Fatal("long description advanced")
	}
	description := strings.Repeat("ก", 120)
	sessionEdit(t, m, "description", description)
	sessionReach(t, m, "review")
	sessionEdit(t, m, "description", "")
	sessionEdit(t, m, "quality", "32")
	sessionEdit(t, m, "audio", "96")
	sessionConfirm(m)
	got, err := sessionOutcome(m)
	if err != nil || got == nil {
		t.Fatalf("confirmation: %v, %s", err, m.msg)
	}
	if got.Preset.Label != label || got.Preset.Description != "" || got.Preset.Quality != "" || *got.Preset.CRF != 32 || *got.Preset.AudioKbps != 96 || !got.Edited["label"] {
		t.Fatalf("recorded preset: %+v", got)
	}
	if err := config.ValidatePreset(got.Preset); err != nil || !reflect.DeepEqual(got.Settings, got.Preset.Settings()) {
		t.Fatalf("preset settings: %+v, %v", got, err)
	}
	if s.Preset.Label != "" || s.Preset.CRF != nil {
		t.Fatal("recorder mutated its input")
	}
}

func TestSessionRecorderCustomFields(t *testing.T) {
	s := sessionFixture(ModePreset)
	s.Preset.ID = 0
	m := newSessionModel(s, SessionOptions{})
	for _, field := range []struct{ key, value string }{
		{"label", "Share"}, {"description", "Clips for sharing"}, {"codec", "h264"},
		{"container", "mkv"}, {"preset", "fast"}, {"quality", "0"},
		{"audio", "source"}, {"fps", "1000"}, {"resolution", "16384x16384"},
	} {
		sessionEdit(t, m, field.key, field.value)
		if m.msg != "" {
			t.Fatalf("%s: %s", field.key, m.msg)
		}
	}
	sessionConfirm(m)
	got, err := sessionOutcome(m)
	if err != nil || got == nil {
		t.Fatalf("custom recorder: %v, %s", err, m.msg)
	}
	p := got.Preset
	if p.ID != 0 || p.Codec != "h264" || p.Container != "mkv" || p.Speed != "fast" || p.Quality != "" || *p.CRF != 0 || p.AudioKbps != nil || *p.FPS != 1000 || p.Resolution != "16384x16384" {
		t.Fatalf("custom preset: %+v", p)
	}
	if !reflect.DeepEqual(got.Settings, p.Settings()) || got.Settings.HW != "auto" {
		t.Fatal("preset output settings must use automatic hardware")
	}
	*got.Preset.CRF, *got.Preset.FPS = 1, 30
	if *got.Settings.CRF != 0 || *got.Settings.FPS != 1000 || m.work.Settings.Codec != "h264" || s.Settings.Codec != "av1" {
		t.Fatal("result or input pointers are aliased")
	}
}

func TestSessionReviewBlocksInvalidCombinations(t *testing.T) {
	for _, mode := range []Mode{ModeSetup, ModePreset} {
		for _, test := range []struct {
			name, field, fix, errorText string
			alter                       func(*config.Settings)
		}{
			{"webm", "codec", "av1", "webm", func(s *config.Settings) { s.Codec, s.Container, s.Preset = "hevc", "webm", "fast" }},
			{"speed", "preset", "fast", "preset for hevc", func(s *config.Settings) { s.Codec = "hevc" }},
			{"xor", "quality", "high", "exactly one", func(s *config.Settings) { n := 28.0; s.CRF = &n }},
		} {
			t.Run(fmtStep(int(mode), 2)+test.name, func(t *testing.T) {
				s := sessionFixture(mode)
				test.alter(&s.Settings)
				if mode == ModePreset {
					s.Preset.Codec = ""
				}
				m := newSessionModel(s, SessionOptions{})
				sessionReach(t, m, "review")
				sessionConfirm(m)
				if m.status != statusRunning || !strings.Contains(m.msg, test.errorText) || !strings.Contains(m.View(), "Error:") {
					t.Fatalf("invalid review: %d, %s", m.status, m.msg)
				}
				sessionEdit(t, m, test.field, test.fix)
				sessionConfirm(m)
				if m.status != statusConfirmed {
					t.Fatalf("fixed review: %s", m.msg)
				}
			})
		}
	}
	for _, hw := range []string{"vulkan", "qsv"} {
		m := newSessionModel(sessionFixture(ModeSetup), SessionOptions{})
		sessionEdit(t, m, "quality", "28")
		sessionEdit(t, m, "hw", hw)
		sessionReach(t, m, "review")
		sessionConfirm(m)
		if m.status != statusRunning || !strings.Contains(m.msg, "raw CRF is unsupported on "+hw) {
			t.Fatalf("hardware CRF: %s", m.msg)
		}
		sessionEdit(t, m, "hw", "off")
		sessionConfirm(m)
		if m.status != statusConfirmed {
			t.Fatalf("software CRF: %s", m.msg)
		}
	}
}

func TestSessionCancellationKeys(t *testing.T) {
	for _, mode := range []Mode{ModeSetup, ModePreset} {
		for _, review := range []bool{false, true} {
			for _, key := range []tea.KeyType{tea.KeyEsc, tea.KeyCtrlC} {
				m := newSessionModel(sessionFixture(mode), SessionOptions{})
				if review {
					sessionReach(t, m, "review")
				}
				_, cmd := m.Update(sessionKey(key))
				got, err := sessionOutcome(m)
				wantErr := error(nil)
				if key == tea.KeyCtrlC {
					wantErr = ErrInterrupted
				}
				if got != nil || err != wantErr || cmd == nil {
					t.Fatalf("cancellation: %+v, %v", got, err)
				}
			}
		}
	}
}

func TestSessionPlainDetection(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	old, set := os.LookupEnv("NO_COLOR")
	os.Unsetenv("NO_COLOR")
	t.Cleanup(func() {
		if set {
			os.Setenv("NO_COLOR", old)
		} else {
			os.Unsetenv("NO_COLOR")
		}
	})
	o := SessionOptions{Terminal: true, Out: &bytes.Buffer{}}
	if sessionPlain(o) {
		t.Fatal("terminal buffers must support color")
	}
	o.Terminal = false
	if !sessionPlain(o) {
		t.Fatal("non-terminal option must be plain")
	}
	o.Terminal = true
	t.Setenv("NO_COLOR", "")
	if !sessionPlain(o) {
		t.Fatal("empty NO_COLOR must be plain")
	}
	os.Unsetenv("NO_COLOR")
	t.Setenv("TERM", "dumb")
	if !sessionPlain(o) {
		t.Fatal("dumb terminal must be plain")
	}
	t.Setenv("TERM", "xterm-256color")
	f, err := os.CreateTemp(t.TempDir(), "output")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	o.Out = f
	if !sessionPlain(o) {
		t.Fatal("regular output files lack VT support")
	}
}

func TestSessionLayoutResizeAndSafeText(t *testing.T) {
	s := sessionFixture(ModeSetup)
	s.Preset.Label = "Default\n\x1b[31m"
	m := newSessionModel(s, SessionOptions{Presets: []config.Preset{s.Preset}})
	for _, size := range []tea.WindowSizeMsg{{Width: 120, Height: 24}, {Width: 80, Height: 16}, {Width: 60, Height: 10}, {Width: 30, Height: 1}} {
		m.Update(size)
		view := m.View()
		lines := strings.Split(view, "\n")
		if len(lines) != size.Height || strings.Contains(view, "\x1b") {
			t.Fatalf("plain frame dimensions or escapes: %q", view)
		}
		for _, line := range lines {
			if ansi.StringWidth(line) > size.Width {
				t.Fatalf("wrapped line: %q", line)
			}
		}
		if size.Width >= 60 && !strings.Contains(lines[len(lines)-1], "step 1/11") {
			t.Fatal("context bar must be the last row")
		}
	}
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 12})
	sessionReach(t, m, "preset")
	for i := 0; i < 5; i++ {
		m.Update(sessionKey(tea.KeyDown))
	}
	if !strings.Contains(m.View(), "> 13\n") {
		t.Fatalf("selected item must remain visible: %s", m.View())
	}
}

func TestRunSessionNonTerminal(t *testing.T) {
	for _, test := range []struct {
		name, input          string
		mode                 Mode
		confirm, interrupted bool
	}{
		{"EOF", "", ModeSetup, false, false}, {"escape", "\x1b", ModeSetup, false, false},
		{"interrupt", "\x03", ModeSetup, false, true},
		{"setup", strings.Repeat("\r", 11), ModeSetup, true, false},
		{"CRLF", strings.Repeat("\r\n", 11), ModeSetup, true, false},
		{"preset", strings.Repeat("\n", 10), ModePreset, true, false},
		{"typed preset", "\x15Portable\n" + strings.Repeat("\n", 9), ModePreset, true, false},
		{"navigation", "\x1b[B\x1b[A" + strings.Repeat("\r", 11), ModeSetup, true, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			var out bytes.Buffer
			got, err := RunSession(ctx, sessionFixture(test.mode), SessionOptions{In: strings.NewReader(test.input), Out: &out})
			if (got != nil) != test.confirm || errors.Is(err, ErrInterrupted) != test.interrupted || (err != nil && !test.interrupted) {
				t.Fatalf("result: %+v, %v", got, err)
			}
			if strings.Contains(out.String(), "\x1b") {
				t.Fatal("plain RunSession emitted ANSI")
			}
			if test.name == "typed preset" && got.Preset.Label != "Portable" {
				t.Fatal("pipe text or Ctrl+U was not decoded")
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got, err := RunSession(ctx, sessionFixture(ModeSetup), SessionOptions{}); got != nil || !errors.Is(err, ErrInterrupted) {
		t.Fatalf("cancelled context: %+v, %v", got, err)
	}
	if got, err := RunSession(nil, sessionFixture(ModeSetup), SessionOptions{}); got != nil || err != nil {
		t.Fatalf("nil input EOF: %+v, %v", got, err)
	}
}

func TestRunSessionOpenPipeCancellation(t *testing.T) {
	for _, input := range []string{"\x1b", "\x03", ""} {
		r, w := io.Pipe()
		started := make(chan struct{})
		reader := &sessionReadSignal{Reader: r, started: started}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		var out bytes.Buffer
		done := make(chan error, 1)
		go func() {
			got, err := RunSession(ctx, sessionFixture(ModeSetup), SessionOptions{In: reader, Out: &out, Terminal: true})
			if got != nil {
				err = errors.New("unexpected confirmation")
			}
			done <- err
		}()
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("pipe reader did not start")
		}
		if input != "" {
			if _, err := io.WriteString(w, input); err != nil {
				t.Fatal(err)
			}
		} else {
			cancel()
		}
		select {
		case err := <-done:
			want := error(nil)
			if input != "\x1b" {
				want = ErrInterrupted
			}
			if !errors.Is(err, want) {
				t.Fatalf("pipe input %q: %v", input, err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("open pipe hung")
		}
		cancel()
		r.Close()
		w.Close()
	}
}

type sessionReadSignal struct {
	io.Reader
	started chan struct{}
	once    sync.Once
}

func (r *sessionReadSignal) Read(p []byte) (int, error) {
	r.once.Do(func() { close(r.started) })
	return r.Reader.Read(p)
}

type sessionBrokenWriter struct{}

func (sessionBrokenWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestRunSessionOutputErrorAndInvalidMode(t *testing.T) {
	got, err := RunSession(nil, sessionFixture(ModeSetup), SessionOptions{Out: sessionBrokenWriter{}})
	if got != nil || !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("output error: %+v, %v", got, err)
	}
	got, err = RunSession(nil, Session{Mode: Mode(99)}, SessionOptions{})
	if got != nil || err == nil {
		t.Fatalf("invalid mode: %+v, %v", got, err)
	}
}

func TestRunSessionFilePipeEOFAndKeys(t *testing.T) {
	for _, input := range []string{"", strings.Repeat("\r", 11)} {
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(w, input); err != nil {
			t.Fatal(err)
		}
		w.Close()
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		got, err := RunSession(ctx, sessionFixture(ModeSetup), SessionOptions{In: r, Out: io.Discard, Terminal: true})
		cancel()
		r.Close()
		if err != nil || (got != nil) != (input != "") {
			t.Fatalf("file pipe: %+v, %v", got, err)
		}
	}
}
