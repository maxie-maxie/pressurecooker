package ffmpeg

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/maxie-maxie/pressurecooker/internal/config"
	"github.com/maxie-maxie/pressurecooker/internal/console"
)

type fakeRunner struct {
	calls [][]string
	fn    func(ctx context.Context, args []string, onEvent func(console.Event)) (Result, error)
}

func (f *fakeRunner) Run(ctx context.Context, args []string, onEvent func(console.Event)) (Result, error) {
	cp := append([]string(nil), args...)
	f.calls = append(f.calls, cp)
	if f.fn != nil {
		return f.fn(ctx, cp, onEvent)
	}
	return Result{}, nil
}

func f64ptr(v float64) *float64 { return &v }

func indexOf(args []string, needle string) int {
	for i, a := range args {
		if a == needle {
			return i
		}
	}
	return -1
}

const (
	gpuListLog = "[Vulkan @ 0x1] GPU listing:\n" +
		"[Vulkan @ 0x1]     0: Integrated GPU (integrated) (0x1234)\n" +
		"[Vulkan @ 0x1]     1: Discrete GPU (discrete) (0x5678)\n"
	vkEncodeOKLog = "[hevc_vulkan @ 0x2] Encoder max quality: 3\n"
)

func TestResolveToolMissing(t *testing.T) {
	_, err := resolveToolForOS(filepath.Join(t.TempDir(), "missing-ffmpeg"), "ffmpeg", runtime.GOOS, func(string) (string, error) {
		return "", errors.New("not found")
	})
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("expected missing explicit tool error, got %v", err)
	}

	_, err = resolveToolForOS("", "ffprobe", runtime.GOOS, func(string) (string, error) {
		return "", errors.New("not on PATH")
	})
	if err == nil || !strings.Contains(err.Error(), "PATH") {
		t.Fatalf("expected missing PATH tool error, got %v", err)
	}
}

func TestResolveToolRefusesWindowsBatchWrappers(t *testing.T) {
	root := t.TempDir()
	for _, ext := range []string{".bat", ".cmd"} {
		wrapper := filepath.Join(root, "ffmpeg"+ext)
		if err := os.WriteFile(wrapper, []byte("@echo off\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := resolveToolForOS(wrapper, "ffmpeg", "windows", nil); err == nil || !strings.Contains(err.Error(), "batch") {
			t.Fatalf("expected batch refusal for explicit %s, got %v", ext, err)
		}
		if _, err := resolveToolForOS("", "ffprobe", "windows", func(string) (string, error) {
			return wrapper, nil
		}); err == nil || !strings.Contains(err.Error(), "batch") {
			t.Fatalf("expected batch refusal for PATH %s, got %v", ext, err)
		}
	}

	exe := filepath.Join(root, "ffmpeg.exe")
	if err := os.WriteFile(exe, []byte("exe"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := resolveToolForOS(exe, "ffmpeg", "windows", nil)
	if err != nil {
		t.Fatalf("expected .exe to succeed, got %v", err)
	}
	if !strings.HasSuffix(strings.ToLower(got), "ffmpeg.exe") {
		t.Fatalf("unexpected resolved path %s", got)
	}
}

func TestProbeDurationFallbackAndFirstFiniteLine(t *testing.T) {
	mediaPath := "space %44211323% / $(echo secret); video.mp4"
	r := &fakeRunner{fn: func(_ context.Context, args []string, _ func(console.Event)) (Result, error) {
		if args[len(args)-1] != mediaPath {
			t.Fatalf("media path altered: %q", args[len(args)-1])
		}
		joined := strings.Join(args, " ")
		switch {
		case strings.Contains(joined, "format=duration"):
			return Result{Stdout: "N/A\n"}, nil
		case strings.Contains(joined, "stream=duration"):
			return Result{Stdout: "bad\n-2\n0\nNaN\n12.5\n99\n"}, nil
		case strings.Contains(joined, "select_streams v:0"):
			return Result{Stdout: `{"streams":[{"codec_name":"h264","width":1280,"height":720,"avg_frame_rate":"30000/1001"}],"format":{"format_name":"mov,mp4,m4a","size":"1048576"}}`}, nil
		case strings.Contains(joined, "select_streams a:0"):
			return Result{Stdout: `{"streams":[{"codec_name":"aac","bit_rate":"96000","tags":{"BPS":"256000"}}]}`}, nil
		default:
			return Result{ExitCode: 1}, errors.New("unexpected probe call")
		}
	}}

	m, err := Probe(context.Background(), r, "ffprobe", mediaPath)
	if err != nil {
		t.Fatalf("Probe failed: %v", err)
	}
	if m.Duration != 12.5 {
		t.Fatalf("expected duration 12.5, got %v", m.Duration)
	}
	if m.Width != 1280 || m.Height != 720 || m.VideoCodec != "h264" || m.Container != "mp4" {
		t.Fatalf("unexpected video metadata: %+v", m)
	}
	if m.FPS < 29.96 || m.FPS > 29.98 {
		t.Fatalf("unexpected FPS: %v", m.FPS)
	}
	if !m.HasAudio || m.AudioCodec != "aac" || m.AudioKbps != 96.0 {
		t.Fatalf("expected stream bit_rate 96 kbps over BPS tag, got %+v", m)
	}
	if m.Bytes != 1048576 {
		t.Fatalf("expected fallback format.size 1048576, got %d", m.Bytes)
	}
}

func TestProbeDurationRejectsWhenBothFail(t *testing.T) {
	r := &fakeRunner{fn: func(_ context.Context, _ []string, _ func(console.Event)) (Result, error) {
		return Result{Stdout: "N/A\n"}, nil
	}}
	if _, err := Probe(context.Background(), r, "ffprobe", "clip.mp4"); err == nil {
		t.Fatalf("expected duration probe error when both format and stream fail")
	}
	if len(r.calls) != 2 {
		t.Fatalf("expected 2 duration attempts, got %d", len(r.calls))
	}
}

func TestProbeAudioBPSTagFallbackAndInvalidMetadataRejection(t *testing.T) {
	r := &fakeRunner{fn: func(_ context.Context, args []string, _ func(console.Event)) (Result, error) {
		joined := strings.Join(args, " ")
		switch {
		case strings.Contains(joined, "format=duration"):
			return Result{Stdout: "10.0\n"}, nil
		case strings.Contains(joined, "select_streams v:0"):
			return Result{Stdout: `{"streams":[{"codec_name":"hevc","width":640,"height":360,"r_frame_rate":"24/1"}],"format":{"format_name":"matroska,webm"}}`}, nil
		case strings.Contains(joined, "select_streams a:0"):
			return Result{Stdout: `{"streams":[{"codec_name":"aac","tags":{"BPS-eng":"128000"}}]}`}, nil
		default:
			return Result{ExitCode: 1}, nil
		}
	}}
	m, err := Probe(context.Background(), r, "ffprobe", "clip.mkv")
	if err != nil {
		t.Fatalf("Probe failed: %v", err)
	}
	if !m.HasAudio || m.AudioCodec != "aac" || m.AudioKbps != 128.0 || m.Container != "mkv" {
		t.Fatalf("unexpected BPS fallback metadata: %+v", m)
	}

	for _, tc := range []struct {
		name      string
		res       Result
		wantErr   bool
		wantAudio bool
	}{
		{"nonzero exit", Result{ExitCode: 1}, true, false},
		{"not json", Result{Stdout: "not-json"}, true, false},
		{"streams not list", Result{Stdout: `{"streams":{}}`}, true, false},
		{"no audio streams", Result{Stdout: `{"streams":[]}`}, false, false},
		{"empty audio object", Result{Stdout: `{"streams":[{}]}`}, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ar := &fakeRunner{fn: func(_ context.Context, _ []string, _ func(console.Event)) (Result, error) {
				return tc.res, nil
			}}
			aud, err := probeAudio(context.Background(), ar, "ffprobe", "clip.mp4")
			if tc.wantErr && err == nil {
				t.Fatalf("expected error for %s", tc.name)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected error for %s: %v", tc.name, err)
			}
			if !tc.wantErr && aud.hasAudio != tc.wantAudio {
				t.Fatalf("expected hasAudio=%v, got %v", tc.wantAudio, aud.hasAudio)
			}
		})
	}
}

func TestProbeInvalidVideoDimensionsRejected(t *testing.T) {
	for _, badWidth := range []string{`"NaN"`, `"Infinity"`, `"-1"`, `"0"`, `"1281"`} {
		t.Run(badWidth, func(t *testing.T) {
			r := &fakeRunner{fn: func(_ context.Context, _ []string, _ func(console.Event)) (Result, error) {
				return Result{Stdout: `{"streams":[{"width":` + badWidth + `,"height":144}]}`}, nil
			}}
			if _, err := probeVideo(context.Background(), r, "ffprobe", "clip.mp4"); err == nil {
				t.Fatalf("expected invalid dimension error for width=%s", badWidth)
			}
		})
	}
}

func TestQualityMappingTableAndCRFBounds(t *testing.T) {
	cases := []struct {
		level, enc string
		want       float64
	}{
		{"high", "libx265", 26}, {"high", "libx264", 21}, {"high", "libsvtav1", 30},
		{"high", "hevc_vulkan", 26}, {"high", "hevc_qsv", 26},
		{"balanced", "libx265", 32}, {"balanced", "libx264", 27}, {"balanced", "libsvtav1", 38},
		{"balanced", "hevc_vulkan", 32}, {"balanced", "h264_qsv", 32},
		{"small", "libx265", 36}, {"small", "libx264", 31}, {"small", "libsvtav1", 46},
		{"small", "av1_vulkan", 36}, {"small", "av1_qsv", 36},
	}
	for _, tc := range cases {
		got, err := QualityQuantizer(tc.level, tc.enc)
		if err != nil || got != tc.want {
			t.Fatalf("QualityQuantizer(%s, %s) = %v, %v; want %v", tc.level, tc.enc, got, err, tc.want)
		}
	}

	r := &fakeRunner{fn: func(context.Context, []string, func(console.Event)) (Result, error) {
		t.Fatalf("GPU probe must not run for raw --crf")
		return Result{}, nil
	}}
	for _, hw := range []string{"vulkan", "qsv"} {
		s := config.Factory()
		s.HW = hw
		s.CRF = f64ptr(24)
		_, err := ChooseEncoder(context.Background(), r, "ffmpeg", s)
		if err == nil || !strings.Contains(err.Error(), "use --quality or --hw off") {
			t.Fatalf("expected raw CRF error on %s, got %v", hw, err)
		}
	}

	sAuto := config.Factory()
	sAuto.HW = "auto"
	sAuto.CRF = f64ptr(24)
	enc, err := ChooseEncoder(context.Background(), r, "ffmpeg", sAuto)
	if err != nil || enc.Hardware || enc.Name != "libx265" || enc.Quantizer != 24 {
		t.Fatalf("expected hw=auto + CRF to select software directly, got %+v (%v)", enc, err)
	}

	sBad := config.Factory()
	sBad.HW = "off"
	sBad.Codec = "av1"
	sBad.CRF = f64ptr(0)
	if _, err := ChooseEncoder(context.Background(), r, "ffmpeg", sBad); err == nil {
		t.Fatalf("expected AV1 CRF 0 to fail")
	}
	sBad.Codec = "hevc"
	sBad.CRF = f64ptr(52)
	if _, err := ChooseEncoder(context.Background(), r, "ffmpeg", sBad); err == nil {
		t.Fatalf("expected HEVC CRF 52 to fail")
	}
}

func TestChooseEncoderOffSkipsGPUProbes(t *testing.T) {
	r := &fakeRunner{fn: func(context.Context, []string, func(console.Event)) (Result, error) {
		t.Fatalf("hw=off must skip GPU probes")
		return Result{}, nil
	}}
	s := config.Factory()
	s.HW = "off"
	s.Codec = "h264"
	s.Quality = "high"

	enc, err := ChooseEncoder(context.Background(), r, "ffmpeg", s)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if enc.Hardware || enc.Name != "libx264" || enc.Quantizer != 21 || enc.Preset != "fast" {
		t.Fatalf("unexpected software encoder: %+v", enc)
	}
}

func TestChooseEncoderVulkanFallbackAndExplicitFailure(t *testing.T) {
	failAll := func(context.Context, []string, func(console.Event)) (Result, error) {
		return Result{
			ExitCode:   1,
			StderrTail: strings.Split(gpuListLog+"[hevc_vulkan] Device does not support VK_KHR_video_encode_queue", "\n"),
		}, nil
	}

	rAuto := &fakeRunner{fn: failAll}
	s := config.Factory()
	s.HW = "auto"
	enc, err := ChooseEncoder(context.Background(), rAuto, "ffmpeg", s)
	if err != nil {
		t.Fatalf("hw=auto should fall back, got error: %v", err)
	}
	if enc.Hardware || enc.Name != "libx265" || len(enc.Warnings) == 0 {
		t.Fatalf("expected software fallback with warning, got %+v", enc)
	}
	w := enc.Warnings[0]
	if len(enc.Warnings) != 1 || w != "Vulkan encode unavailable on 2 GPUs; using libx265" || strings.ContainsAny(w, "\r\n") {
		t.Fatalf("unexpected fallback warning: %q", w)
	}

	rExplicit := &fakeRunner{fn: failAll}
	s.HW = "vulkan"
	if _, err := ChooseEncoder(context.Background(), rExplicit, "ffmpeg", s); err == nil {
		t.Fatalf("expected explicit hw=vulkan to fail when unsupported")
	}
}

func TestChooseEncoderSelectsSecondVulkanGPU(t *testing.T) {
	call := 0
	r := &fakeRunner{fn: func(_ context.Context, args []string, _ func(console.Event)) (Result, error) {
		call++
		if call == 1 {
			return Result{ExitCode: 1, StderrTail: strings.Split(gpuListLog+"[hevc_vulkan] Device does not support encode", "\n")}, nil
		}
		return Result{ExitCode: 0, StderrTail: strings.Split(vkEncodeOKLog, "\n")}, nil
	}}
	s := config.Factory()
	s.HW = "auto"
	s.Preset = "fast"

	enc, err := ChooseEncoder(context.Background(), r, "ffmpeg", s)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !enc.Hardware || enc.Backend != "Vulkan" || enc.Device != 1 || enc.DeviceName != "Discrete GPU" || enc.Quantizer != 32 {
		t.Fatalf("expected second GPU selected, got %+v", enc)
	}
}

func TestChooseEncoderMissingVulkanEncoderStopsAfterOneProbe(t *testing.T) {
	r := &fakeRunner{fn: func(context.Context, []string, func(console.Event)) (Result, error) {
		return Result{
			ExitCode:   1,
			StderrTail: strings.Split(gpuListLog+"Unknown encoder 'av1_vulkan'", "\n"),
		}, nil
	}}
	s := config.Factory()
	s.Codec = "av1"
	s.HW = "auto"

	enc, err := ChooseEncoder(context.Background(), r, "ffmpeg", s)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if enc.Name != "libsvtav1" || len(r.calls) != 1 {
		t.Fatalf("expected 1 probe and libsvtav1 fallback, got calls=%d enc=%+v", len(r.calls), enc)
	}
	if len(enc.Warnings) != 1 || enc.Warnings[0] != "Vulkan encode unavailable on 2 GPUs; using libsvtav1" {
		t.Fatalf("expected one GPU summary warning, got %+v", enc.Warnings)
	}
}

func TestChooseEncoderVulkanQualityClampAndFallbacks(t *testing.T) {
	call := 0
	r := &fakeRunner{fn: func(_ context.Context, args []string, _ func(console.Event)) (Result, error) {
		call++
		if call == 1 {
			return Result{ExitCode: 0, StderrTail: strings.Split(gpuListLog+vkEncodeOKLog, "\n")}, nil
		}
		return Result{ExitCode: 0, StderrTail: strings.Split(vkEncodeOKLog, "\n")}, nil
	}}
	s := config.Factory()
	s.HW = "auto"
	s.Preset = "placebo"
	enc, err := ChooseEncoder(context.Background(), r, "ffmpeg", s)
	if err != nil || enc.Quality != 2 {
		t.Fatalf("expected quality clamped to 2, got %+v (%v)", enc, err)
	}
	qIdx := indexOf(r.calls[1], "-quality")
	if qIdx < 0 || r.calls[1][qIdx+1] != "2" {
		t.Fatalf("expected second probe with -quality 2, got %v", r.calls[1])
	}

	call = 0
	rFailQ := &fakeRunner{fn: func(context.Context, []string, func(console.Event)) (Result, error) {
		call++
		if call == 1 {
			return Result{ExitCode: 0, StderrTail: strings.Split(gpuListLog+vkEncodeOKLog, "\n")}, nil
		}
		return Result{ExitCode: 1, StderrTail: []string{"Unsupported quality level"}}, nil
	}}
	s.Preset = "slow"
	enc, err = ChooseEncoder(context.Background(), rFailQ, "ffmpeg", s)
	if err != nil || !enc.Hardware || enc.Quality != 0 || len(enc.Warnings) == 0 || !strings.Contains(enc.Warnings[0], "using quality 0") {
		t.Fatalf("expected GPU retained at level 0 with warning, got %+v (%v)", enc, err)
	}

	rNoLog := &fakeRunner{fn: func(context.Context, []string, func(console.Event)) (Result, error) {
		return Result{ExitCode: 0, StderrTail: strings.Split(gpuListLog, "\n")}, nil
	}}
	enc, err = ChooseEncoder(context.Background(), rNoLog, "ffmpeg", s)
	if err != nil || enc.Quality != 0 || len(rNoLog.calls) != 1 {
		t.Fatalf("expected level 0 with 1 probe, got calls=%d enc=%+v (%v)", len(rNoLog.calls), enc, err)
	}
}

func TestResolveVulkanQualityAndInvalidPresetBeforeGPUProbe(t *testing.T) {
	for _, tc := range []struct {
		preset string
		want   int
	}{
		{"ultrafast", 0}, {"fast", 0}, {"medium", 1}, {"slow", 2}, {"placebo", 5},
	} {
		for _, codec := range []string{"h264", "hevc", "av1"} {
			got, err := ResolveVulkanQuality(tc.preset, codec)
			if err != nil || got != tc.want {
				t.Fatalf("ResolveVulkanQuality(%s, %s) = %d, %v; want %d", tc.preset, codec, got, err, tc.want)
			}
		}
	}
	for _, tc := range []struct {
		preset string
		want   int
	}{{"0", 8}, {"7", 1}, {"13", 0}} {
		got, err := ResolveVulkanQuality(tc.preset, "av1")
		if err != nil || got != tc.want {
			t.Fatalf("ResolveVulkanQuality(%s, av1) = %d, %v; want %d", tc.preset, got, err, tc.want)
		}
	}

	r := &fakeRunner{fn: func(context.Context, []string, func(console.Event)) (Result, error) {
		t.Fatalf("GPU probe must not run on invalid preset")
		return Result{}, nil
	}}
	for _, tc := range []struct{ codec, preset string }{
		{"hevc", "7"}, {"av1", "14"}, {"h264", "invalid"},
	} {
		s := config.Factory()
		s.Codec = tc.codec
		s.Preset = tc.preset
		s.HW = "auto"
		if _, err := ChooseEncoder(context.Background(), r, "ffmpeg", s); err == nil {
			t.Fatalf("expected invalid preset error for %s/%s", tc.codec, tc.preset)
		}
	}
}

func TestChooseEncoderProbeTimeoutAndLoaderErrorRecoverable(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want string
	}{
		{context.DeadlineExceeded, "timed out"},
		{errors.New("Vulkan loader missing"), "Vulkan loader missing"},
	} {
		r := &fakeRunner{fn: func(context.Context, []string, func(console.Event)) (Result, error) {
			return Result{ExitCode: -1}, tc.err
		}}
		s := config.Factory()
		s.HW = "auto"
		enc, err := ChooseEncoder(context.Background(), r, "ffmpeg", s)
		if err != nil || enc.Hardware || len(enc.Warnings) == 0 || !strings.Contains(enc.Warnings[0], tc.want) {
			t.Fatalf("expected recoverable fallback mentioning %q, got %+v (%v)", tc.want, enc, err)
		}
	}
}

func TestChooseEncoderExplicitQSVFailsAndPresetMapping(t *testing.T) {
	rFail := &fakeRunner{fn: func(context.Context, []string, func(console.Event)) (Result, error) {
		return Result{ExitCode: 1, StderrTail: []string{"unsupported driver"}}, nil
	}}
	s := config.Factory()
	s.HW = "qsv"
	s.Preset = "ultrafast"
	if _, err := ChooseEncoder(context.Background(), rFail, "ffmpeg", s); err == nil || !strings.Contains(err.Error(), "unsupported driver") {
		t.Fatalf("expected explicit QSV failure, got %v", err)
	}
	pIdx := indexOf(rFail.calls[0], "-preset")
	if pIdx < 0 || rFail.calls[0][pIdx+1] != "veryfast" {
		t.Fatalf("expected QSV preset veryfast, got %v", rFail.calls[0])
	}
	if indexOf(rFail.calls[0], "-global_quality") < 0 || indexOf(rFail.calls[0], "-b:v") >= 0 {
		t.Fatalf("expected QSV ICQ probe without -b:v, got %v", rFail.calls[0])
	}

	rOK := &fakeRunner{fn: func(context.Context, []string, func(console.Event)) (Result, error) {
		return Result{ExitCode: 0}, nil
	}}
	s.Codec = "av1"
	s.Preset = "7"
	s.Quality = "small"
	enc, err := ChooseEncoder(context.Background(), rOK, "ffmpeg", s)
	if err != nil || !enc.Hardware || enc.Backend != "QSV" || enc.Name != "av1_qsv" || enc.Preset != "faster" || enc.Quantizer != 36 {
		t.Fatalf("unexpected QSV encoder: %+v (%v)", enc, err)
	}
}
