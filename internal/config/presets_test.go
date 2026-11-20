package config

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func integer(value int) *int { return &value }

func TestFactoryPreset(t *testing.T) {
	want := Preset{
		ID: 1, Label: "Default", Description: "AV1, 1080p max, source audio and FPS",
		Codec: "av1", Container: "mp4", Speed: "8", Quality: "balanced", Resolution: "1080p",
	}
	if got := FactoryPreset(); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v want %+v", got, want)
	}
	if err := ValidatePreset(want); err != nil {
		t.Fatal(err)
	}
}

func TestValidatePreset(t *testing.T) {
	for name, change := range map[string]func(*Preset){
		"negative id":   func(p *Preset) { p.ID = -1 },
		"blank label":   func(p *Preset) { p.Label = " \t" },
		"long label":    func(p *Preset) { p.Label = strings.Repeat("é", 41) },
		"long desc":     func(p *Preset) { p.Description = strings.Repeat("x", 121) },
		"codec":         func(p *Preset) { p.Codec = "vp9" },
		"container":     func(p *Preset) { p.Container = "avi" },
		"webm":          func(p *Preset) { p.Codec, p.Container, p.Speed = "hevc", "webm", "fast" },
		"speed":         func(p *Preset) { p.Speed = "14" },
		"both rate":     func(p *Preset) { p.CRF = number(30) },
		"no rate":       func(p *Preset) { p.Quality = "" },
		"quality":       func(p *Preset) { p.Quality = "best" },
		"av1 crf low":   func(p *Preset) { p.Quality, p.CRF = "", number(0) },
		"crf nonfinite": func(p *Preset) { p.Quality, p.CRF = "", number(math.Inf(1)) },
		"audio":         func(p *Preset) { p.AudioKbps = integer(0) },
		"fps":           func(p *Preset) { p.FPS = number(1000.1) },
		"resolution":    func(p *Preset) { p.Resolution = "1281x720" },
	} {
		t.Run(name, func(t *testing.T) {
			p := FactoryPreset()
			change(&p)
			if err := ValidatePreset(p); err == nil {
				t.Fatalf("accepted %+v", p)
			}
		})
	}

	p := FactoryPreset()
	p.ID, p.Quality, p.CRF, p.AudioKbps, p.FPS = 0, "", number(63), integer(1), number(1000)
	p.Resolution = "16384x16384"
	if err := ValidatePreset(p); err != nil {
		t.Fatalf("rejected boundary preset: %v", err)
	}
}

func TestPresetSettings(t *testing.T) {
	p := FactoryPreset()
	p.Quality, p.CRF, p.AudioKbps, p.FPS = "", number(38), integer(96), number(29.97)
	s := p.Settings()
	if s.HW != "auto" || s.AudioSource || s.AudioKbps != 96 {
		t.Fatalf("settings: %+v", s)
	}
	*s.CRF, *s.FPS = 30, 24
	if *p.CRF != 38 || *p.FPS != 29.97 {
		t.Fatal("settings shares preset pointers")
	}

	s = FactoryPreset().Settings()
	if !s.AudioSource || s.AudioKbps != 128 || s.CRF != nil || s.FPS != nil {
		t.Fatalf("source defaults: %+v", s)
	}
}

func TestPresetStoreRoundTrip(t *testing.T) {
	profile := t.TempDir()
	t.Setenv("APPDATA", profile)
	t.Setenv("XDG_CONFIG_HOME", profile)
	s := Store{Dir: filepath.Join(t.TempDir(), "config")}
	if got, err := s.LoadPresets(); got != nil || err != nil {
		t.Fatalf("missing: %+v %v", got, err)
	}

	p3 := FactoryPreset()
	p3.ID, p3.Label = 3, "Third"
	if got, err := s.SavePreset(p3); err != nil || got.ID != 3 {
		t.Fatalf("insert: %+v %v", got, err)
	}
	appendPreset := FactoryPreset()
	appendPreset.ID, appendPreset.Label = 0, "Next"
	if got, err := s.SavePreset(appendPreset); err != nil || got.ID != 4 {
		t.Fatalf("append: %+v %v", got, err)
	}
	p1 := FactoryPreset()
	if _, err := s.SavePreset(p1); err != nil {
		t.Fatal(err)
	}
	p3.Description = "replaced"
	if _, err := s.SavePreset(p3); err != nil {
		t.Fatal(err)
	}
	got, err := s.LoadPresets()
	if err != nil || len(got) != 3 || got[0].ID != 1 || got[1].ID != 3 || got[2].ID != 4 || got[1].Description != "replaced" {
		t.Fatalf("round trip: %+v %v", got, err)
	}
	assertOnly(t, s.Dir, "presets.json")
	assertOnly(t, profile)
}

func TestPresetStoreCRFRoundTrip(t *testing.T) {
	s := Store{Dir: t.TempDir()}
	p := FactoryPreset()
	p.Quality, p.CRF, p.AudioKbps, p.FPS = "", number(38.5), integer(96), number(29.97)
	if _, err := s.SavePreset(p); err != nil {
		t.Fatal(err)
	}
	got, err := s.LoadPresets()
	if err != nil || len(got) != 1 || !reflect.DeepEqual(got[0], p) {
		t.Fatalf("round trip: %+v %v", got, err)
	}
	data, err := os.ReadFile(filepath.Join(s.Dir, "presets.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), `"quality"`) || !strings.Contains(string(data), `"crf": 38.5`) {
		t.Fatalf("rate fields: %s", data)
	}
}

func TestPresetStoreRejectsInvalidWithoutWriting(t *testing.T) {
	s := Store{Dir: filepath.Join(t.TempDir(), "missing")}
	p := FactoryPreset()
	p.Label = ""
	if _, err := s.SavePreset(p); err == nil {
		t.Fatal("accepted invalid preset")
	}
	if _, err := os.Stat(s.Dir); !os.IsNotExist(err) {
		t.Fatalf("validation touched disk: %v", err)
	}

	s = Store{Dir: t.TempDir()}
	path := filepath.Join(s.Dir, "presets.json")
	before := []byte(`{"version":99,"presets":[]}`)
	if err := os.WriteFile(path, before, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SavePreset(FactoryPreset()); err == nil || strings.Contains(err.Error(), "\n") {
		t.Fatalf("corrupt store error: %v", err)
	}
	assertFile(t, path, before)
}

func TestPresetStrictJSON(t *testing.T) {
	valid, err := json.Marshal(struct {
		Version int      `json:"version"`
		Presets []Preset `json:"presets"`
	}{3, []Preset{FactoryPreset()}})
	if err != nil {
		t.Fatal(err)
	}
	var root map[string]any
	if err := json.Unmarshal(valid, &root); err != nil {
		t.Fatal(err)
	}
	cases := map[string][]byte{
		"non-object":       []byte(`[]`),
		"trailing":         append(append([]byte{}, valid...), []byte(` true`)...),
		"root duplicate":   []byte(strings.Replace(string(valid), `"version":3`, `"version":3,"version":3`, 1)),
		"root unknown":     []byte(strings.Replace(string(valid), `"version":3`, `"version":3,"extra":1`, 1)),
		"preset duplicate": []byte(strings.Replace(string(valid), `"id":1`, `"id":1,"id":1`, 1)),
		"preset unknown":   []byte(strings.Replace(string(valid), `"id":1`, `"id":1,"extra":1`, 1)),
		"missing label":    []byte(strings.Replace(string(valid), `"label":"Default",`, ``, 1)),
		"null description": []byte(strings.Replace(string(valid), `"description":"AV1, 1080p max, source audio and FPS"`, `"description":null`, 1)),
		"null resolution":  []byte(strings.Replace(string(valid), `"resolution":"1080p"`, `"resolution":null`, 1)),
		"both rate":        []byte(strings.Replace(string(valid), `"quality":"balanced"`, `"quality":"balanced","crf":38`, 1)),
		"no rate":          []byte(strings.Replace(string(valid), `,"quality":"balanced"`, ``, 1)),
		"zero id":          []byte(strings.Replace(string(valid), `"id":1`, `"id":0`, 1)),
	}
	duplicateIDs := root
	duplicateIDs["presets"] = []any{FactoryPreset(), FactoryPreset()}
	cases["duplicate ids"], _ = json.Marshal(duplicateIDs)
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			s := Store{Dir: t.TempDir()}
			path := filepath.Join(s.Dir, "presets.json")
			if err := os.WriteFile(path, data, 0600); err != nil {
				t.Fatal(err)
			}
			if got, err := s.LoadPresets(); got != nil || err == nil || !strings.Contains(err.Error(), path) || strings.Contains(err.Error(), "\n") {
				t.Fatalf("got %+v err %v", got, err)
			}
			assertFile(t, path, data)
		})
	}
}
