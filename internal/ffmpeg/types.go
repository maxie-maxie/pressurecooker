package ffmpeg

import (
	"context"
	"fmt"
	"runtime"
	"strconv"
	"strings"

	"github.com/maxie-maxie/pressurecooker/internal/config"
	"github.com/maxie-maxie/pressurecooker/internal/console"
)

type Result struct {
	Stdout     string
	StderrTail []string
	ExitCode   int
}

type Runner interface {
	Run(context.Context, []string, func(console.Event)) (Result, error)
}

type ExecRunner struct{}

type Media struct {
	Duration, FPS                     float64
	Width, Height                     int
	VideoCodec, AudioCodec, Container string
	AudioKbps                         float64
	HasAudio                          bool
	Bytes                             int64
}

type Encoder struct {
	Name, Backend, Preset, DeviceName string
	Device, Quality                   int // Quality is Vulkan speed level, distinct from quantizer
	Hardware, Decode                  bool
	Quantizer                         float64
	Warnings                          []string
}

type Request struct {
	Source, Output, FFmpeg, FFprobe string
	Settings                        config.Settings
	Origins                         config.Origins
	Encoder                         Encoder
	Media                           Media
	RateControl                     string // "quality" or "abr"
	VideoKbps                       int
	Pass                            int
	SinglePass                      bool
	PassLog                         string
	StartSeconds, LengthSeconds     float64 // zero length means full input
}

var softwareEncoders = map[string]string{
	"h264":   "libx264",
	"hevc":   "libx265",
	"av1":    "libsvtav1",
	"vp9":    "libvpx-vp9",
	"prores": "prores_ks",
	"ffv1":   "ffv1",
	"theora": "libtheora",
}

var vulkanEncoders = map[string]string{
	"h264":   "h264_vulkan",
	"hevc":   "hevc_vulkan",
	"av1":    "av1_vulkan",
	"prores": "prores_ks_vulkan",
	"ffv1":   "ffv1_vulkan",
}

var qsvEncoders = map[string]string{
	"h264": "h264_qsv",
	"hevc": "hevc_qsv",
	"av1":  "av1_qsv",
	"vp9":  "vp9_qsv",
}

var muxerByContainer = map[string]string{
	"mp4":  "mp4",
	"mkv":  "matroska",
	"webm": "webm",
	"mov":  "mov",
	"avi":  "avi",
	"ogg":  "ogg",
	"ts":   "mpegts",
	"nut":  "nut",
}

// QualityQuantizer returns the CRF, Vulkan CQP, or QSV ICQ value for a quality level and encoder.
func QualityQuantizer(level, encoderName string) (float64, error) {
	lvl := strings.ToLower(strings.TrimSpace(level))
	if lvl == "" {
		lvl = "balanced"
	}
	var hevcVal, h264Val, av1Val, vp9Val, proresVal, theoraVal float64
	switch lvl {
	case "high":
		hevcVal, h264Val, av1Val = 26, 21, 30
		vp9Val, proresVal, theoraVal = 24, 3, 8
	case "balanced":
		hevcVal, h264Val, av1Val = 32, 27, 38
		vp9Val, proresVal, theoraVal = 32, 2, 6
	case "small":
		hevcVal, h264Val, av1Val = 36, 31, 46
		vp9Val, proresVal, theoraVal = 40, 1, 4
	default:
		return 0, fmt.Errorf("quality must be high, balanced or small; got %q", level)
	}
	switch strings.ToLower(strings.TrimSpace(encoderName)) {
	case "libx265", "hevc_vulkan", "h264_vulkan", "av1_vulkan", "hevc_qsv", "h264_qsv", "av1_qsv":
		return hevcVal, nil
	case "libx264":
		return h264Val, nil
	case "libsvtav1":
		return av1Val, nil
	case "libvpx-vp9", "vp9_qsv":
		return vp9Val, nil
	case "prores_ks", "prores_ks_vulkan":
		return proresVal, nil
	case "libtheora":
		return theoraVal, nil
	default:
		return 0, fmt.Errorf("unsupported encoder %q for quality mapping", encoderName)
	}
}

var av1NamedSpeed = map[string]int{
	"ultrafast": 12, "superfast": 11, "veryfast": 10, "faster": 9, "fast": 8,
	"medium": 7, "slow": 6, "slower": 5, "veryslow": 4, "placebo": 3,
}

// ResolveVulkanQuality maps a preset and codec to the Vulkan speed quality level (higher is slower).
func ResolveVulkanQuality(preset, codec string) (int, error) {
	resolved, err := config.ResolvePreset(preset, codec, false)
	if err != nil {
		return 0, err
	}
	var speed int
	if codec == "av1" {
		speed, err = strconv.Atoi(resolved)
		if err != nil {
			return 0, err
		}
	} else {
		var ok bool
		speed, ok = av1NamedSpeed[resolved]
		if !ok {
			return 0, fmt.Errorf("preset %q is not valid for %s", preset, codec)
		}
	}
	if 8-speed < 0 {
		return 0, nil
	}
	return 8 - speed, nil
}

// FormatCommand renders an argv slice for display using PowerShell quoting on Windows and POSIX shlex elsewhere.
func FormatCommand(args []string, goos string) string {
	if goos == "" {
		goos = runtime.GOOS
	}
	if goos == "windows" {
		parts := make([]string, len(args))
		for i, a := range args {
			parts[i] = "'" + strings.ReplaceAll(a, "'", "''") + "'"
		}
		return "& " + strings.Join(parts, " ")
	}
	parts := make([]string, len(args))
	for i, a := range args {
		parts[i] = shlexQuote(a)
	}
	return strings.Join(parts, " ")
}

func shlexQuote(s string) string {
	if s == "" {
		return "''"
	}
	safe := true
	for i := 0; i < len(s); i++ {
		c := s[i]
		isSafe := (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') ||
			c == '_' || c == '@' || c == '%' || c == '+' || c == '=' || c == ':' || c == ',' || c == '.' || c == '/' || c == '-'
		if !isSafe {
			safe = false
			break
		}
	}
	if safe {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'"'"'`) + "'"
}

func formatFloat(v float64) string {
	s := strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.3f", v), "0"), ".")
	if s == "" || s == "-0" {
		return "0"
	}
	return s
}
