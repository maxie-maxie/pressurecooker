package tui

import (
	"fmt"
	"io"
	"math"
	"math/big"
	"reflect"
	"strconv"
	"strings"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"github.com/maxie-maxie/pressurecooker/internal/config"
)

const (
	statusRunning = iota
	statusConfirmed
	statusCancelled
	statusInterrupted
)

var namedPresetChoices = []string{"ultrafast", "superfast", "veryfast", "faster", "fast", "medium", "slow", "slower", "veryslow", "placebo"}

type sessionModel struct {
	base, work Session
	presets    []config.Preset
	steps      []string
	idx, cur   int
	active     int
	status     int
	width      int
	height     int
	text       string
	msg        string
	dirty      bool
	moved      bool
	fromReview bool
	plain      bool
	renderer   *lipgloss.Renderer
	out        io.Writer
	writeErr   error
}

func newSessionModel(s Session, o SessionOptions) *sessionModel {
	m := &sessionModel{work: cloneSession(s), plain: sessionPlain(o), width: 120, height: 24}
	m.steps = []string{"start", "codec", "container", "preset", "quality", "audio", "fps", "resolution", "hw", "target", "review"}
	m.presets = append([]config.Preset(nil), o.Presets...)
	if len(m.presets) == 0 {
		m.presets = []config.Preset{config.FactoryPreset()}
	}
	for i, p := range m.presets {
		if p.ID == s.Preset.ID {
			m.active = i
			break
		}
	}
	if s.Mode == ModePreset {
		m.steps = []string{"label", "description", "codec", "container", "preset", "quality", "audio", "fps", "resolution", "review"}
		if s.Preset.Codec != "" {
			m.work.Settings = s.Preset.Settings()
		}
	} else {
		m.work.Preset = m.presets[m.active]
	}
	if m.work.Settings.Codec == "" {
		m.work.Settings = m.presets[m.active].Settings()
	}
	m.base = cloneSession(m.work)
	m.renderer = lipgloss.NewRenderer(o.Out)
	if m.plain {
		m.renderer.SetColorProfile(termenv.Ascii)
	} else {
		m.renderer.SetColorProfile(termenv.ANSI256)
	}
	m.enterStep()
	return m
}

func cloneSession(s Session) Session {
	s.Settings = config.Clone(s.Settings)
	if s.TargetBytes != nil {
		v := *s.TargetBytes
		s.TargetBytes = &v
	}
	p := s.Preset
	pointers := config.Clone(config.Settings{CRF: p.CRF, FPS: p.FPS})
	p.CRF, p.FPS = pointers.CRF, pointers.FPS
	if p.AudioKbps != nil {
		v := *p.AudioKbps
		p.AudioKbps = &v
	}
	s.Preset = p
	edited := make(map[string]bool, len(s.Edited))
	for k, v := range s.Edited {
		edited[k] = v
	}
	s.Edited = edited
	return s
}

func (m *sessionModel) key() string { return m.steps[m.idx] }

func (m *sessionModel) textOnly() bool {
	return m.key() == "label" || m.key() == "description" || m.key() == "target"
}

func (m *sessionModel) enterStep() {
	m.cur, m.text, m.msg = 0, "", ""
	m.dirty, m.moved = false, false
	if m.key() == "review" {
		return
	}
	if m.key() == "start" {
		m.cur = m.active
		return
	}
	value := m.rawValue(m.key())
	for i, choice := range m.choices() {
		if value == choice {
			m.cur = i
			return
		}
	}
	m.text = value
}

func (m *sessionModel) choices() []string {
	switch m.key() {
	case "start":
		list := make([]string, len(m.presets))
		for i, p := range m.presets {
			list[i] = fmt.Sprintf("%d. %s", p.ID, p.Label)
		}
		return list
	case "codec":
		return []string{"av1", "hevc", "h264"}
	case "container":
		return []string{"mp4", "mkv", "webm"}
	case "preset":
		if m.work.Settings.Codec == "av1" {
			list := make([]string, 14)
			for i := range list {
				list[i] = strconv.Itoa(i)
			}
			return list
		}
		return namedPresetChoices
	case "quality":
		return []string{"high", "balanced", "small"}
	case "audio":
		return []string{"source", "128", "96", "64", "48"}
	case "fps":
		return []string{"source", "60", "30", "24"}
	case "resolution":
		return []string{"source", "2160p", "1440p", "1080p", "720p", "480p", "360p"}
	case "hw":
		return []string{"auto", "vulkan", "qsv", "off"}
	}
	return nil
}

func (m *sessionModel) Init() tea.Cmd { return nil }

func (m *sessionModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if m.status != statusRunning {
		return m, tea.Quit
	}
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = max(1, msg.Width), max(1, msg.Height)
	case tea.KeyMsg:
		m.handleKey(msg)
	}
	if m.out != nil {
		_, m.writeErr = fmt.Fprintln(m.out, m.View())
	}
	if m.status != statusRunning || m.writeErr != nil {
		return m, tea.Quit
	}
	return m, nil
}

func (m *sessionModel) handleKey(k tea.KeyMsg) {
	switch k.Type {
	case tea.KeyEsc:
		m.status = statusCancelled
		return
	case tea.KeyCtrlC:
		m.status = statusInterrupted
		return
	case tea.KeyShiftTab:
		m.backStep()
		return
	case tea.KeyLeft:
		if !m.textOnly() {
			m.backStep()
		}
		return
	}
	if m.key() == "review" {
		switch k.Type {
		case tea.KeyUp, tea.KeyDown:
			m.moveSelection(k, len(m.steps))
		case tea.KeyEnter:
			if m.cur == 0 {
				if err := m.validate(); err != nil {
					m.msg = err.Error()
				} else {
					m.status = statusConfirmed
				}
			} else {
				m.idx, m.fromReview = m.cur-1, true
				m.enterStep()
			}
		}
		return
	}
	switch k.Type {
	case tea.KeyUp, tea.KeyDown:
		if n := len(m.choices()); n > 0 {
			m.moveSelection(k, n)
			m.text, m.dirty, m.moved = "", false, true
		}
	case tea.KeyEnter:
		m.acceptStep()
	case tea.KeyCtrlU:
		if m.key() != "start" {
			m.text, m.dirty = "", true
		}
	case tea.KeyBackspace:
		if m.key() != "start" {
			if !m.dirty && !m.textOnly() {
				m.text = m.rawValue(m.key())
			}
			r := []rune(m.text)
			if len(r) > 0 {
				m.text = string(r[:len(r)-1])
			}
			m.dirty = true
		}
	case tea.KeyRunes, tea.KeySpace:
		if m.key() != "start" {
			if !m.dirty && !m.textOnly() {
				m.text = ""
			}
			if k.Type == tea.KeySpace {
				m.text += " "
			} else {
				m.text += string(k.Runes)
			}
			m.dirty = true
		}
	}
}

func (m *sessionModel) moveSelection(k tea.KeyMsg, n int) {
	delta := 1
	if k.Type == tea.KeyUp {
		delta = -1
	}
	m.cur = (m.cur + delta + n) % n
}

func (m *sessionModel) acceptStep() {
	if m.key() == "start" {
		if m.cur != m.active {
			m.active = m.cur
			m.work.Preset = m.presets[m.active]
			s := m.work.Preset.Settings()
			s.HW = m.work.Settings.HW
			m.work.Settings = s
		}
	} else if m.dirty || m.textOnly() || m.moved {
		value := m.text
		if m.moved && !m.dirty {
			value = m.choices()[m.cur]
		}
		if err := m.applyValue(m.key(), value); err != nil {
			m.msg = err.Error()
			return
		}
	}
	if m.fromReview {
		m.idx, m.fromReview = len(m.steps)-1, false
	} else {
		m.idx++
	}
	m.enterStep()
}

func (m *sessionModel) backStep() {
	if m.idx > 0 {
		m.idx--
		m.fromReview = false
		m.enterStep()
	}
}

func (m *sessionModel) applyValue(key, raw string) error {
	value := strings.ToLower(strings.TrimSpace(raw))
	s := &m.work.Settings
	switch key {
	case "label":
		if strings.TrimSpace(raw) == "" {
			return fmt.Errorf("label is required")
		}
		if utf8.RuneCountInString(raw) > 40 {
			return fmt.Errorf("label must be at most 40 characters")
		}
		m.work.Preset.Label = raw
	case "description":
		if utf8.RuneCountInString(raw) > 120 {
			return fmt.Errorf("description must be at most 120 characters")
		}
		m.work.Preset.Description = raw
	case "codec", "container", "hw":
		allowed := map[string][]string{"codec": {"av1", "hevc", "h264"}, "container": {"mp4", "mkv", "webm"}, "hw": {"auto", "vulkan", "qsv", "off"}}
		if !inList(value, allowed[key]...) {
			return fmt.Errorf("%s must be %s", key, strings.Join(allowed[key], ", "))
		}
		switch key {
		case "codec":
			s.Codec = value
		case "container":
			s.Container = value
		case "hw":
			s.HW = value
		}
	case "preset":
		if _, err := config.ResolvePreset(value, s.Codec, false); err != nil {
			return err
		}
		s.Preset = value
	case "quality":
		if inList(value, "high", "balanced", "small") {
			s.Quality, s.CRF = value, nil
			return nil
		}
		n, err := strconv.ParseFloat(value, 64)
		lo, hi := 0.0, 51.0
		if s.Codec == "av1" {
			lo, hi = 1, 63
		}
		if err != nil || math.IsNaN(n) || math.IsInf(n, 0) || n < lo || n > hi {
			return fmt.Errorf("CRF for %s must be %.0f-%.0f", s.Codec, lo, hi)
		}
		s.Quality, s.CRF = "", &n
	case "audio":
		if value == "source" {
			s.AudioSource = true
			return nil
		}
		n, err := strconv.Atoi(value)
		if err != nil || n <= 0 {
			return fmt.Errorf("audio must be source or a positive integer in kbps")
		}
		s.AudioSource, s.AudioKbps = false, n
	case "fps":
		if value == "source" {
			s.FPS = nil
			return nil
		}
		n, err := strconv.ParseFloat(value, 64)
		if err != nil || math.IsNaN(n) || math.IsInf(n, 0) || n <= 0 || n > 1000 {
			return fmt.Errorf("FPS must be source or a number greater than 0 and at most 1000")
		}
		s.FPS = &n
	case "resolution":
		if value == "source" {
			s.Resolution = ""
			return nil
		}
		if value == "" {
			return fmt.Errorf("resolution must be source, an alias or even WxH")
		}
		if _, _, err := config.ParseResolution(value); err != nil {
			return err
		}
		s.Resolution = value
	case "target":
		if value == "" {
			m.work.TargetBytes = nil
			return nil
		}
		// Decimal arithmetic keeps a large MB value from wrapping the byte count.
		number, err := strconv.ParseFloat(value, 64)
		if err != nil || math.IsNaN(number) || math.IsInf(number, 0) || number <= 0 || number > float64(math.MaxInt64)/1_000_000 {
			return fmt.Errorf("size must be a positive decimal number within the supported byte range")
		}
		mb, ok := new(big.Rat).SetString(value)
		if !ok || mb.Sign() <= 0 || strings.ContainsAny(value, "/xX") {
			return fmt.Errorf("size must be empty or a positive decimal number")
		}
		mb.Mul(mb, big.NewRat(1_000_000, 1))
		mb.Add(mb, big.NewRat(1, 2))
		bytes := new(big.Int).Quo(mb.Num(), mb.Denom())
		if !bytes.IsInt64() || bytes.Sign() <= 0 {
			return fmt.Errorf("size is outside the supported byte range")
		}
		n := bytes.Int64()
		m.work.TargetBytes = &n
	}
	return nil
}

func (m *sessionModel) resultPreset() config.Preset {
	p, s := m.work.Preset, config.Clone(m.work.Settings)
	p.Codec, p.Container, p.Speed = s.Codec, s.Container, s.Preset
	p.Quality, p.CRF, p.FPS, p.Resolution = s.Quality, s.CRF, s.FPS, s.Resolution
	p.AudioKbps = nil
	if !s.AudioSource {
		p.AudioKbps = &s.AudioKbps
	}
	return p
}

func (m *sessionModel) validate() error {
	p := m.resultPreset()
	if m.work.Mode == ModeSetup {
		p.ID, p.Label, p.Description = 0, "Setup", ""
		if m.work.TargetBytes != nil && *m.work.TargetBytes <= 0 {
			return fmt.Errorf("size must be a positive byte count")
		}
		hw := m.work.Settings.HW
		if !inList(hw, "auto", "vulkan", "qsv", "off") {
			return fmt.Errorf("hardware must be auto, vulkan, qsv or off")
		}
		if p.CRF != nil && (hw == "vulkan" || hw == "qsv") {
			return fmt.Errorf("raw CRF is unsupported on %s", hw)
		}
	}
	return config.ValidatePreset(p)
}

func (m *sessionModel) finalSession() Session {
	s := cloneSession(m.work)
	if s.Mode == ModePreset {
		s.Preset = m.resultPreset()
		s.Settings = s.Preset.Settings()
	}
	s.Edited = m.editedFields()
	return s
}

func (m *sessionModel) editedFields() map[string]bool {
	edited := make(map[string]bool)
	for key, value := range m.base.Edited {
		if value {
			edited[key] = true
		}
	}
	a, b := sessionFields(m.base), sessionFields(m.work)
	for key, value := range a {
		if !reflect.DeepEqual(value, b[key]) {
			edited[key] = true
		}
	}
	return edited
}

func sessionFields(s Session) map[string]any {
	v := s.Settings
	fields := map[string]any{
		"codec": v.Codec, "container": v.Container, "preset": v.Preset,
		"quality": v.Quality, "crf": v.CRF, "audio": sessionAudio(v),
		"fps": v.FPS, "resolution": v.Resolution,
	}
	if s.Mode == ModeSetup {
		fields["target"], fields["hw"] = s.TargetBytes, v.HW
	} else {
		fields["label"], fields["description"] = s.Preset.Label, s.Preset.Description
	}
	return fields
}

func sessionAudio(s config.Settings) any {
	if s.AudioSource {
		return nil
	}
	return s.AudioKbps
}

func trimNum(f float64) string { return strconv.FormatFloat(f, 'f', -1, 64) }

func inList(v string, list ...string) bool {
	for _, s := range list {
		if v == s {
			return true
		}
	}
	return false
}
