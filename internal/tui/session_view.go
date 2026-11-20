package tui

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

var sessionTitles = map[string]string{
	"start": "Start from preset", "label": "Label", "description": "Description",
	"codec": "Codec", "container": "Container", "preset": "Encoder speed",
	"quality": "Quality or CRF", "audio": "Audio", "fps": "FPS",
	"resolution": "Resolution", "hw": "Hardware", "target": "Size", "review": "Review",
}

func (m *sessionModel) rawValue(key string) string {
	s := m.work.Settings
	switch key {
	case "start", "label":
		return m.work.Preset.Label
	case "description":
		return m.work.Preset.Description
	case "codec":
		return s.Codec
	case "container":
		return s.Container
	case "preset":
		return s.Preset
	case "quality":
		if s.CRF != nil {
			return trimNum(*s.CRF)
		}
		return s.Quality
	case "audio":
		if s.AudioSource {
			return "source"
		}
		return strconv.Itoa(s.AudioKbps)
	case "fps":
		if s.FPS != nil {
			return trimNum(*s.FPS)
		}
		return "source"
	case "resolution":
		if s.Resolution == "" {
			return "source"
		}
		return s.Resolution
	case "hw":
		return s.HW
	case "target":
		if m.work.TargetBytes == nil {
			return ""
		}
		n := *m.work.TargetBytes
		if n%1_000_000 == 0 {
			return strconv.FormatInt(n/1_000_000, 10)
		}
		return strings.TrimRight(fmt.Sprintf("%d.%06d", n/1_000_000, n%1_000_000), "0")
	}
	return ""
}

func (m *sessionModel) displayValue(key string) string {
	value := m.rawValue(key)
	switch key {
	case "quality":
		if m.work.Settings.CRF != nil {
			return "CRF " + value
		}
	case "audio":
		if value != "source" {
			return value + " kbps"
		}
	case "fps":
		if value != "source" {
			return value + " fps"
		}
	case "target":
		if value == "" {
			return "Conversion"
		}
		return "Default (" + value + " MB)"
	}
	if value == "" {
		return "(empty)"
	}
	return value
}

func cleanSessionText(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
}

func (m *sessionModel) style(s string, kind string) string {
	s = cleanSessionText(s)
	if m.plain {
		return s
	}
	style := m.renderer.NewStyle()
	switch kind {
	case "heading":
		style = style.Bold(true)
	case "help":
		style = style.Faint(true)
	case "selected":
		style = style.Bold(true).Foreground(lipgloss.Color("81"))
	case "value":
		style = style.Foreground(lipgloss.Color("229")).Background(lipgloss.Color("236"))
	case "error":
		style = style.Foreground(lipgloss.Color("203"))
	}
	return style.Render(s)
}

func (m *sessionModel) View() string {
	title := "Setup"
	if m.work.Mode == ModePreset {
		title = "New preset"
	}
	header := m.style(title, "heading")
	if m.work.Mode == ModeSetup {
		header += "  " + m.style("an interactive TUI-type setup", "help")
	}
	lines := []string{header, "", m.style(sessionTitles[m.key()], "heading")}
	var items []string
	selected := m.cur
	if m.key() == "review" {
		confirm := "Confirm"
		if selected == 0 {
			confirm = m.style(confirm, "selected")
		}
		items = append(items, confirm)
		edited := m.editedFields()
		for i, key := range m.steps[:len(m.steps)-1] {
			label := sessionTitles[key]
			if selected == i+1 {
				label = m.style(label, "selected")
			}
			line := label + ": " + m.style(m.displayValue(key), "value")
			if edited[key] || (key == "quality" && edited["crf"]) {
				line += " (edited)"
			}
			items = append(items, line)
		}
	} else if m.textOnly() {
		if m.key() == "target" {
			lines = append(lines, m.style("MB; empty selects Conversion", "help"))
		}
		lines = append(lines, "> "+m.style(m.text, "value"))
	} else {
		lines = append(lines, "Current: "+m.style(m.displayValue(m.key()), "value"))
		items = m.choices()
	}
	var tail []string
	if !m.textOnly() && m.key() != "review" && (m.dirty || m.text != "") {
		tail = append(tail, "Type: "+m.style(m.text, "value"))
	}
	if m.msg != "" {
		tail = append(tail, m.style("Error: "+m.msg, "error"))
	}
	room := max(0, m.height-1-len(lines)-len(tail))
	start := max(0, selected-room+1)
	if len(items) <= room {
		start = 0
	}
	for i := start; i < min(len(items), start+room); i++ {
		marker := "  "
		item := items[i]
		if i == selected {
			marker = "> "
			if m.key() != "review" {
				item = m.style(item, "selected")
			}
		} else if m.key() != "review" {
			item = cleanSessionText(item)
		}
		lines = append(lines, marker+item)
	}
	lines = append(lines, tail...)
	if len(lines) > m.height-1 {
		lines = lines[:m.height-1]
	}
	for len(lines) < m.height-1 {
		lines = append(lines, "")
	}
	lines = append(lines, m.contextBar())
	for i := range lines {
		lines[i] = ansi.Truncate(lines[i], m.width, "")
	}
	return strings.Join(lines, "\n")
}

func (m *sessionModel) contextBar() string {
	keys := "Up/Down select  Enter next  Left/Shift+Tab back  Esc cancel"
	if m.textOnly() {
		keys = "Type value  Ctrl+U clear  Enter next  Shift+Tab back  Esc cancel"
	}
	if m.key() == "review" {
		keys = "Up/Down select  Enter confirm/edit  Left/Shift+Tab back  Esc cancel"
	} else if m.fromReview {
		keys = "Enter review  Left/Shift+Tab back  Esc cancel"
		if m.textOnly() {
			keys = "Ctrl+U clear  Enter review  Shift+Tab back  Esc cancel"
		}
	}
	label := m.work.Preset.Label
	if m.work.Mode == ModePreset && strings.TrimSpace(label) == "" {
		label = "(new)"
	}
	context := fmt.Sprintf("step %d/%d  preset %s", m.idx+1, len(m.steps), cleanSessionText(label))
	if lipgloss.Width(keys+"  |  "+context) > m.width {
		keys = "Up/Down Enter Left/Shift+Tab Esc"
		if m.textOnly() {
			keys = "Type Ctrl+U Enter Shift+Tab Esc"
		}
		keys = ansi.Truncate(keys, max(0, m.width-lipgloss.Width(context)-5), "")
	}
	return m.style(keys+"  |  "+context, "help")
}
