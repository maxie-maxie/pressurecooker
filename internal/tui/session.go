package tui

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"golang.org/x/term"

	"github.com/maxie-maxie/pressurecooker/internal/config"
)

var ErrInterrupted = errors.New("interrupted")

type Mode int

const (
	ModeSetup Mode = iota
	ModePreset
)

type Session struct {
	Mode        Mode
	Settings    config.Settings
	Preset      config.Preset // ModePreset result; Setup start-from choice
	TargetBytes *int64        // Setup only; nil = Conversion
	Edited      map[string]bool
}

type SessionOptions struct {
	Presets  []config.Preset
	In       io.Reader
	Out      io.Writer
	Terminal bool // false: plain mode
}

func RunSession(ctx context.Context, s Session, o SessionOptions) (*Session, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if ctx.Err() != nil {
		return nil, ErrInterrupted
	}
	if s.Mode != ModeSetup && s.Mode != ModePreset {
		return nil, errors.New("invalid session mode")
	}
	if o.In == nil {
		o.In = strings.NewReader("")
	}
	if o.Out == nil {
		o.Out = io.Discard
	}
	m := newSessionModel(s, o)
	f, terminal := o.In.(*os.File)
	if !o.Terminal || !terminal || !term.IsTerminal(int(f.Fd())) {
		return m.runSessionFallback(ctx, o)
	}
	opts := []tea.ProgramOption{
		tea.WithInput(o.In), tea.WithOutput(o.Out), tea.WithContext(ctx), tea.WithoutSignalHandler(),
	}
	if m.plain {
		opts = append(opts, tea.WithoutRenderer())
		m.out = o.Out
		if _, err := fmt.Fprintln(o.Out, m.View()); err != nil {
			return nil, err
		}
	} else {
		opts = append(opts, tea.WithAltScreen())
	}
	_, err := tea.NewProgram(m, opts...).Run()
	if ctx.Err() != nil {
		return nil, ErrInterrupted
	}
	if err != nil {
		return nil, err
	}
	if m.writeErr != nil {
		return nil, m.writeErr
	}
	return sessionOutcome(m)
}

func sessionPlain(o SessionOptions) bool {
	_, noColor := os.LookupEnv("NO_COLOR")
	if !o.Terminal || noColor || os.Getenv("TERM") == "dumb" {
		return true
	}
	if f, ok := o.Out.(*os.File); ok {
		return !sessionVT(f)
	}
	return false
}

func (m *sessionModel) runSessionFallback(ctx context.Context, o SessionOptions) (*Session, error) {
	r := bufio.NewReader(o.In)
	if _, err := fmt.Fprintln(o.Out, m.View()); err != nil {
		return nil, err
	}
	type keyRead struct {
		msg tea.KeyMsg
		ok  bool
	}
	for m.status == statusRunning {
		if ctx.Err() != nil {
			return nil, ErrInterrupted
		}
		key := make(chan keyRead, 1)
		// A pipe may be waiting for data when the caller cancels the context.
		go func() {
			msg, ok := readSessionKey(r)
			key <- keyRead{msg, ok}
		}()
		select {
		case <-ctx.Done():
			return nil, ErrInterrupted
		case k := <-key:
			if ctx.Err() != nil {
				return nil, ErrInterrupted
			}
			if !k.ok {
				return nil, nil
			}
			m.Update(k.msg)
		}
		if _, err := fmt.Fprintln(o.Out, m.View()); err != nil {
			return nil, err
		}
	}
	return sessionOutcome(m)
}

func readSessionKey(r *bufio.Reader) (tea.KeyMsg, bool) {
	peek, err := r.Peek(1)
	if err != nil {
		return tea.KeyMsg{}, false
	}
	first := peek[0]
	// A lone Escape on an open pipe must not wait for a possible CSI sequence.
	if first == 27 && r.Buffered() < 3 {
		r.ReadByte()
		return tea.KeyMsg{Type: tea.KeyEsc}, true
	}
	key, ok := readKey(r)
	if first == '\r' && r.Buffered() > 0 {
		if next, _ := r.Peek(1); next[0] == '\n' {
			r.ReadByte()
		}
	}
	return key, ok
}

func sessionOutcome(m *sessionModel) (*Session, error) {
	switch m.status {
	case statusConfirmed:
		s := m.finalSession()
		return &s, nil
	case statusInterrupted:
		return nil, ErrInterrupted
	default:
		return nil, nil
	}
}
