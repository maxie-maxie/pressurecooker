package config

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"
)

func number(value float64) *float64 { return &value }

func writePayload(t *testing.T, store Store, name string, payload any) []byte {
	t.Helper()
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(store.Dir, name), data, 0600); err != nil {
		t.Fatal(err)
	}
	return data
}

func payload(t *testing.T) map[string]any {
	t.Helper()
	return map[string]any{
		"version": 2, "target_bytes": 20000000, "codec": "hevc",
		"container": "mp4", "preset": "fast", "quality": "balanced", "crf": nil,
		"audio_kbps": 128, "fps": nil, "resolution": "", "hw": "auto", "verbose": false,
	}
}

func assertFile(t *testing.T, file string, expected []byte) {
	t.Helper()
	data, err := os.ReadFile(file)
	if err != nil || string(data) != string(expected) {
		t.Fatalf("file changed: %s; data=%q err=%v", file, data, err)
	}
}

func assertOnly(t *testing.T, dir string, names ...string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	actual := []string{}
	for _, entry := range entries {
		actual = append(actual, entry.Name())
	}
	sort.Strings(names)
	if !slices.Equal(actual, names) {
		t.Fatalf("directory contents: got %v, want %v", actual, names)
	}
}

func TestWindowsConfigPath(t *testing.T) {
	home, appdata := t.TempDir(), t.TempDir()
	t.Setenv("USERPROFILE", home)
	t.Setenv("HOME", home)
	t.Setenv("APPDATA", appdata)
	s, err := userStore("windows")
	if err != nil || s.Dir != filepath.Join(appdata, "pressurecooker") {
		t.Fatalf("APPDATA: %+v %v", s, err)
	}
	t.Setenv("APPDATA", "")
	s, err = userStore("windows")
	if err != nil || s.Dir != filepath.Join(home, "AppData", "Roaming", "pressurecooker") {
		t.Fatalf("fallback: %+v %v", s, err)
	}
}

func TestPOSIXConfigPath(t *testing.T) {
	home, xdg := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", xdg)
	s, err := userStore("linux")
	if err != nil || s.Dir != filepath.Join(xdg, "pressurecooker") {
		t.Fatalf("XDG: %+v %v", s, err)
	}
	t.Setenv("XDG_CONFIG_HOME", "")
	s, err = userStore("linux")
	if err != nil || s.Dir != filepath.Join(home, ".config", "pressurecooker") {
		t.Fatalf("fallback: %+v %v", s, err)
	}
}

func TestExplicitStoreIsolation(t *testing.T) {
	profile := t.TempDir()
	t.Setenv("APPDATA", profile)
	t.Setenv("XDG_CONFIG_HOME", profile)
	s := Store{Dir: t.TempDir()}
	if err := s.SaveLatest(Factory()); err != nil {
		t.Fatal(err)
	}
	assertOnly(t, s.Dir, "latest.json")
	assertOnly(t, profile)
}

func TestNonFiniteJSON(t *testing.T) {
	s := Store{Dir: t.TempDir()}
	data, _ := json.Marshal(payload(t))
	for _, field := range []string{"crf", "fps"} {
		for _, value := range []string{"NaN", "Infinity", "-Infinity", "1e309"} {
			text := strings.Replace(string(data), `"`+field+`":null`, `"`+field+`":`+value, 1)
			if err := os.WriteFile(filepath.Join(s.Dir, "latest.json"), []byte(text), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := s.LoadLatest(); err == nil {
				t.Fatalf("accepted %s=%s", field, value)
			}
		}
	}
}

func TestLoadWithoutTools(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	s := Store{Dir: t.TempDir()}
	if err := s.SaveLatest(Factory()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.LoadLatest(); err != nil {
		t.Fatal(err)
	}
}

func TestUnknownSchemaKeys(t *testing.T) {
	s := Store{Dir: t.TempDir()}
	for _, key := range []string{"unknown", "source", "destination", "overwrite", "ffmpeg", "ffprobe", "dry_run", "conversion", "force_target", "origins", "target_mib", "Codec"} {
		p := payload(t)
		p[key] = "extra"
		writePayload(t, s, "latest.json", p)
		if _, err := s.LoadLatest(); err == nil {
			t.Fatalf("accepted %s", key)
		}
	}
	data, _ := json.Marshal(payload(t))
	duplicate := strings.Replace(string(data), `"version":2`, `"version":2,"version":2`, 1)
	if err := os.WriteFile(filepath.Join(s.Dir, "latest.json"), []byte(duplicate), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.LoadLatest(); err == nil {
		t.Fatal("accepted duplicate key")
	}
}

func TestRequiredSchemaKeys(t *testing.T) {
	s := Store{Dir: t.TempDir()}
	for _, key := range append([]string{"version"}, settingKeys...) {
		p := payload(t)
		delete(p, key)
		writePayload(t, s, "latest.json", p)
		if _, err := s.LoadLatest(); err == nil {
			t.Fatalf("accepted missing %s", key)
		}
		if key != "crf" && key != "fps" && key != "resolution" {
			p = payload(t)
			p[key] = nil
			writePayload(t, s, "latest.json", p)
			if _, err := s.LoadLatest(); err == nil {
				t.Fatalf("accepted null %s", key)
			}
		}
	}
}

func TestJSONNumericTypes(t *testing.T) {
	s := Store{Dir: t.TempDir()}
	for _, key := range []string{"target_bytes", "audio_kbps", "crf", "fps", "version"} {
		for _, value := range []any{true, false, "30"} {
			p := payload(t)
			p[key] = value
			writePayload(t, s, "latest.json", p)
			if _, err := s.LoadLatest(); err == nil {
				t.Fatalf("accepted %s=%v", key, value)
			}
		}
	}
	for _, entry := range []struct{ key, raw string }{{"audio_kbps", "128.0"}, {"audio_kbps", "2147483648"}, {"target_bytes", "1.5"}, {"target_bytes", "9223372036854775808"}, {"verbose", "1"}, {"verbose", `"yes"`}, {"resolution", "1080"}, {"preset", "8"}} {
		if entry.key == "audio_kbps" && entry.raw == "2147483648" && strconv.IntSize == 64 {
			continue
		}
		p := payload(t)
		data, _ := json.Marshal(p)
		old, _ := json.Marshal(p[entry.key])
		text := strings.Replace(string(data), `"`+entry.key+`":`+string(old), `"`+entry.key+`":`+entry.raw, 1)
		if err := os.WriteFile(filepath.Join(s.Dir, "latest.json"), []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := s.LoadLatest(); err == nil {
			t.Fatalf("accepted %s=%s", entry.key, entry.raw)
		}
	}
}

func TestBadChoiceSettings(t *testing.T) {
	for _, change := range []func(*Settings){
		func(s *Settings) { s.Codec = "vp9" }, func(s *Settings) { s.Codec = "HEVC" },
		func(s *Settings) { s.Container = "avi" }, func(s *Settings) { s.Container = "mp4 " },
		func(s *Settings) { s.HW = "cuda" }, func(s *Settings) { s.HW = "AUTO" },
		func(s *Settings) { s.Preset = "bogus" }, func(s *Settings) { s.Preset = "" },
		func(s *Settings) { s.Quality = "best" }, func(s *Settings) { s.Resolution = "wide" },
		func(s *Settings) { s.Quality, s.CRF = "best", number(30) },
	} {
		s := Factory()
		change(&s)
		if err := Validate(s); err == nil {
			t.Fatalf("accepted %+v", s)
		}
	}
}

func TestIncompatibleSettings(t *testing.T) {
	for _, change := range []func(*Settings){
		func(s *Settings) { s.Container = "webm" },
		func(s *Settings) { s.Codec = "h264"; s.Container = "webm" },
		func(s *Settings) { s.Preset = "8" },
		func(s *Settings) { s.Codec = "h264"; s.Preset = "13" },
		func(s *Settings) { s.Codec = "h264"; s.Preset = "3"; s.HW = "qsv" },
		func(s *Settings) { s.Codec = "av1"; s.Preset = "14" },
	} {
		s := Factory()
		change(&s)
		if err := Validate(s); err == nil {
			t.Fatalf("accepted %+v", s)
		}
	}
	for _, hw := range []string{"qsv", "vulkan"} {
		s := Factory()
		s.HW, s.CRF = hw, number(28)
		if err := Validate(s); err == nil || !strings.Contains(err.Error(), "use --quality or --hw off") {
			t.Fatalf("hardware CRF: %v", err)
		}
	}
}

func TestNonObjectConfiguration(t *testing.T) {
	s := Store{Dir: t.TempDir()}
	for _, p := range []any{nil, "text", 5, true, []string{"list"}} {
		writePayload(t, s, "latest.json", p)
		if _, err := s.LoadLatest(); err == nil {
			t.Fatalf("accepted %v", p)
		}
	}
}

func TestBlockedStoreDirectory(t *testing.T) {
	file := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(file, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	s := Store{Dir: file}
	if err := s.SaveLatest(Factory()); err == nil || !strings.Contains(err.Error(), "latest.json") {
		t.Fatalf("blocked directory: %v", err)
	}
	assertFile(t, file, []byte("keep"))
}

func TestClonePointerIsolation(t *testing.T) {
	s := Factory()
	s.CRF, s.FPS = number(30), number(60)
	copy := Clone(s)
	*copy.CRF, *copy.FPS = 20, 24
	if *s.CRF != 30 || *s.FPS != 60 {
		t.Fatal("clone changed original pointers")
	}
}

func TestQSVNamedPresets(t *testing.T) {
	for _, codec := range []string{"h264", "hevc", "av1"} {
		for input, want := range map[string]string{"ultrafast": "veryfast", "superfast": "veryfast", "veryfast": "veryfast", "placebo": "veryslow", "slow": "slow"} {
			got, err := ResolvePreset(input, codec, true)
			if err != nil || got != want {
				t.Fatalf("%s %s: %s %v", codec, input, got, err)
			}
		}
	}
}

func TestQSVNumericPresets(t *testing.T) {
	for input, want := range map[string]string{"0": "veryslow", "1": "veryslow", "2": "veryslow", "3": "slower", "4": "slow", "5": "medium", "6": "fast", "7": "faster", "13": "veryfast"} {
		got, err := ResolvePreset(input, "h264", true)
		if err != nil || got != want {
			t.Fatalf("%s: %s %v", input, got, err)
		}
	}
}

func TestAV1PresetSpeedOrder(t *testing.T) {
	for i, input := range []string{"ultrafast", "superfast", "veryfast", "faster", "fast", "medium", "slow", "slower", "veryslow", "placebo"} {
		got, err := ResolvePreset(input, "av1", false)
		want := []string{"12", "11", "10", "9", "8", "7", "6", "5", "4", "3"}[i]
		if err != nil || got != want {
			t.Fatalf("%s: %s %v", input, got, err)
		}
	}
}

func TestAV1PresetBounds(t *testing.T) {
	for _, input := range []string{"-1", "14", "999999999999999999999", "5.5"} {
		if _, err := ResolvePreset(input, "av1", false); err == nil || !strings.Contains(err.Error(), "AV1") {
			t.Fatalf("%s: %v", input, err)
		}
	}
}

func TestSoftwarePresetsNamedOnly(t *testing.T) {
	for _, codec := range []string{"h264", "hevc"} {
		if _, err := ResolvePreset("5", codec, false); err == nil {
			t.Fatalf("accepted numeric %s preset", codec)
		}
		if got, err := ResolvePreset(" SLOW ", codec, false); err != nil || got != "slow" {
			t.Fatalf("named %s: %s %v", codec, got, err)
		}
	}
}

func TestDescribePresetMapping(t *testing.T) {
	for _, tc := range []struct {
		input, resolved string
		qsv, mapped     bool
	}{{"ultrafast", "veryfast", true, true}, {"veryfast", "veryfast", true, false}, {"fast", "8", false, true}, {"08", "8", false, false}} {
		got := DescribePreset(tc.input, tc.resolved, tc.qsv)
		if strings.Contains(got, "mapped from") != tc.mapped {
			t.Fatalf("description: %s", got)
		}
	}
}

func TestOddResolutionRejected(t *testing.T) {
	for _, text := range []string{"1081x1081", "1281x720", "1280x721", "16383x2", "1x1"} {
		if _, _, err := ParseResolution(text); err == nil {
			t.Fatalf("accepted odd resolution %s", text)
		}
	}
}

func TestResolutionAliases(t *testing.T) {
	for text, want := range map[string][2]int{"sd": {640, 360}, "360p": {640, 360}, "480p": {854, 480}, "hd": {1280, 720}, "720p": {1280, 720}, "fullhd": {1920, 1080}, "fhd": {1920, 1080}, "1080p": {1920, 1080}, "qhd": {2560, 1440}, "2k": {2560, 1440}, "1440p": {2560, 1440}, "uhd": {3840, 2160}, "4k": {3840, 2160}, "2160p": {3840, 2160}, " HD ": {1280, 720}, "16384x16384": {16384, 16384}, "": {0, 0}} {
		w, h, err := ParseResolution(text)
		if err != nil || [2]int{w, h} != want {
			t.Fatalf("%q: %dx%d %v", text, w, h, err)
		}
	}
}

func TestResolutionBounds(t *testing.T) {
	for _, text := range []string{"20000x8", "0x0", "16386x2", "0x720", "16384x16386", "999999999999999999999x2"} {
		if _, _, err := ParseResolution(text); err == nil {
			t.Fatalf("accepted %s", text)
		}
	}
}

func TestResolutionMalformed(t *testing.T) {
	for _, text := range []string{"abc", "640x", "x360", "640x360x1", "640.0x360", " ", "-2x360", "+640x360"} {
		if _, _, err := ParseResolution(text); err == nil {
			t.Fatalf("accepted %q", text)
		}
	}
}

func TestFailedReplacePreservesFile(t *testing.T) {
	s := Store{Dir: t.TempDir()}
	before := writePayload(t, s, "latest.json", payload(t))
	err := s.saveData("latest.json", []byte("replacement data"), writeSynced, func(from, to string) error {
		if filepath.Dir(from) != s.Dir || to != filepath.Join(s.Dir, "latest.json") {
			t.Fatal("replacement escaped store")
		}
		return errors.New("locked file")
	})
	if err == nil || !strings.Contains(err.Error(), "locked file") {
		t.Fatalf("replace failure: %v", err)
	}
	assertFile(t, filepath.Join(s.Dir, "latest.json"), before)
	assertOnly(t, s.Dir, "latest.json")
}

func TestFailedWriteCleansTemp(t *testing.T) {
	s := Store{Dir: t.TempDir()}
	before := writePayload(t, s, "latest.json", payload(t))
	err := s.saveData("latest.json", []byte("replacement data"), func(file *os.File, data []byte) error {
		if _, err := file.Write(data[:5]); err != nil {
			t.Fatal(err)
		}
		return errors.New("disk full")
	}, func(string, string) error {
		t.Fatal("replaced after failed write")
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "disk full") {
		t.Fatalf("write failure: %v", err)
	}
	assertFile(t, filepath.Join(s.Dir, "latest.json"), before)
	assertOnly(t, s.Dir, "latest.json")
}
