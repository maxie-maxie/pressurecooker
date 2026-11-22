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
		{[]string{"-PS=2", "in", "out", "5", "-FT", "--VMAF", "-FF=f", "-FP", "p"}, ModeDefault, false, 5000000},
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
	for _, args := range [][]string{{"--new-preset"}, {"--new-preset", "2"}, {"--NEW-PRESET=3"}, {"--preset-list"}} {
		run, err := Parse(args)
		if err != nil || run.NewPreset == nil && !run.ListPresets {
			t.Fatalf("%v: %+v %v", args, run, err)
		}
	}
}

func TestErrorsBeforeConfigOrTools(t *testing.T) {
	cases := [][]string{
		{}, {"--setup"}, {"--setup", "in"}, {"in"}, {"in", "20"},
		{"in", "out", "3", "-t", "4"}, {"in", "out", "3", "--crf", "38"}, {"in", "out", "-t", "3", "-l", "high"},
		{"in", "out", "-ft"}, {"in", "out", "--vmaf"}, {"in", "out", "--latest", "-ps", "1"},
		{"in", "out", "--crf", "38", "-l", "high"}, {"--new-preset", "in", "out"}, {"--preset-list", "in", "out"},
		{"--new-preset", "--setup"}, {"--preset-list", "--dry-run"}, {"--new-preset", "--preset-list"},
		{"--new-preset", "0"}, {"--new-preset=-1"}, {"in", "out", "-ps", "0"},
		{"in", "out", "--force"}, {"in", "out", "-v"}, {"in", "out", "--verbose"}, {"in", "out", "-co"},
		{"in", "out", "--conversion"}, {"--defaults"}, {"in", "out", "--setup=true"},
		{"in", "out", "--latest=false"}, {"in", "out", "--new-preset", "--fps", "30"},
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
	for _, pair := range [][2]string{{"-c", "vp9"}, {"-f", "avi"}, {"-p", "14"}, {"-l", "best"}, {"-a", "0"}, {"-a", "1.5"}, {"-a", "99999999999999999999"}, {"-r", "NaN"}, {"-r", "1001"}, {"-r", "0"}, {"-r", "0x1p2"}, {"-s", "641x360"}, {"-q", "cuda"}, {"--crf", "Inf"}, {"--crf", "64"}, {"--crf", "0x1p2"}} {
		if _, err := Parse([]string{"in", "out", pair[0], pair[1]}); err == nil {
			t.Fatalf("accepted %v", pair)
		}
	}
	for _, flag := range []string{"-t", "-c", "-f", "-p", "-l", "--crf", "-a", "-r", "-s", "-q", "-ps", "-ff", "-fp"} {
		for _, tail := range [][]string{{flag}, {flag + "="}, {flag, "--"}, {flag, "--overwrite"}} {
			if _, err := Parse(append([]string{"in", "out"}, tail...)); err == nil || !strings.Contains(err.Error(), "requires a value") {
				t.Fatalf("%v: %v", tail, err)
			}
		}
	}
	for _, args := range [][]string{{"--crf", "52", "-c", "hevc", "-p", "fast"}, {"--crf", "0"}, {"--crf", "38", "-q", "qsv"}} {
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
	for _, required := range []string{conversionQuestion, "0.97", "20%", "128/96/64/48", "3 sample encodes", "3 full encodes", "libvmaf", "2.4 times", "720p60", "presets.json", "latest.json", "version 3", "%APPDATA%", "$XDG_CONFIG_HOME", "1 MB = 1,000,000 bytes", "MiB", "libx265 CRF", "libx264 CRF", "libsvtav1 CRF", "130"} {
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
	want := "1  Default  AV1, 1080p max, source audio and FPS\n   av1 mp4 speed 8 balanced audio:source fps:source res:1080p\n"
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
	if code != 0 || out.String() != "2  Small\n   av1 mp4 speed 8 crf 38 audio:96k fps:25 res:source\n" {
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
