package tui

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/exp/teatest"
)

func TestSessionGolden(t *testing.T) {
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
	frames := make(map[string]string)
	for _, plain := range []bool{false, true} {
		name := "color"
		if plain {
			name = "plain"
		}
		for _, frame := range []struct {
			name                  string
			mode                  Mode
			height, width, enters int
			keys                  []tea.Msg
			step                  string
		}{
			{"setup_start", ModeSetup, 24, 120, 0, nil, "step 1/11"},
			{"setup_resize", ModeSetup, 18, 100, 1, []tea.Msg{sessionKey(tea.KeyDown)}, "step 2/11"},
			{"setup_review", ModeSetup, 24, 120, 10, nil, "step 11/11"},
			{"setup_error", ModeSetup, 24, 120, 10, []tea.Msg{sessionKey(tea.KeyEnter)}, "step 11/11"},
			{"preset_description", ModePreset, 20, 120, 1, []tea.Msg{sessionKey(tea.KeyCtrlU), sessionType("A small file for sharing")}, "step 2/10"},
			{"preset_review", ModePreset, 24, 120, 9, nil, "step 10/10"},
		} {
			t.Run(name+"/"+frame.name, func(t *testing.T) {
				s := sessionFixture(frame.mode)
				if frame.mode == ModeSetup {
					n := int64(20_000_000)
					s.TargetBytes = &n
				}
				if frame.name == "setup_error" {
					s.Settings.Codec, s.Settings.Container, s.Settings.Preset = "hevc", "webm", "fast"
				}
				m := newSessionModel(s, SessionOptions{Terminal: !plain, Out: &bytes.Buffer{}})
				tm := teatest.NewTestModel(t, m, teatest.WithInitialTermSize(120, 24))
				teatest.WaitFor(t, tm.Output(), func(out []byte) bool { return bytes.Contains(out, []byte("step 1/")) })
				for i := 0; i < frame.enters; i++ {
					tm.Send(sessionKey(tea.KeyEnter))
				}
				for _, key := range frame.keys {
					tm.Send(key)
				}
				tm.Send(tea.WindowSizeMsg{Width: frame.width, Height: frame.height})
				teatest.WaitFor(t, tm.Output(), func(out []byte) bool { return bytes.Contains(out, []byte(frame.step)) })
				tm.Send(sessionKey(tea.KeyEsc))
				final := tm.FinalModel(t, teatest.WithFinalTimeout(time.Second)).(*sessionModel)
				view := final.View()
				if plain {
					if view != frames[frame.name] {
						t.Fatal("plain and color frames have different text")
					}
				} else {
					frames[frame.name] = ansi.Strip(view)
				}
				if strings.Contains(view, "\x1b") == plain {
					t.Fatal("incorrect frame color profile")
				}
				lines := strings.Split(ansi.Strip(view), "\n")
				if len(lines) != frame.height || final.width != frame.width {
					t.Fatal("resize did not redraw the frame")
				}
				footer := lines[len(lines)-1]
				if !strings.Contains(footer, frame.step) || !strings.Contains(footer, "preset Default") || !strings.Contains(footer, "Esc cancel") {
					t.Fatalf("bottom context bar: %q", footer)
				}
				teatest.RequireEqualOutput(t, []byte(view+"\n"))
				out, err := io.ReadAll(tm.FinalOutput(t))
				if err != nil || len(out) == 0 {
					t.Fatalf("missing Bubble Tea output: %v", err)
				}
			})
		}
	}
}
