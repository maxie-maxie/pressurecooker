package target

import (
	"context"
	"encoding/json"
	"fmt"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/maxie-maxie/pressurecooker/internal/config"
	"github.com/maxie-maxie/pressurecooker/internal/console"
	"github.com/maxie-maxie/pressurecooker/internal/ffmpeg"
)

type fakeRunner struct {
	hasLibVMAF   bool
	hasXPSNR     bool
	filterCalls  int
	encodeCalls  int
	scoreCalls   int
	recordedArgs [][]string
	onEncode     func(args []string, outPath string) (fileBytes int64, videoPkt int64, audioPkt int64, dur float64, err error)
	onScore      func(args []string, samplePath string, metric string, logPath string) ([]float64, error)
	fileMeta     map[string]fakeFileMeta
	encoderList  string
	encoderCalls int
	hwEncoders   map[string]bool
}

type fakeFileMeta struct {
	bytes    int64
	videoPkt int64
	audioPkt int64
	dur      float64
	w, h     int
	fps      float64
	hasAudio bool
}

func newFakeRunner(hasLibVMAF bool) *fakeRunner {
	return &fakeRunner{
		hasLibVMAF: hasLibVMAF,
		fileMeta:   make(map[string]fakeFileMeta),
	}
}

func (f *fakeRunner) Run(ctx context.Context, args []string, onEvent func(console.Event)) (ffmpeg.Result, error) {
	if err := ctx.Err(); err != nil {
		return ffmpeg.Result{ExitCode: -1}, err
	}
	f.recordedArgs = append(f.recordedArgs, append([]string(nil), args...))
	bin := strings.ToLower(filepath.Base(args[0]))

	if strings.Contains(bin, "ffprobe") {
		return f.handleFFprobe(args)
	}
	if slices.Contains(args, "-encoders") {
		f.encoderCalls++
		return ffmpeg.Result{Stdout: f.encoderList}, nil
	}
	if i := slices.Index(args, "-i"); i >= 0 && strings.HasPrefix(args[i+1], "color=") {
		name := args[slices.Index(args, "-c:v")+1]
		if f.hwEncoders[name] {
			return ffmpeg.Result{}, nil
		}
		return ffmpeg.Result{ExitCode: 1, StderrTail: []string{"no supported GPU"}}, nil
	}
	if slices.Contains(args, "-filters") {
		f.filterCalls++
		if f.hasXPSNR {
			return ffmpeg.Result{Stdout: " TS xpsnr VV->V XPSNR\n .. libvmaf VV->V VMAF\n TS ssim VV->V SSIM\n"}, nil
		}
		if f.hasLibVMAF {
			return ffmpeg.Result{Stdout: " .. libvmaf VV->V Calculate the VMAF\n TS ssim VV->V Calculate the SSIM\n"}, nil
		}
		return ffmpeg.Result{Stdout: " TS ssim VV->V Calculate the SSIM\n"}, nil
	}
	if idx := slices.Index(args, "-lavfi"); idx >= 0 && idx+1 < len(args) {
		f.scoreCalls++
		return f.handleScore(args, args[idx+1])
	}
	return f.handleEncode(args, onEvent)
}

func (f *fakeRunner) handleFFprobe(args []string) (ffmpeg.Result, error) {
	path := args[len(args)-1]
	meta, ok := f.fileMeta[path]
	if !ok {
		meta = fakeFileMeta{bytes: 1000, dur: 2.0, w: 1280, h: 720, fps: 30, hasAudio: true}
	}
	joined := strings.Join(args, " ")
	switch {
	case strings.Contains(joined, "packet=codec_type,size"):
		if meta.videoPkt == 0 && meta.audioPkt == 0 {
			return ffmpeg.Result{Stdout: ""}, nil
		}
		var b strings.Builder
		if meta.videoPkt > 0 {
			fmt.Fprintf(&b, "video,%d\n", meta.videoPkt)
		}
		if meta.audioPkt > 0 {
			fmt.Fprintf(&b, "audio,%d,\n", meta.audioPkt)
		}
		return ffmpeg.Result{Stdout: b.String()}, nil
	case strings.Contains(joined, "format=duration"):
		return ffmpeg.Result{Stdout: fmt.Sprintf("%.3f\n", meta.dur)}, nil
	case strings.Contains(joined, "select_streams v:0"):
		payload := fmt.Sprintf(`{"streams":[{"codec_name":"hevc","width":%d,"height":%d,"avg_frame_rate":"%.0f/1","r_frame_rate":"%.0f/1"}],"format":{"format_name":"mov,mp4","size":"%d"}}`,
			meta.w, meta.h, meta.fps, meta.fps, meta.bytes)
		return ffmpeg.Result{Stdout: payload}, nil
	case strings.Contains(joined, "select_streams a:0"):
		if !meta.hasAudio {
			return ffmpeg.Result{Stdout: `{"streams":[]}`}, nil
		}
		return ffmpeg.Result{Stdout: `{"streams":[{"codec_name":"aac","bit_rate":"128000"}]}`}, nil
	default:
		return ffmpeg.Result{Stdout: ""}, nil
	}
}

var (
	logPathRe   = regexp.MustCompile(`(?:log_path|stats_file)='([^']+)'`)
	scaleArgRe  = regexp.MustCompile(`scale=(\d+):(\d+)`)
	fpsArgRe    = regexp.MustCompile(`fps=([0-9]+(?:\.[0-9]+)?)`)
	passLogX265 = regexp.MustCompile(`pass=(\d+):stats=(.+)$`)
)

func (f *fakeRunner) handleScore(args []string, graph string) (ffmpeg.Result, error) {
	samplePath := ""
	if i := slices.Index(args, "-i"); i >= 0 && i+1 < len(args) {
		samplePath = args[i+1]
	}
	metric := metricSSIM
	if strings.Contains(graph, "libvmaf=") {
		metric = metricVMAF
	} else if strings.Contains(graph, "xpsnr=") {
		metric = metricXPSNR
	}
	logPath := ""
	if m := logPathRe.FindStringSubmatch(graph); len(m) == 2 {
		logPath = strings.ReplaceAll(m[1], `\:`, ":")
		logPath = filepath.FromSlash(logPath)
	}
	var scores []float64
	if f.onScore != nil {
		var err error
		scores, err = f.onScore(args, samplePath, metric, logPath)
		if err != nil {
			return ffmpeg.Result{ExitCode: 1, StderrTail: []string{err.Error()}}, err
		}
	} else {
		scores = []float64{85, 86, 87, 88}
	}
	if logPath != "" {
		if metric == metricVMAF {
			type frameObj struct {
				Metrics struct {
					VMAF float64 `json:"vmaf"`
				} `json:"metrics"`
			}
			var doc struct {
				Frames []frameObj `json:"frames"`
			}
			for _, v := range scores {
				var fr frameObj
				fr.Metrics.VMAF = v
				doc.Frames = append(doc.Frames, fr)
			}
			raw, _ := json.Marshal(doc)
			_ = os.WriteFile(logPath, raw, 0o644)
		} else if metric == metricXPSNR {
			var b strings.Builder
			for i, v := range scores {
				fmt.Fprintf(&b, "n: %d XPSNR y: %.6f u: %.6f v: %.6f\n", i+1, v, v, v)
			}
			_ = os.WriteFile(logPath, []byte(b.String()), 0o644)
		} else {
			var b strings.Builder
			for i, v := range scores {
				fmt.Fprintf(&b, "n:%d Y:%.6f U:%.6f V:%.6f All:%.6f (20.0)\n", i+1, v/100.0, v/100.0, v/100.0, v/100.0)
			}
			_ = os.WriteFile(logPath, []byte(b.String()), 0o644)
		}
	}
	return ffmpeg.Result{ExitCode: 0}, nil
}

func (f *fakeRunner) handleEncode(args []string, onEvent func(console.Event)) (ffmpeg.Result, error) {
	f.encodeCalls++
	if isPass1Args(args) {
		if statsPath := passStatsPath(args); statsPath != "" {
			_ = os.WriteFile(statsPath, []byte("stats"), 0o644)
		}
		if onEvent != nil {
			onEvent(console.Event{Timeline: 1, Duration: 2, Speed: 2})
		}
		return ffmpeg.Result{ExitCode: 0}, nil
	}

	outPath := args[len(args)-1]
	w, h := parseScaleFromArgs(args, 1920, 1080)
	fps := parseFPSFromArgs(args, 30)
	dur := parseDurationFromArgs(args, 60)

	var (
		fileBytes int64 = 100000
		videoPkt  int64 = 80000
		audioPkt  int64 = 17000
		err       error
	)
	if f.onEncode != nil {
		fileBytes, videoPkt, audioPkt, dur, err = f.onEncode(args, outPath)
		if err != nil {
			return ffmpeg.Result{ExitCode: 1, StderrTail: []string{err.Error()}}, err
		}
	}
	if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
		return ffmpeg.Result{ExitCode: 1}, err
	}
	if fileBytes <= 4*1024*1024 {
		if err := os.WriteFile(outPath, make([]byte, fileBytes), 0o644); err != nil {
			return ffmpeg.Result{ExitCode: 1}, err
		}
	} else {
		fh, err := os.Create(outPath)
		if err != nil {
			return ffmpeg.Result{ExitCode: 1}, err
		}
		if err := fh.Truncate(fileBytes); err != nil {
			_ = fh.Close()
			return ffmpeg.Result{ExitCode: 1}, err
		}
		_ = fh.Close()
	}

	f.fileMeta[outPath] = fakeFileMeta{
		bytes:    fileBytes,
		videoPkt: videoPkt,
		audioPkt: audioPkt,
		dur:      dur,
		w:        w,
		h:        h,
		fps:      fps,
		hasAudio: !slices.Contains(args, "-an"),
	}
	if onEvent != nil {
		onEvent(console.Event{Timeline: dur, Duration: dur, Speed: 2, End: true})
	}
	return ffmpeg.Result{ExitCode: 0}, nil
}

func isPass1Args(args []string) bool {
	if i := slices.Index(args, "-pass:v"); i >= 0 && i+1 < len(args) && args[i+1] == "1" {
		return true
	}
	if i := slices.Index(args, "-x265-params"); i >= 0 && i+1 < len(args) && strings.Contains(args[i+1], "pass=1:") {
		return true
	}
	return false
}

func passStatsPath(args []string) string {
	if i := slices.Index(args, "-passlogfile:v"); i >= 0 && i+1 < len(args) {
		return args[i+1] + "-0.log"
	}
	if i := slices.Index(args, "-x265-params"); i >= 0 && i+1 < len(args) {
		if m := passLogX265.FindStringSubmatch(args[i+1]); len(m) == 3 {
			s := strings.ReplaceAll(m[2], `\:`, ":")
			s = strings.ReplaceAll(s, `\\`, `\`)
			s = strings.ReplaceAll(s, `\'`, `'`)
			return s
		}
	}
	return ""
}

func parseScaleFromArgs(args []string, defW, defH int) (int, int) {
	if i := slices.Index(args, "-vf"); i >= 0 && i+1 < len(args) {
		if m := scaleArgRe.FindStringSubmatch(args[i+1]); len(m) == 3 {
			w, _ := strconv.Atoi(m[1])
			h, _ := strconv.Atoi(m[2])
			return w, h
		}
	}
	return defW, defH
}

func parseFPSFromArgs(args []string, defFPS float64) float64 {
	if i := slices.Index(args, "-vf"); i >= 0 && i+1 < len(args) {
		if m := fpsArgRe.FindStringSubmatch(args[i+1]); len(m) == 2 {
			v, _ := strconv.ParseFloat(m[1], 64)
			return v
		}
	}
	return defFPS
}

func parseDurationFromArgs(args []string, defDur float64) float64 {
	if i := slices.Index(args, "-t"); i >= 0 && i+1 < len(args) {
		v, _ := strconv.ParseFloat(args[i+1], 64)
		return v
	}
	return defDur
}

func parseVideoKbpsFromArgs(args []string) int {
	if i := slices.Index(args, "-b:v"); i >= 0 && i+1 < len(args) {
		v, _ := strconv.Atoi(strings.TrimSuffix(args[i+1], "k"))
		return v
	}
	return 0
}

func baseTestRequest(dir string, dur float64) ffmpeg.Request {
	s := config.Factory()
	s.HW = "off"
	return ffmpeg.Request{
		Source:   filepath.Join(dir, "input.mp4"),
		Output:   filepath.Join(dir, "out.mp4.part"),
		FFmpeg:   "ffmpeg",
		FFprobe:  "ffprobe",
		Settings: s,
		Origins:  config.Origins{},
		Encoder: ffmpeg.Encoder{
			Name:      "libx265",
			Backend:   "software",
			Preset:    "fast",
			Quantizer: 32,
		},
		Media: ffmpeg.Media{
			Duration:   dur,
			FPS:        60,
			Width:      1920,
			Height:     1080,
			VideoCodec: "h264",
			AudioCodec: "aac",
			Container:  "mp4",
			AudioKbps:  192,
			HasAudio:   true,
			Bytes:      50_000_000,
		},
		RateControl: "quality",
	}
}

func TestResolutionAndFPSRungsNeverUpscaleAndPreserveAspect(t *testing.T) {
	req := baseTestRequest(t.TempDir(), 60)
	rungs, err := buildResolutionRungs(req)
	if err != nil {
		t.Fatalf("buildResolutionRungs failed: %v", err)
	}
	if rungs[0].width != 1920 || rungs[0].height != 1080 || rungs[0].setting != "" {
		t.Fatalf("first rung should be source, got %+v", rungs[0])
	}
	for _, rg := range rungs {
		if rg.width > 1920 || rg.height > 1080 || rg.width%2 != 0 || rg.height%2 != 0 {
			t.Fatalf("invalid landscape rung %+v", rg)
		}
	}

	req.Media.Width, req.Media.Height = 720, 1280
	pRungs, err := buildResolutionRungs(req)
	if err != nil {
		t.Fatalf("portrait rungs failed: %v", err)
	}
	for _, rg := range pRungs {
		if rg.width > 720 || rg.height > 1280 || rg.height < rg.width {
			t.Fatalf("portrait rung broke aspect or upscaled: %+v", rg)
		}
	}

	req.Media.Width, req.Media.Height = 320, 240
	sRungs, err := buildResolutionRungs(req)
	if err != nil {
		t.Fatalf("small rungs failed: %v", err)
	}
	for _, rg := range sRungs {
		if rg.width > 320 || rg.height > 240 {
			t.Fatalf("small source upscaled to %+v", rg)
		}
	}

	req.Media.FPS = 60
	_, fps60, err := fastRungs(req)
	if err != nil {
		t.Fatal(err)
	}
	if len(fps60) != 3 || fps60[0].fps != 60 || fps60[1].fps != 30 || fps60[2].fps != 24 {
		t.Fatalf("unexpected 60fps rungs: %+v", fps60)
	}
	req.Media.FPS = 24
	_, fps24, err := fastRungs(req)
	if err != nil {
		t.Fatal(err)
	}
	if len(fps24) != 1 || fps24[0].fps != 24 {
		t.Fatalf("unexpected 24fps rungs: %+v", fps24)
	}
}

func TestRankCandidatesHandBuiltTable(t *testing.T) {
	winner := rankCandidates([]scoredCandidate{
		{cand: candidate{width: 1920, height: 1080, fps: 30, audioKbps: 128}, mean: 82.0, p05: 78.0},
		{cand: candidate{width: 1280, height: 720, fps: 30, audioKbps: 128}, mean: 89.5, p05: 85.0},
		{cand: candidate{width: 854, height: 480, fps: 24, audioKbps: 128}, mean: 84.0, p05: 81.0},
	})
	if winner.cand.width != 1280 {
		t.Fatalf("expected 1280x720 clear winner, got %dx%d", winner.cand.width, winner.cand.height)
	}

	winnerFPS := rankCandidates([]scoredCandidate{
		{cand: candidate{width: 1280, height: 720, fps: 24, audioKbps: 128}, mean: 86.7, p05: 83.0},
		{cand: candidate{width: 1280, height: 720, fps: 30, audioKbps: 128}, mean: 86.0, p05: 82.5},
	})
	if winnerFPS.cand.fps != 30 {
		t.Fatalf("within 1.0 point tie-break should prefer 30fps over 24fps, got %.0f", winnerFPS.cand.fps)
	}

	winnerAudio := rankCandidates([]scoredCandidate{
		{cand: candidate{width: 1280, height: 720, fps: 30, audioKbps: 96}, mean: 86.6, p05: 83.0},
		{cand: candidate{width: 1280, height: 720, fps: 30, audioKbps: 128}, mean: 86.0, p05: 82.5},
	})
	if winnerAudio.cand.audioKbps != 128 {
		t.Fatalf("within 1.0 point tie-break should prefer 128k audio over 96k, got %d", winnerAudio.cand.audioKbps)
	}

	winnerGuard := rankCandidates([]scoredCandidate{
		{cand: candidate{width: 1920, height: 1080, fps: 30, audioKbps: 128}, mean: 84.0, p05: 55.0},
		{cand: candidate{width: 1280, height: 720, fps: 30, audioKbps: 128}, mean: 82.0, p05: 79.0},
	})
	if winnerGuard.cand.width != 1280 {
		t.Fatalf("5th-percentile guard should prefer consistent 720p over collapsed-p05 1080p, got %dx%d", winnerGuard.cand.width, winnerGuard.cand.height)
	}
}

func TestNoOSExecImportInTargetPackage(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("ReadDir failed: %v", err)
	}
	fset := token.NewFileSet()
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") {
			continue
		}
		file, err := parser.ParseFile(fset, entry.Name(), nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse %s: %v", entry.Name(), err)
		}
		for _, imp := range file.Imports {
			if strings.Trim(imp.Path.Value, `"`) == "os/exec" {
				t.Fatalf("forbidden os/exec import found in %s", entry.Name())
			}
		}
	}
}
