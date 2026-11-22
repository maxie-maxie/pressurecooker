package ffmpeg

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/maxie-maxie/pressurecooker/internal/config"
)

const hardwareProbeTimeout = 30 * time.Second

var (
	vulkanDevicePattern = regexp.MustCompile(`(?im)^\[[^\]]+\]\s+(\d+):\s+(.+?)\s+\([^()]+\)\s+\(0x[0-9a-f]+\)\s*$`)
	vulkanMaxQualityRe  = regexp.MustCompile(`Encoder max quality:\s*(\d+)`)
	vulkanErrorLineRe   = regexp.MustCompile(`(?i)not support|unsupported|unknown encoder|not found|error|fail|timed out`)
	bracketPrefixRe     = regexp.MustCompile(`^(?:\[[^\]]+\]\s*)+`)
)

type vulkanDevice struct {
	index int
	name  string
}

// ChooseEncoder selects a software or hardware encoder, running GPU probes only when permitted.
func ChooseEncoder(ctx context.Context, r Runner, ffmpegBin string, s config.Settings) (Encoder, error) {
	if err := ctx.Err(); err != nil {
		return Encoder{}, err
	}
	codec := strings.ToLower(strings.TrimSpace(s.Codec))
	swName, ok := softwareEncoders[codec]
	if !ok {
		return Encoder{}, fmt.Errorf("unsupported video codec %q", s.Codec)
	}
	caps := config.CodecCaps(codec)
	hw := strings.ToLower(strings.TrimSpace(s.HW))
	if hw == "" {
		hw = "auto"
	}
	if hw != "auto" && hw != "vulkan" && hw != "qsv" && hw != "off" {
		return Encoder{}, fmt.Errorf("hw must be auto, vulkan, qsv or off; got %q", s.HW)
	}
	if (hw == "vulkan" || hw == "qsv") && !slices.Contains(caps.HW, hw) {
		return Encoder{}, fmt.Errorf("%s does not support %s hardware encoding", codec, hw)
	}
	qualityLevel := strings.ToLower(strings.TrimSpace(s.Quality))
	if qualityLevel == "" {
		qualityLevel = "balanced"
	}
	if caps.Quality && qualityLevel != "high" && qualityLevel != "balanced" && qualityLevel != "small" {
		return Encoder{}, fmt.Errorf("quality must be high, balanced or small; got %q", s.Quality)
	}

	// Validate preset before any hardware probe runs.
	var swPreset string
	var err error
	if caps.Speed {
		swPreset, err = config.ResolvePreset(s.Preset, codec, false)
		if err != nil {
			return Encoder{}, err
		}
	}

	if s.CRF != nil {
		if err := validateCRF(*s.CRF, codec); err != nil {
			return Encoder{}, err
		}
		if hw == "vulkan" || hw == "qsv" {
			return Encoder{}, fmt.Errorf("raw CRF is unsupported on %s; use --quality or --hw off", hw)
		}
		// Under hw=auto, explicit --crf routes straight to software rather than guessing a GPU QP.
		return Encoder{
			Name:      swName,
			Backend:   "software",
			Preset:    swPreset,
			Hardware:  false,
			Quantizer: *s.CRF,
		}, nil
	}

	var swQuantizer float64
	if caps.Quality {
		swQuantizer, err = QualityQuantizer(qualityLevel, swName)
		if err != nil {
			return Encoder{}, err
		}
	}
	if hw == "off" || len(caps.HW) == 0 {
		return Encoder{
			Name:      swName,
			Backend:   "software",
			Preset:    swPreset,
			Hardware:  false,
			Quantizer: swQuantizer,
		}, nil
	}

	if r == nil {
		return Encoder{}, fmt.Errorf("runner is required for hardware probing")
	}
	if strings.TrimSpace(ffmpegBin) == "" {
		ffmpegBin = "ffmpeg"
	}

	if hw == "vulkan" || hw == "auto" && slices.Contains(caps.HW, "vulkan") {
		vkName := vulkanEncoders[codec]
		var vkQuality int
		if caps.Speed {
			vkQuality, err = ResolveVulkanQuality(s.Preset, codec)
			if err != nil {
				return Encoder{}, err
			}
		}
		var vkQP float64
		if caps.Quality {
			vkQP, err = QualityQuantizer(qualityLevel, vkName)
			if err != nil {
				return Encoder{}, err
			}
		}
		enc, reason, probeErr := probeVulkan(ctx, r, ffmpegBin, vkName, vkQuality, vkQP)
		if probeErr != nil {
			return Encoder{}, probeErr
		}
		if enc != nil {
			return *enc, nil
		}
		if hw == "vulkan" {
			return Encoder{}, fmt.Errorf("%s", reason)
		}
		warning := fmt.Sprintf("%s; using %s", reason, swName)
		return Encoder{
			Name:      swName,
			Backend:   "software",
			Preset:    swPreset,
			Hardware:  false,
			Quantizer: swQuantizer,
			Warnings:  []string{warning},
		}, nil
	}

	qsvName := qsvEncoders[codec]
	qsvPreset, err := config.ResolvePreset(s.Preset, codec, true)
	if err != nil {
		return Encoder{}, err
	}
	qsvICQ, err := QualityQuantizer(qualityLevel, qsvName)
	if err != nil {
		return Encoder{}, err
	}
	okProbe, reason, probeErr := probeQSV(ctx, r, ffmpegBin, qsvName, qsvPreset, qsvICQ)
	if probeErr != nil {
		return Encoder{}, probeErr
	}
	if okProbe {
		return Encoder{
			Name:      qsvName,
			Backend:   "QSV",
			Preset:    qsvPreset,
			Hardware:  true,
			Quantizer: qsvICQ,
		}, nil
	}
	if reason == "" {
		reason = "probe failed"
	}
	if hw == "auto" {
		return Encoder{Name: swName, Backend: "software", Preset: swPreset, Quantizer: swQuantizer,
			Warnings: []string{fmt.Sprintf("QSV encode unavailable: %s; using %s", reason, swName)}}, nil
	}
	return Encoder{}, fmt.Errorf("QSV requested but %s probe failed: %s", qsvName, reason)
}

func validateCRF(v float64, codec string) error {
	caps := config.CodecCaps(codec)
	if caps.CRFHi == 0 {
		return fmt.Errorf("crf is unsupported for %s", codec)
	}
	lo, hi := caps.CRFLo, caps.CRFHi
	if math.IsNaN(v) || math.IsInf(v, 0) || v < lo || v > hi {
		return fmt.Errorf("crf for %s must be a finite number from %.0f to %.0f", codec, lo, hi)
	}
	return nil
}

func probeVulkan(ctx context.Context, r Runner, ffmpegBin, name string, targetQuality int, qp float64) (*Encoder, string, error) {
	firstOK, firstStderr, err := probeVulkanEncoder(ctx, r, ffmpegBin, name, 0, 0, qp)
	if err != nil {
		return nil, "", err
	}
	devices := parseVulkanDevices(firstStderr)
	if len(devices) == 0 {
		devices = []vulkanDevice{{index: 0, name: "GPU 0"}}
	}

	var lastError string
	for _, dev := range devices {
		var ok bool
		var stderr string
		if dev.index == 0 {
			ok, stderr = firstOK, firstStderr
		} else {
			ok, stderr, err = probeVulkanEncoder(ctx, r, ffmpegBin, name, dev.index, 0, qp)
			if err != nil {
				return nil, "", err
			}
		}
		if !ok {
			msg := vulkanError(stderr)
			if strings.Contains(strings.ToLower(msg), "unknown encoder") {
				if len(devices) > 1 {
					return nil, fmt.Sprintf("Vulkan encode unavailable on %d GPUs", len(devices)), nil
				}
				return nil, "Vulkan encode unavailable: " + msg, nil
			}
			lastError = msg
			continue
		}

		selected := 0
		if m := vulkanMaxQualityRe.FindStringSubmatch(stderr); len(m) == 2 {
			if levels, convErr := strconv.Atoi(m[1]); convErr == nil && levels > 0 {
				selected = min(targetQuality, levels-1)
			}
		}
		var warnings []string
		if selected > 0 {
			qOK, qStderr, err := probeVulkanEncoder(ctx, r, ffmpegBin, name, dev.index, selected, qp)
			if err != nil {
				return nil, "", err
			}
			if !qOK {
				warnings = append(warnings, fmt.Sprintf("Vulkan quality %d unavailable; using quality 0: %s", selected, vulkanError(qStderr)))
				selected = 0
			}
		}
		return &Encoder{
			Name:       name,
			Backend:    "Vulkan",
			Preset:     strconv.Itoa(selected),
			DeviceName: dev.name,
			Device:     dev.index,
			Quality:    selected,
			Hardware:   true,
			Decode:     false,
			Quantizer:  qp,
			Warnings:   warnings,
		}, "", nil
	}
	if len(devices) > 1 {
		return nil, fmt.Sprintf("Vulkan encode unavailable on %d GPUs", len(devices)), nil
	}
	return nil, "Vulkan encode unavailable: " + lastError, nil
}

func parseVulkanDevices(stderr string) []vulkanDevice {
	matches := vulkanDevicePattern.FindAllStringSubmatch(stderr, -1)
	if len(matches) == 0 {
		return nil
	}
	devices := make([]vulkanDevice, 0, len(matches))
	for _, m := range matches {
		idx, err := strconv.Atoi(m[1])
		if err != nil {
			continue
		}
		devices = append(devices, vulkanDevice{index: idx, name: strings.TrimSpace(m[2])})
	}
	return devices
}

func probeVulkanEncoder(ctx context.Context, r Runner, ffmpegBin, name string, device, quality int, qp float64) (bool, string, error) {
	cmd := []string{
		ffmpegBin, "-nostdin", "-hide_banner", "-loglevel", "verbose",
		"-init_hw_device", fmt.Sprintf("vulkan=pc:%d", device), "-filter_hw_device", "pc",
		"-f", "lavfi", "-i", "color=c=black:s=1280x720:r=30:d=0.3",
		"-vf", vulkanFilter(nil, false, vulkanPixelFormat(name)),
		"-c:v", name,
	}
	cmd = append(cmd, vulkanCodecArgs(name, quality, qp, "quality", 0)...)
	cmd = append(cmd, "-an", "-f", "null", os.DevNull)
	return runHardwareProbe(ctx, r, cmd)
}

func probeQSV(ctx context.Context, r Runner, ffmpegBin, name, preset string, icq float64) (bool, string, error) {
	cmd := []string{
		ffmpegBin, "-nostdin", "-y", "-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "color=c=black:s=256x144:r=30:d=0.3",
		"-c:v", name, "-preset", preset, "-global_quality", formatFloat(icq),
		"-f", "null", os.DevNull,
	}
	ok, stderr, err := runHardwareProbe(ctx, r, cmd)
	if err != nil {
		return false, "", err
	}
	if ok {
		return true, "", nil
	}
	return false, firstMeaningfulLine(stderr), nil
}

// ProbeVulkanDecode tests whether the selected Vulkan device can decode source directly on GPU.
func ProbeVulkanDecode(ctx context.Context, r Runner, ffmpegBin, source string, enc Encoder, settings config.Settings, media Media) (bool, string) {
	if r == nil || strings.TrimSpace(source) == "" {
		return false, "missing runner or source"
	}
	if strings.TrimSpace(ffmpegBin) == "" {
		ffmpegBin = "ffmpeg"
	}
	filters, err := buildVideoFilters(settings, media)
	if err != nil {
		return false, err.Error()
	}
	qp := enc.Quantizer
	if qp <= 0 {
		qp = 32
	}
	cmd := []string{
		ffmpegBin, "-nostdin", "-hide_banner", "-loglevel", "error",
		"-init_hw_device", fmt.Sprintf("vulkan=pc:%d", enc.Device), "-filter_hw_device", "pc",
		"-hwaccel", "vulkan", "-hwaccel_device", "pc", "-hwaccel_output_format", "vulkan",
		"-i", source, "-map", "0:v:0",
		"-vf", vulkanFilter(filters, true, vulkanPixelFormat(enc.Name)),
		"-c:v", enc.Name,
	}
	cmd = append(cmd, vulkanCodecArgs(enc.Name, enc.Quality, qp, "quality", 0)...)
	cmd = append(cmd, "-frames:v", "8", "-an", "-f", "null", os.DevNull)
	ok, stderr, runErr := runHardwareProbe(ctx, r, cmd)
	if runErr != nil {
		return false, runErr.Error()
	}
	if ok {
		return true, ""
	}
	return false, vulkanError(stderr)
}

func runHardwareProbe(ctx context.Context, r Runner, cmd []string) (bool, string, error) {
	if err := ctx.Err(); err != nil {
		return false, "", err
	}
	probeCtx, cancel := context.WithTimeout(ctx, hardwareProbeTimeout)
	defer cancel()

	res, err := r.Run(probeCtx, cmd, nil)
	stderr := strings.Join(res.StderrTail, "\n")
	if ctx.Err() != nil {
		return false, stderr, ctx.Err()
	}
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || probeCtx.Err() == context.DeadlineExceeded {
			return false, "probe timed out", nil
		}
		if strings.TrimSpace(stderr) != "" {
			return false, stderr, nil
		}
		return false, err.Error(), nil
	}
	return res.ExitCode == 0, stderr, nil
}

func vulkanError(stderr string) string {
	for _, line := range strings.Split(stderr, "\n") {
		if vulkanErrorLineRe.MatchString(line) {
			return strings.Join(strings.Fields(bracketPrefixRe.ReplaceAllString(line, "")), " ")
		}
	}
	if first := firstMeaningfulLine(stderr); first != "" {
		return first
	}
	return "probe failed"
}

func firstMeaningfulLine(text string) string {
	for _, line := range strings.Split(text, "\n") {
		s := strings.Join(strings.Fields(line), " ")
		if s == "" {
			continue
		}
		if len(s) > 200 {
			return s[:200] + "..."
		}
		return s
	}
	return ""
}
