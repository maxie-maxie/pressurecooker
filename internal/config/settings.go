package config

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

type Origin string
type Origins map[string]Origin

type Settings struct {
	Codec       string   `json:"codec"`
	Container   string   `json:"container"`
	Preset      string   `json:"preset"`
	Quality     string   `json:"quality"`
	CRF         *float64 `json:"crf"`
	AudioKbps   int      `json:"audio_kbps"`
	AudioSource bool     `json:"-"`
	AudioCodec  string   `json:"-"` // v4 contract; serialized from schema v4
	FPS         *float64 `json:"fps"`
	Resolution  string   `json:"resolution"`
	HW          string   `json:"hw"`
}

func Factory() Settings {
	return Settings{Codec: "hevc", Container: "mp4", Preset: "fast",
		Quality: "balanced", AudioKbps: 128, HW: "auto"}
}

func Clone(s Settings) Settings {
	if s.CRF != nil {
		v := *s.CRF
		s.CRF = &v
	}
	if s.FPS != nil {
		v := *s.FPS
		s.FPS = &v
	}
	return s
}

func Validate(s Settings) error {
	if s.Codec != "h264" && s.Codec != "hevc" && s.Codec != "av1" {
		return fmt.Errorf("codec must be h264, hevc or av1")
	}
	if s.Container != "mp4" && s.Container != "mkv" && s.Container != "webm" {
		return fmt.Errorf("container must be mp4, mkv or webm")
	}
	if s.Container == "webm" && s.Codec != "av1" {
		return fmt.Errorf("webm requires codec av1")
	}
	if s.HW != "auto" && s.HW != "vulkan" && s.HW != "qsv" && s.HW != "off" {
		return fmt.Errorf("hw must be auto, vulkan, qsv or off")
	}
	if s.Quality == "" && s.CRF == nil || s.Quality != "" && s.Quality != "high" && s.Quality != "balanced" && s.Quality != "small" {
		return fmt.Errorf("quality must be high, balanced or small")
	}
	if s.CRF != nil {
		if s.HW == "vulkan" || s.HW == "qsv" {
			return fmt.Errorf("raw CRF is unsupported on %s; use --quality or --hw off", s.HW)
		}
		lo, hi := 0.0, 51.0
		if s.Codec == "av1" {
			lo, hi = 1, 63
		}
		if math.IsNaN(*s.CRF) || math.IsInf(*s.CRF, 0) || *s.CRF < lo || *s.CRF > hi {
			return fmt.Errorf("crf for %s must be a finite number from %.0f to %.0f", s.Codec, lo, hi)
		}
	}
	// Numeric saved presets keep their SVT-AV1 meaning even when hardware is selected.
	if _, err := ResolvePreset(s.Preset, s.Codec, false); err != nil {
		return err
	}
	if s.HW == "qsv" {
		if _, err := ResolvePreset(s.Preset, s.Codec, true); err != nil {
			return err
		}
	}
	if s.AudioKbps <= 0 {
		return fmt.Errorf("audio-kbps must be a positive integer")
	}
	if s.FPS != nil && (math.IsNaN(*s.FPS) || math.IsInf(*s.FPS, 0) || *s.FPS <= 0 || *s.FPS > 1000) {
		return fmt.Errorf("fps must be a finite number greater than 0 and at most 1000")
	}
	_, _, err := ParseResolution(s.Resolution)
	return err
}

var namedPresets = []string{"ultrafast", "superfast", "veryfast", "faster", "fast", "medium", "slow", "slower", "veryslow", "placebo"}

func ResolvePreset(preset, codec string, qsv bool) (string, error) {
	value := strings.ToLower(strings.TrimSpace(preset))
	for i, name := range namedPresets {
		if value != name {
			continue
		}
		if qsv {
			if i < 2 {
				return "veryfast", nil
			}
			if name == "placebo" {
				return "veryslow", nil
			}
			return name, nil
		}
		if codec == "av1" {
			return strconv.Itoa(12 - i), nil
		}
		if codec == "h264" || codec == "hevc" {
			return name, nil
		}
	}
	if digits(value) {
		n, err := strconv.Atoi(value)
		if err == nil {
			if qsv {
				mapped := []string{"veryslow", "veryslow", "veryslow", "slower", "slow", "medium", "fast", "faster"}
				if n < len(mapped) {
					return mapped[n], nil
				}
				return "veryfast", nil
			}
			if codec == "av1" && n <= 13 {
				return strconv.Itoa(n), nil
			}
		}
	}
	if codec == "av1" && !qsv {
		return "", fmt.Errorf("preset for AV1 must be 0-13 or a name from ultrafast to placebo")
	}
	return "", fmt.Errorf("preset for %s must be a name from ultrafast to placebo", codec)
}

func DescribePreset(requested, resolved string, qsv bool) string {
	value := strings.ToLower(strings.TrimSpace(requested))
	if value != resolved {
		if qsv {
			return fmt.Sprintf("%s (mapped from '%s')", resolved, value)
		}
		for _, name := range namedPresets {
			if value == name {
				return fmt.Sprintf("%s (mapped from '%s')", resolved, value)
			}
		}
	}
	return resolved
}

func digits(value string) bool {
	if value == "" {
		return false
	}
	for _, c := range value {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

func ParseResolution(text string) (width, height int, err error) {
	if text == "" {
		return 0, 0, nil
	}
	value := strings.ToLower(strings.TrimSpace(text))
	switch value {
	case "sd", "360p":
		return 640, 360, nil
	case "480p":
		return 854, 480, nil
	case "hd", "720p":
		return 1280, 720, nil
	case "fullhd", "fhd", "1080p":
		return 1920, 1080, nil
	case "qhd", "2k", "1440p":
		return 2560, 1440, nil
	case "uhd", "4k", "2160p":
		return 3840, 2160, nil
	}
	parts := strings.Split(value, "x")
	if len(parts) == 2 && digits(parts[0]) && digits(parts[1]) {
		w, ew := strconv.Atoi(parts[0])
		h, eh := strconv.Atoi(parts[1])
		if ew == nil && eh == nil && w > 0 && h > 0 && w <= 16384 && h <= 16384 && w%2 == 0 && h%2 == 0 {
			return w, h, nil
		}
	}
	return 0, 0, fmt.Errorf("resolution must be an alias or positive even WxH dimensions at most 16384; got %q", text)
}
