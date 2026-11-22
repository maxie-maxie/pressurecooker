package ffmpeg

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/maxie-maxie/pressurecooker/internal/config"
	"github.com/maxie-maxie/pressurecooker/internal/console"
)

func TestProbeVulkanDecodeAndPipelines(t *testing.T) {
	src := "space %44211323% / $(echo secret); video.mp4"
	enc := Encoder{Name: "hevc_vulkan", Backend: "Vulkan", Device: 1, Quality: 0, Hardware: true, Quantizer: 32}
	s := config.Factory()
	s.Resolution = "hd"
	s.FPS = f64ptr(30)
	m := Media{Width: 1920, Height: 1080, HasAudio: true, AudioCodec: "aac", AudioKbps: 96}

	r := &fakeRunner{fn: func(context.Context, []string, func(console.Event)) (Result, error) {
		return Result{ExitCode: 0}, nil
	}}
	ok, errMsg := ProbeVulkanDecode(context.Background(), r, "ffmpeg", src, enc, s, m)
	if !ok || errMsg != "" {
		t.Fatalf("expected decode probe success, got ok=%v err=%q", ok, errMsg)
	}
	cmd := r.calls[0]
	iIdx := indexOf(cmd, "-i")
	if iIdx < 0 || cmd[iIdx+1] != src {
		t.Fatalf("source path not preserved as argument: %v", cmd)
	}
	if indexOf(cmd, "-hwaccel") >= iIdx {
		t.Fatalf("-hwaccel must precede -i: %v", cmd)
	}
	if !slices.Contains(cmd, "vulkan=pc:1") || !slices.Contains(cmd, "scale_vulkan=format=nv12:w=1280:h=720,fps=30") || cmd[len(cmd)-1] != os.DevNull {
		t.Fatalf("unexpected decode probe command: %v", cmd)
	}

	enc.Decode = true
	args, err := BuildArgs(Request{
		Source: src, Output: "out.mp4", FFmpeg: "ffmpeg", Settings: s, Encoder: enc, Media: m, RateControl: "quality",
	})
	if err != nil {
		t.Fatalf("BuildArgs failed: %v", err)
	}
	if indexOf(args, "-hwaccel") < 0 || !slices.Contains(args, "scale_vulkan=format=nv12:w=1280:h=720,fps=30") {
		t.Fatalf("expected GPU decode pipeline, got %v", args)
	}
	if idx := indexOf(args, "-rc_mode"); idx < 0 || args[idx+1] != "cqp" {
		t.Fatalf("expected -rc_mode cqp, got %v", args)
	}
	if idx := indexOf(args, "-qp"); idx < 0 || args[idx+1] != "32" {
		t.Fatalf("expected -qp 32, got %v", args)
	}
	if idx := indexOf(args, "-quality"); idx < 0 || args[idx+1] != "0" {
		t.Fatalf("expected -quality 0, got %v", args)
	}
	for _, forbidden := range []string{"-b:v", "-maxrate", "-bufsize", "-preset", "-look_ahead", "-pass:v", "-passlogfile:v"} {
		if slices.Contains(args, forbidden) {
			t.Fatalf("forbidden flag %s in Vulkan CQP command: %v", forbidden, args)
		}
	}

	enc.Decode = false
	argsCPU, err := BuildArgs(Request{
		Source: src, Output: "out.mp4", FFmpeg: "ffmpeg", Settings: s, Encoder: enc, Media: m, RateControl: "quality",
	})
	if err != nil {
		t.Fatalf("BuildArgs CPU upload failed: %v", err)
	}
	if slices.Contains(argsCPU, "-hwaccel") {
		t.Fatalf("expected no -hwaccel when Decode=false, got %v", argsCPU)
	}
	if !slices.Contains(argsCPU, "scale=1280:720,fps=30,format=nv12,hwupload") {
		t.Fatalf("expected hwupload filter chain, got %v", argsCPU)
	}
}

func TestBuildArgsOrdinaryQualityInvarianceAndAudioPolicy(t *testing.T) {
	base := Request{
		Source: "in.mp4", Output: "out.mp4", FFmpeg: "ffmpeg",
		Settings:    config.Factory(),
		Encoder:     Encoder{Name: "libx265", Backend: "software", Preset: "fast", Quantizer: 32},
		Media:       Media{Width: 1920, Height: 1080, Duration: 60, HasAudio: true, AudioCodec: "aac", AudioKbps: 128},
		RateControl: "quality",
	}
	base.Settings.AudioSource = true

	args, err := BuildArgs(base)
	if err != nil {
		t.Fatalf("BuildArgs failed: %v", err)
	}
	if idx := indexOf(args, "-crf"); idx < 0 || args[idx+1] != "32" {
		t.Fatalf("expected -crf 32, got %v", args)
	}
	if idx := indexOf(args, "-c:a"); idx < 0 || args[idx+1] != "copy" {
		t.Fatalf("expected source AAC copy, got %v", args)
	}
	for _, forbidden := range []string{"-b:v", "-maxrate", "-bufsize", "-vf", "-pass:v", "-passlogfile:v", "-x265-params"} {
		if slices.Contains(args, forbidden) {
			t.Fatalf("unexpected flag %s in ordinary quality mode: %v", forbidden, args)
		}
	}

	smallTarget := base
	argsSmall, err := BuildArgs(smallTarget)
	if err != nil || !slices.Equal(args, argsSmall) {
		t.Fatalf("ordinary mode must ignore target size: %v vs %v", args, argsSmall)
	}

	above := base
	above.Media.AudioKbps = 129
	argsAbove, err := BuildArgs(above)
	if err != nil {
		t.Fatal(err)
	}
	if idx := indexOf(argsAbove, "-c:a"); idx < 0 || argsAbove[idx+1] != "copy" || slices.Contains(argsAbove, "-b:a") {
		t.Fatalf("expected source AAC copy regardless of bitrate, got %v", argsAbove)
	}

	webmCopy := base
	webmCopy.Settings.Container = "webm"
	webmCopy.Settings.Codec = "av1"
	webmCopy.Encoder = Encoder{Name: "libsvtav1", Preset: "8", Quantizer: 38}
	webmCopy.Media.AudioCodec = "opus"
	webmCopy.Media.AudioKbps = 96
	argsWebmCopy, err := BuildArgs(webmCopy)
	if err != nil {
		t.Fatal(err)
	}
	if idx := indexOf(argsWebmCopy, "-c:a"); idx < 0 || argsWebmCopy[idx+1] != "copy" {
		t.Fatalf("expected opus copy in webm, got %v", argsWebmCopy)
	}

	webmEnc := webmCopy
	webmEnc.Media.AudioCodec = "aac"
	argsWebmEnc, err := BuildArgs(webmEnc)
	if err != nil {
		t.Fatal(err)
	}
	if idx := indexOf(argsWebmEnc, "-c:a"); idx < 0 || argsWebmEnc[idx+1] != "libopus" {
		t.Fatalf("expected libopus in webm, got %v", argsWebmEnc)
	}
	if idx := indexOf(argsWebmEnc, "-b:a"); idx < 0 || argsWebmEnc[idx+1] != "128k" {
		t.Fatalf("expected -b:a 128k, got %v", argsWebmEnc)
	}

	noAudio := base
	noAudio.Media.HasAudio = false
	argsNoAudio, err := BuildArgs(noAudio)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(argsNoAudio, "-an") || slices.Contains(argsNoAudio, "0:a:0") {
		t.Fatalf("expected -an and no audio map, got %v", argsNoAudio)
	}
}

func TestBuildArgsResolutionValidationAndSampleWindow(t *testing.T) {
	req := Request{
		Source: "in.mp4", Output: "out.mp4", FFmpeg: "ffmpeg",
		Settings:      config.Factory(),
		Encoder:       Encoder{Name: "libx264", Preset: "fast", Quantizer: 27},
		Media:         Media{Width: 1280, Height: 720, Duration: 60},
		RateControl:   "quality",
		StartSeconds:  10.5,
		LengthSeconds: 2.0,
	}

	for _, bad := range []string{"1081x1081", "20000x8", "0x720", "abc", "640x"} {
		badReq := req
		badReq.Settings.Resolution = bad
		if _, err := BuildArgs(badReq); err == nil {
			t.Fatalf("expected invalid resolution %q to fail", bad)
		}
	}

	req.Settings.Resolution = "sd"
	args, err := BuildArgs(req)
	if err != nil {
		t.Fatal(err)
	}
	if idx := indexOf(args, "-ss"); idx < 0 || args[idx+1] != "10.5" {
		t.Fatalf("expected -ss 10.5, got %v", args)
	}
	if idx := indexOf(args, "-t"); idx < 0 || args[idx+1] != "2" {
		t.Fatalf("expected -t 2, got %v", args)
	}
	if idx := indexOf(args, "-vf"); idx < 0 || args[idx+1] != "scale=640:360" {
		t.Fatalf("expected scale=640:360, got %v", args)
	}
}

func TestBuildArgsABRTwoPassX265X264AndHardwareSinglePass(t *testing.T) {
	rawPrefix := `C:\tmp\pressurecooker:1'a`
	p1, err := BuildArgs(Request{
		Source: "in.mp4", Output: "out.mp4", FFmpeg: "ffmpeg",
		Settings:    config.Factory(),
		Encoder:     Encoder{Name: "libx265", Preset: "fast"},
		RateControl: "abr", VideoKbps: 1500, Pass: 1, PassLog: rawPrefix,
	})
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(p1, "-passlogfile:v") || slices.Contains(p1, "-passlogfile") {
		t.Fatalf("libx265 must not use -passlogfile: %v", p1)
	}
	idx := indexOf(p1, "-x265-params")
	if idx < 0 {
		t.Fatalf("expected -x265-params in %v", p1)
	}
	wantParam := `pass=1:stats=C\:\\tmp\\pressurecooker\:1\'a`
	if p1[idx+1] != wantParam {
		t.Fatalf("escaped x265-params = %q, want %q", p1[idx+1], wantParam)
	}
	if p1[len(p1)-1] != os.DevNull || !slices.Contains(p1, "-an") {
		t.Fatalf("expected pass 1 null output with -an: %v", p1)
	}

	p2, err := BuildArgs(Request{
		Source: "in.mp4", Output: "out.mp4", FFmpeg: "ffmpeg",
		Settings:    config.Factory(),
		Encoder:     Encoder{Name: "libx264", Preset: "fast"},
		RateControl: "abr", VideoKbps: 1500, Pass: 2, PassLog: "/tmp/prefix",
	})
	if err != nil {
		t.Fatal(err)
	}
	if i := indexOf(p2, "-pass:v"); i < 0 || p2[i+1] != "2" {
		t.Fatalf("expected -pass:v 2, got %v", p2)
	}
	if i := indexOf(p2, "-passlogfile:v"); i < 0 || p2[i+1] != "/tmp/prefix" {
		t.Fatalf("expected -passlogfile:v /tmp/prefix, got %v", p2)
	}

	qsvABR, err := BuildArgs(Request{
		Source: "in.mp4", Output: "out.mp4", FFmpeg: "ffmpeg",
		Settings:    config.Factory(),
		Encoder:     Encoder{Name: "hevc_qsv", Backend: "QSV", Preset: "fast", Hardware: true},
		RateControl: "abr", VideoKbps: 1200, Pass: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(qsvABR, "-pass:v") || slices.Contains(qsvABR, "-passlogfile:v") || slices.Contains(qsvABR, "-x265-params") {
		t.Fatalf("QSV ABR must not emit pass flags: %v", qsvABR)
	}
	if i := indexOf(qsvABR, "-b:v"); i < 0 || qsvABR[i+1] != "1200k" {
		t.Fatalf("expected -b:v 1200k, got %v", qsvABR)
	}
}

func TestFormatCommandWindowsAndPOSIX(t *testing.T) {
	win := FormatCommand([]string{"ffmpeg", "-i", "a b's.mp4"}, "windows")
	if !strings.HasPrefix(win, "& ") || !strings.Contains(win, "'a b''s.mp4'") {
		t.Fatalf("unexpected Windows PowerShell formatting: %q", win)
	}
	posix := FormatCommand([]string{"ffmpeg", "-i", "a b.mp4"}, "linux")
	if posix != "ffmpeg -i 'a b.mp4'" {
		t.Fatalf("unexpected POSIX shlex formatting: %q", posix)
	}
}

func TestEncodeEmptyOutputRefused(t *testing.T) {
	out := filepath.Join(t.TempDir(), "out.mp4")
	r := &fakeRunner{fn: func(_ context.Context, args []string, _ func(console.Event)) (Result, error) {
		return Result{ExitCode: 0}, nil
	}}
	_, err := Encode(context.Background(), r, Request{
		Source: "in.mp4", Output: out, FFmpeg: "ffmpeg", FFprobe: "ffprobe",
		Settings:    config.Factory(),
		Encoder:     Encoder{Name: "libx265", Preset: "fast", Quantizer: 32},
		RateControl: "quality",
	}, nil)
	if err == nil || !strings.Contains(err.Error(), "no output") {
		t.Fatalf("expected empty output error, got %v", err)
	}
}

func TestEncodeEmptyPassStatsStopsBeforeSecondPassAndCleansLogs(t *testing.T) {
	tmpDir := t.TempDir()
	passLog := filepath.Join(tmpDir, "run-1")
	otherLog := filepath.Join(tmpDir, "run-10-0.log")
	if err := os.WriteFile(otherLog, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}

	r := &fakeRunner{fn: func(_ context.Context, args []string, _ func(console.Event)) (Result, error) {
		_ = os.WriteFile(passLog+"-0.log", nil, 0o600)
		return Result{ExitCode: 0}, nil
	}}
	_, err := Encode(context.Background(), r, Request{
		Source: "in.mp4", Output: filepath.Join(tmpDir, "out.mp4"), FFmpeg: "ffmpeg", FFprobe: "ffprobe",
		Settings:    config.Factory(),
		Encoder:     Encoder{Name: "libx264", Preset: "fast"},
		RateControl: "abr", VideoKbps: 1000, PassLog: passLog,
	}, nil)
	if err == nil || !strings.Contains(err.Error(), "two-pass statistics") {
		t.Fatalf("expected missing stats error, got %v", err)
	}
	if len(r.calls) != 1 {
		t.Fatalf("expected pass 2 not to run, got %d calls", len(r.calls))
	}
	if _, statErr := os.Stat(passLog + "-0.log"); !os.IsNotExist(statErr) {
		t.Fatalf("expected run pass log to be cleaned up")
	}
	if _, statErr := os.Stat(otherLog); statErr != nil {
		t.Fatalf("other run's pass log must not be deleted: %v", statErr)
	}
}

func TestEncodeTwoPassProgressAndCancelCleanup(t *testing.T) {
	tmpDir := t.TempDir()
	passLog := filepath.Join(tmpDir, "run-cancel")
	ctx, cancel := context.WithCancel(context.Background())

	var stages []string
	r := &fakeRunner{fn: func(ctx context.Context, args []string, onEvent func(console.Event)) (Result, error) {
		if !slices.Contains(args, "-progress") || !slices.Contains(args, "-nostats") {
			t.Fatalf("expected -progress pipe:1 -nostats in encode args: %v", args)
		}
		if onEvent != nil {
			onEvent(console.Event{Timeline: 1.0, Speed: 2.0})
		}
		if slices.Contains(args, "pass=1:stats="+escapeX265Param(passLog)) {
			_ = os.WriteFile(passLog, []byte("stats"), 0o600)
			_ = os.WriteFile(passLog+".cutree", []byte("tree"), 0o600)
			return Result{ExitCode: 0}, nil
		}
		cancel()
		return Result{ExitCode: 1}, ctx.Err()
	}}

	_, err := Encode(ctx, r, Request{
		Source: "in.mp4", Output: filepath.Join(tmpDir, "out.mp4"), FFmpeg: "ffmpeg", FFprobe: "ffprobe",
		Settings:    config.Factory(),
		Encoder:     Encoder{Name: "libx265", Preset: "fast"},
		Media:       Media{Duration: 10},
		RateControl: "abr", VideoKbps: 800, PassLog: passLog,
	}, func(ev console.Event) {
		stages = append(stages, ev.Stage)
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
	if !slices.Equal(stages, []string{"Pass 1/2", "Pass 2/2"}) {
		t.Fatalf("expected Pass 1/2 and Pass 2/2 events, got %v", stages)
	}
	for _, leftover := range []string{passLog, passLog + ".cutree"} {
		if _, statErr := os.Stat(leftover); !os.IsNotExist(statErr) {
			t.Fatalf("expected pass log %s cleaned on cancel", leftover)
		}
	}
}

func TestReadStderrTailBoundedTo15Lines(t *testing.T) {
	var b strings.Builder
	for i := 1; i <= 40; i++ {
		b.WriteString("line-xxx\n")
	}
	b.WriteString("last-line\n")
	tail := readStderrTail(strings.NewReader(b.String()), defaultStderrTail)
	if len(tail) != 15 {
		t.Fatalf("expected 15 lines, got %d", len(tail))
	}
	if tail[len(tail)-1] != "last-line" {
		t.Fatalf("expected last-line at end, got %q", tail[len(tail)-1])
	}
}
