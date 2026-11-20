package tui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"math/big"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"github.com/maxie-maxie/pressurecooker/internal/config"
	"github.com/maxie-maxie/pressurecooker/internal/ffmpeg"
)

const (
	statusRunning = iota
	statusConfirmed
	statusCancelled
	statusInterrupted
)

var namedPresetChoices = []string{"ultrafast", "superfast", "veryfast", "faster", "fast", "medium", "slow", "slower", "veryslow", "placebo"}

type hwProbeResultMsg struct {
	codec string
	avail ffmpeg.HWAvailability
}

type sessionModel struct {
	ctx            context.Context
	base, work     Session
	presets        []config.Preset
	steps          []string
	idx, cur       int
	active         int
	status         int
	width          int
	height         int
	text           string
	msg            string
	dirty          bool
	moved          bool
	fromReview     bool
	plain          bool
	renderer       *lipgloss.Renderer
	out            io.Writer
	writeErr       error
	hwProbe        func(ctx context.Context, codec string) ffmpeg.HWAvailability
	hwCache        map[string]ffmpeg.HWAvailability
	hwProbingCodec string
	pendingCmd     tea.Cmd
	resets         map[string]bool
}

func newSessionModel(s Session, o SessionOptions) *sessionModel {
	m := &sessionModel{
		ctx:     context.Background(),
		work:    cloneSession(s),
		plain:   sessionPlain(o),
		width:   120,
		height:  24,
		hwProbe: o.HWProbe,
		hwCache: make(map[string]ffmpeg.HWAvailability),
		resets:  make(map[string]bool),
	}
	m.steps = []string{"start", "codec", "container", "preset", "quality", "audio_codec", "audio", "fps", "resolution", "hw", "target", "review"}
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
		if s.Preset.ID == 0 {
			m.steps = []string{"start", "label", "description", "codec", "container", "preset", "quality", "audio_codec", "audio", "fps", "resolution", "review"}
		} else {
			m.steps = []string{"label", "description", "codec", "container", "preset", "quality", "audio_codec", "audio", "fps", "resolution", "review"}
		}
		if s.Preset.Codec != "" {
			m.work.Settings = s.Preset.Settings()
		}
	} else {
		m.work.Preset = m.presets[m.active]
	}
	if m.work.Settings.Codec == "" {
		m.work.Settings = m.presets[m.active].Settings()
	}
	if m.work.Settings.AudioCodec == "" {
		m.work.Settings.AudioCodec = "opus"
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

func (m *sessionModel) isStepSkipped(key string) bool {
	caps := config.CodecCaps(m.work.Settings.Codec)
	switch key {
	case "preset":
		return !caps.Speed
	case "quality":
		return !caps.Quality
	case "audio":
		return m.work.Settings.AudioCodec == "flac"
	case "target":
		return m.work.Mode == ModeSetup && !caps.SizeMode
	}
	return false
}

func (m *sessionModel) reviewKeys() []string {
	keys := make([]string, 0, len(m.steps)-1)
	for _, key := range m.steps[:len(m.steps)-1] {
		if key != "start" {
			keys = append(keys, key)
		}
	}
	return keys
}

func (m *sessionModel) isReviewRowDisabled(key string) bool {
	return m.isStepSkipped(key)
}

func (m *sessionModel) optionReason(key, val string) string {
	s := m.work.Settings
	switch key {
	case "container":
		if !config.Accepts(val, s.Codec, "") {
			return fmt.Sprintf("incompatible with %s", s.Codec)
		}
	case "audio_codec":
		if !config.Accepts(s.Container, "", val) {
			return fmt.Sprintf("incompatible with %s", s.Container)
		}
	case "hw":
		if val == "auto" || val == "off" {
			return ""
		}
		caps := config.CodecCaps(s.Codec)
		if !slices.Contains(caps.HW, val) {
			return fmt.Sprintf("unsupported by %s", s.Codec)
		}
		if avail, ok := m.hwCache[s.Codec]; ok {
			if reason := avail[val]; reason != "" {
				return reason
			}
		}
	}
	return ""
}

func (m *sessionModel) isChoiceGreyed(key, val string) bool {
	return m.optionReason(key, val) != ""
}

func (m *sessionModel) enterStep() {
	m.cur, m.text, m.msg = 0, "", ""
	m.dirty, m.moved = false, false
	key := m.key()
	if key == "review" {
		return
	}
	if key == "start" {
		m.cur = m.active
		return
	}
	if key == "hw" {
		codec := m.work.Settings.Codec
		caps := config.CodecCaps(codec)
		if m.hwProbe != nil && len(caps.HW) > 0 {
			if _, ok := m.hwCache[codec]; !ok && m.hwProbingCodec != codec {
				m.hwProbingCodec = codec
				probe := m.hwProbe
				ctx := m.ctx
				if ctx == nil {
					ctx = context.Background()
				}
				m.pendingCmd = func() tea.Msg {
					return hwProbeResultMsg{codec: codec, avail: probe(ctx, codec)}
				}
			}
		}
	}
	choices := m.choices()
	firstValid := 0
	foundValid := false
	for i, choice := range choices {
		if !m.isChoiceGreyed(key, choice) {
			firstValid = i
			foundValid = true
			break
		}
	}
	value := m.rawValue(key)
	for i, choice := range choices {
		if value == choice {
			if !m.isChoiceGreyed(key, choice) {
				m.cur = i
			} else if foundValid {
				m.cur = firstValid
			}
			return
		}
	}
	if foundValid {
		m.cur = firstValid
	}
	m.text = value
}

func (m *sessionModel) ensureValidCursor() {
	key := m.key()
	choices := m.choices()
	if len(choices) == 0 {
		return
	}
	if m.cur >= 0 && m.cur < len(choices) && !m.isChoiceGreyed(key, choices[m.cur]) {
		return
	}
	for i, c := range choices {
		if !m.isChoiceGreyed(key, c) {
			m.cur = i
			return
		}
	}
}

func (m *sessionModel) choices() []string {
	return m.choicesFor(m.key())
}

func (m *sessionModel) choicesFor(key string) []string {
	switch key {
	case "start":
		list := make([]string, len(m.presets)+1)
		for i, p := range m.presets {
			list[i] = fmt.Sprintf("%d. %s", p.ID, p.Label)
		}
		if m.work.Mode == ModeSetup {
			list[len(m.presets)] = "Setup manually"
		} else {
			list[len(m.presets)] = "New preset"
		}
		return list
	case "codec":
		return config.VideoCodecs
	case "container":
		return config.Containers
	case "preset":
		switch m.work.Settings.Codec {
		case "av1":
			list := make([]string, 14)
			for i := range list {
				list[i] = strconv.Itoa(i)
			}
			return list
		case "vp9":
			list := make([]string, 9)
			for i := range list {
				list[i] = strconv.Itoa(i)
			}
			return list
		}
		return namedPresetChoices
	case "quality":
		return []string{"high", "balanced", "small"}
	case "audio_codec":
		return config.AudioCodecs
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

func (m *sessionModel) Init() tea.Cmd {
	cmd := m.pendingCmd
	m.pendingCmd = nil
	return cmd
}

func (m *sessionModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if m.status != statusRunning {
		return m, tea.Quit
	}
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = max(1, msg.Width), max(1, msg.Height)
	case tea.KeyMsg:
		m.handleKey(msg)
	case hwProbeResultMsg:
		avail := msg.avail
		if avail == nil {
			avail = ffmpeg.HWAvailability{}
		}
		m.hwCache[msg.codec] = avail
		if m.hwProbingCodec == msg.codec {
			m.hwProbingCodec = ""
		}
		if m.key() == "hw" && m.work.Settings.Codec == msg.codec {
			m.ensureValidCursor()
		}
	}
	cmd := m.pendingCmd
	m.pendingCmd = nil
	if m.out != nil {
		_, m.writeErr = fmt.Fprintln(m.out, m.View())
	}
	if m.status != statusRunning || m.writeErr != nil {
		return m, tea.Quit
	}
	return m, cmd
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
		rkeys := m.reviewKeys()
		switch k.Type {
		case tea.KeyUp, tea.KeyDown:
			m.moveSelection(k, 1+len(rkeys))
		case tea.KeyEnter:
			if m.cur == 0 {
				if err := m.validate(); err != nil {
					m.msg = err.Error()
				} else {
					m.status = statusConfirmed
				}
			} else if m.cur > 0 && m.cur <= len(rkeys) {
				targetKey := rkeys[m.cur-1]
				if m.isReviewRowDisabled(targetKey) {
					return
				}
				for i, stepKey := range m.steps {
					if stepKey == targetKey {
						m.idx, m.fromReview = i, true
						m.enterStep()
						return
					}
				}
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
	if n <= 0 {
		return
	}
	delta := 1
	if k.Type == tea.KeyUp {
		delta = -1
	}
	if m.key() == "review" {
		rkeys := m.reviewKeys()
		for step := 1; step <= n; step++ {
			cand := (m.cur + delta*step%n + n) % n
			if cand == 0 || !m.isReviewRowDisabled(rkeys[cand-1]) {
				m.cur = cand
				return
			}
		}
		return
	}
	choices := m.choices()
	for step := 1; step <= n; step++ {
		cand := (m.cur + delta*step%n + n) % n
		if !m.isChoiceGreyed(m.key(), choices[cand]) {
			m.cur = cand
			return
		}
	}
}

func (m *sessionModel) acceptStep() {
	if m.key() == "start" {
		if m.work.Mode == ModeSetup {
			if m.cur < len(m.presets) {
				m.active = m.cur
				m.work.ManualSetup = false
				m.work.Preset = m.presets[m.active]
				m.work.Settings = m.work.Preset.Settings()
				m.status = statusConfirmed
				return
			}
			m.work.ManualSetup = true
			m.active = 0
			m.work.Preset = m.presets[0]
			m.work.Settings = m.presets[0].Settings()
			m.base.Preset = m.work.Preset
			m.base.Settings = m.work.Settings
			m.resets = make(map[string]bool)
		} else {
			if m.cur < len(m.presets) {
				m.active = m.cur
				m.work.Preset = m.presets[m.active]
				m.work.Settings = m.work.Preset.Settings()
			} else {
				maxID := 0
				for _, p := range m.presets {
					if p.ID > maxID {
						maxID = p.ID
					}
				}
				p := config.FactoryPreset()
				p.ID = maxID + 1
				p.Label = ""
				p.Description = ""
				m.work.Preset = p
				m.work.Settings = p.Settings()
			}
			edited := m.base.Edited
			m.base = cloneSession(m.work)
			m.base.Edited = edited
			m.resets = make(map[string]bool)
		}
	} else if m.dirty || m.textOnly() || m.moved {
		value := m.text
		if m.moved && !m.dirty {
			choices := m.choices()
			if m.cur < 0 || m.cur >= len(choices) || m.isChoiceGreyed(m.key(), choices[m.cur]) {
				return
			}
			value = choices[m.cur]
		}
		if err := m.applyValue(m.key(), value); err != nil {
			m.msg = err.Error()
			return
		}
		delete(m.resets, m.key())
	}
	if m.fromReview {
		m.idx, m.fromReview = len(m.steps)-1, false
	} else {
		m.idx++
		for m.idx < len(m.steps)-1 && m.isStepSkipped(m.steps[m.idx]) {
			m.idx++
		}
	}
	m.enterStep()
}

func (m *sessionModel) backStep() {
	idx := m.idx - 1
	for idx > 0 && m.isStepSkipped(m.steps[idx]) {
		idx--
	}
	if idx >= 0 {
		m.idx = idx
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
	case "codec":
		if !slices.Contains(config.VideoCodecs, value) {
			return fmt.Errorf("codec must be %s", strings.Join(config.VideoCodecs, ", "))
		}
		old := s.Codec
		s.Codec = value
		if old != s.Codec {
			m.resetAfterCodecChange()
		}
	case "container":
		if !slices.Contains(config.Containers, value) {
			return fmt.Errorf("container must be %s", strings.Join(config.Containers, ", "))
		}
		if reason := m.optionReason("container", value); reason != "" {
			return fmt.Errorf("%s is unavailable: %s", value, reason)
		}
		old := s.Container
		s.Container = value
		if old != s.Container {
			m.resetAfterContainerChange()
		}
	case "audio_codec":
		if !slices.Contains(config.AudioCodecs, value) {
			return fmt.Errorf("audio codec must be %s", strings.Join(config.AudioCodecs, ", "))
		}
		if reason := m.optionReason("audio_codec", value); reason != "" {
			return fmt.Errorf("%s is unavailable: %s", value, reason)
		}
		old := s.AudioCodec
		s.AudioCodec = value
		if old != s.AudioCodec {
			m.resetAfterAudioCodecChange()
		}
	case "hw":
		if !inList(value, "auto", "vulkan", "qsv", "off") {
			return fmt.Errorf("hw must be auto, vulkan, qsv, off")
		}
		if reason := m.optionReason("hw", value); reason != "" {
			return fmt.Errorf("%s is unavailable: %s", value, reason)
		}
		s.HW = value
	case "preset":
		if _, err := config.ResolvePreset(value, s.Codec, false); err != nil {
			return err
		}
		s.Preset = value
	case "quality":
		caps := config.CodecCaps(s.Codec)
		if inList(value, "high", "balanced", "small") {
			s.Quality, s.CRF = value, nil
			return nil
		}
		if caps.CRFHi == 0 {
			return fmt.Errorf("CRF is not supported for %s; choose high, balanced or small", s.Codec)
		}
		n, err := strconv.ParseFloat(value, 64)
		if err != nil || math.IsNaN(n) || math.IsInf(n, 0) || n < caps.CRFLo || n > caps.CRFHi {
			return fmt.Errorf("CRF for %s must be %.0f-%.0f", s.Codec, caps.CRFLo, caps.CRFHi)
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

func (m *sessionModel) resetAfterCodecChange() {
	s := &m.work.Settings
	caps := config.CodecCaps(s.Codec)
	if !config.Accepts(s.Container, s.Codec, "") {
		s.Container = config.Recommended(s.Codec)
		m.resets["container"] = true
	}
	if !caps.Speed {
		s.Preset = ""
		delete(m.resets, "preset")
	} else if _, err := config.ResolvePreset(s.Preset, s.Codec, false); err != nil {
		s.Preset = m.choicesFor("preset")[0]
		m.resets["preset"] = true
	}
	if !caps.Quality {
		s.Quality, s.CRF = "", nil
		delete(m.resets, "quality")
	} else {
		invalidQuality := (s.Quality == "" && s.CRF == nil) ||
			(s.CRF != nil && (caps.CRFHi == 0 || *s.CRF < caps.CRFLo || *s.CRF > caps.CRFHi))
		if invalidQuality {
			s.Quality, s.CRF = "high", nil
			m.resets["quality"] = true
		}
	}
	m.resetAfterContainerChange()
	if m.work.Mode == ModeSetup {
		if s.HW == "vulkan" || s.HW == "qsv" {
			if m.optionReason("hw", s.HW) != "" || s.CRF != nil {
				s.HW = "auto"
				m.resets["hw"] = true
			}
		}
		if !caps.SizeMode && m.work.TargetBytes != nil {
			m.work.TargetBytes = nil
			m.resets["target"] = true
		}
	}
}

func (m *sessionModel) resetAfterContainerChange() {
	s := &m.work.Settings
	if !config.Accepts(s.Container, "", s.AudioCodec) {
		for _, ac := range config.AudioCodecs {
			if config.Accepts(s.Container, "", ac) {
				s.AudioCodec = ac
				m.resets["audio_codec"] = true
				break
			}
		}
	}
	m.resetAfterAudioCodecChange()
}

func (m *sessionModel) resetAfterAudioCodecChange() {
	s := &m.work.Settings
	if s.AudioCodec != "flac" && !s.AudioSource && s.AudioKbps <= 0 {
		s.AudioSource = true
		s.AudioKbps = 128
		m.resets["audio"] = true
	}
}

func (m *sessionModel) resultPreset() config.Preset {
	p, s := m.work.Preset, config.Clone(m.work.Settings)
	p.Codec, p.Container, p.Speed = s.Codec, s.Container, s.Preset
	p.Quality, p.CRF, p.FPS, p.Resolution = s.Quality, s.CRF, s.FPS, s.Resolution
	p.AudioCodec = s.AudioCodec
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
		if m.work.TargetBytes != nil {
			if !config.CodecCaps(m.work.Settings.Codec).SizeMode {
				return fmt.Errorf("%s supports conversion mode only", m.work.Settings.Codec)
			}
			if *m.work.TargetBytes <= 0 {
				return fmt.Errorf("size must be a positive byte count")
			}
		}
		if reason := m.optionReason("hw", m.work.Settings.HW); reason != "" {
			return fmt.Errorf("hardware %s is unavailable: %s", m.work.Settings.HW, reason)
		}
		check := config.Clone(m.work.Settings)
		if check.AudioSource && check.AudioKbps <= 0 {
			check.AudioKbps = 128
		}
		if err := config.Validate(check); err != nil {
			return err
		}
	}
	if err := config.ValidatePreset(p); err != nil {
		return err
	}
	if m.work.Mode == ModePreset && !config.CodecCaps(p.Codec).Quality && (p.Quality != "" || p.CRF != nil) {
		return errors.New("ffv1 does not accept quality or crf")
	}
	return nil
}

func (m *sessionModel) finalSession() Session {
	s := cloneSession(m.work)
	if s.Mode == ModeSetup && !s.ManualSetup {
		s.Edited = map[string]bool{}
		return s
	}
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
		"quality": v.Quality, "crf": v.CRF, "audio_codec": v.AudioCodec,
		"audio": sessionAudio(v), "fps": v.FPS, "resolution": v.Resolution,
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
