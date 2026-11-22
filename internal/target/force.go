package target

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/maxie-maxie/pressurecooker/internal/config"
	"github.com/maxie-maxie/pressurecooker/internal/console"
	"github.com/maxie-maxie/pressurecooker/internal/ffmpeg"
)

const (
	maxFullEncodes   = 3
	maxABRCandidates = 3
	hwABRHeadroom    = 0.96
	retryScaleSafety = 0.98
	minDimension     = 64
)

type Outcome struct {
	Settings                                config.Settings
	Changed                                 []string
	Metric, MetricVersion, File, Infeasible string
	Media                                   ffmpeg.Media
	SampleEncodes, FullEncodes              int
	ActualBytes                             int64
	Oversize                                bool
}

type bppBand struct {
	lo, hi float64
}

var codecBPPBands = map[string]bppBand{
	"h264":   {lo: 0.08, hi: 0.18},
	"hevc":   {lo: 0.05, hi: 0.12},
	"av1":    {lo: 0.04, hi: 0.10},
	"vp9":    {lo: 0.05, hi: 0.12},
	"theora": {lo: 0.10, hi: 0.24},
}

var standardShortSides = []int{2160, 1440, 1080, 720, 480, 360, 240, 180, 144}
var standardAudioLadder = []int{128, 96, 64, 48}

type resRung struct {
	setting string
	width   int
	height  int
}

type fpsRung struct {
	setting *float64
	fps     float64
}

type candidate struct {
	res       resRung
	fpsR      fpsRung
	width     int
	height    int
	fps       float64
	audioKbps int
	videoKbps int
	bpp       float64
}

func useSoftware(ctx context.Context, r ffmpeg.Runner, req *ffmpeg.Request) error {
	settings := config.Clone(req.Settings)
	settings.HW = "off"
	enc, err := ffmpeg.ChooseEncoder(ctx, r, req.FFmpeg, settings)
	if err != nil {
		return err
	}
	req.Encoder, req.Settings.HW = enc, "off"
	return nil
}

func buildResolutionRungs(req ffmpeg.Request) ([]resRung, error) {
	srcW, srcH := req.Media.Width, req.Media.Height
	if isExplicit(req.Origins, "resolution") {
		if strings.TrimSpace(req.Settings.Resolution) != "" {
			_, _, err := config.ParseResolution(req.Settings.Resolution)
			if err != nil {
				return nil, err
			}
		}
		w, h := effectiveDims(req.Settings.Resolution, srcW, srcH)
		return []resRung{{setting: req.Settings.Resolution, width: w, height: h}}, nil
	}

	rungs := []resRung{{setting: "", width: srcW, height: srcH}}
	seen := map[[2]int]bool{{srcW, srcH}: true}
	shortSide := min(srcW, srcH)

	for _, targetShort := range standardShortSides {
		if targetShort >= shortSide {
			continue
		}
		var w, h int
		if srcW >= srcH {
			h = targetShort
			w = roundEven(float64(srcW) * float64(targetShort) / float64(srcH))
		} else {
			w = targetShort
			h = roundEven(float64(srcH) * float64(targetShort) / float64(srcW))
		}
		if w > srcW {
			w = srcW - (srcW % 2)
		}
		if h > srcH {
			h = srcH - (srcH % 2)
		}
		if w < minDimension || h < minDimension || (w == srcW && h == srcH) {
			continue
		}
		key := [2]int{w, h}
		if seen[key] {
			continue
		}
		seen[key] = true
		rungs = append(rungs, resRung{
			setting: fmt.Sprintf("%dx%d", w, h),
			width:   w,
			height:  h,
		})
	}
	return rungs, nil
}

func computeBPP(videoKbps, width, height int, fps float64) float64 {
	denom := float64(width) * float64(height) * fps
	if denom <= 0 || videoKbps <= 0 {
		return 0
	}
	return (float64(videoKbps) * 1000.0) / denom
}

func bandForCodec(codec string) bppBand {
	if b, ok := codecBPPBands[strings.ToLower(strings.TrimSpace(codec))]; ok {
		return b
	}
	return codecBPPBands["hevc"]
}

func bppDistance(bpp float64, band bppBand) float64 {
	if bpp <= 0 {
		return math.Inf(1)
	}
	if bpp < band.lo {
		return math.Log(band.lo / bpp)
	}
	if bpp > band.hi {
		return math.Log(bpp / band.hi)
	}
	return 0
}

func selectABRCandidates(
	resRungs []resRung,
	fpsRungs []fpsRung,
	audioKbps, videoKbps int,
	band bppBand,
	limit int,
) []candidate {
	all := make([]candidate, 0, len(resRungs)*len(fpsRungs))
	for _, rr := range resRungs {
		for _, fr := range fpsRungs {
			all = append(all, candidate{
				res:       rr,
				fpsR:      fr,
				width:     rr.width,
				height:    rr.height,
				fps:       fr.fps,
				audioKbps: audioKbps,
				videoKbps: videoKbps,
				bpp:       computeBPP(videoKbps, rr.width, rr.height, fr.fps),
			})
		}
	}
	sort.SliceStable(all, func(i, j int) bool {
		di := bppDistance(all[i].bpp, band)
		dj := bppDistance(all[j].bpp, band)
		if math.Abs(di-dj) > 1e-6 {
			return di < dj
		}
		// Prefer sharper/smoother rungs among equally well-matched candidates.
		if all[i].width*all[i].height != all[j].width*all[j].height {
			return all[i].width*all[i].height > all[j].width*all[j].height
		}
		return all[i].fps > all[j].fps
	})
	if limit > 0 && len(all) > limit {
		return all[:limit]
	}
	return all
}

func applyCandidate(orig config.Settings, cand candidate, media ffmpeg.Media) config.Settings {
	out := config.Clone(orig)
	if !sameResolution(orig.Resolution, cand.res.setting, media.Width, media.Height) {
		out.Resolution = cand.res.setting
	}
	if !sameFPS(orig.FPS, cand.fpsR.setting, media.FPS) {
		if cand.fpsR.setting != nil {
			v := *cand.fpsR.setting
			out.FPS = &v
		} else {
			out.FPS = nil
		}
	}
	if media.HasAudio {
		out.AudioKbps = cand.audioKbps
	}
	return out
}

func changedFields(orig, chosen config.Settings, media ffmpeg.Media, hwFallback bool) []string {
	var changed []string
	if orig.Codec != chosen.Codec {
		changed = append(changed, "codec")
	}
	if orig.Container != chosen.Container {
		changed = append(changed, "container")
	}
	if media.HasAudio && orig.AudioCodec != chosen.AudioCodec {
		changed = append(changed, "audio_codec")
	}
	if orig.Preset != chosen.Preset {
		changed = append(changed, "preset")
	}
	if !sameResolution(orig.Resolution, chosen.Resolution, media.Width, media.Height) {
		changed = append(changed, "resolution")
	}
	if !sameFPS(orig.FPS, chosen.FPS, media.FPS) {
		changed = append(changed, "fps")
	}
	if media.HasAudio && orig.AudioKbps != chosen.AudioKbps {
		changed = append(changed, "audio")
	}
	if hwFallback || !strings.EqualFold(orig.HW, chosen.HW) {
		changed = append(changed, "hw")
	}
	return changed
}

func sameResolution(a, b string, srcW, srcH int) bool {
	aw, ah := effectiveDims(a, srcW, srcH)
	bw, bh := effectiveDims(b, srcW, srcH)
	return aw == bw && ah == bh
}

func effectiveDims(res string, srcW, srcH int) (int, int) {
	if strings.TrimSpace(res) == "" {
		return srcW, srcH
	}
	w, h, err := config.ParseResolution(res)
	if err != nil || srcW <= 0 || srcH <= 0 {
		return srcW, srcH
	}
	ratio := math.Min(1, math.Min(float64(w)/float64(srcW), float64(h)/float64(srcH)))
	if ratio == 1 {
		return srcW, srcH
	}
	return min(w, roundEven(float64(srcW)*ratio)), min(h, roundEven(float64(srcH)*ratio))
}

func sameFPS(a, b *float64, srcFPS float64) bool {
	af := srcFPS
	if a != nil {
		af = *a
	}
	bf := srcFPS
	if b != nil {
		bf = *b
	}
	return math.Abs(af-bf) < 0.05
}

func emitFieldSummary(onEvent func(console.Event), req ffmpeg.Request) {
	if onEvent == nil {
		return
	}
	fixed := []string{"enc-speed"}
	var adjustable []string
	for _, field := range []string{"codec", "container"} {
		if isExplicit(req.Origins, field) {
			fixed = append(fixed, field)
		} else {
			adjustable = append(adjustable, field)
		}
	}

	if isExplicit(req.Origins, "resolution") {
		fixed = append(fixed, "resolution")
	} else {
		adjustable = append(adjustable, "resolution")
	}
	if isExplicit(req.Origins, "fps") {
		fixed = append(fixed, "fps")
	} else {
		adjustable = append(adjustable, "fps")
	}
	if req.Media.HasAudio {
		if isExplicit(req.Origins, "audio_codec") {
			fixed = append(fixed, "audio_codec")
		} else {
			adjustable = append(adjustable, "audio_codec")
		}
		if isExplicit(req.Origins, "audio") {
			fixed = append(fixed, "audio")
		} else {
			adjustable = append(adjustable, "audio")
		}
	}
	if isExplicit(req.Origins, "crf") {
		fixed = append(fixed, "crf")
	} else if isExplicit(req.Origins, "quality") {
		fixed = append(fixed, "quality")
	} else {
		adjustable = append(adjustable, "rate-control")
	}
	if isExplicit(req.Origins, "hw") || (strings.ToLower(strings.TrimSpace(req.Settings.HW)) != "" && strings.ToLower(strings.TrimSpace(req.Settings.HW)) != "auto") {
		fixed = append(fixed, "hw")
	} else {
		adjustable = append(adjustable, "hw")
	}
	adjText := "none"
	if len(adjustable) > 0 {
		adjText = strings.Join(adjustable, ", ")
	}
	onEvent(console.Event{
		Stage: fmt.Sprintf("Force-target fixed: %s | adjustable: %s", strings.Join(fixed, ", "), adjText),
	})
}

func emitTrial(onEvent func(console.Event), trial, trials int) {
	if onEvent == nil {
		return
	}
	onEvent(console.Event{
		Stage:  fmt.Sprintf("trial %d/%d", trial, trials),
		Trial:  trial,
		Trials: trials,
	})
}

func trialEvent(onEvent func(console.Event), trial, trials int) func(console.Event) {
	if onEvent == nil {
		return nil
	}
	return func(ev console.Event) {
		ev.Trial = trial
		ev.Trials = trials
		// Never invent an overall search percentage during candidate trials.
		ev.Timeline = 0
		ev.Duration = 0
		ev.Speed = 0
		ev.End = false
		onEvent(ev)
	}
}

func isExplicit(origins config.Origins, key string) bool {
	if origins == nil {
		return false
	}
	origin := strings.ToLower(strings.TrimSpace(string(origins[key])))
	return origin == "explicit" || key == "container" && (origin == "extension" || origin == "locked")
}

func roundEven(v float64) int {
	n := int(math.Round(v/2.0)) * 2
	if n < 2 {
		return 2
	}
	return n
}
