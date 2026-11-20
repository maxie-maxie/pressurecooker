package ffmpeg

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Probe inspects duration, video, audio, container, and file size using ffprobe.
func Probe(ctx context.Context, r Runner, ffprobe, path string) (Media, error) {
	if r == nil {
		return Media{}, fmt.Errorf("runner is required")
	}
	if strings.TrimSpace(ffprobe) == "" {
		ffprobe = "ffprobe"
	}
	if strings.TrimSpace(path) == "" {
		return Media{}, fmt.Errorf("media path cannot be empty")
	}

	dur, err := probeDuration(ctx, r, ffprobe, path)
	if err != nil {
		return Media{}, err
	}
	vid, err := probeVideo(ctx, r, ffprobe, path)
	if err != nil {
		return Media{}, err
	}
	aud, err := probeAudio(ctx, r, ffprobe, path)
	if err != nil {
		return Media{}, err
	}

	media := Media{
		Duration:   dur,
		FPS:        vid.fps,
		Width:      vid.width,
		Height:     vid.height,
		VideoCodec: vid.codec,
		AudioCodec: aud.codec,
		Container:  vid.container,
		AudioKbps:  aud.kbps,
		HasAudio:   aud.hasAudio,
		Bytes:      vid.bytes,
	}
	if info, statErr := os.Stat(path); statErr == nil && !info.IsDir() {
		media.Bytes = info.Size()
	}
	return media, nil
}

func probeDuration(ctx context.Context, r Runner, ffprobe, path string) (float64, error) {
	attempts := [][]string{
		{ffprobe, "-v", "error", "-show_entries", "format=duration",
			"-of", "default=noprint_wrappers=1:nokey=1", path},
		{ffprobe, "-v", "error", "-select_streams", "v:0", "-show_entries", "stream=duration",
			"-of", "default=noprint_wrappers=1:nokey=1", path},
	}
	for _, cmd := range attempts {
		res, err := r.Run(ctx, cmd, nil)
		if ctx.Err() != nil {
			return 0, ctx.Err()
		}
		if err != nil || res.ExitCode != 0 || strings.TrimSpace(res.Stdout) == "" {
			continue
		}
		for _, line := range strings.Split(res.Stdout, "\n") {
			text := strings.TrimSpace(line)
			if text == "" {
				continue
			}
			v, parseErr := strconv.ParseFloat(text, 64)
			if parseErr == nil && !math.IsNaN(v) && !math.IsInf(v, 0) && v > 0 {
				return v, nil
			}
		}
	}
	return 0, fmt.Errorf("could not determine the duration of %q with ffprobe", path)
}

type videoProbeResult struct {
	width, height int
	fps           float64
	codec         string
	container     string
	bytes         int64
}

func probeVideo(ctx context.Context, r Runner, ffprobe, path string) (videoProbeResult, error) {
	cmd := []string{
		ffprobe, "-v", "error", "-select_streams", "v:0",
		"-show_entries", "stream=codec_name,width,height,avg_frame_rate,r_frame_rate:format=format_name,size",
		"-of", "json", path,
	}
	res, err := r.Run(ctx, cmd, nil)
	if ctx.Err() != nil {
		return videoProbeResult{}, ctx.Err()
	}
	if err != nil || res.ExitCode != 0 || strings.TrimSpace(res.Stdout) == "" {
		return videoProbeResult{}, fmt.Errorf("ffprobe could not inspect the source video")
	}

	var payload map[string]any
	if err := json.Unmarshal([]byte(res.Stdout), &payload); err != nil {
		return videoProbeResult{}, fmt.Errorf("ffprobe returned invalid video metadata")
	}
	rawStreams, ok := payload["streams"].([]any)
	if !ok || len(rawStreams) == 0 {
		return videoProbeResult{}, fmt.Errorf("source has no readable video stream")
	}
	stream, ok := rawStreams[0].(map[string]any)
	if !ok {
		return videoProbeResult{}, fmt.Errorf("ffprobe returned invalid video stream metadata")
	}

	w, wok := parseJSONFloat(stream["width"])
	h, hok := parseJSONFloat(stream["height"])
	if !wok || !hok || w <= 0 || h <= 0 || math.Trunc(w) != w || math.Trunc(h) != h || int(w)%2 != 0 || int(h)%2 != 0 {
		return videoProbeResult{}, fmt.Errorf("ffprobe returned invalid video dimensions")
	}

	codec, _ := stream["codec_name"].(string)
	fps := parseFrameRate(stream["avg_frame_rate"])
	if fps <= 0 {
		fps = parseFrameRate(stream["r_frame_rate"])
	}

	var fmtName string
	var bytes int64
	if fmtObj, ok := payload["format"].(map[string]any); ok {
		fmtName, _ = fmtObj["format_name"].(string)
		if sz, ok := parseJSONFloat(fmtObj["size"]); ok && sz >= 0 {
			bytes = int64(sz)
		}
	}

	return videoProbeResult{
		width:     int(w),
		height:    int(h),
		fps:       fps,
		codec:     strings.ToLower(strings.TrimSpace(codec)),
		container: normalizeContainer(fmtName, path),
		bytes:     bytes,
	}, nil
}

type audioProbeResult struct {
	hasAudio bool
	codec    string
	kbps     float64
}

func probeAudio(ctx context.Context, r Runner, ffprobe, path string) (audioProbeResult, error) {
	cmd := []string{
		ffprobe, "-v", "error", "-select_streams", "a:0",
		"-show_entries", "stream=codec_name,bit_rate:stream_tags=BPS,BPS-eng",
		"-of", "json", path,
	}
	res, err := r.Run(ctx, cmd, nil)
	if ctx.Err() != nil {
		return audioProbeResult{}, ctx.Err()
	}
	if err != nil || res.ExitCode != 0 || strings.TrimSpace(res.Stdout) == "" {
		return audioProbeResult{}, fmt.Errorf("ffprobe could not inspect the source audio")
	}

	var payload map[string]any
	if err := json.Unmarshal([]byte(res.Stdout), &payload); err != nil {
		return audioProbeResult{}, fmt.Errorf("ffprobe returned invalid audio metadata")
	}
	rawStreams, ok := payload["streams"].([]any)
	if !ok {
		return audioProbeResult{}, fmt.Errorf("ffprobe returned invalid audio streams")
	}
	if len(rawStreams) == 0 {
		return audioProbeResult{hasAudio: false}, nil
	}
	stream, ok := rawStreams[0].(map[string]any)
	if !ok {
		return audioProbeResult{}, fmt.Errorf("ffprobe returned invalid audio stream metadata")
	}

	codec, _ := stream["codec_name"].(string)
	codec = strings.ToLower(strings.TrimSpace(codec))

	bits, ok := parseJSONFloat(stream["bit_rate"])
	if !ok || bits <= 0 {
		bits = 0
		if tags, ok := stream["tags"].(map[string]any); ok {
			for _, key := range []string{"BPS", "BPS-eng"} {
				if v, vok := parseJSONFloat(tags[key]); vok && v > 0 {
					bits = v
					break
				}
			}
		}
	}
	kbps := 0.0
	if bits > 0 {
		kbps = bits / 1000.0
	}
	return audioProbeResult{hasAudio: true, codec: codec, kbps: kbps}, nil
}

func parseJSONFloat(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		if math.IsNaN(x) || math.IsInf(x, 0) {
			return 0, false
		}
		return x, true
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(x), 64)
		if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
			return 0, false
		}
		return f, true
	default:
		return 0, false
	}
}

func parseFrameRate(v any) float64 {
	s, ok := v.(string)
	if !ok {
		if f, fok := parseJSONFloat(v); fok && f > 0 {
			return f
		}
		return 0
	}
	s = strings.TrimSpace(s)
	if s == "" || strings.EqualFold(s, "n/a") {
		return 0
	}
	if numStr, denStr, hasSlash := strings.Cut(s, "/"); hasSlash {
		num, err1 := strconv.ParseFloat(strings.TrimSpace(numStr), 64)
		den, err2 := strconv.ParseFloat(strings.TrimSpace(denStr), 64)
		if err1 == nil && err2 == nil && den > 0 && num > 0 {
			f := num / den
			if !math.IsNaN(f) && !math.IsInf(f, 0) {
				return f
			}
		}
		return 0
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) || f <= 0 {
		return 0
	}
	return f
}

func normalizeContainer(formatName, path string) string {
	ext := strings.ToLower(filepath.Ext(path))
	lower := strings.ToLower(strings.TrimSpace(formatName))
	if strings.Contains(lower, "mp4") {
		return "mp4"
	}
	if strings.Contains(lower, "webm") && ext == ".webm" {
		return "webm"
	}
	if strings.Contains(lower, "matroska") {
		return "mkv"
	}
	if strings.Contains(lower, "webm") {
		return "webm"
	}
	if lower != "" {
		first, _, _ := strings.Cut(lower, ",")
		return strings.TrimSpace(first)
	}
	if len(ext) > 1 {
		return ext[1:]
	}
	return ""
}
