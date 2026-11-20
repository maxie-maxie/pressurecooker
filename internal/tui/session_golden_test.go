package tui

import (
	"bytes"
	"context"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/exp/teatest"

	"github.com/maxie-maxie/pressurecooker/internal/ffmpeg"
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
			name          string
			mode          Mode
			presetZero    bool
			height, width int
			keys          []tea.Msg
			step          string
			waitText      string
			waitChecking  bool
			waitProbed    bool
			blockProbe    bool
			hasProbe      bool
			sameText      bool
		}{
			{
				name: "setup_start", mode: ModeSetup, height: 24, width: 120,
				step: "step 1/12", sameText: true,
			},
			{
				name: "setup_resize", mode: ModeSetup, height: 18, width: 100,
				keys: []tea.Msg{
					sessionKey(tea.KeyDown), sessionKey(tea.KeyEnter), // Setup manually -> codec
					sessionKey(tea.KeyDown), // hevc
				},
				step: "step 2/12", sameText: true,
			},
			{
				name: "setup_container_greyed", mode: ModeSetup, height: 24, width: 120,
				keys: []tea.Msg{
					sessionKey(tea.KeyDown), sessionKey(tea.KeyEnter), // Setup manually -> codec
					sessionKey(tea.KeyDown), sessionKey(tea.KeyEnter), // hevc -> container
				},
				step: "step 3/12",
			},
			{
				name: "setup_audio_codec_greyed", mode: ModeSetup, height: 24, width: 120,
				keys: []tea.Msg{
					sessionKey(tea.KeyDown), sessionKey(tea.KeyEnter), // Setup manually -> codec
					sessionKey(tea.KeyDown), sessionKey(tea.KeyEnter), // hevc -> container
					sessionKey(tea.KeyDown), sessionKey(tea.KeyDown), sessionKey(tea.KeyEnter), // mov -> preset
					sessionKey(tea.KeyEnter), sessionKey(tea.KeyEnter), // quality -> audio_codec
				},
				step: "step 6/12",
			},
			{
				name: "setup_hw_checking", mode: ModeSetup, height: 24, width: 120,
				keys: []tea.Msg{
					sessionKey(tea.KeyDown), sessionKey(tea.KeyEnter), // Setup manually -> codec
					sessionKey(tea.KeyEnter), sessionKey(tea.KeyEnter), sessionKey(tea.KeyEnter),
					sessionKey(tea.KeyEnter), sessionKey(tea.KeyEnter), sessionKey(tea.KeyEnter),
					sessionKey(tea.KeyEnter), sessionKey(tea.KeyEnter), // -> hw
				},
				step: "step 10/12", hasProbe: true, blockProbe: true, waitChecking: true, sameText: true,
			},
			{
				name: "setup_hw_probed", mode: ModeSetup, height: 24, width: 120,
				keys: []tea.Msg{
					sessionKey(tea.KeyDown), sessionKey(tea.KeyEnter), // Setup manually -> codec
					sessionKey(tea.KeyEnter), sessionKey(tea.KeyEnter), sessionKey(tea.KeyEnter),
					sessionKey(tea.KeyEnter), sessionKey(tea.KeyEnter), sessionKey(tea.KeyEnter),
					sessionKey(tea.KeyEnter), sessionKey(tea.KeyEnter), // -> hw
				},
				step: "step 10/12", hasProbe: true, waitProbed: true,
			},
			{
				name: "setup_prores_review", mode: ModeSetup, height: 24, width: 120,
				keys: []tea.Msg{
					sessionKey(tea.KeyDown), sessionKey(tea.KeyEnter), // Setup manually -> codec
					sessionKey(tea.KeyDown), sessionKey(tea.KeyDown), sessionKey(tea.KeyDown), sessionKey(tea.KeyDown),
					sessionKey(tea.KeyEnter), // prores -> container (reset mov)
					sessionKey(tea.KeyEnter), // -> quality (skips preset)
					sessionKey(tea.KeyEnter), // -> audio_codec (reset aac)
					sessionKey(tea.KeyEnter), // -> audio
					sessionKey(tea.KeyEnter), // -> fps
					sessionKey(tea.KeyEnter), // -> resolution
					sessionKey(tea.KeyEnter), // -> hw
					sessionKey(tea.KeyEnter), // -> review (skips target)
				},
				step: "step 12/12",
			},
			{
				name: "setup_review", mode: ModeSetup, height: 24, width: 120,
				keys: []tea.Msg{
					sessionKey(tea.KeyDown), sessionKey(tea.KeyEnter), // Setup manually -> codec
					sessionKey(tea.KeyEnter), sessionKey(tea.KeyEnter), sessionKey(tea.KeyEnter),
					sessionKey(tea.KeyEnter), sessionKey(tea.KeyEnter), sessionKey(tea.KeyEnter),
					sessionKey(tea.KeyEnter), sessionKey(tea.KeyEnter), sessionKey(tea.KeyEnter),
					sessionKey(tea.KeyEnter), // -> review
				},
				step: "step 12/12", sameText: true,
			},
			{
				name: "setup_error", mode: ModeSetup, height: 24, width: 120,
				keys: []tea.Msg{
					sessionKey(tea.KeyDown), sessionKey(tea.KeyEnter), // Setup manually -> codec
					sessionKey(tea.KeyEnter), sessionKey(tea.KeyEnter), sessionKey(tea.KeyEnter),
					sessionType("28"), sessionKey(tea.KeyEnter), // quality CRF 28 -> audio_codec
					sessionKey(tea.KeyEnter), sessionKey(tea.KeyEnter), sessionKey(tea.KeyEnter), sessionKey(tea.KeyEnter), // -> hw
					sessionKey(tea.KeyDown), sessionKey(tea.KeyEnter), // hw vulkan -> target
					sessionKey(tea.KeyEnter), // -> review
					sessionKey(tea.KeyEnter), // Confirm triggers error
				},
				step: "step 12/12", waitText: "Error:", sameText: true,
			},
			{
				name: "preset_start", mode: ModePreset, presetZero: true, height: 24, width: 120,
				step: "step 1/12", sameText: true,
			},
			{
				name: "preset_description", mode: ModePreset, height: 20, width: 120,
				keys: []tea.Msg{
					sessionKey(tea.KeyEnter), sessionKey(tea.KeyCtrlU), sessionType("A small file for sharing"),
				},
				step: "step 2/11", sameText: true,
			},
			{
				name: "preset_review", mode: ModePreset, height: 24, width: 120,
				keys: []tea.Msg{
					sessionKey(tea.KeyEnter), sessionKey(tea.KeyEnter), sessionKey(tea.KeyEnter),
					sessionKey(tea.KeyEnter), sessionKey(tea.KeyEnter), sessionKey(tea.KeyEnter),
					sessionKey(tea.KeyEnter), sessionKey(tea.KeyEnter), sessionKey(tea.KeyEnter),
					sessionKey(tea.KeyEnter),
				},
				step: "step 11/11", sameText: true,
			},
		} {
			t.Run(name+"/"+frame.name, func(t *testing.T) {
				s := sessionFixture(frame.mode)
				if frame.mode == ModeSetup {
					n := int64(20_000_000)
					s.TargetBytes = &n
				}
				if frame.presetZero {
					s.Preset.ID = 0
				}
				opts := SessionOptions{Terminal: !plain, Out: &bytes.Buffer{}}
				release := make(chan struct{})
				closed := false
				t.Cleanup(func() {
					if !closed {
						close(release)
					}
				})
				if frame.hasProbe {
					opts.HWProbe = func(ctx context.Context, codec string) ffmpeg.HWAvailability {
						if frame.blockProbe {
							select {
							case <-release:
							case <-ctx.Done():
							}
						}
						return ffmpeg.HWAvailability{"vulkan": "no supported GPU", "qsv": ""}
					}
				}
				m := newSessionModel(s, opts)
				tm := teatest.NewTestModel(t, m, teatest.WithInitialTermSize(120, 24))
				teatest.WaitFor(t, tm.Output(), func(out []byte) bool { return bytes.Contains(out, []byte("step 1/")) })
				for _, key := range frame.keys {
					tm.Send(key)
				}
				if frame.width != 120 || frame.height != 24 {
					tm.Send(tea.WindowSizeMsg{Width: frame.width, Height: frame.height})
				}
				if frame.waitChecking {
					teatest.WaitFor(t, tm.Output(), func(out []byte) bool {
						return bytes.Contains(out, []byte(frame.step)) && bytes.Contains(out, []byte("checking..."))
					})
				} else if frame.waitProbed {
					teatest.WaitFor(t, tm.Output(), func(out []byte) bool {
						i := bytes.LastIndex(out, []byte("Hardware"))
						return i >= 0 && bytes.Contains(out[i:], []byte(frame.step)) && !bytes.Contains(out[i:], []byte("checking..."))
					})
				} else if frame.waitText != "" {
					teatest.WaitFor(t, tm.Output(), func(out []byte) bool {
						return bytes.Contains(out, []byte(frame.step)) && bytes.Contains(out, []byte(frame.waitText))
					})
				} else if len(frame.keys) > 0 || frame.width != 120 || frame.height != 24 {
					teatest.WaitFor(t, tm.Output(), func(out []byte) bool { return bytes.Contains(out, []byte(frame.step)) })
				}
				tm.Send(sessionKey(tea.KeyEsc))
				if !closed {
					close(release)
					closed = true
				}
				final := tm.FinalModel(t, teatest.WithFinalTimeout(time.Second)).(*sessionModel)
				view := final.View()
				if frame.sameText {
					if plain {
						if view != frames[frame.name] {
							t.Fatal("plain and color frames have different text")
						}
					} else {
						frames[frame.name] = ansi.Strip(view)
					}
				}
				if strings.Contains(view, "\x1b") == plain {
					t.Fatal("incorrect frame color profile")
				}
				lines := strings.Split(ansi.Strip(view), "\n")
				if len(lines) != frame.height || final.width != frame.width {
					t.Fatal("resize did not redraw the frame")
				}
				footer := lines[len(lines)-1]
				wantPreset := "preset Default"
				if !strings.Contains(footer, frame.step) || !strings.Contains(footer, wantPreset) || !strings.Contains(footer, "Esc cancel") {
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
