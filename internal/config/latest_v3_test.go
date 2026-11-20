package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestLatestV3AudioSourceRoundTrip(t *testing.T) {
	s := Store{Dir: t.TempDir()}
	want := FactoryPreset().Settings()
	if err := s.SaveLatest(want); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(s.Dir, "latest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatal(err)
	}
	if fields["version"] != float64(4) || fields["audio_codec"] != "opus" || fields["audio_kbps"] != nil {
		t.Fatalf("v4 payload: %v", fields)
	}
	for _, absent := range []string{"id", "label", "description", "target_bytes", "verbose", "crf"} {
		if _, ok := fields[absent]; ok {
			t.Fatalf("latest includes %s: %v", absent, fields)
		}
	}
	want.AudioKbps = 128
	got, err := s.LoadLatest()
	if err != nil || !reflect.DeepEqual(got, &want) {
		t.Fatalf("round trip: %+v %v", got, err)
	}
}

func TestLatestV3CRFWinsForOlderCaller(t *testing.T) {
	s := Store{Dir: t.TempDir()}
	want := Factory()
	want.Codec, want.Preset, want.Quality, want.CRF, want.HW = "av1", "8", "balanced", number(38), "off"
	if err := s.SaveLatest(want); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(s.Dir, "latest.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), `"quality"`) || !strings.Contains(string(data), `"crf": 38`) {
		t.Fatalf("rate fields: %s", data)
	}
	want.Quality = ""
	got, err := s.LoadLatest()
	if err != nil || !reflect.DeepEqual(got, &want) {
		t.Fatalf("round trip: %+v %v", got, err)
	}
}

func TestLatestV2MigrationPreservesBytes(t *testing.T) {
	s := Store{Dir: t.TempDir()}
	p := payload(t)
	p["target_bytes"], p["codec"], p["preset"], p["quality"], p["crf"], p["hw"], p["verbose"] =
		float64(7000000), "av1", "8", "small", float64(38), "off", true
	before := writePayload(t, s, "latest.json", p)
	want := Factory()
	want.Codec, want.Preset, want.Quality, want.CRF, want.HW = "av1", "8", "", number(38), "off"
	for range 2 {
		got, err := s.LoadLatest()
		if err != nil || !reflect.DeepEqual(got, &want) {
			t.Fatalf("migration: %+v %v", got, err)
		}
		assertFile(t, filepath.Join(s.Dir, "latest.json"), before)
	}
}

func TestLatestV3StrictJSON(t *testing.T) {
	valid := []byte(`{"version":4,"codec":"av1","container":"mp4","preset":"8","quality":"balanced","audio_codec":"opus","audio_kbps":null,"fps":null,"resolution":"","hw":"auto"}`)
	cases := map[string][]byte{
		"non-object":      []byte(`[]`),
		"trailing":        append(append([]byte{}, valid...), []byte(` true`)...),
		"duplicate":       []byte(strings.Replace(string(valid), `"version":4`, `"version":4,"version":4`, 1)),
		"unknown":         []byte(strings.Replace(string(valid), `"hw":"auto"`, `"hw":"auto","target_bytes":1`, 1)),
		"missing":         []byte(strings.Replace(string(valid), `,"fps":null`, ``, 1)),
		"missing audioc":  []byte(strings.Replace(string(valid), `,"audio_codec":"opus"`, ``, 1)),
		"null audioc":     []byte(strings.Replace(string(valid), `"audio_codec":"opus"`, `"audio_codec":null`, 1)),
		"both rate":       []byte(strings.Replace(string(valid), `"quality":"balanced"`, `"quality":"balanced","crf":38`, 1)),
		"no rate":         []byte(strings.Replace(string(valid), `,"quality":"balanced"`, ``, 1)),
		"null crf":        []byte(strings.Replace(string(valid), `"quality":"balanced"`, `"crf":null`, 1)),
		"null resolution": []byte(strings.Replace(string(valid), `"resolution":""`, `"resolution":null`, 1)),
		"null codec":      []byte(strings.Replace(string(valid), `"codec":"av1"`, `"codec":null`, 1)),
		"audio type":      []byte(strings.Replace(string(valid), `"audio_kbps":null`, `"audio_kbps":128.5`, 1)),
		"fps type":        []byte(strings.Replace(string(valid), `"fps":null`, `"fps":"30"`, 1)),
		"version 5":       []byte(strings.Replace(string(valid), `"version":4`, `"version":5`, 1)),
		"v3 with audioc":  []byte(strings.Replace(string(valid), `"version":4`, `"version":3`, 1)),
		"ffv1 both rates": []byte(`{"version":4,"codec":"ffv1","container":"mkv","preset":"","quality":"balanced","crf":0,"audio_codec":"flac","audio_kbps":null,"fps":null,"resolution":"","hw":"auto"}`),
		"ffv1 with crf":   []byte(`{"version":4,"codec":"ffv1","container":"mkv","preset":"","crf":0,"audio_codec":"flac","audio_kbps":null,"fps":null,"resolution":"","hw":"auto"}`),
		"prores with crf": []byte(`{"version":4,"codec":"prores","container":"mov","preset":"","crf":20,"audio_codec":"aac","audio_kbps":128,"fps":null,"resolution":"","hw":"auto"}`),
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			s := Store{Dir: t.TempDir()}
			path := filepath.Join(s.Dir, "latest.json")
			if err := os.WriteFile(path, data, 0600); err != nil {
				t.Fatal(err)
			}
			if got, err := s.LoadLatest(); got != nil || err == nil || !strings.Contains(err.Error(), path) || strings.Contains(err.Error(), "\n") {
				t.Fatalf("got %+v err %v", got, err)
			}
			assertFile(t, path, data)
		})
	}
}

func TestSaveLatestPreservesCorruptFile(t *testing.T) {
	s := Store{Dir: t.TempDir()}
	path := filepath.Join(s.Dir, "latest.json")
	before := []byte(`{"version":99}`)
	if err := os.WriteFile(path, before, 0600); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveLatest(Factory()); err == nil || !strings.Contains(err.Error(), path) {
		t.Fatalf("save error: %v", err)
	}
	assertFile(t, path, before)
}

func TestLatestV1MigrationDiscardsOperationalValues(t *testing.T) {
	store := Store{Dir: t.TempDir()}
	before := []byte(`{"version":1,"target_mib":10,"codec":"h264","container":"mkv","preset":"slow","audio_kbps":96,"fps":25,"resolution":"720p","hw":"off","verbose":true}`)
	path := filepath.Join(store.Dir, "latest.json")
	if err := os.WriteFile(path, before, 0600); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		latest, err := store.LoadLatest()
		if err != nil || latest == nil || latest.Codec != "h264" || latest.Container != "mkv" || latest.Preset != "slow" || latest.Quality != "balanced" || latest.AudioKbps != 96 || latest.AudioSource || latest.FPS == nil || *latest.FPS != 25 {
			t.Fatalf("%+v %v", latest, err)
		}
		assertFile(t, path, before)
	}
}

func TestLatestV3MigrationPreservesBytes(t *testing.T) {
	s := Store{Dir: t.TempDir()}
	path := filepath.Join(s.Dir, "latest.json")
	before := []byte(`{"version":3,"codec":"av1","container":"mp4","preset":"8","quality":"balanced","audio_kbps":null,"fps":null,"resolution":"1080p","hw":"auto"}`)
	if err := os.WriteFile(path, before, 0600); err != nil {
		t.Fatal(err)
	}
	want := FactoryPreset().Settings()
	for range 2 {
		got, err := s.LoadLatest()
		if err != nil || !reflect.DeepEqual(got, &want) {
			t.Fatalf("v3 migration: %+v %v", got, err)
		}
		assertFile(t, path, before)
	}
}

func TestLatestV4FFV1RoundTrip(t *testing.T) {
	s := Store{Dir: t.TempDir()}
	want := Settings{
		Codec: "ffv1", Container: "mkv", Preset: "", Quality: "",
		AudioKbps: 128, AudioSource: true, AudioCodec: "flac", HW: "vulkan",
	}
	if err := s.SaveLatest(want); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(s.Dir, "latest.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), `"quality"`) || strings.Contains(string(data), `"crf"`) {
		t.Fatalf("ffv1 latest should omit quality/crf: %s", data)
	}
	got, err := s.LoadLatest()
	if err != nil || !reflect.DeepEqual(got, &want) {
		t.Fatalf("ffv1 latest round trip: %+v %v", got, err)
	}
}
