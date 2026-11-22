package cli

import (
	"bytes"
	"context"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/maxie-maxie/pressurecooker/internal/config"
	"github.com/maxie-maxie/pressurecooker/internal/console"
	"github.com/maxie-maxie/pressurecooker/internal/tui"
)

func number(n float64) *float64 { return &n }
func boolPtr(b bool) *bool      { return &b }

func TestSyntaxAndWholeFlags(t *testing.T) {
	for _, tc := range []struct {
		args  []string
		mode  Mode
		setup bool
		size  int64
	}{
		{[]string{"in", "out", "20"}, ModeDefault, false, 20000000},
		{[]string{"in", "out", "-T=8.5"}, ModeDefault, false, 8500000},
		{[]string{"--TARGET", "2", "in", "out"}, ModeDefault, false, 2000000},
		{[]string{"in", "out"}, ModeConversion, false, 0},
		{[]string{"in", "out", "--setup"}, ModeConversion, true, 0},
		{[]string{"in", "out", "5", "--setup", "--latest"}, ModeDefault, true, 5000000},
		{[]string{"-P=2", "in", "out", "5", "-FT", "--VMAF", "-FF=f", "-FP", "p"}, ModeDefault, false, 5000000},
		{[]string{"--", "--help", "-ft", "2"}, ModeDefault, false, 2000000},
	} {
		run, err := Parse(tc.args)
		if err != nil || run.Mode != tc.mode || run.Setup != tc.setup {
			t.Fatalf("%v: %+v %v", tc.args, run, err)
		}
		if tc.size == 0 && run.TargetBytes != nil || tc.size > 0 && (run.TargetBytes == nil || *run.TargetBytes != tc.size) {
			t.Fatalf("size: %+v", run)
		}
	}
	for _, args := range [][]string{{"--preset"}, {"--preset", "2"}, {"--PRESET=3"}, {"--preset-list"}} {
		run, err := Parse(args)
		if err != nil || run.EditPreset == nil && !run.ListPresets {
			t.Fatalf("%v: %+v %v", args, run, err)
		}
	}
}

func TestErrorsBeforeConfigOrTools(t *testing.T) {
	cases := [][]string{
		{}, {"--setup"}, {"--setup", "in"}, {"in"}, {"in", "20"},
		{"in", "out", "3", "-t", "4"}, {"in", "out", "3", "--crf", "38"}, {"in", "out", "-t", "3", "-l", "high"},
		{"in", "out", "-ft"}, {"in", "out", "--vmaf"}, {"in", "out", "--latest", "-p", "1"},
		{"in", "out", "--crf", "38", "-l", "high"}, {"--preset", "in", "out"}, {"--preset-list", "in", "out"},
		{"--preset", "--setup"}, {"--preset-list", "--dry-run"}, {"--preset", "--preset-list"},
		{"--preset", "0"}, {"--preset=-1"}, {"in", "out", "-p", "0"},
		{"in", "out", "--force"}, {"in", "out", "-v"}, {"in", "out", "--verbose"}, {"in", "out", "-co"},
		{"in", "out", "--conversion"}, {"--defaults"}, {"in", "out", "--setup=true"},
		{"in", "out", "--latest=false"}, {"in", "out", "--preset", "--fps", "30"},
	}
	for _, args := range cases {
		var out bytes.Buffer
		code := MainWith(context.Background(), args, "test", Env{Out: &out,
			Store:       func() (config.Store, error) { t.Fatalf("config accessed for %v", args); return config.Store{}, nil },
			ResolveTool: func(string, string) (string, error) { t.Fatalf("tools accessed for %v", args); return "", nil },
		})
		if code != 2 || !strings.HasPrefix(out.String(), "error: ") || strings.Count(out.String(), "\n") != 1 {
			t.Fatalf("%v: %d %s", args, code, out.String())
		}
	}
	for flag, replacement := range retired {
		_, err := Parse([]string{"in", "out", flag})
		if err == nil || !strings.Contains(err.Error(), replacement) {
			t.Fatalf("%s: %v", flag, err)
		}
	}
}

func TestInformationPriority(t *testing.T) {
	for _, flag := range []string{"--help", "-H", "/?", "-?", "--HELP-DETAILS", "--VERSION"} {
		var out bytes.Buffer
		code := MainWith(context.Background(), []string{"--nonsense", "--defaults", flag}, "3", Env{Out: &out,
			Store:       func() (config.Store, error) { t.Fatal("config read"); return config.Store{}, nil },
			ResolveTool: func(string, string) (string, error) { t.Fatal("tool lookup"); return "", nil },
		})
		if code != 0 || strings.Contains(out.String(), reminderLine) {
			t.Fatalf("%s: %d %s", flag, code, out.String())
		}
	}
}

func TestResolutionPrecedenceAndIsolation(t *testing.T) {
	preset := config.FactoryPreset().Settings()
	preset.Container, preset.Quality, preset.FPS = "mkv", "small", number(24)
	latest := config.Clone(preset)
	latest.CRF, latest.Quality, latest.FPS = number(38), "", number(50)
	parsed, err := Parse([]string{"in", "out.mp4", "--latest", "-l", "high", "-a", "64", "-r", "30"})
	if err != nil {
		t.Fatal(err)
	}
	run, err := Resolve(parsed, &preset, &latest)
	if err != nil || run.Settings.Container != "mp4" || run.Settings.Quality != "high" || run.Settings.CRF != nil || run.Settings.AudioSource || run.Settings.AudioKbps != 64 || *run.Settings.FPS != 30 || run.TargetBytes != nil {
		t.Fatalf("%+v %v", run, err)
	}
	if run.Origins["quality"] != "explicit" || run.Origins["audio"] != "explicit" || run.Origins["resolution"] != "latest" {
		t.Fatalf("origins: %v", run.Origins)
	}
	*run.Settings.FPS = 1
	if *latest.FPS != 50 || *preset.FPS != 24 || *parsed.Settings.FPS != 30 {
		t.Fatal("pointer alias")
	}
	parsed, _ = Parse([]string{"in", "out", "--crf", "30", "-q", "off"})
	run, err = Resolve(parsed, &preset, nil)
	if err != nil || run.Settings.Quality != "" || *run.Settings.CRF != 30 {
		t.Fatalf("%+v %v", run, err)
	}
	if _, err = Resolve(parsed, &preset, &latest); err != nil {
		t.Fatal(err)
	}
	parsed, _ = Parse([]string{"in", "out.mkv", "-f", "mp4"})
	if _, err = Resolve(parsed, nil, nil); err == nil {
		t.Fatal("container conflict")
	}
	parsed, _ = Parse([]string{"in", "out", "--latest"})
	run, err = Resolve(parsed, &preset, nil)
	if err != nil || !reflect.DeepEqual(run.Settings, preset) || run.Origins["codec"] != "preset" {
		t.Fatalf("%+v %v", run, err)
	}
}

func TestTargetByteBounds(t *testing.T) {
	for text, want := range map[string]int64{"20": 20000000, "8.5": 8500000, ".5": 500000, "1e2": 100000000, "0.0000005": 1, "1.0000005": 1000001, "9223372036854.775807": math.MaxInt64, "9223372036854.775806": math.MaxInt64 - 1} {
		got, err := parseTarget(text)
		if err != nil || got != want {
			t.Fatalf("%s: %d %v", text, got, err)
		}
	}
	for _, text := range []string{"0", "-1", "NaN", "Inf", "1e309", "9223372036854.7758075", "9223372036855", "0.0000004", "0x1p2", "true", "bad", "1e99999999999999999999"} {
		if _, err := parseTarget(text); err == nil {
			t.Fatalf("accepted %s", text)
		}
	}
}

func TestValueValidation(t *testing.T) {
	for _, pair := range [][2]string{{"-c", "bad"}, {"-f", "bad"}, {"-ac", "bad"}, {"-e", "14"}, {"-l", "best"}, {"-a", "0"}, {"-a", "1.5"}, {"-a", "99999999999999999999"}, {"-r", "NaN"}, {"-r", "1001"}, {"-r", "0"}, {"-r", "0x1p2"}, {"-s", "641x360"}, {"-q", "cuda"}, {"--crf", "Inf"}, {"--crf", "64"}, {"--crf", "0x1p2"}} {
		if _, err := Parse([]string{"in", "out", pair[0], pair[1]}); err == nil {
			t.Fatalf("accepted %v", pair)
		}
	}
	for _, flag := range []string{"-t", "-c", "-f", "-e", "-ac", "-l", "--crf", "-a", "-r", "-s", "-q", "-ff", "-fp"} {
		for _, tail := range [][]string{{flag}, {flag + "="}, {flag, "--"}, {flag, "--overwrite"}} {
			if _, err := Parse(append([]string{"in", "out"}, tail...)); err == nil || !strings.Contains(err.Error(), "requires a value") {
				t.Fatalf("%v: %v", tail, err)
			}
		}
	}
	for _, args := range [][]string{{"--crf", "52", "-c", "hevc", "-e", "fast"}, {"--crf", "0"}, {"--crf", "38", "-q", "qsv"}} {
		p, err := Parse(append([]string{"in", "out"}, args...))
		if err != nil {
			t.Fatal(err)
		}
		if _, err = Resolve(p, nil, nil); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
}

func TestHelpContract(t *testing.T) {
	for flag := range aliases {
		if !strings.Contains(Help, flag) {
			t.Fatalf("help lacks %s", flag)
		}
	}
	if strings.Count(strings.Split(Help, "Examples:\n")[1], "pressurecooker ") != 3 {
		t.Fatal("example count")
	}
	for _, banned := range []string{"Explicit values win", "no arguments also opens it", "Its extension fixes the container", "Use ./20", "Tokens after --", "advisory", "1,000,000", "--defaults", "--verbose", "--conversion", "20 MB"} {
		if strings.Contains(Help, banned) {
			t.Fatalf("short help contains %s", banned)
		}
	}
	for _, required := range []string{conversionQuestion, "0.97", "20%", "128/96/64/48", "3 sample encodes", "3 full encodes", "libvmaf", "2.4 times", "720p60", "presets.json", "latest.json", "version 4", "%APPDATA%", "$XDG_CONFIG_HOME", "1 MB = 1,000,000 bytes", "MiB", "libx265 CRF", "libx264 CRF", "libsvtav1 CRF", "130"} {
		if !strings.Contains(Details, required) {
			t.Fatalf("details lack %s", required)
		}
	}
}

func TestPresetListFormattingAndNoWrites(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "config")
	store := config.Store{Dir: dir}
	var out bytes.Buffer
	code := mainWith([]string{"--preset-list"}, "test", &out, func() (config.Store, error) { return store, nil })
	want := "1  Default  AV1, 1080p max, source audio and FPS\n   av1 mp4 speed 8 balanced audio_codec:opus audio:source fps:source res:1080p\n"
	if code != 0 || out.String() != want {
		t.Fatalf("%d %q", code, out.String())
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("list created config")
	}
	p := config.FactoryPreset()
	p.ID, p.Label, p.Description, p.Quality, p.CRF, p.Resolution = 2, "Small", "", "", number(38), ""
	n := 96
	p.AudioKbps = &n
	p.FPS = number(25)
	if _, err := store.SavePreset(p); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	code = mainWith([]string{"--preset-list"}, "test", &out, func() (config.Store, error) { return store, nil })
	if code != 0 || out.String() != "2  Small\n   av1 mp4 speed 8 crf 38 audio_codec:opus audio:96k fps:25 res:source\n" {
		t.Fatalf("%d %s", code, out.String())
	}
	if err := os.WriteFile(filepath.Join(dir, "presets.json"), []byte("{broken"), 0600); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	code = mainWith([]string{"--preset-list"}, "test", &out, func() (config.Store, error) { return store, nil })
	if code != 2 || !strings.Contains(out.String(), "presets.json") {
		t.Fatalf("%d %s", code, out.String())
	}
}

func TestFactoryPresetUsedWithoutSavedFiles(t *testing.T) {
	p, _ := Parse([]string{"in", "out"})
	run, err := Resolve(p, nil, nil)
	if err != nil || !reflect.DeepEqual(run.Settings, config.FactoryPreset().Settings()) || run.TargetBytes != nil {
		t.Fatalf("%+v %v", run, err)
	}
}

func TestPresetParsingV4(t *testing.T) {
	for _, tc := range []struct {
		args []string
		id   int
		size int64
		edit bool
	}{
		{[]string{"-p"}, 0, 0, true},
		{[]string{"--preset", "2"}, 2, 0, true},
		{[]string{"-p=3"}, 3, 0, true},
		{[]string{"in", "out", "25", "-p", "2"}, 2, 25000000, false},
		{[]string{"in", "out", "-p", "25"}, 25, 0, false},
		{[]string{"-p", "2", "in", "out", "5", "-e", "fast"}, 2, 5000000, false},
	} {
		run, err := Parse(tc.args)
		if err != nil {
			t.Fatalf("%v: %v", tc.args, err)
		}
		if tc.edit {
			if run.EditPreset == nil || *run.EditPreset != tc.id || run.Origins["use-preset"] != "" {
				t.Fatalf("editor: %+v", run)
			}
		} else if run.EditPreset != nil || run.PresetID != tc.id || run.Origins["use-preset"] != "explicit" {
			t.Fatalf("selection: %+v", run)
		}
		if tc.size == 0 && run.TargetBytes != nil || tc.size != 0 && (run.TargetBytes == nil || *run.TargetBytes != tc.size) {
			t.Fatalf("size: %+v", run)
		}
	}
	for _, args := range [][]string{{"-p", "fast"}, {"-p=fast"}, {"in", "out", "-p", "fast"}, {"-p", "1.5"}, {"-p", "0"}, {"-p", "-1"}, {"-p="}, {"-p", "in.mp4", "out.mp4"}, {"-p", "input"}} {
		_, err := Parse(args)
		if err == nil || err.Error() != "preset number must be an integer; encoder speed is -e/--enc-speed" {
			t.Fatalf("%v: %v", args, err)
		}
	}
}

func TestV4ErrorsBeforeStoreToolsAndTUI(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	src, dst, _, _, _, _ := runFixture(t, true)
	cases := [][]string{
		{src, dst, "-p", "2", "--setup"}, {src, dst, "-p", "2", "--latest"},
		{"-p", "--", src, dst}, {src, dst, "-p"}, {"-p", "2", "--setup"}, {"-p", "2", "--latest"},
		{"-p", "--preset-list"}, {"-p", "2", "--preset-list"}, {"--preset-list", src, dst},
		{"-p", "fast"}, {"-p=fast"}, {src, dst, "-p", "fast"}, {"-p", src, dst},
		{"-ps", "2"}, {"--use-preset", "2"}, {"--new-preset"}, {"--defaults"},
		{src, dst, "-ac", "flac", "-a", "128"},
		{src, filepath.Join(filepath.Dir(src), "out.mov"), "-c", "av1"},
		{src, filepath.Join(filepath.Dir(src), "out.webm"), "-c", "h264", "-ac", "aac"},
		{src, dst, "-c", "hevc", "-f", "webm"},
		{src, filepath.Join(filepath.Dir(src), "out.mov"), "-ac", "opus"},
		{src, filepath.Join(filepath.Dir(src), "out.qt"), "-f", "mkv"},
		{src, dst, "-c", "hevc", "-e", "8"},
	}
	for _, flag := range [][]string{{"-c", "av1"}, {"-f", "mkv"}, {"-ac", "aac"}, {"-e", "8"}, {"-l", "high"}, {"--crf", "30"}, {"-a", "64"}, {"-r", "30"}, {"-s", "720p"}, {"-q", "off"}, {"-d"}, {"--overwrite"}, {"-ff", "ffmpeg"}, {"-fp", "ffprobe"}, {"-t", "1"}, {"-ft"}, {"--vmaf"}} {
		for _, editor := range [][]string{{"-p"}, {"-p", "2"}, {"--preset-list"}} {
			cases = append(cases, append(append([]string{}, editor...), flag...))
		}
	}
	for _, codec := range []string{"prores", "ffv1", "theora"} {
		out := filepath.Join(filepath.Dir(src), "output."+config.Recommended(codec))
		for _, flag := range [][]string{{"--crf", "30"}, {"-e", "fast"}} {
			cases = append(cases, append([]string{src, out, "-c", codec}, flag...))
		}
		if codec != "theora" {
			cases = append(cases, []string{src, out, "1", "-c", codec})
		}
	}
	cases = append(cases, []string{src, dst, "-c", "ffv1", "-l", "high"})
	for _, args := range cases {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			var out bytes.Buffer
			code := MainWith(context.Background(), args, "test", Env{Out: &out, IsTerminalIn: boolPtr(true), IsTerminalOut: boolPtr(true),
				Store:       func() (config.Store, error) { t.Fatal("store opened"); return config.Store{}, nil },
				ResolveTool: func(string, string) (string, error) { t.Fatal("tool resolved"); return "", nil },
				RunSession: func(context.Context, tui.Session, tui.SessionOptions) (*tui.Session, error) {
					t.Fatal("TUI opened")
					return nil, nil
				},
			})
			if code != 2 || !strings.HasPrefix(out.String(), "error: ") || strings.Count(out.String(), "\n") != 1 {
				t.Fatalf("%d %s", code, out.String())
			}
		})
	}
}

func TestV4CodecContainerAudioAndSpeedValues(t *testing.T) {
	for _, codec := range config.VideoCodecs {
		p, err := Parse([]string{"in", "out." + config.Recommended(codec), "-C", codec})
		if err != nil {
			t.Fatal(err)
		}
		run, err := Resolve(p, nil, nil)
		if err != nil || run.Settings.Codec != codec || run.Origins["codec"] != "explicit" {
			t.Fatalf("%s: %+v %v", codec, run, err)
		}
	}
	for _, container := range append(append([]string{}, config.Containers...), "quicktime", "mpegts") {
		p, err := Parse([]string{"in", "out", "--container=" + strings.ToUpper(container)})
		want := container
		if container == "quicktime" {
			want = "mov"
		}
		if container == "mpegts" {
			want = "ts"
		}
		if err != nil || p.Settings.Container != want || p.Origins["container"] != "explicit" {
			t.Fatalf("%s: %+v %v", container, p, err)
		}
	}
	for _, codec := range config.AudioCodecs {
		p, err := Parse([]string{"in", "out.mkv", "--AUDIO-CODEC=" + strings.ToUpper(codec)})
		run, resolveErr := Resolve(p, nil, nil)
		if err != nil || resolveErr != nil || run.Settings.AudioCodec != codec || run.Origins["audio_codec"] != "explicit" {
			t.Fatalf("%s: %+v %v %v", codec, run, err, resolveErr)
		}
	}
	for _, speed := range []string{"ultrafast", "superfast", "veryfast", "faster", "fast", "medium", "slow", "slower", "veryslow", "placebo", "0", "13"} {
		p, err := Parse([]string{"in", "out", "--ENC-SPEED=" + speed})
		if err != nil || p.Settings.Preset != speed || p.Origins["preset"] != "explicit" {
			t.Fatalf("%s: %+v %v", speed, p, err)
		}
	}
}

func TestV4ResolutionAdaptationLocksAndWarnings(t *testing.T) {
	for _, codec := range []string{"h264", "hevc"} {
		p, _ := Parse([]string{"in", "out.mp4", "5", "-c", codec})
		run, err := Resolve(p, nil, nil)
		if err != nil || run.Settings.Preset != "fast" || run.Origins["preset"] != "factory" || run.Origins["container"] != "extension" {
			t.Fatalf("%s: %+v %v", codec, run, err)
		}
	}
	latest := config.FactoryPreset().Settings()
	latest.CRF, latest.Quality, latest.AudioCodec = number(38), "", ""
	for _, codec := range []string{"prores", "theora", "ffv1"} {
		p, _ := Parse([]string{"in", "out." + config.Recommended(codec), "--latest", "-c", codec})
		run, err := Resolve(p, nil, &latest)
		quality := "balanced"
		if codec == "ffv1" {
			quality = ""
		}
		if err != nil || run.Settings.CRF != nil || run.Settings.Quality != quality || latest.CRF == nil || latest.AudioCodec != "" {
			t.Fatalf("%s: %+v %v", codec, run, err)
		}
	}
	for ext, container := range map[string]string{"MP4": "mp4", "mkv": "mkv", "webm": "webm", "mov": "mov", "qt": "mov", "avi": "avi", "ogg": "ogg", "ogv": "ogg", "ts": "ts", "nut": "nut"} {
		if got := destinationContainer(`C:\export\out.` + ext); got != container {
			t.Fatalf("%s: %s", ext, got)
		}
	}
	p, _ := Parse([]string{"in", "out.qt", "-c", "hevc"})
	run, err := Resolve(p, nil, nil)
	if err != nil || run.Settings.AudioCodec != "aac" || !reflect.DeepEqual(run.Warnings, []string{"mov does not accept opus; using aac"}) {
		t.Fatalf("%+v %v", run, err)
	}
	run, err = validateRun(run)
	if err != nil || len(run.Warnings) != 0 {
		t.Fatalf("stale warnings: %+v %v", run, err)
	}
	p, _ = Parse([]string{"in", "out.ogv", "-c", "theora"})
	run, err = Resolve(p, nil, nil)
	if err != nil || run.Settings.Container != "ogg" || run.Origins["container"] != "extension" {
		t.Fatalf("%+v %v", run, err)
	}
	for _, args := range [][]string{{"in", "exports"}, {"in", "exports", "-f", "mkv"}} {
		p, _ = Parse(args)
		run, err = Resolve(p, nil, nil)
		if err != nil || run.Origins["container"] == "extension" {
			t.Fatalf("directory locked: %+v %v", run, err)
		}
	}
	p, _ = Parse([]string{"in", "out.mov", "-ac", "opus"})
	_, err = Resolve(p, nil, nil)
	if err == nil || err.Error() != "mov does not accept av1; use -c hevc, h264, prores, ffv1 or theora" {
		t.Fatalf("video priority: %v", err)
	}
}

func TestV4PresetListControlLabels(t *testing.T) {
	for _, codec := range []string{"prores", "theora", "ffv1"} {
		p := config.FactoryPreset()
		p.Codec, p.Container, p.AudioCodec = codec, "mkv", "flac"
		kbps := 128
		p.AudioKbps = &kbps
		var out bytes.Buffer
		printPreset(console.New(&out, false), p)
		if !strings.Contains(out.String(), "speed n/a") || !strings.Contains(out.String(), "audio_codec:flac audio:lossless") || strings.Contains(out.String(), "128k") {
			t.Fatalf("%s", out.String())
		}
		if codec == "ffv1" && !strings.Contains(out.String(), "n/a lossless") {
			t.Fatalf("%s", out.String())
		}
	}
}
