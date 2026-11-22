package target

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"

	"github.com/maxie-maxie/pressurecooker/internal/config"
	"github.com/maxie-maxie/pressurecooker/internal/ffmpeg"
)

type CalibrationPoint struct {
	Backend   string
	Codec     string
	Encoder   string
	Level     string
	Quantizer float64
	Metric    string
	Score     float64
	P05       float64
	Bytes     int64
}

type CalibrationMatch struct {
	Codec            string
	Backend          string
	Encoder          string
	Level            string
	RefQuantizer     float64
	RefScore         float64
	MatchedQuantizer float64
	MatchedScore     float64
	ScoreDiff        float64
	RefBytes         int64
	MatchedBytes     int64
}

type CalibrationReport struct {
	Metric        string
	MetricVersion string
	References    []CalibrationPoint
	Probes        []CalibrationPoint
	Matches       []CalibrationMatch
	Unavailable   map[string]string
}

var calibrationLevels = []string{"high", "balanced", "small"}

var calibrationSweeps = map[string][]float64{
	"h264": {18, 22, 26, 28, 30, 31, 34, 38, 42},
	"hevc": {20, 24, 28, 30, 32, 34, 36, 37, 38, 40, 44},
	"av1":  {24, 30, 36, 42, 48, 54},
}

// Calibrate measures software CRF reference quality at high/balanced/small and sweeps functional
// hardware encoders (Vulkan/QSV) across QP/ICQ values to propose closest-quality hardware mappings.
func Calibrate(ctx context.Context, r ffmpeg.Runner, ffmpegBin, ffprobeBin, clipPath string, codecs []string) (CalibrationReport, error) {
	if r == nil {
		return CalibrationReport{}, fmt.Errorf("runner is required")
	}
	if strings.TrimSpace(clipPath) == "" {
		return CalibrationReport{}, fmt.Errorf("reference clip path is required")
	}
	if strings.TrimSpace(ffmpegBin) == "" {
		ffmpegBin = "ffmpeg"
	}
	if strings.TrimSpace(ffprobeBin) == "" {
		ffprobeBin = "ffprobe"
	}
	if len(codecs) == 0 {
		codecs = []string{"h264", "hevc", "av1"}
	}

	media, err := ffmpeg.Probe(ctx, r, ffprobeBin, clipPath)
	if err != nil {
		return CalibrationReport{}, fmt.Errorf("probe calibration clip: %w", err)
	}
	scorer, err := detectMetric(ctx, r, ffmpegBin)
	if err != nil {
		return CalibrationReport{}, err
	}

	tmpDir, err := os.MkdirTemp("", "pressurecooker-cal-*")
	if err != nil {
		return CalibrationReport{}, fmt.Errorf("create calibration temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	report := CalibrationReport{
		Metric:        scorer.metric,
		MetricVersion: scorer.version,
		Unavailable:   make(map[string]string),
	}

	scoreID := 0
	fullWin := sampleWindow{start: 0, length: 0}

	for _, rawCodec := range codecs {
		codec := strings.ToLower(strings.TrimSpace(rawCodec))
		if codec == "" {
			continue
		}
		refsByLevel := make(map[string]CalibrationPoint, len(calibrationLevels))

		for _, level := range calibrationLevels {
			if err := ctx.Err(); err != nil {
				return CalibrationReport{}, err
			}
			s := config.Factory()
			s.Codec = codec
			s.Container = "mp4"
			s.Preset = "fast"
			s.Quality = level
			s.HW = "off"

			enc, err := ffmpeg.ChooseEncoder(ctx, r, ffmpegBin, s)
			if err != nil {
				report.Unavailable["software/"+codec] = err.Error()
				continue
			}
			outPath := filepath.Join(tmpDir, fmt.Sprintf("ref-%s-%s.mp4", codec, level))
			encMedia, err := ffmpeg.Encode(ctx, r, ffmpeg.Request{
				Source:      clipPath,
				Output:      outPath,
				FFmpeg:      ffmpegBin,
				FFprobe:     ffprobeBin,
				Settings:    s,
				Encoder:     enc,
				Media:       media,
				RateControl: "quality",
			}, nil)
			if err != nil {
				report.Unavailable["software/"+codec+"/"+level] = err.Error()
				continue
			}
			scoreID++
			frames, err := scorer.scoreSample(ctx, r, ffmpegBin, outPath, clipPath, media, fullWin, tmpDir, scoreID)
			if err != nil {
				return CalibrationReport{}, err
			}
			mean, p05 := summarizeFrames(frames)
			pt := CalibrationPoint{
				Backend:   "software",
				Codec:     codec,
				Encoder:   enc.Name,
				Level:     level,
				Quantizer: enc.Quantizer,
				Metric:    scorer.metric,
				Score:     mean,
				P05:       p05,
				Bytes:     encMedia.Bytes,
			}
			report.References = append(report.References, pt)
			refsByLevel[level] = pt
		}

		sweep := calibrationSweeps[codec]
		if len(sweep) == 0 {
			sweep = calibrationSweeps["hevc"]
		}

		for _, backend := range []string{"vulkan", "qsv"} {
			if err := ctx.Err(); err != nil {
				return CalibrationReport{}, err
			}
			s := config.Factory()
			s.Codec = codec
			s.Container = "mp4"
			s.Preset = "fast"
			s.Quality = "balanced"
			s.HW = backend

			enc, err := ffmpeg.ChooseEncoder(ctx, r, ffmpegBin, s)
			if err != nil {
				report.Unavailable[backend+"/"+codec] = err.Error()
				continue
			}

			var points []CalibrationPoint
			for _, q := range sweep {
				if err := ctx.Err(); err != nil {
					return CalibrationReport{}, err
				}
				probeEnc := enc
				probeEnc.Quantizer = q
				outPath := filepath.Join(tmpDir, fmt.Sprintf("hw-%s-%s-q%.0f.mp4", backend, codec, q))
				encMedia, encErr := ffmpeg.Encode(ctx, r, ffmpeg.Request{
					Source:      clipPath,
					Output:      outPath,
					FFmpeg:      ffmpegBin,
					FFprobe:     ffprobeBin,
					Settings:    s,
					Encoder:     probeEnc,
					Media:       media,
					RateControl: "quality",
				}, nil)
				if encErr != nil {
					report.Unavailable[backend+"/"+codec] = encErr.Error()
					points = nil
					break
				}
				scoreID++
				frames, scoreErr := scorer.scoreSample(ctx, r, ffmpegBin, outPath, clipPath, media, fullWin, tmpDir, scoreID)
				if scoreErr != nil {
					return CalibrationReport{}, scoreErr
				}
				mean, p05 := summarizeFrames(frames)
				pt := CalibrationPoint{
					Backend:   enc.Backend,
					Codec:     codec,
					Encoder:   enc.Name,
					Quantizer: q,
					Metric:    scorer.metric,
					Score:     mean,
					P05:       p05,
					Bytes:     encMedia.Bytes,
				}
				points = append(points, pt)
				report.Probes = append(report.Probes, pt)
			}

			if len(points) == 0 {
				continue
			}
			for _, level := range calibrationLevels {
				ref, ok := refsByLevel[level]
				if !ok {
					continue
				}
				best := points[0]
				bestDiff := math.Abs(points[0].Score - ref.Score)
				for _, pt := range points[1:] {
					if d := math.Abs(pt.Score - ref.Score); d < bestDiff {
						best = pt
						bestDiff = d
					}
				}
				report.Matches = append(report.Matches, CalibrationMatch{
					Codec:            codec,
					Backend:          best.Backend,
					Encoder:          best.Encoder,
					Level:            level,
					RefQuantizer:     ref.Quantizer,
					RefScore:         ref.Score,
					MatchedQuantizer: best.Quantizer,
					MatchedScore:     best.Score,
					ScoreDiff:        best.Score - ref.Score,
					RefBytes:         ref.Bytes,
					MatchedBytes:     best.Bytes,
				})
			}
		}
	}

	return report, nil
}
