package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"unicode/utf8"
)

type Preset struct {
	ID          int      `json:"id"`
	Label       string   `json:"label"`
	Description string   `json:"description"`
	Codec       string   `json:"codec"`
	Container   string   `json:"container"`
	Speed       string   `json:"preset"`
	Quality     string   `json:"quality,omitempty"`
	CRF         *float64 `json:"crf,omitempty"`
	AudioKbps   *int     `json:"audio_kbps"` // nil: keep source audio
	FPS         *float64 `json:"fps"`        // nil: source; otherwise a cap
	Resolution  string   `json:"resolution"` // "": source; otherwise a cap
	AudioCodec  string   `json:"-"`          // v4 contract; serialized from schema v4
}

func FactoryPreset() Preset {
	return Preset{
		ID:          1,
		Label:       "Default",
		Description: "AV1, 1080p max, source audio and FPS",
		Codec:       "av1",
		Container:   "mp4",
		Speed:       "8",
		Quality:     "balanced",
		Resolution:  "1080p",
	}
}

func ValidatePreset(p Preset) error {
	if p.ID < 0 {
		return fmt.Errorf("preset id cannot be negative")
	}
	if strings.TrimSpace(p.Label) == "" {
		return fmt.Errorf("preset label is required")
	}
	if utf8.RuneCountInString(p.Label) > 40 {
		return fmt.Errorf("preset label must be at most 40 characters")
	}
	if utf8.RuneCountInString(p.Description) > 120 {
		return fmt.Errorf("preset description must be at most 120 characters")
	}
	if p.Codec != "h264" && p.Codec != "hevc" && p.Codec != "av1" {
		return fmt.Errorf("codec must be h264, hevc or av1")
	}
	if p.Container != "mp4" && p.Container != "mkv" && p.Container != "webm" {
		return fmt.Errorf("container must be mp4, mkv or webm")
	}
	if p.Container == "webm" && p.Codec != "av1" {
		return fmt.Errorf("webm requires codec av1")
	}
	if _, err := ResolvePreset(p.Speed, p.Codec, false); err != nil {
		return err
	}
	if (p.Quality == "") == (p.CRF == nil) {
		return fmt.Errorf("preset must set exactly one of quality or crf")
	}
	if p.Quality != "" && p.Quality != "high" && p.Quality != "balanced" && p.Quality != "small" {
		return fmt.Errorf("quality must be high, balanced or small")
	}
	if p.CRF != nil {
		lo, hi := 0.0, 51.0
		if p.Codec == "av1" {
			lo, hi = 1, 63
		}
		if math.IsNaN(*p.CRF) || math.IsInf(*p.CRF, 0) || *p.CRF < lo || *p.CRF > hi {
			return fmt.Errorf("crf for %s must be a finite number from %.0f to %.0f", p.Codec, lo, hi)
		}
	}
	if p.AudioKbps != nil && *p.AudioKbps <= 0 {
		return fmt.Errorf("audio-kbps must be a positive integer")
	}
	if p.FPS != nil && (math.IsNaN(*p.FPS) || math.IsInf(*p.FPS, 0) || *p.FPS <= 0 || *p.FPS > 1000) {
		return fmt.Errorf("fps must be a finite number greater than 0 and at most 1000")
	}
	_, _, err := ParseResolution(p.Resolution)
	return err
}

func (p Preset) Settings() Settings {
	s := Settings{
		Codec:       p.Codec,
		Container:   p.Container,
		Preset:      p.Speed,
		Quality:     p.Quality,
		AudioKbps:   128,
		AudioSource: p.AudioKbps == nil,
		Resolution:  p.Resolution,
		HW:          "auto",
	}
	if p.CRF != nil {
		v := *p.CRF
		s.CRF = &v
	}
	if p.AudioKbps != nil {
		s.AudioKbps = *p.AudioKbps
	}
	if p.FPS != nil {
		v := *p.FPS
		s.FPS = &v
	}
	return s
}

func (s Store) LoadPresets() ([]Preset, error) {
	path := filepath.Join(s.Dir, "presets.json")
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	presets, err := decodePresets(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return presets, nil
}

func decodePresets(data []byte) ([]Preset, error) {
	fields, err := decodeObject(data)
	if err != nil {
		return nil, err
	}
	if err := requireKeys(fields, []string{"version", "presets"}, []string{"version", "presets"}); err != nil {
		return nil, err
	}
	var version int
	if err := json.Unmarshal(fields["version"], &version); err != nil || version != 3 {
		return nil, fmt.Errorf("unsupported preset version; expected 3")
	}
	if firstNonSpace(fields["presets"]) != '[' {
		return nil, fmt.Errorf("presets must be a JSON array")
	}
	var entries []json.RawMessage
	if err := json.Unmarshal(fields["presets"], &entries); err != nil {
		return nil, fmt.Errorf("invalid presets: %w", err)
	}
	presets := make([]Preset, 0, len(entries))
	seen := make(map[int]bool, len(entries))
	for i, entry := range entries {
		p, err := decodePreset(entry)
		if err != nil {
			return nil, fmt.Errorf("preset %d: %w", i+1, err)
		}
		if p.ID <= 0 {
			return nil, fmt.Errorf("preset %d: id must be positive", i+1)
		}
		if seen[p.ID] {
			return nil, fmt.Errorf("duplicate preset id %d", p.ID)
		}
		seen[p.ID] = true
		presets = append(presets, p)
	}
	sort.Slice(presets, func(i, j int) bool { return presets[i].ID < presets[j].ID })
	return presets, nil
}

func decodePreset(data []byte) (Preset, error) {
	fields, err := decodeObject(data)
	if err != nil {
		return Preset{}, err
	}
	required := []string{"id", "label", "description", "codec", "container", "preset", "audio_kbps", "fps", "resolution"}
	allowed := append(slices.Clone(required), "quality", "crf")
	if err := requireKeys(fields, required, allowed); err != nil {
		return Preset{}, err
	}
	_, quality := fields["quality"]
	_, crf := fields["crf"]
	if quality == crf {
		return Preset{}, fmt.Errorf("preset must contain exactly one of quality or crf")
	}
	var p Preset
	for key, destination := range map[string]any{
		"id": &p.ID, "label": &p.Label, "description": &p.Description,
		"codec": &p.Codec, "container": &p.Container, "preset": &p.Speed,
		"audio_kbps": &p.AudioKbps, "fps": &p.FPS, "resolution": &p.Resolution,
	} {
		if (key == "label" || key == "description" || key == "codec" || key == "container" || key == "preset" || key == "resolution") && isJSONNull(fields[key]) {
			return Preset{}, fmt.Errorf("%s cannot be null", key)
		}
		if err := json.Unmarshal(fields[key], destination); err != nil {
			return Preset{}, fmt.Errorf("invalid %s: %w", key, err)
		}
	}
	if quality {
		if err := json.Unmarshal(fields["quality"], &p.Quality); err != nil {
			return Preset{}, fmt.Errorf("invalid quality: %w", err)
		}
	} else {
		if bytes.Equal(bytes.TrimSpace(fields["crf"]), []byte("null")) {
			return Preset{}, fmt.Errorf("crf cannot be null")
		}
		if err := json.Unmarshal(fields["crf"], &p.CRF); err != nil {
			return Preset{}, fmt.Errorf("invalid crf: %w", err)
		}
	}
	if err := ValidatePreset(p); err != nil {
		return Preset{}, err
	}
	return p, nil
}

func (s Store) SavePreset(p Preset) (Preset, error) {
	path := filepath.Join(s.Dir, "presets.json")
	if err := ValidatePreset(p); err != nil {
		return Preset{}, fmt.Errorf("%s: %w", path, err)
	}
	presets, err := s.LoadPresets()
	if err != nil {
		return Preset{}, err
	}
	if p.ID == 0 {
		for _, existing := range presets {
			p.ID = max(p.ID, existing.ID)
		}
		p.ID++
		presets = append(presets, p)
	} else {
		replaced := false
		for i := range presets {
			if presets[i].ID == p.ID {
				presets[i], replaced = p, true
				break
			}
		}
		if !replaced {
			presets = append(presets, p)
		}
	}
	sort.Slice(presets, func(i, j int) bool { return presets[i].ID < presets[j].ID })
	data, err := json.MarshalIndent(struct {
		Version int      `json:"version"`
		Presets []Preset `json:"presets"`
	}{Version: 3, Presets: presets}, "", "  ")
	if err != nil {
		return Preset{}, fmt.Errorf("%s: %w", path, err)
	}
	if err := s.saveData("presets.json", append(data, '\n'), writeSynced, replaceFile); err != nil {
		return Preset{}, err
	}
	return p, nil
}
