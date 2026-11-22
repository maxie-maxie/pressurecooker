package config

import (
	"encoding/json"
	"fmt"
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
	AudioCodec  string   `json:"audio_codec"`
	AudioKbps   *int     `json:"audio_kbps"` // nil: keep source audio
	FPS         *float64 `json:"fps"`        // nil: source; otherwise a cap
	Resolution  string   `json:"resolution"` // "": source; otherwise a cap
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
		AudioCodec:  "opus",
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
	audioKbps := 0
	if p.AudioKbps != nil {
		audioKbps = *p.AudioKbps
	}
	return validateEncoding(p.Codec, p.Container, p.AudioCodec, p.Speed, p.Quality, p.CRF, p.AudioKbps != nil, audioKbps, p.FPS, p.Resolution)
}

func (p Preset) Settings() Settings {
	audioCodec := p.AudioCodec
	if audioCodec == "" {
		audioCodec = "opus"
	}
	s := Settings{
		Codec:       p.Codec,
		Container:   p.Container,
		Preset:      p.Speed,
		Quality:     p.Quality,
		AudioKbps:   128,
		AudioSource: p.AudioKbps == nil,
		AudioCodec:  audioCodec,
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
	if err := json.Unmarshal(fields["version"], &version); err != nil || (version != 3 && version != 4) {
		return nil, fmt.Errorf("unsupported preset version; expected 4")
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
		p, err := decodePreset(entry, version)
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

func decodeQualityAndCRF(fields map[string]json.RawMessage, codec string, qualityOut *string, crfOut **float64) error {
	_, hasQuality := fields["quality"]
	_, hasCRF := fields["crf"]
	if hasQuality && hasCRF {
		return fmt.Errorf("must contain at most one of quality or crf")
	}
	caps := CodecCaps(codec)
	if caps.CRFHi == 0 && hasCRF {
		if isJSONNull(fields["crf"]) {
			return fmt.Errorf("crf cannot be null")
		}
		return fmt.Errorf("crf is not supported for %s", codec)
	}
	if caps.Quality {
		if caps.CRFHi > 0 && !hasQuality && !hasCRF {
			return fmt.Errorf("must contain exactly one of quality or crf")
		}
		if caps.CRFHi == 0 && !hasQuality {
			return fmt.Errorf("must contain quality")
		}
	}
	if hasQuality {
		if isJSONNull(fields["quality"]) {
			return fmt.Errorf("quality cannot be null")
		}
		if err := json.Unmarshal(fields["quality"], qualityOut); err != nil {
			return fmt.Errorf("invalid quality: %w", err)
		}
	}
	if hasCRF {
		if isJSONNull(fields["crf"]) {
			return fmt.Errorf("crf cannot be null")
		}
		if err := json.Unmarshal(fields["crf"], crfOut); err != nil {
			return fmt.Errorf("invalid crf: %w", err)
		}
	}
	return nil
}

func decodePreset(data []byte, version int) (Preset, error) {
	fields, err := decodeObject(data)
	if err != nil {
		return Preset{}, err
	}
	required := []string{"id", "label", "description", "codec", "container", "preset", "audio_kbps", "fps", "resolution"}
	if version == 4 {
		required = append(required, "audio_codec")
	}
	allowed := append(slices.Clone(required), "quality", "crf")
	if err := requireKeys(fields, required, allowed); err != nil {
		return Preset{}, err
	}
	var p Preset
	targets := map[string]any{
		"id": &p.ID, "label": &p.Label, "description": &p.Description,
		"codec": &p.Codec, "container": &p.Container, "preset": &p.Speed,
		"audio_kbps": &p.AudioKbps, "fps": &p.FPS, "resolution": &p.Resolution,
	}
	if version == 4 {
		targets["audio_codec"] = &p.AudioCodec
	} else {
		p.AudioCodec = "opus"
	}
	for key, destination := range targets {
		if (key == "label" || key == "description" || key == "codec" || key == "container" || key == "preset" || key == "audio_codec" || key == "resolution") && isJSONNull(fields[key]) {
			return Preset{}, fmt.Errorf("%s cannot be null", key)
		}
		if err := json.Unmarshal(fields[key], destination); err != nil {
			return Preset{}, fmt.Errorf("invalid %s: %w", key, err)
		}
	}
	if err := decodeQualityAndCRF(fields, p.Codec, &p.Quality, &p.CRF); err != nil {
		return Preset{}, fmt.Errorf("preset: %w", err)
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
	}{Version: 4, Presets: presets}, "", "  ")
	if err != nil {
		return Preset{}, fmt.Errorf("%s: %w", path, err)
	}
	if err := s.saveData("presets.json", append(data, '\n'), writeSynced, replaceFile); err != nil {
		return Preset{}, err
	}
	return p, nil
}
