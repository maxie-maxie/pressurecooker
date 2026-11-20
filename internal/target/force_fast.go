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
	selection := config.Clone(work.Settings)
	selection.CRF = nil
	work.Settings.CRF = nil
	if isExplicit(work.Origins, "audio") {
		work.Settings.AudioSource = false
	}
	if work.Encoder.Name == "" {
		work.Encoder, err = ffmpeg.ChooseEncoder(ctx, r, work.FFmpeg, selection)
		if err != nil {
			return out, err
		}
	}
	scorer, err := detectFastMetric(ctx, r, work.FFmpeg, metric)
	if err != nil {
		return out, err
	}
	out.Metric, out.MetricVersion = scorer.metric, scorer.version
	resRungs, fpsRungs, err := fastRungs(work)
	if err != nil {
		return out, err
	}
	fit := CheckFit(work.Media, work.Settings, targetBytes)
	band := bandForCodec(work.Settings.Codec)
	if work.Media.HasAudio && !isExplicit(work.Origins, "audio") {
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
			fit = CheckFit(work.Media, settings, targetBytes)
		}
	}
	out.Settings = config.Clone(req.Settings)
	out.Settings.AudioKbps, out.Settings.AudioSource = fit.AudioKbps, fit.AudioCopy
	if !fit.Fits {
		out.Infeasible = fit.Reason
		return out, nil
	}
	work.Settings = config.Clone(out.Settings)
	emitFieldSummary(on, work)
	candidates := selectABRCandidates(resRungs, fpsRungs, fit.AudioKbps, fit.VideoKbps, band, maxABRCandidates)
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
		sample.SinglePass = true
		sample.VideoKbps = sizeVideoKbps(work.Encoder, cand.videoKbps)
		sample.Output = filepath.Join(tmpDir, fmt.Sprintf("sample-%d.%s", i+1, containerExt(work.Settings.Container)))
		out.SampleEncodes++
		if _, err = ffmpeg.Encode(ctx, r, sample, trialEvent(on, i+1, len(candidates))); err != nil {
			if ctx.Err() == nil && canFallbackHW(work) && i+1 < len(candidates) {
				if fallbackErr := useSoftware(ctx, r, &work); fallbackErr == nil {
					scored = scored[:0]
					continue
				}
			}
			return out, err
		}
		frames, scoreErr := scorer.scoreSample(ctx, r, work.FFmpeg, sample.Output, reference, referenceMedia, sampleWindow{}, tmpDir, i+1)
		if scoreErr != nil {
			return out, scoreErr
		}
		mean, p05 := summarizeFrames(frames)
		scored = append(scored, scoredCandidate{cand: cand, mean: mean, p05: p05, count: len(frames)})
	}
	winner := rankCandidates(scored).cand
	out.Settings = applyCandidate(work.Settings, winner, work.Media)
	out.Changed = changedFields(req.Settings, out.Settings, work.Media, req.Settings.HW != work.Settings.HW)
	if req.Settings.AudioSource != out.Settings.AudioSource && !slices.Contains(out.Changed, "audio") {
		out.Changed = append(out.Changed, "audio")
	}
	work.Settings = config.Clone(out.Settings)
	work.VideoKbps = sizeVideoKbps(work.Encoder, winner.videoKbps)
	return encodeSize(ctx, r, work, targetBytes, maxFullEncodes, true, out, on)
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
