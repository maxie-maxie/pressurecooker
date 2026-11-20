package target

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/maxie-maxie/pressurecooker/internal/config"
	"github.com/maxie-maxie/pressurecooker/internal/console"
	"github.com/maxie-maxie/pressurecooker/internal/ffmpeg"
)

func ForceFast(ctx context.Context, r ffmpeg.Runner, req ffmpeg.Request, targetBytes int64, metric string, on func(console.Event)) (out Outcome, err error) {
	if err = validateSizeRequest(r, req, targetBytes); err != nil {
		return out, err
	}
	defer cleanupSizeOutput(req.Output, &out, &err)
	if req.Media.Width <= 0 || req.Media.Height <= 0 || req.Media.FPS <= 0 || math.IsNaN(req.Media.FPS) || math.IsInf(req.Media.FPS, 0) {
		return out, fmt.Errorf("source video dimensions and finite FPS must be positive")
	}
	work := cloneRequest(req)
	work.RateControl, work.SinglePass = "abr", false
	work.StartSeconds, work.LengthSeconds, work.Pass, work.PassLog = 0, 0, 0, ""
	work.Settings.CRF = nil
	if err = selectForceFormats(ctx, r, &work); err != nil {
		return out, err
	}
	if isExplicit(work.Origins, "audio") {
		work.Settings.AudioSource = false
	}
	emitFieldSummary(on, work)
	scorer, err := detectFastMetric(ctx, r, work.FFmpeg, metric)
	if err != nil {
		return out, err
	}
	out.Metric, out.MetricVersion = scorer.metric, scorer.version
	resRungs, fpsRungs, err := fastRungs(work)
	if err != nil {
		return out, err
	}
	fit := checkFit(work.Media, work.Settings, targetBytes, work.Origins, nil)
	if work.Settings.AudioCodec == "flac" && work.Media.HasAudio && !fit.AudioCopy {
		var audioBytes int64
		audioBytes, err = exactAudioBytes(ctx, r, work)
		if err != nil {
			return out, err
		}
		fit = checkFit(work.Media, work.Settings, targetBytes, work.Origins, &audioBytes)
	}
	band := bandForCodec(work.Settings.Codec)
	if work.Media.HasAudio && work.Settings.AudioCodec != "flac" && !isExplicit(work.Origins, "audio") {
		smallestRes, smallestFPS := resRungs[len(resRungs)-1], fpsRungs[len(fpsRungs)-1]
		for _, audio := range standardAudioLadder {
			if fit.Fits && computeBPP(fit.VideoKbps, smallestRes.width, smallestRes.height, smallestFPS.fps) >= band.lo {
				break
			}
			if audio >= fit.AudioKbps {
				continue
			}
			settings := config.Clone(work.Settings)
			settings.AudioSource, settings.AudioKbps = false, audio
			fit = checkFit(work.Media, settings, targetBytes, work.Origins, nil)
		}
	}
	out.Settings = config.Clone(work.Settings)
	out.Settings.AudioKbps, out.Settings.AudioSource = fit.AudioKbps, fit.AudioCopy
	if !fit.Fits {
		out.Infeasible = fit.Reason
		return out, nil
	}
	work.Settings = config.Clone(out.Settings)
	candidates := selectABRCandidates(resRungs, fpsRungs, fit.AudioKbps, fit.VideoKbps, band, maxABRCandidates)
	autoHW := !isExplicit(work.Origins, "hw") && work.Settings.HW == "auto"
	var gpu ffmpeg.Encoder
	gpuHW := ""
	if autoHW {
		if err = useSoftware(ctx, r, &work); err != nil {
			return out, err
		}
		if len(config.CodecCaps(work.Settings.Codec).HW) > 0 {
			avail, probeErr := ffmpeg.ProbeHardware(ctx, r, work.FFmpeg, work.Settings.Codec)
			if probeErr != nil {
				return out, probeErr
			}
			for _, backend := range config.CodecCaps(work.Settings.Codec).HW {
				if reason, ok := avail[backend]; !ok || reason != "" {
					continue
				}
				selection := config.Clone(work.Settings)
				selection.HW = backend
				gpu, err = ffmpeg.ChooseEncoder(ctx, r, work.FFmpeg, selection)
				if ctx.Err() != nil {
					return out, ctx.Err()
				}
				if err == nil {
					gpuHW = backend
					break
				}
			}
		}
	} else if work.Encoder.Name == "" || work.Settings.HW == "off" || work.Settings.HW == "" {
		selection := config.Clone(work.Settings)
		if selection.HW == "" {
			selection.HW, work.Settings.HW = "off", "off"
		}
		work.Encoder, err = ffmpeg.ChooseEncoder(ctx, r, work.FFmpeg, selection)
		if err != nil {
			return out, err
		}
	}
	if gpuHW != "" {
		candidates = candidates[:min(len(candidates), maxABRCandidates-1)]
		top := selectABRCandidates(resRungs[:1], fpsRungs[:1], fit.AudioKbps, fit.VideoKbps, band, 1)[0]
		candidates = append(candidates, top)
	}
	tmpDir, err := os.MkdirTemp("", "pressurecooker-ft-*")
	if err != nil {
		return out, fmt.Errorf("create force-target temp directory: %w", err)
	}
	defer func() {
		if removeErr := os.RemoveAll(tmpDir); removeErr != nil {
			err = errors.Join(err, fmt.Errorf("remove force-target samples: %w", removeErr))
		}
	}()
	windows := fastReferenceWindows(work.Media.Duration)
	reference := filepath.Join(tmpDir, "reference.mkv")
	if err = ffmpeg.ExtractReference(ctx, r, work.FFmpeg, work.Source, windows, reference); err != nil {
		return out, err
	}
	referenceMedia := work.Media
	referenceMedia.HasAudio, referenceMedia.AudioKbps = false, 0
	if len(windows) > 0 {
		referenceMedia.Duration = 0
		for _, win := range windows {
			referenceMedia.Duration += win.Length
		}
	}
	scored := make([]scoredCandidate, 0, len(candidates))
	for i, cand := range candidates {
		emitTrial(on, i+1, len(candidates))
		sample := cloneRequest(work)
		sample.Source = reference
		sample.Media = referenceMedia
		sample.Settings = applyCandidate(work.Settings, cand, work.Media)
		if gpuHW != "" && i == len(candidates)-1 {
			sample.Encoder, sample.Settings.HW = gpu, gpuHW
		}
		sample.SinglePass = true
		sample.VideoKbps = sizeVideoKbps(sample.Encoder, cand.videoKbps)
		sample.Output = filepath.Join(tmpDir, fmt.Sprintf("sample-%d.%s", i+1, containerExt(work.Settings.Container)))
		out.SampleEncodes++
		if _, err = ffmpeg.Encode(ctx, r, sample, trialEvent(on, i+1, len(candidates))); err != nil {
			if ctx.Err() == nil && gpuHW != "" && sample.Encoder.Hardware {
				continue
			}
			return out, err
		}
		frames, scoreErr := scorer.scoreSample(ctx, r, work.FFmpeg, sample.Output, reference, referenceMedia, sampleWindow{}, tmpDir, i+1)
		if scoreErr != nil {
			return out, scoreErr
		}
		mean, p05 := summarizeFrames(frames)
		scored = append(scored, scoredCandidate{cand: cand, mean: mean, p05: p05, count: len(frames), encoder: sample.Encoder, hw: sample.Settings.HW})
	}
	winner := rankEncoderCandidates(scored, scorer.metric)
	out.Settings = applyCandidate(work.Settings, winner.cand, work.Media)
	out.Settings.HW = winner.hw
	out.Changed = changedFields(req.Settings, out.Settings, work.Media, req.Settings.HW != work.Settings.HW)
	if req.Settings.AudioSource != out.Settings.AudioSource && !slices.Contains(out.Changed, "audio") {
		out.Changed = append(out.Changed, "audio")
	}
	work.Settings = config.Clone(out.Settings)
	work.Encoder = winner.encoder
	work.VideoKbps = sizeVideoKbps(work.Encoder, winner.cand.videoKbps)
	return encodeSize(ctx, r, work, targetBytes, maxFullEncodes, autoHW, out, on)
}

func selectForceFormats(ctx context.Context, r ffmpeg.Runner, work *ffmpeg.Request) error {
	if !isExplicit(work.Origins, "codec") {
		bin := work.FFmpeg
		if strings.TrimSpace(bin) == "" {
			bin = "ffmpeg"
		}
		res, _ := r.Run(ctx, []string{bin, "-hide_banner", "-encoders"}, nil)
		if err := ctx.Err(); err != nil {
			return err
		}
		available := map[string]bool{}
		for _, line := range strings.Split(res.Stdout, "\n") {
			fields := strings.Fields(line)
			if len(fields) >= 2 && len(fields[0]) == 6 && fields[0][0] == 'V' && fields[1] != "=" {
				available[fields[1]] = true
			}
		}
		chosen := ""
		for _, codec := range []string{"av1", "hevc", "vp9", "h264", "theora"} {
			if codec == "theora" && work.Settings.Container != "ogg" {
				continue
			}
			if isExplicit(work.Origins, "container") && !config.Accepts(work.Settings.Container, codec, "") {
				continue
			}
			settings := config.Clone(work.Settings)
			settings.Codec, settings.HW = codec, "off"
			if _, err := config.ResolvePreset(settings.Preset, codec, false); err != nil && !isExplicit(work.Origins, "preset") {
				settings.Preset = forcePreset(work.Settings)
			}
			enc, err := ffmpeg.ChooseEncoder(ctx, r, work.FFmpeg, settings)
			if err != nil || len(available) > 0 && !available[enc.Name] {
				continue
			}
			chosen = codec
			work.Settings.Codec, work.Settings.Preset = codec, settings.Preset
			work.Encoder = ffmpeg.Encoder{}
			break
		}
		if chosen == "" {
			return fmt.Errorf("no size-mode video encoder is available for %s", work.Settings.Container)
		}
	}
	if !isExplicit(work.Origins, "container") {
		work.Settings.Container = config.Recommended(work.Settings.Codec)
	}
	if !config.Accepts(work.Settings.Container, work.Settings.Codec, "") {
		return fmt.Errorf("%s does not accept %s", work.Settings.Container, work.Settings.Codec)
	}
	if work.Media.HasAudio && !isExplicit(work.Origins, "audio_codec") {
		for _, codec := range []string{"opus", "aac", "mp3", "ac3"} {
			if config.Accepts(work.Settings.Container, "", codec) {
				work.Settings.AudioCodec = codec
				break
			}
		}
	}
	if work.Media.HasAudio && !config.Accepts(work.Settings.Container, "", work.Settings.AudioCodec) {
		return fmt.Errorf("%s does not accept %s audio", work.Settings.Container, work.Settings.AudioCodec)
	}
	return nil
}

func forcePreset(settings config.Settings) string {
	for _, name := range []string{"ultrafast", "superfast", "veryfast", "faster", "fast", "medium", "slow", "slower", "veryslow", "placebo"} {
		if resolved, err := config.ResolvePreset(name, settings.Codec, false); err == nil && resolved == settings.Preset {
			return name
		}
	}
	return "ultrafast"
}

func fastReferenceWindows(duration float64) []ffmpeg.Window {
	if duration <= shortClipMaxSeconds {
		return nil
	}
	count := 2
	if duration > 180 {
		count = 3
	}
	windows := make([]ffmpeg.Window, count)
	for i, win := range evenlySpacedWindows(duration, count, sampleWindowSeconds) {
		windows[i] = ffmpeg.Window{Start: win.start, Length: win.length}
	}
	return windows
}

func fastRungs(req ffmpeg.Request) ([]resRung, []fpsRung, error) {
	capReq := cloneRequest(req)
	if strings.TrimSpace(req.Settings.Resolution) != "" {
		_, _, err := config.ParseResolution(req.Settings.Resolution)
		if err != nil {
			return nil, nil, err
		}
		capReq.Media.Width, capReq.Media.Height = effectiveDims(req.Settings.Resolution, req.Media.Width, req.Media.Height)
	}
	res, err := buildResolutionRungs(capReq)
	if err != nil {
		return nil, nil, err
	}
	res[0].setting = req.Settings.Resolution
	fps := req.Media.FPS
	if req.Settings.FPS != nil {
		if *req.Settings.FPS <= 0 || math.IsNaN(*req.Settings.FPS) || math.IsInf(*req.Settings.FPS, 0) || *req.Settings.FPS > 1000 {
			return nil, nil, fmt.Errorf("FPS cap must be a finite number greater than 0 and at most 1000")
		}
		fps = math.Min(fps, *req.Settings.FPS)
	}
	fpsRungs := []fpsRung{{setting: req.Settings.FPS, fps: fps}}
	if !isExplicit(req.Origins, "fps") {
		for _, rate := range []float64{30, 24} {
			if rate < fps {
				v := rate
				fpsRungs = append(fpsRungs, fpsRung{setting: &v, fps: rate})
			}
		}
	}
	return res, fpsRungs, nil
}
