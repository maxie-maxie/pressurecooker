package target

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"strings"

	"github.com/maxie-maxie/pressurecooker/internal/config"
	"github.com/maxie-maxie/pressurecooker/internal/console"
	"github.com/maxie-maxie/pressurecooker/internal/ffmpeg"
	"github.com/maxie-maxie/pressurecooker/internal/output"
)

type Fit struct {
	Fits      bool
	VideoKbps int
	AudioKbps int
	AudioCopy bool
	Reason    string // numbers only, empty when Fits
}

func CheckFit(m ffmpeg.Media, s config.Settings, targetBytes int64) Fit {
	fit := Fit{}
	budget := float64(targetBytes) * 8 * defaultMuxMargin
	if targetBytes <= 0 || m.Duration <= 0 || math.IsNaN(m.Duration) || math.IsInf(m.Duration, 0) {
		fit.Reason = fmt.Sprintf("Duration is %g s; target is %g MB", m.Duration, float64(targetBytes)/1e6)
		return fit
	}
	audioKbps := 0.0
	if m.HasAudio {
		switch {
		case !s.AudioSource:
			fit.AudioKbps = s.AudioKbps
			audioKbps = float64(s.AudioKbps)
		case canCopyAudio(m, s.Container) && m.AudioKbps > 0 && m.AudioKbps*1000*m.Duration <= 0.20*budget:
			fit.AudioCopy = true
			fit.AudioKbps = int(math.Round(m.AudioKbps))
			audioKbps = m.AudioKbps
		default:
			fit.AudioKbps = 48
			for _, kbps := range standardAudioLadder {
				if float64(kbps)*1000*m.Duration <= 0.20*budget {
					fit.AudioKbps = kbps
					break
				}
			}
			audioKbps = float64(fit.AudioKbps)
		}
	}
	audioBits := audioKbps * 1000 * m.Duration
	videoKbps := (budget - audioBits) / m.Duration / 1000
	fit.VideoKbps = positiveBitrate(videoKbps)
	fit.Fits = fit.VideoKbps > 0
	if !fit.Fits {
		fit.VideoKbps = 0
		fit.Reason = fmt.Sprintf("Audio at %g kbps needs %g MB for %.0f:%02.0f; target is %g MB", audioKbps, audioBits/8/1e6, math.Floor(m.Duration/60), math.Floor(math.Mod(m.Duration, 60)), float64(targetBytes)/1e6)
		if videoKbps > 0 && videoKbps < 1 {
			fit.Reason = fmt.Sprintf("Video budget is %.3f kbps; target is %g MB for %g s", videoKbps, float64(targetBytes)/1e6, m.Duration)
		}
	}
	return fit
}

func FitEncode(ctx context.Context, r ffmpeg.Runner, req ffmpeg.Request, fit Fit, targetBytes int64, on func(console.Event)) (out Outcome, err error) {
	if err = validateSizeRequest(r, req, targetBytes); err != nil {
		return out, err
	}
	defer cleanupSizeOutput(req.Output, &out, &err)
	out.Settings = config.Clone(req.Settings)
	out.Settings.AudioKbps, out.Settings.AudioSource = fit.AudioKbps, fit.AudioCopy
	if !fit.Fits || fit.VideoKbps <= 0 {
		out.Infeasible = fit.Reason
		if out.Infeasible == "" {
			out.Infeasible = "Video budget is 0 kbps"
		}
		return out, nil
	}
	work := cloneRequest(req)
	work.Settings = config.Clone(out.Settings)
	work.RateControl, work.SinglePass = "abr", false
	work.StartSeconds, work.LengthSeconds, work.Pass, work.PassLog = 0, 0, 0, ""
	if work.Encoder.Name == "" {
		selection := config.Clone(work.Settings)
		selection.CRF = nil
		work.Encoder, err = ffmpeg.ChooseEncoder(ctx, r, work.FFmpeg, selection)
		if err != nil {
			return out, err
		}
	}
	work.VideoKbps = sizeVideoKbps(work.Encoder, fit.VideoKbps)
	return encodeSize(ctx, r, work, targetBytes, 2, false, out, on)
}

func canCopyAudio(m ffmpeg.Media, container string) bool {
	codec := "aac"
	if strings.EqualFold(strings.TrimSpace(container), "webm") {
		codec = "opus"
	}
	return strings.EqualFold(m.AudioCodec, codec)
}

func positiveBitrate(kbps float64) int {
	if math.IsNaN(kbps) || kbps < 1 {
		return 0
	}
	// FFmpeg bitrates and 32-bit builds both need a representable integer.
	return int(math.Min(math.Floor(kbps), math.MaxInt32))
}

func sizeVideoKbps(enc ffmpeg.Encoder, kbps int) int {
	if enc.Hardware || strings.EqualFold(enc.Backend, "Vulkan") || strings.EqualFold(enc.Backend, "QSV") || strings.HasSuffix(enc.Name, "_vulkan") || strings.HasSuffix(enc.Name, "_qsv") {
		return max(1, positiveBitrate(float64(kbps)*hwABRHeadroom))
	}
	return kbps
}

func validateSizeRequest(r ffmpeg.Runner, req ffmpeg.Request, targetBytes int64) error {
	if r == nil || strings.TrimSpace(req.Source) == "" || strings.TrimSpace(req.Output) == "" {
		return fmt.Errorf("runner, source and output are required")
	}
	if targetBytes <= 0 || req.Media.Duration <= 0 || math.IsNaN(req.Media.Duration) || math.IsInf(req.Media.Duration, 0) {
		return fmt.Errorf("target bytes and finite source duration must be positive")
	}
	return output.RejectAlias(req.Source, req.Output)
}

func cleanupSizeOutput(path string, out *Outcome, err *error) {
	if *err != nil {
		out.File = ""
	}
	if out.File == "" {
		if removeErr := os.Remove(path); removeErr != nil && !os.IsNotExist(removeErr) {
			*err = errors.Join(*err, fmt.Errorf("remove unfinished output: %w", removeErr))
		}
	}
}

func encodeSize(ctx context.Context, r ffmpeg.Runner, req ffmpeg.Request, targetBytes int64, limit int, allowFallback bool, out Outcome, on func(console.Event)) (Outcome, error) {
	for out.FullEncodes < limit {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		out.FullEncodes++
		media, err := ffmpeg.Encode(ctx, r, req, on)
		if err != nil {
			if allowFallback && ctx.Err() == nil && canFallbackHW(req) && out.FullEncodes < limit {
				if fallbackErr := useSoftware(ctx, r, &req); fallbackErr == nil {
					out.Settings.HW = "off"
					out.Changed = append(out.Changed, "hw")
					continue
				}
			}
			return out, err
		}
		out.Media, out.ActualBytes = media, media.Bytes
		if media.Bytes <= targetBytes {
			out.File = req.Output
			return out, nil
		}
		req.VideoKbps = max(1, positiveBitrate(float64(req.VideoKbps)*float64(targetBytes)/float64(media.Bytes)*retryScaleSafety))
	}
	out.Oversize = true
	out.Infeasible = fmt.Sprintf("Output is %.2f MB after retry; target is %.2f MB", float64(out.ActualBytes)/1e6, float64(targetBytes)/1e6)
	return out, nil
}
