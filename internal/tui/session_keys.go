package tui

import (
	"bufio"

	tea "github.com/charmbracelet/bubbletea"
)

func readKey(r *bufio.Reader) (tea.KeyMsg, bool) {
	b, err := r.ReadByte()
	if err != nil {
		return tea.KeyMsg{}, false
	}
	switch b {
	case '\r', '\n':
		return tea.KeyMsg{Type: tea.KeyEnter}, true
	case '\t':
		return tea.KeyMsg{Type: tea.KeyTab}, true
	case 3:
		return tea.KeyMsg{Type: tea.KeyCtrlC}, true
	case 21:
		return tea.KeyMsg{Type: tea.KeyCtrlU}, true
	case 127, 8:
		return tea.KeyMsg{Type: tea.KeyBackspace}, true
	case 27:
		if msg, ok := readEscape(r); ok {
			return msg, true
		}
		return tea.KeyMsg{Type: tea.KeyEsc}, true
	case ' ':
		return tea.KeyMsg{Type: tea.KeySpace}, true
	}
	if b < 0x80 {
		return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{rune(b)}}, true
	}
	r.UnreadByte()
	rr, _, err := r.ReadRune()
	if err != nil {
		return tea.KeyMsg{}, false
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{rr}}, true
}

func readEscape(r *bufio.Reader) (tea.KeyMsg, bool) {
	peek, err := r.Peek(2)
	if err != nil || len(peek) < 2 || peek[0] != '[' {
		return tea.KeyMsg{}, false
	}
	var kind tea.KeyType
	switch peek[1] {
	case 'A':
		kind = tea.KeyUp
	case 'B':
		kind = tea.KeyDown
	case 'C':
		kind = tea.KeyRight
	case 'D':
		kind = tea.KeyLeft
	case 'Z':
		kind = tea.KeyShiftTab
	default:
		return tea.KeyMsg{}, false
	}
	r.Discard(2)
	return tea.KeyMsg{Type: kind}, true
}
