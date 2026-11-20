package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/maxie-maxie/pressurecooker/internal/config"
	"github.com/maxie-maxie/pressurecooker/internal/console"
	"github.com/maxie-maxie/pressurecooker/internal/ffmpeg"
	"github.com/maxie-maxie/pressurecooker/internal/tui"
	"golang.org/x/term"
)

const (
	exitOK          = 0
	exitCancelled   = 1
	exitUsage       = 2
	exitMissingTool = 3
	exitTargetSmall = 4
	exitEncode      = 5
	exitInterrupted = 130
)

const reminderLine = "Details and limitations: pressurecooker --help-details"

type Env struct {
	In                          io.Reader
	Out                         io.Writer
	IsTerminalIn, IsTerminalOut *bool
	Store                       func() (config.Store, error)
	SaveLatest                  func(config.Store, config.Settings) error
	Runner                      ffmpeg.Runner
	ResolveTool                 func(explicit, name string) (string, error)
	RunSession                  func(context.Context, tui.Session, tui.SessionOptions) (*tui.Session, error)
	Now                         func() time.Time
}

func Main(args []string, version string) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return MainWith(ctx, args, version, Env{In: os.Stdin, Out: os.Stdout, Store: config.UserStore, Runner: ffmpeg.ExecRunner{}})
}

func mainWith(args []string, version string, out io.Writer, store func() (config.Store, error)) int {
	return MainWith(context.Background(), args, version, Env{In: strings.NewReader(""), Out: out, Store: store})
}

func MainWith(ctx context.Context, args []string, version string, env Env) int {
	if ctx == nil {
		ctx = context.Background()
	}
	if env.Out == nil {
		env.Out = io.Discard
	}
	if env.In == nil {
		env.In = strings.NewReader("")
	}
	if env.Store == nil {
		env.Store = config.UserStore
	}
	if env.SaveLatest == nil {
		env.SaveLatest = func(s config.Store, settings config.Settings) error { return s.SaveLatest(settings) }
	}
	if env.Runner == nil {
		env.Runner = ffmpeg.ExecRunner{}
	}
	if env.ResolveTool == nil {
		env.ResolveTool = ffmpeg.ResolveTool
	}
	if env.RunSession == nil {
		env.RunSession = tui.RunSession
	}
	if env.Now == nil {
		env.Now = time.Now
	}
	termOut := isTerminalWriter(env.Out, env.IsTerminalOut)
	interactive := isTerminalReader(env.In, env.IsTerminalIn) && termOut
	w := console.New(env.Out, termOut)
	parsed, err := Parse(args)
	if err != nil {
		usageError(w, err)
		return exitUsage
	}
	switch {
	case parsed.ShowHelp:
		w.Line(strings.TrimSuffix(Help, "\n"))
		return exitOK
	case parsed.ShowDetails:
		w.Line(strings.TrimSuffix(Details, "\n"))
		return exitOK
	case parsed.ShowVersion:
		w.Line("pressurecooker " + version)
		return exitOK
	}
	if parsed.NewPreset != nil && !interactive {
		return failFooter(w, nil, "--new-preset needs a terminal", exitUsage)
	}
	if parsed.Setup && !interactive {
		return failFooter(w, nil, "--setup needs a terminal", exitUsage)
	}
	if parsed.NewPreset == nil && !parsed.ListPresets {
		provisional := parsed
		if container := destinationContainer(parsed.Destination); container != "" {
			if parsed.Origins["container"] == "explicit" && parsed.Settings.Container != container {
				return failFooter(w, nil, fmt.Sprintf("destination extension .%s conflicts with explicit container %s", container, parsed.Settings.Container), exitUsage)
			}
			provisional.Settings.Container = container
		}
		if parsed.Origins["codec"] != "explicit" {
			provisional.Settings.Codec = ""
		}
		if err := validateInitialPaths(provisional); err != nil {
			return failFooter(w, nil, err.Error(), exitUsage)
		}
	}
	storage, err := env.Store()
	if err != nil {
		return failFooter(w, nil, err.Error(), exitUsage)
	}
	presets, err := storage.LoadPresets()
	if err != nil {
		return failFooter(w, nil, err.Error(), exitUsage)
	}
	if parsed.ListPresets {
		if presets == nil {
			presets = []config.Preset{config.FactoryPreset()}
		}
		for _, p := range presets {
			printPreset(w, p)
		}
		return exitOK
	}
	if parsed.NewPreset != nil {
		p := config.FactoryPreset()
		p.ID = *parsed.NewPreset
		for _, existing := range presets {
			if existing.ID == p.ID {
				p = existing
				break
			}
		}
		_, code := recordPreset(ctx, env, w, storage, p, presets)
		return code
	}
	if presets == nil {
		if interactive && !parsed.DryRun {
			p, code := recordPreset(ctx, env, w, storage, config.FactoryPreset(), nil)
			if p == nil {
				return code
			}
			presets = []config.Preset{*p}
		} else {
			presets = []config.Preset{config.FactoryPreset()}
		}
	}
	var active *config.Preset
	for i := range presets {
		if presets[i].ID == parsed.PresetID {
			active = &presets[i]
			break
		}
	}
	if active == nil {
		return failFooter(w, nil, fmt.Sprintf("preset %d not found; see --preset-list", parsed.PresetID), exitUsage)
	}
	var latest *config.Settings
	if parsed.Latest {
		latest, err = storage.LoadLatest()
		if err != nil {
			return failFooter(w, nil, err.Error(), exitUsage)
		}
	}
	settings := active.Settings()
	run, err := Resolve(parsed, &settings, latest)
	if err != nil {
		return failFooter(w, nil, err.Error(), exitUsage)
	}
	if err := validatePaths(run); err != nil {
		return failFooter(w, nil, err.Error(), exitUsage)
	}
	if run.Setup && !run.DryRun {
		confirmed, code := session(ctx, env, w, tui.Session{
			Mode: tui.ModeSetup, Settings: config.Clone(run.Settings), Preset: *active,
			TargetBytes: run.TargetBytes, Edited: map[string]bool{},
		}, presets)
		if confirmed == nil {
			return code
		}
		run.Settings, run.TargetBytes = config.Clone(confirmed.Settings), confirmed.TargetBytes
		run.Origins = cloneOrigins(run.Origins)
		for key, edited := range confirmed.Edited {
			if edited {
				run.Origins[key] = "explicit"
			}
		}
		if confirmed.Edited["crf"] && run.Settings.CRF != nil {
			run.Settings.Quality, run.Origins["quality"] = "", "factory"
		}
		if confirmed.Edited["quality"] && run.Settings.CRF == nil {
			run.Origins["crf"] = "factory"
		}
		run.Mode = ModeConversion
		if run.TargetBytes != nil {
			if *run.TargetBytes <= 0 {
				return failFooter(w, nil, "target must be a positive byte count", exitUsage)
			}
			run.Mode = ModeDefault
		} else if run.ForceTarget || run.VMAF {
			return failFooter(w, nil, "-ft and --vmaf require a size", exitUsage)
		}
		run, err = validateRun(run)
		if err != nil {
			return failFooter(w, nil, err.Error(), exitUsage)
		}
	}
	if err := validatePaths(run); err != nil {
		return failFooter(w, nil, err.Error(), exitUsage)
	}
	w.Line(reminderLine)
	return executeRun(ctx, env, w, storage, run, interactive)
}

func session(ctx context.Context, env Env, w *console.Writer, s tui.Session, presets []config.Preset) (*tui.Session, int) {
	confirmed, err := env.RunSession(ctx, s, tui.SessionOptions{Presets: presets, In: env.In, Out: env.Out, Terminal: true})
	if ctx.Err() != nil || errors.Is(err, tui.ErrInterrupted) || errors.Is(err, context.Canceled) {
		return nil, failFooter(w, nil, "interrupted", exitInterrupted)
	}
	if err != nil {
		return nil, failFooter(w, nil, err.Error(), exitUsage)
	}
	if confirmed == nil {
		w.Line("Cancelled. Nothing was saved.")
		return nil, exitCancelled
	}
	return confirmed, exitOK
}

func recordPreset(ctx context.Context, env Env, w *console.Writer, storage config.Store, p config.Preset, presets []config.Preset) (*config.Preset, int) {
	confirmed, code := session(ctx, env, w, tui.Session{Mode: tui.ModePreset, Preset: p, Settings: p.Settings()}, presets)
	if confirmed == nil {
		return nil, code
	}
	preset := confirmed.Preset
	preset.ID = p.ID
	saved, err := storage.SavePreset(preset)
	if err != nil {
		return nil, failFooter(w, nil, err.Error(), exitUsage)
	}
	return &saved, exitOK
}

func printPreset(w *console.Writer, p config.Preset) {
	header := fmt.Sprintf("%d  %s", p.ID, p.Label)
	if p.Description != "" {
		header += "  " + p.Description
	}
	w.Line(header)
	quality := p.Quality
	if p.CRF != nil {
		quality = "crf " + trimFloat(*p.CRF)
	}
	audio, fps, res := "source", "source", "source"
	if p.AudioKbps != nil {
		audio = fmt.Sprintf("%dk", *p.AudioKbps)
	}
	if p.FPS != nil {
		fps = trimFloat(*p.FPS)
	}
	if p.Resolution != "" {
		res = p.Resolution
	}
	w.Line(fmt.Sprintf("   %s %s speed %s %s audio:%s fps:%s res:%s", p.Codec, p.Container, p.Speed, quality, audio, fps, res))
}

func cloneOrigins(in config.Origins) config.Origins {
	out := make(config.Origins, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func isTerminalReader(r io.Reader, override *bool) bool {
	if override != nil {
		return *override
	}
	f, ok := r.(*os.File)
	return ok && term.IsTerminal(int(f.Fd()))
}

func isTerminalWriter(w io.Writer, override *bool) bool {
	if override != nil {
		return *override
	}
	f, ok := w.(*os.File)
	return ok && term.IsTerminal(int(f.Fd()))
}

func usageError(w *console.Writer, err error) {
	w.Footer([]console.Diagnostic{{Severity: "error", Message: err.Error()}})
}
