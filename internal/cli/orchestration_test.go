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
	calls         [][]string
	full          int
	sizes         []int
	fail          bool
	vmaf          bool
	hardware      bool
	qsvOnly       bool
	weakGPU       bool
	noAudio       bool
	failProbePath string
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
		if args[len(args)-1] == r.failProbePath {
			return ffmpeg.Result{ExitCode: 1, StderrTail: []string{"published probe failed"}}, nil
		}
		joined := strings.Join(args, " ")
		if strings.Contains(joined, "duration") {
			return ffmpeg.Result{Stdout: "60\n"}, nil
		}
		if argValue(args, "-select_streams") == "v:0" {
			return ffmpeg.Result{Stdout: `{"streams":[{"width":1280,"height":720,"codec_name":"av1","avg_frame_rate":"30/1"}],"format":{"format_name":"mov,mp4,m4a"}}`}, nil
		}
		if r.noAudio {
			return ffmpeg.Result{Stdout: `{"streams":[]}`}, nil
		}
		return ffmpeg.Result{Stdout: `{"streams":[{"codec_name":"aac","bit_rate":"96000"}]}`}, nil
	}
	if argValue(args, "-lavfi") != "" {
		if r.weakGPU && strings.Contains(strings.Join(args, " "), "sample-3.") {
			return ffmpeg.Result{Stdout: "n:1 All:0.01 (0.04)\n"}, nil
		}
		return ffmpeg.Result{Stdout: "n:1 All:0.965 (14.5)\nVMAF score: 95\n"}, nil
	}
	for _, a := range args {
		if a == "-encoders" {
			return ffmpeg.Result{Stdout: " V..... libsvtav1\n V..... libx265\n V..... libx264\n V..... libvpx-vp9\n V..... prores_ks\n V..... ffv1\n V..... libtheora\n"}, nil
		}
		if a == "-filters" {
			filters := " TSC ssim VV->V Calculate SSIM\n"
			if r.vmaf {
				filters += " TSC libvmaf VV->V Calculate VMAF\n"
			}
			return ffmpeg.Result{Stdout: filters}, nil
		}
	}
	if args[len(args)-1] == os.DevNull && (argValue(args, "-init_hw_device") != "" || strings.HasSuffix(argValue(args, "-c:v"), "_qsv")) {
		if r.hardware && (!r.qsvOnly || argValue(args, "-init_hw_device") == "") {
			return ffmpeg.Result{}, nil
		}
		return ffmpeg.Result{ExitCode: 1, StderrTail: []string{"no supported GPU"}}, nil
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
	if params := argValue(args, "-x265-params"); strings.HasPrefix(params, "pass=1:stats=") {
		path := strings.TrimPrefix(params, "pass=1:stats=")
		path = strings.NewReplacer(`\\`, `\`, `\:`, `:`, `\'`, `'`).Replace(path)
		return ffmpeg.Result{}, os.WriteFile(path, []byte("stats"), 0600)
	}
	if args[len(args)-1] == os.DevNull {
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
		if code == 0 && bytes.Equal(data, after) {
			t.Fatal("successful force did not save chosen settings")
		}
		if code != 0 && !bytes.Equal(data, after) {
			t.Fatal("failed force decision saved latest")
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
		if size != "" && !strings.Contains(out.String(), "Force-target may change") {
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
		args := []string{"--preset"}
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
			s.ManualSetup = true
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
		env.RunSession = func(_ context.Context, s tui.Session, _ tui.SessionOptions) (*tui.Session, error) {
			s.ManualSetup = true
			return &s, nil
		}
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
	code := MainWith(context.Background(), []string{src, dst, "1", "-p", "2"}, "test", env)
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
	code := MainWith(context.Background(), []string{"--preset", "2"}, "test", env)
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
	for _, args := range [][]string{{"--preset"}, {"input.mp4", "output.mp4", "--setup"}, {"input.mp4", "output.mp4", "--setup", "-d"}} {
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

func TestSavedPresetRunHasNoTUI(t *testing.T) {
	src, dst, store, _, env, out := runFixture(t, true)
	p := config.FactoryPreset()
	p.ID, p.Codec, p.Speed, p.Resolution, p.AudioCodec = 2, "hevc", "slow", "", "mp3"
	if _, err := store.SavePreset(p); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(filepath.Join(store.Dir, "presets.json"))
	env.RunSession = func(context.Context, tui.Session, tui.SessionOptions) (*tui.Session, error) {
		t.Fatal("preset run opened TUI")
		return nil, nil
	}
	code := MainWith(context.Background(), []string{src, dst, "25", "-p", "2", "-q", "off"}, "test", env)
	latest, err := store.LoadLatest()
	after, _ := os.ReadFile(filepath.Join(store.Dir, "presets.json"))
	if code != 0 || err != nil || latest == nil || latest.Codec != "hevc" || latest.Preset != "slow" || latest.AudioCodec != "mp3" || !bytes.Equal(before, after) {
		t.Fatalf("%d %+v %v %s", code, latest, err, out.String())
	}
}

func TestPresetPickerPreservesPickedOrNewID(t *testing.T) {
	for _, id := range []int{1, 2, 7} {
		_, _, store, _, env, out := runFixture(t, true)
		env.IsTerminalIn, env.IsTerminalOut = boolPtr(true), boolPtr(true)
		env.RunSession = func(_ context.Context, s tui.Session, o tui.SessionOptions) (*tui.Session, error) {
			if s.Mode != tui.ModePreset || s.Preset.ID != 0 || len(o.Presets) != 1 || o.HWProbe == nil {
				t.Fatalf("picker: %+v %+v", s, o)
			}
			s.Preset.ID, s.Preset.Label = id, "Picked"
			return &s, nil
		}
		code := MainWith(context.Background(), []string{"-p"}, "test", env)
		presets, err := store.LoadPresets()
		found := false
		for _, p := range presets {
			if p.ID == id && p.Label == "Picked" {
				found = true
			}
		}
		if code != 0 || err != nil || !found {
			t.Fatalf("%d %v %v %s", code, presets, err, out.String())
		}
	}
}

func TestAudioFallbackWarningInNormalAndDryRun(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	for _, dry := range []bool{false, true} {
		src, dst, store, r, env, out := runFixture(t, true)
		dst = strings.TrimSuffix(dst, ".mp4") + ".mov"
		env.IsTerminalIn, env.IsTerminalOut = boolPtr(true), boolPtr(true)
		args := []string{src, dst, "1", "-c", "hevc", "-q", "off"}
		if dry {
			args = append(args, "-d")
		}
		code := MainWith(context.Background(), args, "test", env)
		if code != 0 || strings.Count(out.String(), "warning: mov does not accept opus; using aac") != 1 {
			t.Fatalf("%d %s", code, out.String())
		}
		latest, err := store.LoadLatest()
		if dry {
			if r.full != 0 || latest != nil || !strings.Contains(out.String(), "mov / hevc / aac") {
				t.Fatalf("dry: %+v %s", latest, out.String())
			}
		} else if err != nil || latest == nil || latest.AudioCodec != "aac" {
			t.Fatalf("%+v %v", latest, err)
		}
	}
	// Color is enabled when stdout is a terminal and NO_COLOR is absent.
	if err := os.Unsetenv("NO_COLOR"); err != nil {
		t.Fatal(err)
	}
	src, dst, _, _, env, out := runFixture(t, true)
	env.IsTerminalOut = boolPtr(true)
	code := MainWith(context.Background(), []string{src, strings.TrimSuffix(dst, ".mp4") + ".mov", "1", "-c", "hevc", "-d"}, "test", env)
	if code != 0 || !strings.Contains(out.String(), "\x1b[33mwarning: mov does not accept opus; using aac") {
		t.Fatalf("yellow warning: %d %q", code, out.String())
	}
}

func TestSetupOneClickAndManualConversion(t *testing.T) {
	for _, manual := range []bool{false, true} {
		for _, hasSize := range []bool{false, true} {
			src, _, store, _, env, out := runFixture(t, true)
			p := config.FactoryPreset()
			p.ID, p.Codec, p.Container, p.Speed, p.Resolution = 2, "vp9", "webm", "5", ""
			if _, err := store.SavePreset(p); err != nil {
				t.Fatal(err)
			}
			before, _ := os.ReadFile(filepath.Join(store.Dir, "presets.json"))
			env.IsTerminalIn, env.IsTerminalOut = boolPtr(true), boolPtr(true)
			env.In = strings.NewReader("y\n")
			calls := 0
			env.RunSession = func(_ context.Context, s tui.Session, o tui.SessionOptions) (*tui.Session, error) {
				calls++
				if s.Mode != tui.ModeSetup || o.HWProbe == nil {
					t.Fatalf("%+v", s)
				}
				s.ManualSetup, s.Preset, s.Settings = manual, p, p.Settings()
				s.Settings.HW = "off"
				// A one-click selection cannot replace the CLI target with a draft target.
				if !manual {
					s.TargetBytes = nil
				}
				return &s, nil
			}
			args := []string{src, filepath.Join(filepath.Dir(src), "exports"), "--setup", "-c", "hevc", "-e", "fast", "-f", "mkv", "-q", "off"}
			if hasSize {
				args = append(args, "1")
			}
			code := MainWith(context.Background(), args, "test", env)
			latest, err := store.LoadLatest()
			after, _ := os.ReadFile(filepath.Join(store.Dir, "presets.json"))
			if code != 0 || calls != 1 || err != nil || latest == nil || latest.Codec != "vp9" || latest.Container != "webm" || !bytes.Equal(before, after) {
				t.Fatalf("manual=%t size=%t: %d %+v %v %s", manual, hasSize, code, latest, err, out.String())
			}
			wantPrompt := !manual && !hasSize
			if strings.Contains(out.String(), conversionQuestion) != wantPrompt {
				t.Fatalf("manual=%t size=%t: %s", manual, hasSize, out.String())
			}
			if hasSize && !strings.Contains(out.String(), "input.fit1.webm") {
				t.Fatalf("CLI target lost: %s", out.String())
			}
		}
	}
}

func TestSetupHWProbeWiring(t *testing.T) {
	for _, failure := range []string{"", "resolve", "cancel"} {
		src, dst, _, r, env, out := runFixture(t, true)
		r.hardware = true
		env.IsTerminalIn, env.IsTerminalOut = boolPtr(true), boolPtr(true)
		lookups := 0
		env.ResolveTool = func(explicit, name string) (string, error) {
			lookups++
			if explicit != "custom-ffmpeg" || name != "ffmpeg" {
				t.Fatalf("%s %s", explicit, name)
			}
			if failure == "resolve" {
				return "", errors.New("missing")
			}
			return explicit, nil
		}
		env.RunSession = func(ctx context.Context, s tui.Session, o tui.SessionOptions) (*tui.Session, error) {
			if o.HWProbe == nil {
				t.Fatal("missing HWProbe")
			}
			if failure == "cancel" {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			got := o.HWProbe(ctx, "av1")
			if failure == "" {
				if len(got) != 2 || got["vulkan"] != "" || got["qsv"] != "" || len(r.calls) != 2 {
					t.Fatalf("probe: %v %v", got, r.calls)
				}
			} else if got != nil {
				t.Fatalf("failed probe returned %v", got)
			}
			return nil, nil
		}
		code := MainWith(context.Background(), []string{src, dst, "1", "--setup", "-ff", "custom-ffmpeg"}, "test", env)
		if code != 1 || lookups != 1 {
			t.Fatalf("%d %d %s", code, lookups, out.String())
		}
	}
}

type observingRunner struct {
	*mockRunner
	before func([]string)
}

func (r observingRunner) Run(ctx context.Context, args []string, on func(console.Event)) (ffmpeg.Result, error) {
	r.before(args)
	return r.mockRunner.Run(ctx, args, on)
}

func TestLatestSavedOnlyAfterPublishedProbe(t *testing.T) {
	for _, forced := range []bool{false, true} {
		for _, failure := range []string{"", "overshoot", "probe", "cancel"} {
			src, dst, store, r, env, out := runFixture(t, true)
			old := config.FactoryPreset().Settings()
			old.Quality = "high"
			if err := store.SaveLatest(old); err != nil {
				t.Fatal(err)
			}
			before, _ := os.ReadFile(filepath.Join(store.Dir, "latest.json"))
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if forced {
				r.sizes = []int{2000000, 2000000, 1000}
			}
			if failure == "overshoot" {
				r.sizes = []int{2000000}
			}
			if failure == "probe" {
				r.failProbePath = dst
			}
			probes, saves := 0, 0
			env.Runner = observingRunner{r, func(args []string) {
				if args[0] == "ffprobe" && args[len(args)-1] == dst {
					probes++
					data, _ := os.ReadFile(filepath.Join(store.Dir, "latest.json"))
					if !bytes.Equal(before, data) {
						t.Fatal("latest changed before final probe finished")
					}
					if failure == "cancel" {
						cancel()
					}
				}
			}}
			env.SaveLatest = func(s config.Store, settings config.Settings) error {
				saves++
				if probes != 3 {
					t.Fatalf("saved after %d probe calls", probes)
				}
				if _, err := os.Stat(dst); err != nil {
					t.Fatalf("save before publish: %v", err)
				}
				return s.SaveLatest(settings)
			}
			args := []string{src, dst, "1", "-q", "off"}
			if forced || failure == "overshoot" {
				args = append(args, "-ft")
			}
			code := MainWith(ctx, args, "test", env)
			wantCode := map[string]int{"": 0, "overshoot": 4, "probe": 5, "cancel": 130}[failure]
			after, _ := os.ReadFile(filepath.Join(store.Dir, "latest.json"))
			if code != wantCode {
				t.Fatalf("forced=%t failure=%s: %d %s", forced, failure, code, out.String())
			}
			if failure == "" {
				if saves != 1 || bytes.Equal(before, after) {
					t.Fatal("successful output did not save")
				}
			} else if saves != 0 || !bytes.Equal(before, after) {
				t.Fatal("failed output changed latest")
			}
			if failure == "overshoot" {
				assertNoOutput(t, dst)
			}
		}
	}
}

func TestDirectoryForceUpdatesDestinationAndEncoder(t *testing.T) {
	for _, lock := range []string{"", "extension", "flag"} {
		src, _, store, r, env, out := runFixture(t, true)
		p := config.FactoryPreset()
		p.Codec, p.Container, p.Speed, p.Resolution = "hevc", "mkv", "fast", ""
		if _, err := store.SavePreset(p); err != nil {
			t.Fatal(err)
		}
		before, _ := os.ReadFile(filepath.Join(store.Dir, "presets.json"))
		r.sizes = []int{2000000, 2000000, 1000}
		dst := filepath.Join(filepath.Dir(src), "exports")
		if lock == "extension" {
			dst = filepath.Join(filepath.Dir(src), "out.mkv")
		}
		args := []string{src, dst, "1", "-q", "off", "-ft"}
		if lock == "flag" {
			args = append(args, "-f", "mkv")
		}
		wantContainer := "mp4"
		if lock != "" {
			wantContainer = "mkv"
		}
		finalPath := filepath.Join(dst, "input.fit1."+wantContainer)
		if lock == "extension" {
			finalPath = dst
		}
		env.SaveLatest = func(s config.Store, settings config.Settings) error {
			if settings.Codec != "av1" || settings.Container != wantContainer || settings.AudioCodec != "opus" {
				t.Fatalf("chosen settings: %+v", settings)
			}
			if _, err := os.Stat(finalPath); err != nil {
				t.Fatalf("new destination missing: %v", err)
			}
			if r.calls[len(r.calls)-1][len(r.calls[len(r.calls)-1])-1] != finalPath {
				t.Fatal("save before new destination probe")
			}
			return s.SaveLatest(settings)
		}
		code := MainWith(context.Background(), args, "test", env)
		after, _ := os.ReadFile(filepath.Join(store.Dir, "presets.json"))
		if code != 0 || !strings.Contains(out.String(), " / av1 / libsvtav1") || !strings.Contains(out.String(), finalPath) || !bytes.Equal(before, after) {
			t.Fatalf("lock=%s: %d %s", lock, code, out.String())
		}
		if lock == "" {
			if _, err := os.Stat(filepath.Join(dst, "input.fit1.mkv")); !os.IsNotExist(err) {
				t.Fatal("old destination published")
			}
		}
	}
	if _, err := os.Stat("-encoders"); !os.IsNotExist(err) {
		t.Fatal("mock created stray -encoders file")
	}
}

func TestForceQuestionDirectoryContract(t *testing.T) {
	for _, dir := range []bool{false, true} {
		want := "Audio at 128 kbps needs 1 MB. Force-target may change codec, encoder, "
		if dir {
			want += "container, "
		}
		want += "bitrate, resolution, FPS and audio to fit. Use it? [y/N]"
		if got := forceQuestion("Audio at 128 kbps needs 1 MB.", dir); got != want {
			t.Fatalf("%q", got)
		}
		src, dst, _, _, env, out := runFixture(t, true)
		if dir {
			dst = filepath.Join(filepath.Dir(src), "exports")
		}
		code := MainWith(context.Background(), []string{src, dst, ".001", "-a", "128", "-q", "off", "-d"}, "test", env)
		if code != 0 || strings.Contains(out.String(), "encoder, container,") != dir {
			t.Fatalf("%d %s", code, out.String())
		}
	}
}

func TestLosslessAndNonCRFLabels(t *testing.T) {
	for _, tc := range []struct{ codec, quality, want string }{{"ffv1", "", "lossless"}, {"prores", "high", "high (hq)"}, {"prores", "balanced", "balanced (standard)"}, {"theora", "small", "small (q:v 4)"}} {
		s := config.FactoryPreset().Settings()
		s.Codec, s.Quality = tc.codec, tc.quality
		enc := ffmpeg.Encoder{Name: softwareEncoderName(tc.codec), Backend: "software"}
		if got := qualityLabel(s, enc, "quality"); got != tc.want {
			t.Fatalf("%s: %q", tc.codec, got)
		}
		src, _, _, _, env, out := runFixture(t, true)
		code := MainWith(context.Background(), []string{src, filepath.Join(filepath.Dir(src), "out."+config.Recommended(tc.codec)), "-c", tc.codec, "-ac", "flac", "-d"}, "test", env)
		if tc.codec == "prores" {
			// MOV rejects explicit FLAC; use MKV for this plan.
			out.Reset()
			code = MainWith(context.Background(), []string{src, filepath.Join(filepath.Dir(src), "out.mkv"), "-c", tc.codec, "-ac", "flac", "-d"}, "test", env)
		}
		if code != 0 || !strings.Contains(out.String(), "Speed:        n/a") || !strings.Contains(out.String(), " / flac") {
			t.Fatalf("%s: %d %s", tc.codec, code, out.String())
		}
		if tc.codec == "ffv1" && !strings.Contains(out.String(), "Quality:      lossless") {
			t.Fatal("lossless plan missing")
		}
	}
}

func TestForceResultReflectsChosenBackend(t *testing.T) {
	for _, tc := range []struct {
		qsv, weak          bool
		hw, encoder, label string
	}{
		{false, false, "vulkan", "av1_vulkan", "hardware abr"},
		{true, false, "qsv", "av1_qsv", "hardware abr"},
		{false, true, "off", "libsvtav1", "2-pass abr"},
	} {
		src, _, store, r, env, out := runFixture(t, true)
		p := config.FactoryPreset()
		p.Codec, p.Container, p.Speed, p.Resolution = "hevc", "mkv", "fast", ""
		if _, err := store.SavePreset(p); err != nil {
			t.Fatal(err)
		}
		r.hardware, r.qsvOnly, r.weakGPU = true, tc.qsv, tc.weak
		r.sizes = []int{2000000, 2000000, 1000}
		code := MainWith(context.Background(), []string{src, filepath.Join(filepath.Dir(src), "exports"), "1", "-ft"}, "test", env)
		latest, err := store.LoadLatest()
		if code != 0 || err != nil || latest == nil || latest.Codec != "av1" || latest.HW != tc.hw || !strings.Contains(out.String(), " / "+tc.encoder) || !strings.Contains(out.String(), tc.label) {
			t.Fatalf("%+v: %d %+v %v %s", tc, code, latest, err, out.String())
		}
	}
}

func TestForceDestinationCollisionPreservesFiles(t *testing.T) {
	src, _, store, r, env, out := runFixture(t, true)
	p := config.FactoryPreset()
	p.Codec, p.Container, p.Speed, p.Resolution = "hevc", "mkv", "fast", ""
	if _, err := store.SavePreset(p); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveLatest(p.Settings()); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(filepath.Join(store.Dir, "latest.json"))
	dir := filepath.Join(filepath.Dir(src), "exports")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	finalPath := filepath.Join(dir, "input.fit1.mp4")
	if err := os.WriteFile(finalPath, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	r.sizes = []int{2000000, 2000000, 1000}
	code := MainWith(context.Background(), []string{src, dir, "1", "-q", "off", "-ft"}, "test", env)
	after, _ := os.ReadFile(filepath.Join(store.Dir, "latest.json"))
	data, _ := os.ReadFile(finalPath)
	parts, _ := filepath.Glob(filepath.Join(dir, ".pressurecooker-*.part"))
	if code != 2 || !bytes.Equal(before, after) || string(data) != "keep" || len(parts) != 0 || !strings.Contains(out.String(), "destination already exists") {
		t.Fatalf("%d %s", code, out.String())
	}
}

func TestInheritedMismatchRejectedBeforeToolsOrSetup(t *testing.T) {
	for _, setup := range []bool{false, true} {
		src, dst, _, _, env, out := runFixture(t, true)
		env.IsTerminalIn, env.IsTerminalOut = boolPtr(true), boolPtr(true)
		env.ResolveTool = func(string, string) (string, error) { t.Fatal("mismatch resolved tools"); return "", nil }
		env.RunSession = func(context.Context, tui.Session, tui.SessionOptions) (*tui.Session, error) {
			t.Fatal("mismatch opened setup")
			return nil, nil
		}
		args := []string{src, strings.TrimSuffix(dst, ".mp4") + ".mov", "1"}
		if setup {
			args = append(args, "--setup")
		}
		code := MainWith(context.Background(), args, "test", env)
		if code != 2 || !strings.Contains(out.String(), "mov does not accept av1; use -c hevc, h264, prores, ffv1 or theora") {
			t.Fatalf("%d %s", code, out.String())
		}
	}
}

func TestLatestSaveWithInheritedCRFAndSilentSource(t *testing.T) {
	for _, forced := range []bool{false, true} {
		for _, noAudio := range []bool{false, true} {
			src, dst, store, r, env, out := runFixture(t, true)
			previous := config.FactoryPreset().Settings()
			previous.CRF, previous.Quality, previous.Resolution = number(38), "", ""
			if err := store.SaveLatest(previous); err != nil {
				t.Fatal(err)
			}
			r.noAudio = noAudio
			args := []string{src, dst, "1", "--latest", "-q", "off"}
			if forced {
				r.sizes = []int{2000000, 2000000, 1000}
				args = append(args, "-ft")
			}
			code := MainWith(context.Background(), args, "test", env)
			latest, err := store.LoadLatest()
			if code != 0 || err != nil || latest == nil || strings.Contains(out.String(), "could not save latest") {
				t.Fatalf("forced=%t silent=%t: %d %+v %v %s", forced, noAudio, code, latest, err, out.String())
			}
			if forced && (latest.CRF != nil || latest.Quality != "balanced") {
				t.Fatalf("force quality: %+v", latest)
			}
			if !forced && (latest.CRF == nil || *latest.CRF != 38) {
				t.Fatalf("inherited CRF: %+v", latest)
			}
			if noAudio && (!latest.AudioSource || latest.AudioKbps != 128) {
				t.Fatalf("silent-source preference: %+v", latest)
			}
		}
	}
}

func TestMissingSelectedPresetDoesNotRecordFirstRun(t *testing.T) {
	src, dst, store, _, env, out := runFixture(t, false)
	env.IsTerminalIn, env.IsTerminalOut = boolPtr(true), boolPtr(true)
	env.RunSession = func(context.Context, tui.Session, tui.SessionOptions) (*tui.Session, error) {
		t.Fatal("missing preset opened recorder")
		return nil, nil
	}
	env.ResolveTool = func(string, string) (string, error) { t.Fatal("missing preset resolved tool"); return "", nil }
	code := MainWith(context.Background(), []string{src, dst, "1", "-p", "2"}, "test", env)
	if code != 2 || !strings.Contains(out.String(), "preset 2 not found; see --preset-list") {
		t.Fatalf("%d %s", code, out.String())
	}
	if _, err := os.Stat(store.Dir); !os.IsNotExist(err) {
		t.Fatal("missing preset wrote config")
	}
}
