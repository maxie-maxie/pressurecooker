package ffmpeg

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/maxie-maxie/pressurecooker/internal/config"
	"github.com/maxie-maxie/pressurecooker/internal/console"
	"github.com/maxie-maxie/pressurecooker/internal/output"
)

// BuildArgs returns the full executable plus argument slice for one FFmpeg invocation.
func BuildArgs(req Request) ([]string, error) {
	ffmpegBin := strings.TrimSpace(req.FFmpeg)
	if ffmpegBin == "" {
		ffmpegBin = "ffmpeg"
	}
	if strings.TrimSpace(req.Source) == "" {
		return nil, fmt.Errorf("source path is required")
	}
	if strings.TrimSpace(req.Encoder.Name) == "" {
		return nil, fmt.Errorf("encoder name is required")
	}
	if req.StartSeconds < 0 || req.LengthSeconds < 0 || math.IsNaN(req.StartSeconds) || math.IsNaN(req.LengthSeconds) || math.IsInf(req.StartSeconds, 0) || math.IsInf(req.LengthSeconds, 0) {
		return nil, fmt.Errorf("sample window start and length must be non-negative")
	}

	rateControl := strings.ToLower(strings.TrimSpace(req.RateControl))
	if rateControl == "" {
		rateControl = "quality"
	}
	if rateControl != "quality" && rateControl != "abr" {
		return nil, fmt.Errorf("rate control must be quality or abr; got %q", req.RateControl)
	}

	container := strings.ToLower(strings.TrimSpace(req.Settings.Container))
	if container == "" {
		container = "mp4"
	}
	muxer, ok := muxerByContainer[container]
	if !ok {
		return nil, fmt.Errorf("container must be mp4, mkv or webm; got %q", container)
	}

	isVulkan := req.Encoder.Backend == "Vulkan" || strings.HasSuffix(req.Encoder.Name, "_vulkan")
	isQSV := req.Encoder.Backend == "QSV" || strings.HasSuffix(req.Encoder.Name, "_qsv")
	isHardware := req.Encoder.Hardware || isVulkan || isQSV
	isTwoPassSW := rateControl == "abr" && !isHardware && (req.Pass == 1 || req.Pass == 2)
	isPass1 := isTwoPassSW && req.Pass == 1

	var quantizer float64
	if rateControl == "quality" {
		if req.Pass != 0 && req.Pass != 1 {
			return nil, fmt.Errorf("quality mode is single-pass; got pass %d", req.Pass)
		}
		if req.Settings.CRF != nil {
			if isHardware {
				return nil, fmt.Errorf("raw CRF is unsupported on hardware; use --quality or --hw off")
			}
			codec := codecForEncoder(req.Encoder.Name, req.Settings.Codec)
			if err := validateCRF(*req.Settings.CRF, codec); err != nil {
				return nil, err
			}
			quantizer = *req.Settings.CRF
		} else if req.Encoder.Quantizer > 0 {
			quantizer = req.Encoder.Quantizer
		} else {
			q, err := QualityQuantizer(req.Settings.Quality, req.Encoder.Name)
			if err != nil {
				return nil, err
			}
			quantizer = q
		}
	} else {
		if req.VideoKbps <= 0 {
			return nil, fmt.Errorf("abr mode requires positive VideoKbps")
		}
		if isTwoPassSW && strings.TrimSpace(req.PassLog) == "" {
			return nil, fmt.Errorf("two-pass software ABR requires PassLog")
		}
	}
	if !isPass1 && strings.TrimSpace(req.Output) == "" {
		return nil, fmt.Errorf("output path is required")
	}
	if req.Media.HasAudio && !isPass1 && !req.Settings.AudioSource && req.Settings.AudioKbps <= 0 {
		return nil, fmt.Errorf("audio-kbps must be a positive integer")
	}

	filters, err := buildVideoFilters(req.Settings, req.Media)
	if err != nil {
		return nil, err
	}
	filterArg := strings.Join(filters, ",")
	if isVulkan {
		filterArg = vulkanFilter(filters, req.Encoder.Decode)
	}

	args := []string{ffmpegBin, "-nostdin", "-y", "-hide_banner"}
	if isVulkan {
		args = append(args, "-init_hw_device", fmt.Sprintf("vulkan=pc:%d", req.Encoder.Device), "-filter_hw_device", "pc")
		if req.Encoder.Decode {
			args = append(args, "-hwaccel", "vulkan", "-hwaccel_device", "pc", "-hwaccel_output_format", "vulkan")
		}
	}
	if req.LengthSeconds > 0 {
		args = append(args, "-ss", formatFloat(req.StartSeconds))
	}

	args = append(args, "-i", req.Source, "-map", "0:v:0")
	if req.Media.HasAudio && !isPass1 {
		args = append(args, "-map", "0:a:0")
	}
	if req.LengthSeconds > 0 {
		args = append(args, "-t", formatFloat(req.LengthSeconds))
	}

	args = append(args, "-c:v", req.Encoder.Name)
	switch {
	case isVulkan:
		args = append(args, "-quality", strconv.Itoa(req.Encoder.Quality))
		if rateControl == "quality" {
			args = append(args, "-rc_mode", "cqp", "-qp", formatFloat(quantizer))
		} else {
			args = append(args, "-rc_mode", "vbr", "-b:v", fmt.Sprintf("%dk", req.VideoKbps))
		}
	case isQSV:
		preset := req.Encoder.Preset
		if preset == "" {
			preset, err = config.ResolvePreset(req.Settings.Preset, codecForEncoder(req.Encoder.Name, req.Settings.Codec), true)
			if err != nil {
				return nil, err
			}
		}
		args = append(args, "-preset", preset)
		if rateControl == "quality" {
			args = append(args, "-global_quality", formatFloat(quantizer))
		} else {
			args = append(args, "-b:v", fmt.Sprintf("%dk", req.VideoKbps))
			if req.Encoder.Name == "h264_qsv" {
				args = append(args, "-look_ahead", "1")
			}
		}
	default:
		preset := req.Encoder.Preset
		if preset == "" {
			preset, err = config.ResolvePreset(req.Settings.Preset, codecForEncoder(req.Encoder.Name, req.Settings.Codec), false)
			if err != nil {
				return nil, err
			}
		}
		args = append(args, "-preset", preset)
		if rateControl == "quality" {
			args = append(args, "-crf", formatFloat(quantizer), "-pix_fmt", "yuv420p")
		} else {
			args = append(args, "-b:v", fmt.Sprintf("%dk", req.VideoKbps), "-pix_fmt", "yuv420p")
		}
	}

	if filterArg != "" {
		args = append(args, "-vf", filterArg)
	}

	if isTwoPassSW {
		args = append(args, passArgs(req.Encoder.Name, req.Pass, req.PassLog)...)
	}

	if isPass1 {
		args = append(args, "-an", "-f", "null", os.DevNull)
		return args, nil
	}

	args = append(args, audioArgs(container, req.Settings, req.Media)...)
	if container == "mp4" {
		args = append(args, "-movflags", "+faststart")
	}
	args = append(args, "-f", muxer, req.Output)
	return args, nil
}

func codecForEncoder(encName, fallback string) string {
	switch strings.ToLower(strings.TrimSpace(encName)) {
	case "libx264", "h264_vulkan", "h264_qsv":
		return "h264"
	case "libx265", "hevc_vulkan", "hevc_qsv":
		return "hevc"
	case "libsvtav1", "av1_vulkan", "av1_qsv":
		return "av1"
	default:
		return strings.ToLower(strings.TrimSpace(fallback))
	}
}

func buildVideoFilters(s config.Settings, m Media) ([]string, error) {
	var filters []string
	if strings.TrimSpace(s.Resolution) != "" {
		w, h, err := config.ParseResolution(s.Resolution)
		if err != nil {
			return nil, err
		}
		if m.Width > 0 && m.Height > 0 {
			ratio := math.Min(1, math.Min(float64(w)/float64(m.Width), float64(h)/float64(m.Height)))
			if ratio == 1 {
				w, h = m.Width, m.Height
			} else {
				w = min(w, max(2, int(math.Round(float64(m.Width)*ratio/2))*2))
				h = min(h, max(2, int(math.Round(float64(m.Height)*ratio/2))*2))
			}
		}
		if w != m.Width || h != m.Height {
			filters = append(filters, fmt.Sprintf("scale=%d:%d", w, h))
		}
	}
	if s.FPS != nil {
		fps := *s.FPS
		if math.IsNaN(fps) || math.IsInf(fps, 0) || fps <= 0 || fps > 1000 {
			return nil, fmt.Errorf("fps must be a finite number greater than 0 and at most 1000")
		}
		if m.FPS > 0 {
			fps = math.Min(fps, m.FPS)
		}
		filters = append(filters, "fps="+formatFloat(fps))
	}
	return filters, nil
}

func vulkanFilter(filters []string, decode bool) string {
	if !decode {
		out := append([]string{}, filters...)
		out = append(out, "format=nv12", "hwupload")
		return strings.Join(out, ",")
	}
	scale := "scale_vulkan=format=nv12"
	rest := make([]string, 0, len(filters))
	for _, f := range filters {
		if strings.HasPrefix(f, "scale=") {
			if w, h, ok := strings.Cut(strings.TrimPrefix(f, "scale="), ":"); ok {
				scale += ":w=" + w + ":h=" + h
				continue
			}
		}
		rest = append(rest, f)
	}
	return strings.Join(append([]string{scale}, rest...), ",")
}

func audioArgs(container string, s config.Settings, m Media) []string {
	if !m.HasAudio {
		return []string{"-an"}
	}
	copyCodec, encoder := "aac", "aac"
	if container == "webm" {
		copyCodec, encoder = "opus", "libopus"
	}
	targetKbps := s.AudioKbps
	if s.AudioSource {
		targetKbps = 128
	}
	if s.AudioSource && strings.EqualFold(m.AudioCodec, copyCodec) {
		return []string{"-c:a", "copy"}
	}
	out := []string{"-c:a", encoder, "-b:a", fmt.Sprintf("%dk", targetKbps)}
	if encoder == "libopus" {
		out = append(out, "-vbr", "constrained")
	}
	return out
}

func passArgs(encoderName string, pass int, passLog string) []string {
	if encoderName == "libx265" {
		// FFmpeg builds before 7 do not forward -passlogfile into libx265.
		return []string{"-x265-params", fmt.Sprintf("pass=%d:stats=%s", pass, escapeX265Param(passLog))}
	}
	return []string{"-pass:v", strconv.Itoa(pass), "-passlogfile:v", passLog}
}

func escapeX265Param(value string) string {
	var b strings.Builder
	b.Grow(len(value) + 8)
	for i := 0; i < len(value); i++ {
		c := value[i]
		if c == '\\' || c == ':' || c == '\'' {
			b.WriteByte('\\')
		}
		b.WriteByte(c)
	}
	return b.String()
}

// Encode runs a single-pass quality/hardware encode or a two-pass software ABR pair,
// cleans its pass logs, verifies non-empty output, and probes the encoded file.
func Encode(ctx context.Context, r Runner, req Request, onEvent func(console.Event)) (Media, error) {
	if r == nil {
		return Media{}, fmt.Errorf("runner is required")
	}
	if err := output.RejectAlias(req.Source, req.Output); err != nil {
		return Media{}, err
	}
	work := req
	work.Settings = config.Clone(req.Settings)

	rateControl := strings.ToLower(strings.TrimSpace(work.RateControl))
	if rateControl == "" {
		rateControl = "quality"
	}
	isHardware := work.Encoder.Hardware ||
		work.Encoder.Backend == "Vulkan" ||
		work.Encoder.Backend == "QSV" ||
		strings.HasSuffix(work.Encoder.Name, "_vulkan") ||
		strings.HasSuffix(work.Encoder.Name, "_qsv")

	stageDuration := work.Media.Duration
	if work.LengthSeconds > 0 {
		stageDuration = work.LengthSeconds
	}
	wrapEvent := func(stage string) func(console.Event) {
		if onEvent == nil {
			return nil
		}
		return func(ev console.Event) {
			ev.Stage = stage
			if ev.Duration <= 0 {
				ev.Duration = stageDuration
			}
			onEvent(ev)
		}
	}

	if rateControl == "abr" && !isHardware && !work.SinglePass {
		passLog := strings.TrimSpace(work.PassLog)
		if passLog == "" {
			var err error
			passLog, err = newPassLogPrefix()
			if err != nil {
				return Media{}, err
			}
		}
		defer removePassLogs(passLog)

		p1 := work
		p1.Pass = 1
		p1.PassLog = passLog
		args1, err := BuildArgs(p1)
		if err != nil {
			return Media{}, err
		}
		res1, err := r.Run(ctx, withProgressArgs(args1), wrapEvent("Pass 1/2"))
		if err != nil || res1.ExitCode != 0 {
			return Media{}, formatEncodeError("ffmpeg pass 1", ctx, res1, err)
		}

		statsPath := passLog + "-0.log"
		if work.Encoder.Name == "libx265" {
			statsPath = passLog
		}
		if info, statErr := os.Stat(statsPath); statErr != nil || info.IsDir() || info.Size() == 0 {
			return Media{}, fmt.Errorf("%s did not produce two-pass statistics", work.Encoder.Name)
		}

		p2 := work
		p2.Pass = 2
		p2.PassLog = passLog
		args2, err := BuildArgs(p2)
		if err != nil {
			return Media{}, err
		}
		res2, err := r.Run(ctx, withProgressArgs(args2), wrapEvent("Pass 2/2"))
		if err != nil || res2.ExitCode != 0 {
			return Media{}, formatEncodeError("ffmpeg pass 2", ctx, res2, err)
		}
	} else {
		single := work
		single.Pass = 0
		args, err := BuildArgs(single)
		if err != nil {
			return Media{}, err
		}
		res, err := r.Run(ctx, withProgressArgs(args), wrapEvent("Encoding"))
		if err != nil || res.ExitCode != 0 {
			return Media{}, formatEncodeError("ffmpeg", ctx, res, err)
		}
	}

	info, err := os.Stat(work.Output)
	if err != nil || info.IsDir() || info.Size() == 0 {
		return Media{}, fmt.Errorf("ffmpeg reported success but produced no output")
	}

	ffprobe := strings.TrimSpace(work.FFprobe)
	if ffprobe == "" {
		ffprobe = "ffprobe"
	}
	media, err := Probe(ctx, r, ffprobe, work.Output)
	if err != nil {
		return Media{}, fmt.Errorf("probe encoded output: %w", err)
	}
	media.Bytes = info.Size()
	return media, nil
}

func withProgressArgs(args []string) []string {
	for _, a := range args {
		if a == "-progress" {
			return args
		}
	}
	out := make([]string, 0, len(args)+3)
	inserted := false
	for _, a := range args {
		out = append(out, a)
		if !inserted && a == "-hide_banner" {
			out = append(out, "-progress", "pipe:1", "-nostats")
			inserted = true
		}
	}
	if !inserted && len(out) > 0 {
		out = append([]string{out[0], "-progress", "pipe:1", "-nostats"}, out[1:]...)
	}
	return out
}

func formatEncodeError(label string, ctx context.Context, res Result, err error) error {
	if ctx.Err() != nil {
		return fmt.Errorf("%s cancelled: %w", label, ctx.Err())
	}
	tail := ""
	if len(res.StderrTail) > 0 {
		tail = ": " + strings.Join(res.StderrTail, " | ")
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("%s failed with exit code %d%s", label, res.ExitCode, tail)
	}
	if err != nil {
		return fmt.Errorf("%s failed: %w%s", label, err, tail)
	}
	return fmt.Errorf("%s failed%s", label, tail)
}

func newPassLogPrefix() (string, error) {
	var buf [12]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", fmt.Errorf("generate pass log name: %w", err)
	}
	return filepath.Join(os.TempDir(), "pressurecooker-"+hex.EncodeToString(buf[:])), nil
}

func removePassLogs(prefix string) {
	for _, suffix := range []string{"", ".temp", ".cutree", ".cutree.temp", "-0.log", "-0.log.temp", "-0.log.mbtree", "-0.log.mbtree.temp"} {
		_ = os.Remove(prefix + suffix)
	}
}
