package config

import (
	"fmt"
	"math"
	"slices"
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
	AudioCodec  string   `json:"audio_codec"`
	FPS         *float64 `json:"fps"`
	Resolution  string   `json:"resolution"`
	HW          string   `json:"hw"`
}

func Factory() Settings {
	return Settings{
		Codec:      "hevc",
		Container:  "mp4",
		Preset:     "fast",
		Quality:    "balanced",
		AudioKbps:  128,
		AudioCodec: "opus",
		HW:         "auto",
	}
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

func validateEncoding(codec, container, audioCodec, speed, quality string, crf *float64, checkAudio bool, audioKbps int, fps *float64, resolution string) error {
	if !slices.Contains(VideoCodecs, codec) {
		return fmt.Errorf("codec must be one of %s", strings.Join(VideoCodecs, ", "))
	}
	if !slices.Contains(Containers, container) {
		return fmt.Errorf("container must be one of %s", strings.Join(Containers, ", "))
	}
	if !slices.Contains(AudioCodecs, audioCodec) {
		return fmt.Errorf("audio codec must be one of %s", strings.Join(AudioCodecs, ", "))
	}
	if !Accepts(container, codec, "") {
		return videoMismatchError(container, codec)
	}
	if !Accepts(container, "", audioCodec) {
		return audioMismatchError(container, audioCodec)
	}
	caps := CodecCaps(codec)
	if caps.Speed {
		// Numeric saved presets keep their software meaning even when hardware is selected.
		if _, err := ResolvePreset(speed, codec, false); err != nil {
			return err
		}
	}
	if caps.CRFHi == 0 && crf != nil {
		return fmt.Errorf("crf is not supported for %s", codec)
	}
	if caps.Quality {
		if caps.CRFHi > 0 && (quality == "") == (crf == nil) {
			return fmt.Errorf("must set exactly one of quality or crf")
		}
		if caps.CRFHi == 0 && quality == "" {
			return fmt.Errorf("quality must be high, balanced or small")
		}
	}
	if quality != "" && quality != "high" && quality != "balanced" && quality != "small" {
		return fmt.Errorf("quality must be high, balanced or small")
	}
	if crf != nil {
		if math.IsNaN(*crf) || math.IsInf(*crf, 0) || *crf < caps.CRFLo || *crf > caps.CRFHi {
			return fmt.Errorf("crf for %s must be a finite number from %.0f to %.0f", codec, caps.CRFLo, caps.CRFHi)
		}
	}
	if checkAudio && audioCodec != "flac" && audioKbps <= 0 {
		return fmt.Errorf("audio-kbps must be a positive integer")
	}
	if fps != nil && (math.IsNaN(*fps) || math.IsInf(*fps, 0) || *fps <= 0 || *fps > 1000) {
		return fmt.Errorf("fps must be a finite number greater than 0 and at most 1000")
	}
	_, _, err := ParseResolution(resolution)
	return err
}

func Validate(s Settings) error {
	if s.HW != "auto" && s.HW != "vulkan" && s.HW != "qsv" && s.HW != "off" {
		return fmt.Errorf("hw must be auto, vulkan, qsv or off")
	}
	if err := validateEncoding(s.Codec, s.Container, s.AudioCodec, s.Preset, s.Quality, s.CRF, true, s.AudioKbps, s.FPS, s.Resolution); err != nil {
		return err
	}
	caps := CodecCaps(s.Codec)
	if (s.HW == "vulkan" || s.HW == "qsv") && !slices.Contains(caps.HW, s.HW) {
		return fmt.Errorf("%s does not support %s hardware encoding", s.Codec, s.HW)
	}
	if s.CRF != nil && (s.HW == "vulkan" || s.HW == "qsv") {
		return fmt.Errorf("raw CRF is unsupported on %s; use --quality or --hw off", s.HW)
	}
	if s.HW == "qsv" && caps.Speed {
		if _, err := ResolvePreset(s.Preset, s.Codec, true); err != nil {
			return err
		}
	}
	return nil
}

var namedPresets = []string{"ultrafast", "superfast", "veryfast", "faster", "fast", "medium", "slow", "slower", "veryslow", "placebo"}

func ResolvePreset(preset, codec string, qsv bool) (string, error) {
	if !slices.Contains(VideoCodecs, codec) {
		return "", fmt.Errorf("unsupported codec %q", codec)
	}
	if !CodecCaps(codec).Speed {
		return "", nil
	}
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
		switch codec {
		case "av1":
			return strconv.Itoa(12 - i), nil
		case "vp9":
			return strconv.Itoa(max(0, 8-i)), nil
		case "h264", "hevc":
			return name, nil
		}
	}
	if digits(value) {
		n, err := strconv.Atoi(value)
		if err == nil {
			if codec == "vp9" {
				if n <= 8 {
					if qsv {
						return qsvNumericPreset(n), nil
					}
					return strconv.Itoa(n), nil
				}
			} else if qsv {
				return qsvNumericPreset(n), nil
			} else if codec == "av1" && n <= 13 {
				return strconv.Itoa(n), nil
			}
		}
	}
	if codec == "av1" && !qsv {
		return "", fmt.Errorf("preset for AV1 must be 0-13 or a name from ultrafast to placebo")
	}
	if codec == "vp9" {
		return "", fmt.Errorf("preset for vp9 must be 0-8 or a name from ultrafast to placebo")
	}
	return "", fmt.Errorf("preset for %s must be a name from ultrafast to placebo", codec)
}

func qsvNumericPreset(n int) string {
	mapped := []string{"veryslow", "veryslow", "veryslow", "slower", "slow", "medium", "fast", "faster"}
	if n < len(mapped) {
		return mapped[n]
	}
	return "veryfast"
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
