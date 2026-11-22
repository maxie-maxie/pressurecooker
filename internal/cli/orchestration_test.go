package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/maxie-maxie/pressurecooker/internal/config"
	"github.com/maxie-maxie/pressurecooker/internal/console"
	"github.com/maxie-maxie/pressurecooker/internal/ffmpeg"
	"github.com/maxie-maxie/pressurecooker/internal/tui"
)

type mockRunner struct {
	calls [][]string
	full  int
	sizes []int
	fail  bool
	vmaf  bool
}

func argValue(args []string, key string) string {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == key {
			return args[i+1]
		}
	}
	return ""
}
func (r *mockRunner) Run(ctx context.Context, args []string, on func(console.Event)) (ffmpeg.Result, error) {
	r.calls = append(r.calls, append([]string(nil), args...))
	if err := ctx.Err(); err != nil {
		return ffmpeg.Result{}, err
	}
	if strings.Contains(filepath.Base(args[0]), "ffprobe") {
		joined := strings.Join(args, " ")
		if strings.Contains(joined, "duration") {
			return ffmpeg.Result{Stdout: "60\n"}, nil
		}
		if argValue(args, "-select_streams") == "v:0" {
			return ffmpeg.Result{Stdout: `{"streams":[{"width":1280,"height":720,"codec_name":"av1","avg_frame_rate":"30/1"}],"format":{"format_name":"mov,mp4,m4a"}}`}, nil
		}
		return ffmpeg.Result{Stdout: `{"streams":[{"codec_name":"aac","bit_rate":"96000"}]}`}, nil
	}
	if argValue(args, "-lavfi") != "" {
		return ffmpeg.Result{Stdout: "n:1 All:0.965 (14.5)\nVMAF score: 95\n"}, nil
	}
	for _, a := range args {
		if a == "-filters" {
			filters := " TSC ssim VV->V Calculate SSIM\n"
			if r.vmaf {
				filters += " TSC libvmaf VV->V Calculate VMAF\n"
			}
			return ffmpeg.Result{Stdout: filters}, nil
		}
	}
	if r.fail {
		return ffmpeg.Result{ExitCode: 1, StderrTail: []string{"encode failed"}}, nil
	}
	if argValue(args, "-pass:v") == "1" {
		log := argValue(args, "-passlogfile:v")
		if err := os.WriteFile(log+"-0.log", []byte("stats"), 0600); err != nil {
			return ffmpeg.Result{}, err
		}
		return ffmpeg.Result{}, nil
	}
	out := args[len(args)-1]
	size := 1000
	if strings.HasSuffix(out, ".part") {
		r.full++
		if len(r.sizes) > 0 {
			size = r.sizes[min(r.full-1, len(r.sizes)-1)]
		}
	}
	if err := os.WriteFile(out, bytes.Repeat([]byte("x"), size), 0600); err != nil {
		return ffmpeg.Result{}, err
	}
	if on != nil {
		on(console.Event{Duration: 60, Timeline: 60, End: true})
	}
	return ffmpeg.Result{}, nil
}
func runFixture(t *testing.T, saved bool) (string, string, config.Store, *mockRunner, Env, *bytes.Buffer) {
	t.Helper()
	root := t.TempDir()
	src, dst := filepath.Join(root, "input.mp4"), filepath.Join(root, "output.mp4")
	if err := os.WriteFile(src, []byte("source"), 0600); err != nil {
		t.Fatal(err)
	}
	store := config.Store{Dir: filepath.Join(root, "config")}
	if saved {
		p := config.FactoryPreset()
		p.Resolution = ""
		if _, err := store.SavePreset(p); err != nil {
			t.Fatal(err)
		}
	}
	runner := &mockRunner{}
	out := &bytes.Buffer{}
	env := Env{In: strings.NewReader(""), Out: out, IsTerminalIn: boolPtr(false), IsTerminalOut: boolPtr(false),
		Store: func() (config.Store, error) { return store, nil }, Runner: runner,
		ResolveTool: func(explicit, name string) (string, error) {
			if explicit != "" {
				return explicit, nil
			}
			return name, nil
		},
	}
	return src, dst, store, runner, env, out
}
func assertNoOutput(t *testing.T, dst string) {
	t.Helper()
	if _, err := os.Stat(dst); !os.IsNotExist(err) {
		t.Fatalf("published %s", dst)
	}
	parts, _ := filepath.Glob(filepath.Join(filepath.Dir(dst), ".pressurecooker-*.part"))
	if len(parts) > 0 {
		t.Fatalf("parts: %v", parts)
	}
}
func TestConversionAndSourcePrompts(t *testing.T) {
	for _, tc := range []struct {
		input    string
		terminal bool
		code     int
	}{{"", false, 2}, {"", true, 1}, {"n\n", true, 1}, {"y\n", true, 0}, {"\x03\n", true, 130}} {
		src, dst, _, r, env, out := runFixture(t, true)
		env.In = strings.NewReader(tc.input)
		env.IsTerminalIn, env.IsTerminalOut = boolPtr(tc.terminal), boolPtr(tc.terminal)
		code := MainWith(context.Background(), []string{src, dst, "-q", "off"}, "test", env)
		if code != tc.code || !strings.Contains(out.String(), conversionQuestion) {
			t.Fatalf("%+v: %d %s", tc, code, out.String())
		}
		if code != 0 {
			assertNoOutput(t, dst)
			if r.full != 0 {
				t.Fatal("encoded before answer")
			}
		}
	}
	for _, tc := range []struct {
		input    string
		terminal bool
		code     int
	}{{"", false, 2}, {"n\n", true, 1}, {"y\n", true, 0}} {
		src, dst, _, r, env, out := runFixture(t, true)
		env.In = strings.NewReader(tc.input)
		env.IsTerminalIn, env.IsTerminalOut = boolPtr(tc.terminal), boolPtr(tc.terminal)
		code := MainWith(context.Background(), []string{src, dst, "1", "-q", "off", "-s", "1080p", "-r", "60"}, "test", env)
		if code != tc.code || !strings.Contains(out.String(), "1920x1080 and 60 fps") {
			t.Fatalf("%+v: %d %s", tc, code, out.String())
		}
		if code != 0 {
			assertNoOutput(t, dst)
		} else {
			for _, args := range r.calls {
				if strings.HasSuffix(args[len(args)-1], ".part") && argValue(args, "-vf") != "" {
					t.Fatalf("cap not cleared: %v", args)
				}
			}
		}
	}
}
func TestDefaultAndLatestSave(t *testing.T) {
	src, dst, store, r, env, out := runFixture(t, true)
	code := MainWith(context.Background(), []string{src, dst, "1", "-q", "off"}, "test", env)
	if code != 0 || r.full != 1 || strings.Count(out.String(), reminderLine) != 1 || !strings.Contains(out.String(), "2-pass abr") {
		t.Fatalf("%d %s", code, out.String())
	}
	latest, err := store.LoadLatest()
	if err != nil || latest == nil || latest.HW != "off" {
		t.Fatalf("%+v %v", latest, err)
	}
	data, _ := os.ReadFile(filepath.Join(store.Dir, "latest.json"))
	if strings.Contains(string(data), "target") || strings.Contains(string(data), "verbose") {
		t.Fatalf("retired fields: %s", data)
	}
	sawPublishedProbe := false
	for _, args := range r.calls {
		if args[0] == "ffprobe" && args[len(args)-1] == dst {
			sawPublishedProbe = true
		}
	}
	if !sawPublishedProbe {
		t.Fatal("published file not probed")
	}
}
func TestForceDecisionAndCleanup(t *testing.T) {
	for _, tc := range []struct {
		terminal bool
		input    string
		flag     string
		code     int
		vmaf     bool
	}{
		{false, "", "", 2, false}, {true, "n\n", "", 4, false}, {true, "y\n", "", 0, false},
		{false, "", "-ft", 0, false}, {false, "", "--vmaf", 2, false},
	} {
		src, dst, store, r, env, out := runFixture(t, true)
		before := config.FactoryPreset().Settings()
		before.HW = "off"
		if err := store.SaveLatest(before); err != nil {
			t.Fatal(err)
		}
		data, _ := os.ReadFile(filepath.Join(store.Dir, "latest.json"))
		r.sizes = []int{2000000, 2000000, 1000}
		env.In = strings.NewReader(tc.input)
		env.IsTerminalIn, env.IsTerminalOut = boolPtr(tc.terminal), boolPtr(tc.terminal)
		r.vmaf = tc.vmaf
		args := []string{src, dst, "1", "-q", "off"}
		if tc.flag != "" {
			args = append(args, tc.flag)
		}
		code := MainWith(context.Background(), args, "test", env)
		if code != tc.code {
			t.Fatalf("%+v: %d %s", tc, code, out.String())
		}
		after, _ := os.ReadFile(filepath.Join(store.Dir, "latest.json"))
		if !bytes.Equal(data, after) {
			t.Fatal("force decision saved latest")
		}
		if code != 0 {
			assertNoOutput(t, dst)
		}
		if code == 0 && !strings.Contains(out.String(), "Metric:       ssim") {
			t.Fatalf("metric absent: %s", out.String())
		}
	}
}
func TestInfeasibleAndVMAFErrors(t *testing.T) {
	for _, tc := range []struct {
		flags []string
		code  int
		text  string
	}{
		{nil, 2, "needs a terminal"},
		{[]string{"-ft"}, 4, "Audio at"},
		{[]string{"-ft", "--vmaf"}, 2, "--vmaf needs FFmpeg built with libvmaf"},
	} {
		src, dst, _, _, env, out := runFixture(t, true)
		args := append([]string{src, dst, ".001", "-a", "128", "-q", "off"}, tc.flags...)
		code := MainWith(context.Background(), args, "test", env)
		if code != tc.code || !strings.Contains(out.String(), tc.text) {
			t.Fatalf("%v: %d %s", tc.flags, code, out.String())
		}
		assertNoOutput(t, dst)
	}
	src, dst, _, r, env, out := runFixture(t, true)
	r.sizes = []int{2000000, 2000000, 1000}
	r.vmaf = true
	code := MainWith(context.Background(), []string{src, dst, "1", "-q", "off", "-ft", "--vmaf"}, "test", env)
	if code != 0 || !strings.Contains(out.String(), "Metric:       vmaf") {
		t.Fatalf("%d %s", code, out.String())
	}
}
func TestDryRunNoTUIOrEncodesOrWrites(t *testing.T) {
	for _, size := range []string{"", ".001"} {
		src, dst, store, r, env, out := runFixture(t, false)
		env.IsTerminalIn, env.IsTerminalOut = boolPtr(true), boolPtr(true)
		env.RunSession = func(context.Context, tui.Session, tui.SessionOptions) (*tui.Session, error) {
			t.Fatal("dry run opened TUI")
			return nil, nil
		}
		args := []string{src, dst, "-d", "--setup", "-a", "128"}
		if size != "" {
			args = append(args, size)
		}
		code := MainWith(context.Background(), args, "test", env)
		if code != 0 || !strings.Contains(out.String(), "warning: Source is") {
			t.Fatalf("%d %s", code, out.String())
		}
		if size == "" && !strings.Contains(out.String(), "warning: "+conversionQuestion) {
			t.Fatal("missing conversion warning")
		}
		if size != "" && !strings.Contains(out.String(), "Force-target changes") {
			t.Fatal("missing force warning")
		}
		for _, args := range r.calls {
			if args[0] != "ffprobe" {
				t.Fatalf("dry run used FFmpeg: %v", args)
			}
		}
		assertNoOutput(t, dst)
		if _, err := os.Stat(store.Dir); !os.IsNotExist(err) {
			t.Fatal("dry run wrote config")
		}
	}
}
func TestPathsValidatedBeforeSessions(t *testing.T) {
	for _, kind := range []string{"missing", "empty", "directory", "alias", "parent-file", "existing"} {
		src, dst, _, r, env, out := runFixture(t, false)
		switch kind {
		case "missing":
			src += "missing"
		case "empty":
			if err := os.WriteFile(src, nil, 0600); err != nil {
				t.Fatal(err)
			}
		case "directory":
			src = filepath.Dir(src)
		case "alias":
			dst = src
		case "parent-file":
			dst = filepath.Join(src, "output.mp4")
		case "existing":
			if err := os.WriteFile(dst, []byte("existing"), 0600); err != nil {
				t.Fatal(err)
			}
		}
		env.IsTerminalIn, env.IsTerminalOut = boolPtr(true), boolPtr(true)
		env.RunSession = func(context.Context, tui.Session, tui.SessionOptions) (*tui.Session, error) {
			t.Fatal("invalid path opened session")
			return nil, nil
		}
		code := MainWith(context.Background(), []string{src, dst, "1", "--setup", "-q", "off"}, "test", env)
		if code != 2 || len(r.calls) > 0 {
			t.Fatalf("%s: %d %s", kind, code, out.String())
		}
	}
}
func TestPresetRecorderAndCancellation(t *testing.T) {
	for _, tc := range []struct {
		arg    string
		status int
	}{{"", 0}, {"2", 0}, {"", 1}, {"", 130}} {
		_, _, store, _, env, out := runFixture(t, false)
		env.IsTerminalIn, env.IsTerminalOut = boolPtr(true), boolPtr(true)
		env.RunSession = func(_ context.Context, s tui.Session, o tui.SessionOptions) (*tui.Session, error) {
			id := 0
			if tc.arg != "" {
				id, _ = strconv.Atoi(tc.arg)
			}
			if s.Mode != tui.ModePreset || s.Preset.ID != id || s.Preset.Codec != "av1" {
				t.Fatalf("prefill: %+v", s)
			}
			if tc.status == 1 {
				return nil, nil
			}
			if tc.status == 130 {
				return nil, tui.ErrInterrupted
			}
			s.Preset.Label = "Saved"
			return &s, nil
		}
		args := []string{"--new-preset"}
		if tc.arg != "" {
			args = append(args, tc.arg)
		}
		code := MainWith(context.Background(), args, "test", env)
		if code != tc.status {
			t.Fatalf("%+v: %d %s", tc, code, out.String())
		}
		if code == 0 {
			p, err := store.LoadPresets()
			if err != nil || len(p) != 1 || p[0].Label != "Saved" {
				t.Fatalf("%v %v", p, err)
			}
		} else {
			if _, err := os.Stat(store.Dir); !os.IsNotExist(err) {
				t.Fatal("cancel wrote config")
			}
		}
	}
}
func TestFirstRunAndSetupSaveRules(t *testing.T) {
	src, dst, store, _, env, out := runFixture(t, false)
	env.IsTerminalIn, env.IsTerminalOut = boolPtr(true), boolPtr(true)
	env.In = strings.NewReader("y\n")
	calls := 0
	env.RunSession = func(_ context.Context, s tui.Session, o tui.SessionOptions) (*tui.Session, error) {
		calls++
		if calls == 1 {
			if s.Mode != tui.ModePreset || s.Preset.ID != 1 {
				t.Fatal("first run prefill")
			}
			s.Preset.Resolution = ""
		} else {
			if s.Mode != tui.ModeSetup || s.TargetBytes == nil || *s.TargetBytes != 1000000 || s.Preset.ID != 1 {
				t.Fatalf("setup: %+v", s)
			}
			s.TargetBytes = nil
			s.Settings.Quality = "high"
			s.Edited = map[string]bool{"quality": true, "target": true}
		}
		return &s, nil
	}
	code := MainWith(context.Background(), []string{src, dst, "1", "--setup", "-q", "off"}, "test", env)
	if code != 0 || calls != 2 || strings.Contains(out.String(), conversionQuestion) {
		t.Fatalf("%d %s calls %d", code, out.String(), calls)
	}
	latest, err := store.LoadLatest()
	if err != nil || latest == nil || latest.Quality != "high" {
		t.Fatalf("%+v %v", latest, err)
	}
}
func TestSetupFailureAndSaveFailureWarning(t *testing.T) {
	for _, failEncode := range []bool{true, false} {
		src, dst, store, r, env, out := runFixture(t, true)
		env.IsTerminalIn, env.IsTerminalOut = boolPtr(true), boolPtr(true)
		env.RunSession = func(_ context.Context, s tui.Session, _ tui.SessionOptions) (*tui.Session, error) { return &s, nil }
		r.fail = failEncode
		saved := 0
		env.SaveLatest = func(config.Store, config.Settings) error {
			saved++
			return errors.New("disk full\nsettings unavailable")
		}
		code := MainWith(context.Background(), []string{src, dst, "1", "--setup", "-q", "off"}, "test", env)
		if failEncode {
			if code != 5 || saved != 0 {
				t.Fatalf("%d %s", code, out.String())
			}
			assertNoOutput(t, dst)
			if latest, err := store.LoadLatest(); err != nil || latest != nil {
				t.Fatalf("%+v %v", latest, err)
			}
		} else {
			if code != 0 || saved != 1 {
				t.Fatalf("%d %s", code, out.String())
			}
			result := strings.Index(out.String(), "Output:")
			warning := strings.Index(out.String(), "warning: could not save latest")
			if result < 0 || warning < result || !strings.Contains(out.String(), "disk full; settings unavailable") {
				t.Fatalf("footer: %s", out.String())
			}
		}
	}
}
func TestMissingPresetAndCorruptLatest(t *testing.T) {
	src, dst, store, r, env, out := runFixture(t, true)
	code := MainWith(context.Background(), []string{src, dst, "1", "-ps", "2"}, "test", env)
	if code != 2 || len(r.calls) > 0 || !strings.Contains(out.String(), "preset 2 not found; see --preset-list") {
		t.Fatalf("%d %s", code, out.String())
	}
	before := []byte("{broken")
	if err := os.WriteFile(filepath.Join(store.Dir, "latest.json"), before, 0600); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	code = MainWith(context.Background(), []string{src, dst, "1", "--latest"}, "test", env)
	after, _ := os.ReadFile(filepath.Join(store.Dir, "latest.json"))
	if code != 2 || len(r.calls) > 0 || !bytes.Equal(before, after) || !strings.Contains(out.String(), "latest.json") {
		t.Fatalf("%d %s", code, out.String())
	}
}
func TestMissingToolsExitThree(t *testing.T) {
	src, dst, _, r, env, out := runFixture(t, true)
	env.ResolveTool = func(string, string) (string, error) { return "", fmt.Errorf("tool missing") }
	code := MainWith(context.Background(), []string{src, dst, "1", "-q", "off"}, "test", env)
	if code != 3 || len(r.calls) > 0 {
		t.Fatalf("%d %s", code, out.String())
	}
}

func TestDirectoryCollisionCheckedBeforeSetup(t *testing.T) {
	src, _, store, _, env, out := runFixture(t, true)
	p := config.FactoryPreset()
	p.Container = "mkv"
	p.Resolution = ""
	if _, err := store.SavePreset(p); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(filepath.Dir(src), "exports")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	collision := filepath.Join(dir, "input.fit1.mkv")
	if err := os.WriteFile(collision, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	env.IsTerminalIn, env.IsTerminalOut = boolPtr(true), boolPtr(true)
	env.RunSession = func(context.Context, tui.Session, tui.SessionOptions) (*tui.Session, error) {
		t.Fatal("collision opened setup")
		return nil, nil
	}
	code := MainWith(context.Background(), []string{src, dir, "1", "--setup", "-q", "off"}, "test", env)
	if code != 2 || !strings.Contains(out.String(), collision) {
		t.Fatalf("%d %s", code, out.String())
	}
}
func TestPromptRequiresBothTerminals(t *testing.T) {
	for _, terms := range [][2]bool{{false, true}, {true, false}} {
		src, dst, _, r, env, out := runFixture(t, true)
		env.IsTerminalIn, env.IsTerminalOut = boolPtr(terms[0]), boolPtr(terms[1])
		code := MainWith(context.Background(), []string{src, dst, "-q", "off"}, "test", env)
		if code != 2 || r.full != 0 || !strings.Contains(out.String(), "needs a terminal to answer") {
			t.Fatalf("%d %s", code, out.String())
		}
		assertNoOutput(t, dst)
	}
}
func TestSeparateConversionAndSourceQuestions(t *testing.T) {
	src, dst, _, r, env, out := runFixture(t, true)
	env.IsTerminalIn, env.IsTerminalOut = boolPtr(true), boolPtr(true)
	env.In = strings.NewReader("y\nn\n")
	code := MainWith(context.Background(), []string{src, dst, "-q", "off", "-s", "1080p"}, "test", env)
	if code != 1 || r.full != 0 || !strings.Contains(out.String(), conversionQuestion) || !strings.Contains(out.String(), "Keep the source size?") {
		t.Fatalf("%d %s", code, out.String())
	}
	assertNoOutput(t, dst)
}
func TestPresetReplacementPrefillsExistingSlot(t *testing.T) {
	_, _, store, _, env, out := runFixture(t, true)
	p := config.FactoryPreset()
	p.ID, p.Label, p.Description = 2, "Existing", "description"
	if _, err := store.SavePreset(p); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(filepath.Join(store.Dir, "presets.json"))
	env.IsTerminalIn, env.IsTerminalOut = boolPtr(true), boolPtr(true)
	env.RunSession = func(_ context.Context, s tui.Session, _ tui.SessionOptions) (*tui.Session, error) {
		if s.Preset.ID != 2 || s.Preset.Label != "Existing" || s.Preset.Description != "description" {
			t.Fatalf("%+v", s.Preset)
		}
		return nil, nil
	}
	code := MainWith(context.Background(), []string{"--new-preset", "2"}, "test", env)
	after, _ := os.ReadFile(filepath.Join(store.Dir, "presets.json"))
	if code != 1 || !bytes.Equal(before, after) {
		t.Fatalf("%d %s", code, out.String())
	}
}
func TestSetupCancellationPreservesBothFiles(t *testing.T) {
	src, dst, store, _, env, out := runFixture(t, true)
	if err := store.SaveLatest(config.FactoryPreset().Settings()); err != nil {
		t.Fatal(err)
	}
	beforePreset, _ := os.ReadFile(filepath.Join(store.Dir, "presets.json"))
	beforeLatest, _ := os.ReadFile(filepath.Join(store.Dir, "latest.json"))
	env.IsTerminalIn, env.IsTerminalOut = boolPtr(true), boolPtr(true)
	env.RunSession = func(context.Context, tui.Session, tui.SessionOptions) (*tui.Session, error) { return nil, nil }
	code := MainWith(context.Background(), []string{src, dst, "1", "--setup"}, "test", env)
	afterPreset, _ := os.ReadFile(filepath.Join(store.Dir, "presets.json"))
	afterLatest, _ := os.ReadFile(filepath.Join(store.Dir, "latest.json"))
	if code != 1 || !bytes.Equal(beforePreset, afterPreset) || !bytes.Equal(beforeLatest, afterLatest) {
		t.Fatalf("%d %s", code, out.String())
	}
	assertNoOutput(t, dst)
}

func TestSessionCommandsRequireTerminal(t *testing.T) {
	for _, args := range [][]string{{"--new-preset"}, {"input.mp4", "output.mp4", "--setup"}, {"input.mp4", "output.mp4", "--setup", "-d"}} {
		var out bytes.Buffer
		code := MainWith(context.Background(), args, "test", Env{Out: &out, IsTerminalIn: boolPtr(false), IsTerminalOut: boolPtr(false),
			Store: func() (config.Store, error) {
				t.Fatal("non-terminal session accessed config")
				return config.Store{}, nil
			},
			RunSession: func(context.Context, tui.Session, tui.SessionOptions) (*tui.Session, error) {
				t.Fatal("non-terminal session opened")
				return nil, nil
			},
		})
		if code != 2 || !strings.Contains(out.String(), "needs a terminal") {
			t.Fatalf("%v: %d %s", args, code, out.String())
		}
	}
}
