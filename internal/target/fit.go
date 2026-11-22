package target

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"slices"
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
	return checkFit(m, s, targetBytes, nil, nil)
}

func checkFit(m ffmpeg.Media, s config.Settings, targetBytes int64, origins config.Origins, audioBytes *int64) Fit {
	fit := Fit{}
	if s.Codec != "" && !config.CodecCaps(s.Codec).SizeMode {
		fit.Reason = fmt.Sprintf("%s supports conversion only; size mode is unsupported", s.Codec)
		return fit
	}
	budget := float64(targetBytes) * 8 * defaultMuxMargin
	if targetBytes <= 0 || m.Duration <= 0 || math.IsNaN(m.Duration) || math.IsInf(m.Duration, 0) {
		fit.Reason = fmt.Sprintf("Duration is %g s; target is %g MB", m.Duration, float64(targetBytes)/1e6)
		return fit
	}
	audioKbps := 0.0
	if m.HasAudio {
		switch {
		case s.AudioCodec == "flac" && audioBytes != nil:
			audioKbps = float64(*audioBytes) * 8 / m.Duration / 1000
			fit.AudioCopy = s.AudioSource && strings.EqualFold(m.AudioCodec, "flac") && canCopyAudio(m, s, origins) && float64(*audioBytes)*8 <= 0.20*budget
			if fit.AudioCopy {
				fit.AudioKbps = int(math.Round(audioKbps))
			}
		case s.AudioSource && canCopyAudio(m, s, origins) && m.AudioKbps > 0 && m.AudioKbps*1000*m.Duration <= 0.20*budget:
			fit.AudioCopy = true
			fit.AudioKbps = int(math.Round(m.AudioKbps))
			audioKbps = m.AudioKbps
		case s.AudioCodec == "flac" && s.AudioKbps <= 0:
			fit.Reason = fmt.Sprintf("FLAC audio size is unknown for %g s; target is %.2f MB", m.Duration, float64(targetBytes)/1e6)
			return fit
		case !s.AudioSource || s.AudioCodec == "flac":
			fit.AudioKbps = s.AudioKbps
			audioKbps = float64(s.AudioKbps)
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
		if s.AudioCodec == "flac" && audioBytes != nil {
			fit.Reason = fmt.Sprintf("FLAC audio needs %.2f MB for %.0f:%02.0f; target is %.2f MB", float64(*audioBytes)/1e6, math.Floor(m.Duration/60), math.Floor(math.Mod(m.Duration, 60)), float64(targetBytes)/1e6)
		}
	}
	return fit
}

func FitEncode(ctx context.Context, r ffmpeg.Runner, req ffmpeg.Request, fit Fit, targetBytes int64, on func(console.Event)) (out Outcome, err error) {
	if err = validateSizeRequest(r, req, targetBytes); err != nil {
		return out, err
	}
	defer cleanupSizeOutput(req.Output, &out, &err)
	if fit.AudioCopy && !canCopyAudio(req.Media, req.Settings, req.Origins) {
		fit = checkFit(req.Media, req.Settings, targetBytes, req.Origins, nil)
	}
	if req.Settings.AudioCodec == "flac" && req.Media.HasAudio && (!fit.AudioCopy || strings.EqualFold(req.Media.AudioCodec, "flac") && req.Media.AudioKbps <= 0) {
		var audioBytes int64
		audioBytes, err = exactAudioBytes(ctx, r, req)
		if err != nil {
			return out, err
		}
		fit = checkFit(req.Media, req.Settings, targetBytes, req.Origins, &audioBytes)
	}
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

func canCopyAudio(m ffmpeg.Media, s config.Settings, origins config.Origins) bool {
	return m.AudioCodec != "" && config.Accepts(s.Container, "", strings.ToLower(m.AudioCodec)) &&
		(!isExplicit(origins, "audio_codec") || strings.EqualFold(m.AudioCodec, s.AudioCodec))
}

func exactAudioBytes(ctx context.Context, r ffmpeg.Runner, req ffmpeg.Request) (bytes int64, err error) {
	if err = ctx.Err(); err != nil {
		return 0, err
	}
	tmp, err := os.CreateTemp("", "pressurecooker-audio-*.flac")
	if err != nil {
		return 0, fmt.Errorf("create FLAC measurement file: %w", err)
	}
	path := tmp.Name()
	defer func() {
		if removeErr := os.Remove(path); removeErr != nil && !os.IsNotExist(removeErr) {
			err = errors.Join(err, fmt.Errorf("remove FLAC measurement: %w", removeErr))
		}
	}()
	if err = tmp.Close(); err != nil {
		return 0, err
	}
	bin := req.FFmpeg
	if strings.TrimSpace(bin) == "" {
		bin = "ffmpeg"
	}
	res, err := r.Run(ctx, []string{bin, "-nostdin", "-y", "-hide_banner", "-i", req.Source, "-map", "0:a:0", "-vn", "-c:a", "flac", path}, nil)
	if ctx.Err() != nil {
		return 0, ctx.Err()
	}
	if err != nil || res.ExitCode != 0 {
		return 0, fmt.Errorf("measure FLAC audio (exit %d): %v %s", res.ExitCode, err, strings.Join(res.StderrTail, " | "))
	}
	info, err := os.Stat(path)
	if err != nil {
		return 0, fmt.Errorf("stat FLAC measurement: %w", err)
	}
	if info.Size() <= 0 {
		return 0, fmt.Errorf("FLAC measurement produced no audio")
	}
	return info.Size(), nil
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
	if !config.CodecCaps(req.Settings.Codec).SizeMode {
		return fmt.Errorf("%s supports conversion only; size mode is unsupported", req.Settings.Codec)
	}
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
			if allowFallback && ctx.Err() == nil && req.Encoder.Hardware && !isExplicit(req.Origins, "hw") && out.FullEncodes < limit {
				if fallbackErr := useSoftware(ctx, r, &req); fallbackErr == nil {
					out.Settings.HW = "off"
					if !slices.Contains(out.Changed, "hw") {
						out.Changed = append(out.Changed, "hw")
					}
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
