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
	"github.com/maxie-maxie/pressurecooker/internal/ffmpeg"
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

func sessionEnterManual(t *testing.T, m *sessionModel) {
	t.Helper()
	if m.key() != "start" || m.work.Mode != ModeSetup {
		return
	}
	for m.cur != len(m.presets) {
		m.Update(sessionKey(tea.KeyDown))
	}
	m.Update(sessionKey(tea.KeyEnter))
}

func sessionReach(t *testing.T, m *sessionModel, key string) {
	t.Helper()
	if m.key() == "start" && key != "start" {
		if m.work.Mode == ModeSetup {
			sessionEnterManual(t, m)
		} else {
			m.Update(sessionKey(tea.KeyEnter))
		}
	}
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
		for i, field := range m.reviewKeys() {
			if field == key {
				want = i + 1
				break
			}
		}
		if want == 0 {
			t.Fatalf("unknown review field %s", key)
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

func fmtStep(current, total int) string {
	return strconv.Itoa(current) + "/" + strconv.Itoa(total)
}

func TestSessionStepsAndUnchangedPrefill(t *testing.T) {
	t.Run("setup_manual", func(t *testing.T) {
		m := newSessionModel(sessionFixture(ModeSetup), SessionOptions{})
		want := []string{"start", "codec", "container", "preset", "quality", "audio_codec", "audio", "fps", "resolution", "hw", "target", "review"}
		if !reflect.DeepEqual(m.steps, want) {
			t.Fatalf("steps %v", m.steps)
		}
		if m.key() != "start" || !strings.Contains(m.View(), "step 1/12") {
			t.Fatalf("unexpected start view: %s", m.View())
		}
		sessionEnterManual(t, m)
		for i := 1; i < len(want); i++ {
			if m.key() != want[i] || !strings.Contains(m.View(), "step "+fmtStep(i+1, len(want))) {
				t.Fatalf("missing step %d (%s): %s", i, want[i], m.View())
			}
			m.Update(sessionKey(tea.KeyEnter))
		}
		got, err := sessionOutcome(m)
		if err != nil || got == nil || !got.ManualSetup || !reflect.DeepEqual(got.Settings, sessionFixture(ModeSetup).Settings) || len(got.Edited) != 0 {
			t.Fatalf("unchanged manual prefill: %+v, %v", got, err)
		}
	})

	t.Run("preset_direct", func(t *testing.T) {
		m := newSessionModel(sessionFixture(ModePreset), SessionOptions{})
		want := []string{"label", "description", "codec", "container", "preset", "quality", "audio_codec", "audio", "fps", "resolution", "review"}
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
		if err != nil || got == nil || !reflect.DeepEqual(got.Settings, sessionFixture(ModePreset).Settings) || len(got.Edited) != 0 {
			t.Fatalf("unchanged preset prefill: %+v, %v", got, err)
		}
	})
}

func TestSessionOneClickPresetAndSetupManually(t *testing.T) {
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
	if !strings.Contains(m.View(), "Setup manually") {
		t.Fatalf("missing Setup manually option: %s", m.View())
	}
	// Pressing Enter on saved preset returns immediately without review or edits.
	m.Update(sessionKey(tea.KeyEnter))
	got, err := sessionOutcome(m)
	if err != nil || got == nil {
		t.Fatalf("one-click confirm: %+v, %v", got, err)
	}
	if got.ManualSetup || got.Preset.ID != 2 || got.Settings.Codec != "h264" || got.Settings.Preset != "fast" || *got.TargetBytes != n || len(got.Edited) != 0 {
		t.Fatalf("one-click result: %+v", got)
	}

	// Selecting preset 1 also returns immediately with preset 1's settings.
	m1 := newSessionModel(s, SessionOptions{Presets: []config.Preset{one, two}})
	sessionFeed(m1, sessionKey(tea.KeyUp), sessionKey(tea.KeyEnter))
	got1, err := sessionOutcome(m1)
	if err != nil || got1 == nil || got1.ManualSetup || got1.Preset.ID != 1 || got1.Settings.Codec != "av1" || *got1.TargetBytes != n {
		t.Fatalf("one-click preset 1: %+v, %v", got1, err)
	}

	// Selecting Setup manually sets ManualSetup = true and resets to Preset 1's values while keeping TargetBytes.
	m2 := newSessionModel(s, SessionOptions{Presets: []config.Preset{one, two}})
	sessionEnterManual(t, m2)
	if m2.key() != "codec" || !m2.work.ManualSetup || m2.work.Preset.ID != 1 || m2.work.Settings.Codec != "av1" || m2.work.Settings.HW != "auto" || *m2.work.TargetBytes != n {
		t.Fatalf("setup manually state: key=%s work=%+v", m2.key(), m2.work)
	}

	// Missing preset ID falls back cursor to 0.
	s.Preset.ID = 99
	fallback := newSessionModel(s, SessionOptions{Presets: []config.Preset{one, two}})
	if fallback.cur != 0 || fallback.work.Preset.ID != one.ID {
		t.Fatalf("missing preset fallback: cur=%d id=%d", fallback.cur, fallback.work.Preset.ID)
	}
}

func TestSessionModePresetStartPicker(t *testing.T) {
	one, three := config.FactoryPreset(), config.FactoryPreset()
	three.ID, three.Label, three.Codec, three.Speed = 3, "Portable", "h264", "fast"

	// Picking "New preset" sets ID = maxID + 1 and clears label/description.
	s := Session{Mode: ModePreset}
	m := newSessionModel(s, SessionOptions{Presets: []config.Preset{one, three}})
	if m.key() != "start" || !strings.Contains(m.View(), "New preset") || !strings.Contains(m.View(), "3. Portable") {
		t.Fatalf("preset start view: %s", m.View())
	}
	for m.cur != 2 {
		m.Update(sessionKey(tea.KeyDown))
	}
	m.Update(sessionKey(tea.KeyEnter))
	if m.key() != "label" || m.work.Preset.ID != 4 || m.work.Preset.Label != "" || m.work.Preset.Description != "" || m.work.Settings.Codec != "av1" {
		t.Fatalf("new preset init: key=%s preset=%+v", m.key(), m.work.Preset)
	}

	// Picking a saved preset loads its ID and settings into the editor.
	m2 := newSessionModel(s, SessionOptions{Presets: []config.Preset{one, three}})
	sessionFeed(m2, sessionKey(tea.KeyDown), sessionKey(tea.KeyEnter))
	if m2.key() != "label" || m2.work.Preset.ID != 3 || m2.work.Preset.Label != "Portable" || m2.work.Settings.Codec != "h264" {
		t.Fatalf("saved preset edit init: key=%s preset=%+v", m2.key(), m2.work.Preset)
	}
}

func TestSessionAllCodecsContainersAudioAndControls(t *testing.T) {
	m := newSessionModel(sessionFixture(ModeSetup), SessionOptions{})
	sessionReach(t, m, "codec")
	if !reflect.DeepEqual(m.choices(), config.VideoCodecs) {
		t.Fatalf("codec choices: %v", m.choices())
	}
	sessionReach(t, m, "container")
	if !reflect.DeepEqual(m.choices(), config.Containers) {
		t.Fatalf("container choices: %v", m.choices())
	}
	if !strings.Contains(m.View(), "mp4 (recommended)") || m.work.Settings.Container != "mp4" {
		t.Fatalf("recommended container label: %s", m.View())
	}
	sessionReach(t, m, "audio_codec")
	if !reflect.DeepEqual(m.choices(), config.AudioCodecs) {
		t.Fatalf("audio_codec choices: %v", m.choices())
	}

	// VP9 has 0..8 and accepts named presets via typing; recommended container is webm.
	mVP9 := newSessionModel(sessionFixture(ModeSetup), SessionOptions{})
	sessionEdit(t, mVP9, "codec", "vp9")
	sessionReach(t, mVP9, "container")
	if !strings.Contains(mVP9.View(), "webm (recommended)") {
		t.Fatalf("vp9 recommended container: %s", mVP9.View())
	}
	sessionReach(t, mVP9, "preset")
	if len(mVP9.choices()) != 9 || mVP9.choices()[0] != "0" || mVP9.choices()[8] != "8" {
		t.Fatalf("vp9 speed choices: %v", mVP9.choices())
	}
	sessionEdit(t, mVP9, "preset", "fast")
	if mVP9.msg != "" || mVP9.work.Settings.Preset != "fast" {
		t.Fatalf("vp9 named preset failed: %s", mVP9.msg)
	}

	// ProRes and Theora reject typed CRF because CRFHi == 0.
	for _, codec := range []string{"prores", "theora"} {
		mc := newSessionModel(sessionFixture(ModeSetup), SessionOptions{})
		sessionEdit(t, mc, "codec", codec)
		sessionEdit(t, mc, "quality", "24")
		if mc.key() != "quality" || mc.msg == "" {
			t.Fatalf("%s should reject typed CRF: key=%s msg=%q", codec, mc.key(), mc.msg)
		}
	}
}

func TestSessionGreyedOptionsAndSkipping(t *testing.T) {
	// Container greying for hevc: webm and ogg are unavailable; cursor skips them.
	m := newSessionModel(sessionFixture(ModeSetup), SessionOptions{})
	sessionEdit(t, m, "codec", "hevc")
	sessionReach(t, m, "container")
	view := m.View()
	if !strings.Contains(view, "webm [unavailable:") || !strings.Contains(view, "ogg [unavailable:") {
		t.Fatalf("plain container unavailable text missing: %s", view)
	}
	for i := 0; i < len(config.Containers); i++ {
		c := m.choices()[m.cur]
		if c == "webm" || c == "ogg" {
			t.Fatalf("cursor landed on greyed container %s", c)
		}
		m.Update(sessionKey(tea.KeyDown))
	}
	sessionEdit(t, m, "container", "webm")
	if m.key() != "container" || m.msg == "" {
		t.Fatalf("typing unavailable container must fail: key=%s msg=%q", m.key(), m.msg)
	}

	// Audio codec greying for mov: opus and flac are unavailable.
	sessionEdit(t, m, "container", "mov")
	sessionReach(t, m, "audio_codec")
	aview := m.View()
	if !strings.Contains(aview, "opus [unavailable:") || !strings.Contains(aview, "flac [unavailable:") {
		t.Fatalf("plain audio_codec unavailable text missing: %s", aview)
	}
	for i := 0; i < len(config.AudioCodecs); i++ {
		ac := m.choices()[m.cur]
		if ac == "opus" || ac == "flac" {
			t.Fatalf("cursor landed on greyed audio codec %s", ac)
		}
		m.Update(sessionKey(tea.KeyDown))
	}
	sessionEdit(t, m, "audio_codec", "opus")
	if m.key() != "audio_codec" || m.msg == "" {
		t.Fatalf("typing unavailable audio codec must fail: key=%s msg=%q", m.key(), m.msg)
	}

	// Color mode renders dim ANSI escapes for greyed options without [unavailable:...].
	mc := newSessionModel(sessionFixture(ModeSetup), SessionOptions{Terminal: true, Out: &bytes.Buffer{}})
	sessionEdit(t, mc, "codec", "hevc")
	sessionReach(t, mc, "container")
	cview := mc.View()
	if strings.Contains(cview, "[unavailable:") || !strings.Contains(cview, "\x1b[2m") {
		t.Fatalf("color mode should render dim without [unavailable:...]: %q", cview)
	}

	// FFV1 + FLAC skips preset, quality, audio, and target; shows n/a and [unavailable: conversion only] on review.
	mf := newSessionModel(sessionFixture(ModeSetup), SessionOptions{})
	sessionEdit(t, mf, "codec", "ffv1")
	if mf.key() != "container" {
		t.Fatalf("expected container after codec, got %s", mf.key())
	}
	mf.Update(sessionKey(tea.KeyEnter)) // container (mkv)
	if mf.key() != "audio_codec" {
		t.Fatalf("ffv1 must skip preset and quality, got %s", mf.key())
	}
	sessionEdit(t, mf, "audio_codec", "flac")
	if mf.key() != "fps" {
		t.Fatalf("flac must skip audio kbps, got %s", mf.key())
	}
	sessionReach(t, mf, "hw")
	// hw for ffv1 supports vulkan only (not qsv).
	if !strings.Contains(mf.View(), "qsv [unavailable:") {
		t.Fatalf("ffv1 hw should grey qsv: %s", mf.View())
	}
	mf.Update(sessionKey(tea.KeyEnter)) // skips target -> lands on review
	if mf.key() != "review" {
		t.Fatalf("ffv1 must skip target to review, got %s", mf.key())
	}
	rview := mf.View()
	if !strings.Contains(rview, "Encoder speed: n/a") || !strings.Contains(rview, "Quality or CRF: n/a") ||
		!strings.Contains(rview, "Audio: n/a") || !strings.Contains(rview, "Size: Conversion [unavailable: conversion only]") {
		t.Fatalf("ffv1+flac review view: %s", rview)
	}
	// Cursor on review skips n/a and target rows.
	for i := 0; i < len(mf.reviewKeys())+2; i++ {
		if mf.cur > 0 {
			rk := mf.reviewKeys()[mf.cur-1]
			if rk == "preset" || rk == "quality" || rk == "audio" || rk == "target" {
				t.Fatalf("review cursor landed on disabled row %s", rk)
			}
		}
		mf.Update(sessionKey(tea.KeyDown))
	}
	// Back from review skips target and lands on hw.
	mf.Update(sessionKey(tea.KeyLeft))
	if mf.key() != "hw" {
		t.Fatalf("back from review on ffv1 should land on hw, got %s", mf.key())
	}
}

func TestSessionBackNavReset(t *testing.T) {
	n := int64(10_000_000)
	s := sessionFixture(ModeSetup)
	s.TargetBytes = &n
	m := newSessionModel(s, SessionOptions{})
	sessionEdit(t, m, "quality", "60")
	sessionEdit(t, m, "hw", "qsv")
	sessionReach(t, m, "review")
	// Go back to codec via review edit or Shift+Tab; test going back via Shift+Tab from review.
	for m.key() != "codec" {
		m.Update(sessionKey(tea.KeyShiftTab))
	}
	// Change av1 -> theora: container mp4 -> ogg (reset), quality CRF 60 -> high (reset), hw qsv -> auto (reset).
	sessionEdit(t, m, "codec", "theora")
	if m.work.Settings.Container != "ogg" || m.work.Settings.Quality != "high" || m.work.Settings.CRF != nil || m.work.Settings.HW != "auto" {
		t.Fatalf("unexpected reset values for theora: %+v", m.work.Settings)
	}
	sessionReach(t, m, "review")
	rview := m.View()
	if !strings.Contains(rview, "Container: ogg (reset)") || !strings.Contains(rview, "Quality or CRF: high (reset)") || !strings.Contains(rview, "Hardware: auto (reset)") {
		t.Fatalf("missing (reset) markers on review: %s", rview)
	}
	if strings.Contains(rview, "Audio codec: opus (reset)") {
		t.Fatalf("valid audio_codec opus should not be marked (reset): %s", rview)
	}

	// Change theora -> prores: container ogg -> mov (reset), audio_codec opus -> aac (reset), target 10MB -> nil (reset).
	for m.key() != "codec" {
		m.Update(sessionKey(tea.KeyShiftTab))
	}
	sessionEdit(t, m, "codec", "prores")
	if m.work.Settings.Container != "mov" || m.work.Settings.AudioCodec != "aac" || m.work.TargetBytes != nil {
		t.Fatalf("unexpected reset values for prores: %+v target=%v", m.work.Settings, m.work.TargetBytes)
	}
	sessionReach(t, m, "review")
	pview := m.View()
	if !strings.Contains(pview, "Container: mov (reset)") || !strings.Contains(pview, "Audio codec: aac (reset)") || !strings.Contains(pview, "Size: Conversion (reset) [unavailable: conversion only]") {
		t.Fatalf("missing prores reset markers: %s", pview)
	}

	// Changing container mp4 -> webm resets incompatible aac -> opus.
	m2 := newSessionModel(sessionFixture(ModeSetup), SessionOptions{})
	sessionEdit(t, m2, "audio_codec", "aac")
	sessionReach(t, m2, "review")
	for m2.key() != "container" {
		m2.Update(sessionKey(tea.KeyShiftTab))
	}
	sessionEdit(t, m2, "container", "webm")
	sessionReach(t, m2, "review")
	if m2.work.Settings.AudioCodec != "opus" || !strings.Contains(m2.View(), "Audio codec: opus (reset)") {
		t.Fatalf("container change should reset audio_codec: %s", m2.View())
	}
}

func TestSessionHWProbeAsyncCacheAndFallback(t *testing.T) {
	calls := make(map[string]int)
	probe := func(_ context.Context, codec string) ffmpeg.HWAvailability {
		calls[codec]++
		return ffmpeg.HWAvailability{"vulkan": "no supported GPU", "qsv": ""}
	}

	m := newSessionModel(sessionFixture(ModeSetup), SessionOptions{HWProbe: probe})
	sessionReach(t, m, "resolution")
	_, cmd := m.Update(sessionKey(tea.KeyEnter))
	if m.key() != "hw" || cmd == nil {
		t.Fatalf("entering hw must produce probe cmd (key=%s cmd=%v)", m.key(), cmd != nil)
	}
	if !strings.Contains(m.View(), "vulkan (checking...)") || !strings.Contains(m.View(), "qsv (checking...)") {
		t.Fatalf("missing checking... before probe completes: %s", m.View())
	}
	msg := cmd()
	m.Update(msg)
	view := m.View()
	if strings.Contains(view, "checking...") {
		t.Fatalf("checking... remained after probe: %s", view)
	}
	if !strings.Contains(view, "vulkan [unavailable: no supported GPU]") {
		t.Fatalf("failed backend not greyed: %s", view)
	}
	if calls["av1"] != 1 {
		t.Fatalf("expected 1 probe call for av1, got %d", calls["av1"])
	}
	// Cursor skips greyed vulkan.
	m.Update(sessionKey(tea.KeyDown))
	if m.choices()[m.cur] != "qsv" {
		t.Fatalf("expected cursor to skip vulkan to qsv, got %s", m.choices()[m.cur])
	}
	// Leaving hw and re-entering hw for same codec uses cached result (no extra probe).
	m.Update(sessionKey(tea.KeyLeft))
	_, cmd2 := m.Update(sessionKey(tea.KeyEnter))
	if cmd2 != nil || calls["av1"] != 1 {
		t.Fatalf("expected cached hw probe result, cmd=%v calls=%d", cmd2 != nil, calls["av1"])
	}
	// Switching codec triggers probe for the new codec.
	sessionReach(t, m, "review")
	sessionEdit(t, m, "codec", "hevc")
	for m.cur == 0 || m.reviewKeys()[m.cur-1] != "hw" {
		m.Update(sessionKey(tea.KeyDown))
	}
	_, cmd3 := m.Update(sessionKey(tea.KeyEnter))
	if m.key() != "hw" || cmd3 == nil {
		t.Fatal("switching codec must trigger hw probe for new codec")
	}
	m.Update(cmd3())
	if calls["hevc"] != 1 {
		t.Fatalf("expected 1 probe call for hevc, got %d", calls["hevc"])
	}

	// Non-terminal fallback loop executes HWProbe cmd and prints checking... then resolved.
	var out bytes.Buffer
	// Down + 12 Enters: start(Setup manually) -> codec -> container -> preset -> quality -> audio_codec -> audio -> fps -> resolution -> hw -> target -> review -> confirm.
	input := "\x1b[B" + strings.Repeat("\r", 12)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	got, err := RunSession(ctx, sessionFixture(ModeSetup), SessionOptions{
		In:      strings.NewReader(input),
		Out:     &out,
		HWProbe: probe,
	})
	if err != nil || got == nil || !got.ManualSetup {
		t.Fatalf("fallback with HWProbe: got=%+v err=%v", got, err)
	}
	if !strings.Contains(out.String(), "checking...") || !strings.Contains(out.String(), "vulkan [unavailable: no supported GPU]") {
		t.Fatalf("fallback output missing probe states: %s", out.String())
	}

	// Nil HWProbe works safely using CodecCaps strictly.
	mNil := newSessionModel(sessionFixture(ModeSetup), SessionOptions{})
	sessionReach(t, mNil, "hw")
	if mNil.pendingCmd != nil || strings.Contains(mNil.View(), "checking...") || strings.Contains(mNil.View(), "unavailable") {
		t.Fatalf("nil HWProbe should allow av1 backends without checking: %s", mNil.View())
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
		{"codec", []string{"divx"}}, {"container", []string{"flv"}}, {"preset", []string{"14", "quick"}},
		{"quality", []string{"NaN", "Inf", "0", "64"}}, {"audio_codec", []string{"vorbis"}}, {"audio", []string{"0", "-1", "1.5", "abc"}},
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
	for _, bad := range []string{"", " ", strings.Repeat("a", 41)} {
		sessionEdit(t, m, "label", bad)
		if m.key() != "label" || m.msg == "" {
			t.Fatal("invalid label advanced")
		}
	}
	label := strings.Repeat("a", 40)
	sessionEdit(t, m, "label", label)
	sessionEdit(t, m, "description", strings.Repeat("b", 121))
	if m.key() != "description" || m.msg == "" {
		t.Fatal("long description advanced")
	}
	description := strings.Repeat("b", 120)
	sessionEdit(t, m, "description", description)
	sessionReach(t, m, "review")
	sessionEdit(t, m, "description", "")
	sessionEdit(t, m, "quality", "32")
	sessionEdit(t, m, "audio_codec", "aac")
	sessionEdit(t, m, "audio", "96")
	sessionConfirm(m)
	got, err := sessionOutcome(m)
	if err != nil || got == nil {
		t.Fatalf("confirmation: %v, %s", err, m.msg)
	}
	if got.Preset.Label != label || got.Preset.Description != "" || got.Preset.Quality != "" || *got.Preset.CRF != 32 || got.Preset.AudioCodec != "aac" || *got.Preset.AudioKbps != 96 || !got.Edited["label"] {
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
	// Choose "New preset" on start screen.
	sessionFeed(m, sessionKey(tea.KeyDown), sessionKey(tea.KeyEnter))
	for _, field := range []struct{ key, value string }{
		{"label", "Share"}, {"description", "Clips for sharing"}, {"codec", "h264"},
		{"container", "mkv"}, {"preset", "fast"}, {"quality", "0"},
		{"audio_codec", "ac3"}, {"audio", "source"}, {"fps", "1000"}, {"resolution", "16384x16384"},
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
	if p.ID != 2 || p.Codec != "h264" || p.Container != "mkv" || p.Speed != "fast" || p.Quality != "" || *p.CRF != 0 || p.AudioCodec != "ac3" || p.AudioKbps != nil || *p.FPS != 1000 || p.Resolution != "16384x16384" {
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
	for _, test := range []struct {
		name, field, fix, errorText string
		alter                       func(*config.Settings)
	}{
		{"webm", "codec", "av1", "webm does not accept hevc; use -c av1 or vp9", func(s *config.Settings) { s.Codec, s.Container, s.Preset = "hevc", "webm", "fast" }},
		{"speed", "preset", "fast", "preset for hevc", func(s *config.Settings) { s.Codec = "hevc" }},
		{"xor", "quality", "high", "exactly one", func(s *config.Settings) { n := 28.0; s.CRF = &n }},
	} {
		t.Run("preset/"+test.name, func(t *testing.T) {
			s := sessionFixture(ModePreset)
			test.alter(&s.Settings)
			s.Preset.Codec = ""
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
		if size.Width >= 60 && !strings.Contains(lines[len(lines)-1], "step 1/12") {
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
		name, input                  string
		mode                         Mode
		confirm, interrupted, manual bool
	}{
		{"EOF", "", ModeSetup, false, false, false},
		{"escape", "\x1b", ModeSetup, false, false, false},
		{"interrupt", "\x03", ModeSetup, false, true, false},
		{"one_click", "\r", ModeSetup, true, false, false},
		{"setup_manual", "\x1b[B" + strings.Repeat("\r", 12), ModeSetup, true, false, true},
		{"CRLF", "\x1b[B" + strings.Repeat("\r\n", 12), ModeSetup, true, false, true},
		{"preset", strings.Repeat("\n", 11), ModePreset, true, false, false},
		{"typed preset", "\x15Portable\n" + strings.Repeat("\n", 10), ModePreset, true, false, false},
		{"navigation", "\x1b[B\x1b[B\x1b[A" + strings.Repeat("\r", 12), ModeSetup, true, false, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			var out bytes.Buffer
			got, err := RunSession(ctx, sessionFixture(test.mode), SessionOptions{In: strings.NewReader(test.input), Out: &out})
			if (got != nil) != test.confirm || errors.Is(err, ErrInterrupted) != test.interrupted || (err != nil && !test.interrupted) {
				t.Fatalf("result: %+v, %v", got, err)
			}
			if test.confirm && got.ManualSetup != test.manual {
				t.Fatalf("ManualSetup: got %v want %v", got.ManualSetup, test.manual)
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
	for _, input := range []string{"", strings.Repeat("\r", 1)} {
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
