package target

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/maxie-maxie/pressurecooker/internal/ffmpeg"
)

const (
	metricVMAF         = "vmaf"
	metricVMAFVersion  = "vmaf_v0.6.1"
	metricSSIM         = "ssim"
	metricSSIMVersion  = "ffmpeg-ssim"
	metricXPSNR        = "xpsnr"
	metricXPSNRVersion = "ffmpeg-xpsnr"

	scoreTieMargin = 1.0
	p05GuardMargin = 8.0
	p05GuardWeight = 0.5
)

var (
	filterLibVMAFRe = regexp.MustCompile(`(?m)^\s*\S+\s+libvmaf\b`)
	filterXPSNRRe   = regexp.MustCompile(`(?m)^\s*\S+\s+xpsnr\b`)
	xpsnrFrameRe    = regexp.MustCompile(`(?i)XPSNR\s+y:\s*([0-9]+(?:\.[0-9]+)?|inf)(?:\s+u:\s*([0-9]+(?:\.[0-9]+)?|inf)\s+v:\s*([0-9]+(?:\.[0-9]+)?|inf))?`)
	ssimAllRe       = regexp.MustCompile(`All:([0-9]+(?:\.[0-9]+)?)`)
	vmafStderrRe    = regexp.MustCompile(`(?i)VMAF score:\s*([0-9]+(?:\.[0-9]+)?)`)
)

type metricScorer struct {
	metric  string
	version string
}

type scoredCandidate struct {
	cand    candidate
	mean    float64
	p05     float64
	count   int
	encoder ffmpeg.Encoder
	hw      string
}

func rankEncoderCandidates(scored []scoredCandidate, metric string) scoredCandidate {
	var software []scoredCandidate
	var gpu *scoredCandidate
	bestSWScore := math.Inf(-1)
	for i := range scored {
		sc := &scored[i]
		if sc.encoder.Hardware {
			gpu = sc
		} else {
			software = append(software, *sc)
			bestSWScore = math.Max(bestSWScore, guardedScore(sc.mean, sc.p05))
		}
	}
	if len(software) == 0 {
		return rankCandidates(scored)
	}
	threshold := 0.5
	if metric == metricVMAF {
		threshold = 1.0
	}
	if gpu != nil && guardedScore(gpu.mean, gpu.p05) >= bestSWScore-threshold-1e-9 {
		return *gpu
	}
	return rankCandidates(software)
}

// detectMetric probes FFmpeg filters once per run and selects VMAF when libvmaf is available, else SSIM.
func detectMetric(ctx context.Context, r ffmpeg.Runner, ffmpegBin string) (metricScorer, error) {
	if strings.TrimSpace(ffmpegBin) == "" {
		ffmpegBin = "ffmpeg"
	}
	res, err := r.Run(ctx, []string{ffmpegBin, "-hide_banner", "-filters"}, nil)
	if ctx.Err() != nil {
		return metricScorer{}, ctx.Err()
	}
	if err == nil && res.ExitCode == 0 && filterLibVMAFRe.MatchString(res.Stdout) {
		return metricScorer{metric: metricVMAF, version: metricVMAFVersion}, nil
	}
	return metricScorer{metric: metricSSIM, version: metricSSIMVersion}, nil
}

func detectFastMetric(ctx context.Context, r ffmpeg.Runner, ffmpegBin, metric string) (metricScorer, error) {
	metric = strings.ToLower(strings.TrimSpace(metric))
	if metric != "" && metric != metricXPSNR && metric != metricVMAF {
		return metricScorer{}, fmt.Errorf("force-target metric must be xpsnr or vmaf")
	}
	if strings.TrimSpace(ffmpegBin) == "" {
		ffmpegBin = "ffmpeg"
	}
	res, err := r.Run(ctx, []string{ffmpegBin, "-hide_banner", "-filters"}, nil)
	if ctx.Err() != nil {
		return metricScorer{}, ctx.Err()
	}
	if err != nil || res.ExitCode != 0 {
		return metricScorer{}, fmt.Errorf("check FFmpeg metric filters (exit %d): %v", res.ExitCode, err)
	}
	if metric == metricVMAF {
		if !filterLibVMAFRe.MatchString(res.Stdout) {
			return metricScorer{}, fmt.Errorf("--vmaf needs FFmpeg built with libvmaf")
		}
		return metricScorer{metric: metricVMAF, version: metricVMAFVersion}, nil
	}
	if filterXPSNRRe.MatchString(res.Stdout) {
		return metricScorer{metric: metricXPSNR, version: metricXPSNRVersion}, nil
	}
	return metricScorer{metric: metricSSIM, version: metricSSIMVersion}, nil
}

// scoreSample scores a distorted sample against the matching source window at source display size (0-100 scale).
func (s metricScorer) scoreSample(
	ctx context.Context,
	r ffmpeg.Runner,
	ffmpegBin, samplePath, sourcePath string,
	media ffmpeg.Media,
	win sampleWindow,
	tmpDir string,
	id int,
) ([]float64, error) {
	if strings.TrimSpace(ffmpegBin) == "" {
		ffmpegBin = "ffmpeg"
	}

	var logPath, metricFilter string
	if s.metric == metricVMAF {
		logPath = filepath.Join(tmpDir, fmt.Sprintf("vmaf-%d.json", id))
		metricFilter = fmt.Sprintf(
			"[dist][ref]libvmaf=model=version=%s:log_fmt=json:log_path=%s:eof_action=endall:shortest=1",
			s.version, escapeFilterPath(logPath),
		)
	} else {
		logPath = filepath.Join(tmpDir, fmt.Sprintf("%s-%d.log", s.metric, id))
		metricFilter = fmt.Sprintf(
			"[dist][ref]%s=stats_file=%s:eof_action=endall:shortest=1",
			s.metric, escapeFilterPath(logPath),
		)
	}

	graph := buildScoreFilterGraph(media, metricFilter)
	args := []string{ffmpegBin, "-nostdin", "-hide_banner", "-loglevel", "error", "-i", samplePath}
	if win.length > 0 {
		args = append(args, "-ss", formatSec(win.start))
	}
	args = append(args, "-i", sourcePath)
	if win.length > 0 {
		args = append(args, "-t", formatSec(win.length))
	}
	args = append(args, "-lavfi", graph, "-f", "null", os.DevNull)

	res, err := r.Run(ctx, args, nil)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil || res.ExitCode != 0 {
		tail := ""
		if len(res.StderrTail) > 0 {
			tail = ": " + strings.Join(res.StderrTail, " | ")
		}
		return nil, fmt.Errorf("%s scoring failed (exit %d)%s", s.metric, res.ExitCode, tail)
	}

	if data, readErr := os.ReadFile(logPath); readErr == nil && len(data) > 0 {
		if s.metric == metricVMAF {
			if frames, parseErr := parseVMAFLog(data); parseErr == nil && len(frames) > 0 {
				return frames, nil
			}
		} else if s.metric == metricXPSNR {
			if frames, parseErr := parseXPSNRStats(data); parseErr == nil && len(frames) > 0 {
				return frames, nil
			}
		} else {
			if frames, parseErr := parseSSIMStats(data); parseErr == nil && len(frames) > 0 {
				return frames, nil
			}
		}
	}

	// Fallback for runners that emit summary scores on stderr/stdout without writing a log file.
	combined := strings.Join(res.StderrTail, "\n") + "\n" + res.Stdout
	if s.metric == metricVMAF {
		if frames, ok := parseVMAFFallback(combined); ok {
			return frames, nil
		}
	} else if s.metric == metricXPSNR {
		if frames, parseErr := parseXPSNRStats([]byte(combined)); parseErr == nil {
			return frames, nil
		}
	} else {
		if frames, parseErr := parseSSIMStats([]byte(combined)); parseErr == nil && len(frames) > 0 {
			return frames, nil
		}
	}
	return nil, fmt.Errorf("%s scoring produced no readable frame scores", s.metric)
}

func buildScoreFilterGraph(media ffmpeg.Media, metricFilter string) string {
	dist := []string{"setpts=PTS-STARTPTS"}
	ref := []string{"setpts=PTS-STARTPTS"}
	if media.FPS > 0 && !math.IsNaN(media.FPS) && !math.IsInf(media.FPS, 0) {
		fpsArg := "fps=" + formatSec(media.FPS)
		dist = append(dist, fpsArg)
		ref = append(ref, fpsArg)
	}
	if media.Width > 0 && media.Height > 0 {
		dist = append(dist, fmt.Sprintf("scale=%d:%d:flags=bicubic", media.Width, media.Height))
	}
	dist = append(dist, "setsar=1", "format=yuv420p")
	ref = append(ref, "setsar=1", "format=yuv420p")
	return fmt.Sprintf("[0:v]%s[dist];[1:v]%s[ref];%s",
		strings.Join(dist, ","), strings.Join(ref, ","), metricFilter)
}

type vmafJSONLog struct {
	Frames []struct {
		Metrics struct {
			VMAF *float64 `json:"vmaf"`
		} `json:"metrics"`
	} `json:"frames"`
	PooledMetrics struct {
		VMAF struct {
			Mean *float64 `json:"mean"`
		} `json:"vmaf"`
	} `json:"pooled_metrics"`
}

func parseVMAFLog(data []byte) ([]float64, error) {
	var parsed vmafJSONLog
	if err := json.Unmarshal(data, &parsed); err != nil {
		return nil, err
	}
	scores := make([]float64, 0, len(parsed.Frames))
	for _, f := range parsed.Frames {
		if f.Metrics.VMAF != nil && !math.IsNaN(*f.Metrics.VMAF) && !math.IsInf(*f.Metrics.VMAF, 0) {
			scores = append(scores, clampFloat(*f.Metrics.VMAF, 0, 100))
		}
	}
	if len(scores) == 0 && parsed.PooledMetrics.VMAF.Mean != nil {
		v := *parsed.PooledMetrics.VMAF.Mean
		if !math.IsNaN(v) && !math.IsInf(v, 0) {
			scores = append(scores, clampFloat(v, 0, 100))
		}
	}
	if len(scores) == 0 {
		return nil, fmt.Errorf("no vmaf scores in json log")
	}
	return scores, nil
}

func parseVMAFFallback(text string) ([]float64, bool) {
	matches := vmafStderrRe.FindAllStringSubmatch(text, -1)
	if len(matches) == 0 {
		return nil, false
	}
	scores := make([]float64, 0, len(matches))
	for _, m := range matches {
		if v, err := strconv.ParseFloat(m[1], 64); err == nil && !math.IsNaN(v) && !math.IsInf(v, 0) {
			scores = append(scores, clampFloat(v, 0, 100))
		}
	}
	return scores, len(scores) > 0
}

// parseSSIMStats extracts per-frame All: scores and scales them to 0-100 (SSIM * 100).
func parseSSIMStats(data []byte) ([]float64, error) {
	matches := ssimAllRe.FindAllStringSubmatch(string(data), -1)
	if len(matches) == 0 {
		return nil, fmt.Errorf("no ssim scores found")
	}
	scores := make([]float64, 0, len(matches))
	for _, m := range matches {
		v, err := strconv.ParseFloat(m[1], 64)
		if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
			continue
		}
		scores = append(scores, clampFloat(v*100.0, 0, 100))
	}
	if len(scores) == 0 {
		return nil, fmt.Errorf("no valid ssim scores found")
	}
	return scores, nil
}

func parseXPSNRStats(data []byte) ([]float64, error) {
	matches := xpsnrFrameRe.FindAllStringSubmatch(string(data), -1)
	scores := make([]float64, 0, len(matches))
	for _, match := range matches {
		score := 100.0
		for _, channel := range match[1:] {
			if channel == "" {
				continue
			}
			v, err := strconv.ParseFloat(channel, 64)
			if err != nil || math.IsNaN(v) {
				return nil, fmt.Errorf("invalid XPSNR score %q", channel)
			}
			score = math.Min(score, clampFloat(v, 0, 100))
		}
		scores = append(scores, score)
	}
	if len(scores) == 0 {
		return nil, fmt.Errorf("no xpsnr scores found")
	}
	return scores, nil
}

func summarizeFrames(frames []float64) (mean, p05 float64) {
	if len(frames) == 0 {
		return 0, 0
	}
	sorted := make([]float64, len(frames))
	var sum float64
	for i, v := range frames {
		sorted[i] = v
		sum += v
	}
	sort.Float64s(sorted)
	idx := int(math.Floor(0.05 * float64(len(sorted)-1)))
	if idx < 0 {
		idx = 0
	}
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sum / float64(len(frames)), sorted[idx]
}

func guardedScore(mean, p05 float64) float64 {
	dip := (mean - p05) - p05GuardMargin
	if dip > 0 {
		return mean - dip*p05GuardWeight
	}
	return mean
}

// rankCandidates selects the highest quality candidate guarded by 5th-percentile frame quality.
// Within 1.0 point on the 0-100 scale, higher FPS wins, then higher audio bitrate.
func rankCandidates(scored []scoredCandidate) scoredCandidate {
	bestGuarded := math.Inf(-1)
	for _, sc := range scored {
		g := guardedScore(sc.mean, sc.p05)
		if g > bestGuarded {
			bestGuarded = g
		}
	}
	var winner scoredCandidate
	hasWinner := false
	for _, sc := range scored {
		g := guardedScore(sc.mean, sc.p05)
		if bestGuarded-g > scoreTieMargin+1e-9 {
			continue
		}
		if !hasWinner || betterFinalist(sc, winner) {
			winner = sc
			hasWinner = true
		}
	}
	return winner
}

func betterFinalist(a, b scoredCandidate) bool {
	if math.Abs(a.cand.fps-b.cand.fps) > 0.05 {
		return a.cand.fps > b.cand.fps
	}
	if a.cand.audioKbps != b.cand.audioKbps {
		return a.cand.audioKbps > b.cand.audioKbps
	}
	ga, gb := guardedScore(a.mean, a.p05), guardedScore(b.mean, b.p05)
	if math.Abs(ga-gb) > 1e-6 {
		return ga > gb
	}
	if math.Abs(a.p05-b.p05) > 1e-6 {
		return a.p05 > b.p05
	}
	if math.Abs(a.mean-b.mean) > 1e-6 {
		return a.mean > b.mean
	}
	return a.cand.width*a.cand.height > b.cand.width*b.cand.height
}

func escapeFilterPath(path string) string {
	p := filepath.ToSlash(path)
	p = strings.ReplaceAll(p, ":", `\:`)
	p = strings.ReplaceAll(p, "'", `'\''`)
	return "'" + p + "'"
}

func formatSec(v float64) string {
	s := strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.3f", v), "0"), ".")
	if s == "" || s == "-0" {
		return "0"
	}
	return s
}
