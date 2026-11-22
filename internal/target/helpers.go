package target

import (
	"context"
	"math"
	"strconv"
	"strings"

	"github.com/maxie-maxie/pressurecooker/internal/config"
	"github.com/maxie-maxie/pressurecooker/internal/ffmpeg"
)

const (
	shortClipMaxSeconds = 12.0
	sampleWindowSeconds = 2.0
	defaultMuxMargin    = 0.97
)

type sampleWindow struct {
	start, length float64
}

func evenlySpacedWindows(duration float64, count int, winLen float64) []sampleWindow {
	if count <= 0 {
		return nil
	}
	if winLen > duration {
		winLen = duration
	}
	maxStart := math.Max(0, duration-winLen)
	windows := make([]sampleWindow, 0, count)
	for i := 0; i < count; i++ {
		center := duration * float64(i+1) / float64(count+1)
		start := center - winLen/2
		if start < 0 {
			start = 0
		}
		if start > maxStart {
			start = maxStart
		}
		windows = append(windows, sampleWindow{start: start, length: winLen})
	}
	return windows
}

func cloneRequest(req ffmpeg.Request) ffmpeg.Request {
	cp := req
	cp.Settings = config.Clone(req.Settings)
	if req.Origins != nil {
		cp.Origins = make(config.Origins, len(req.Origins))
		for k, v := range req.Origins {
			cp.Origins[k] = v
		}
	}
	if len(req.Encoder.Warnings) > 0 {
		cp.Encoder.Warnings = append([]string(nil), req.Encoder.Warnings...)
	}
	return cp
}

func containerExt(container string) string {
	switch strings.ToLower(strings.TrimSpace(container)) {
	case "mkv":
		return "mkv"
	case "webm":
		return "webm"
	default:
		return "mp4"
	}
}

func clampFloat(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func probePacketBytes(ctx context.Context, r ffmpeg.Runner, ffprobe, path string) (videoBytes, audioBytes int64, ok bool) {
	if r == nil || strings.TrimSpace(path) == "" {
		return 0, 0, false
	}
	if strings.TrimSpace(ffprobe) == "" {
		ffprobe = "ffprobe"
	}
	res, err := r.Run(ctx, []string{
		ffprobe, "-v", "error",
		"-show_entries", "packet=codec_type,size",
		"-of", "csv=p=0",
		path,
	}, nil)
	if err != nil || res.ExitCode != 0 || strings.TrimSpace(res.Stdout) == "" {
		return 0, 0, false
	}
	var parsed int
	for _, raw := range strings.Split(res.Stdout, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		parts := strings.Split(line, ",")
		if len(parts) < 2 {
			continue
		}
		kind := strings.ToLower(strings.TrimSpace(parts[0]))
		size, parseErr := strconv.ParseInt(strings.TrimSpace(parts[1]), 10, 64)
		if parseErr != nil || size < 0 {
			continue
		}
		switch kind {
		case "video":
			videoBytes += size
			parsed++
		case "audio":
			audioBytes += size
			parsed++
		}
	}
	return videoBytes, audioBytes, parsed > 0
}
