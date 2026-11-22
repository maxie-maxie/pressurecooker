package ffmpeg

import (
	"context"
	"errors"
	"math"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/maxie-maxie/pressurecooker/internal/config"
	"github.com/maxie-maxie/pressurecooker/internal/console"
)

func TestNewVideoArgsGolden(t *testing.T) {
	for _, tc := range []struct {
		codec, encoder, flags string
	}{
		{"vp9", "libvpx-vp9", "-cpu-used 4 -crf 32 -pix_fmt yuv420p -b:v 0"},
		{"prores", "prores_ks", "-profile:v 2 -pix_fmt yuv422p10le"},
		{"ffv1", "ffv1", "-level 3"},
		{"theora", "libtheora", "-q:v 6 -pix_fmt yuv420p"},
	} {
		t.Run(tc.codec, func(t *testing.T) {
			s := config.Settings{Codec: tc.codec, Container: config.Recommended(tc.codec), Preset: "fast", Quality: "balanced"}
			args, err := BuildArgs(Request{Source: "source.mkv", Output: "output.part", Settings: s, Encoder: Encoder{Name: tc.encoder}})
			if err != nil {
				t.Fatal(err)
			}
			want := strings.Fields("ffmpeg -nostdin -y -hide_banner -i source.mkv -map 0:v:0 -c:v " + tc.encoder + " " + tc.flags + " -an -f " + muxerByContainer[s.Container] + " output.part")
			if !slices.Equal(args, want) {
				t.Fatalf("args = %v; want %v", args, want)
			}
		})
	}
}

func TestNewQualityMappings(t *testing.T) {
	for _, tc := range []struct {
		encoder string
		values  []float64
	}{
		{"libvpx-vp9", []float64{24, 32, 40}}, {"vp9_qsv", []float64{24, 32, 40}},
		{"prores_ks", []float64{3, 2, 1}}, {"prores_ks_vulkan", []float64{3, 2, 1}},
		{"libtheora", []float64{8, 6, 4}},
	} {
		for i, level := range []string{"high", "balanced", "small"} {
			q, err := QualityQuantizer(level, tc.encoder)
			if err != nil || q != tc.values[i] {
				t.Fatalf("%s %s = %v, %v; want %v", tc.encoder, level, q, err, tc.values[i])
			}
		}
	}
}

func TestVP9SpeedAndCRFBounds(t *testing.T) {
	for i, name := range []string{"ultrafast", "superfast", "veryfast", "faster", "fast", "medium", "slow", "slower", "veryslow", "placebo"} {
		speed, err := config.ResolvePreset(name, "vp9", false)
		if err != nil || speed != strconv.Itoa(max(0, 8-i)) {
			t.Fatalf("%s = %s, %v", name, speed, err)
		}
	}
	for i := 0; i <= 8; i++ {
		if speed, err := config.ResolvePreset(strconv.Itoa(i), "vp9", false); err != nil || speed != strconv.Itoa(i) {
			t.Fatalf("speed %d = %s, %v", i, speed, err)
		}
	}
	for _, speed := range []string{"-1", "9", "slowest", "1.5", "99999999999999999999"} {
		if _, err := config.ResolvePreset(speed, "vp9", false); err == nil {
			t.Fatalf("accepted speed %s", speed)
		}
	}
	for _, crf := range []float64{0, 63} {
		s := config.Settings{Codec: "vp9", Container: "webm", Preset: "8", HW: "auto", CRF: &crf}
		enc, err := ChooseEncoder(context.Background(), nil, "", s)
		if err != nil || enc.Hardware || enc.Quantizer != crf {
			t.Fatalf("CRF %v: %+v, %v", crf, enc, err)
		}
		args, err := BuildArgs(Request{Source: "source.mkv", Output: "output.webm", Settings: s, Encoder: enc})
		if err != nil || args[indexOf(args, "-crf")+1] != formatFloat(crf) {
			t.Fatalf("CRF args %v, %v", args, err)
		}
	}
	for _, crf := range []float64{-1, 64, math.NaN(), math.Inf(1)} {
		if err := validateCRF(crf, "vp9"); err == nil {
			t.Fatalf("accepted CRF %v", crf)
		}
	}
}

func TestNewVideoABRAndConversionGuards(t *testing.T) {
	for _, codec := range []string{"vp9", "theora"} {
		for _, pass := range []int{1, 2} {
			req := Request{Source: "source.mkv", Output: "output.part", Settings: config.Settings{Codec: codec, Container: config.Recommended(codec), Preset: "8"}, Encoder: Encoder{Name: softwareEncoders[codec]}, RateControl: "abr", VideoKbps: 250, Pass: pass, PassLog: "stats"}
			args, err := BuildArgs(req)
			if err != nil {
				t.Fatal(err)
			}
			for flag, want := range map[string]string{"-b:v": "250k", "-pix_fmt": "yuv420p", "-pass:v": strconv.Itoa(pass), "-passlogfile:v": "stats"} {
				if i := indexOf(args, flag); i < 0 || args[i+1] != want {
					t.Fatalf("missing %s %s in %v", flag, want, args)
				}
			}
			if slices.Contains(args, "-crf") || slices.Contains(args, "-q:v") || slices.Contains(args, "-preset") {
				t.Fatalf("unexpected quality/preset flags in %v", args)
			}
			if pass == 1 && args[len(args)-1] != os.DevNull {
				t.Fatalf("pass 1 output: %v", args)
			}
		}
	}
	for _, codec := range []string{"prores", "ffv1", "theora"} {
		s := config.Settings{Codec: codec, Container: config.Recommended(codec), HW: "off", CRF: f64ptr(0)}
		if _, err := ChooseEncoder(context.Background(), nil, "", s); err == nil || !strings.Contains(err.Error(), "unsupported") {
			t.Fatalf("%s accepted CRF: %v", codec, err)
		}
		req := Request{Source: "source", Output: "output", Settings: s, Encoder: Encoder{Name: softwareEncoders[codec]}}
		if _, err := BuildArgs(req); err == nil {
			t.Fatalf("%s BuildArgs accepted CRF", codec)
		}
		if codec != "theora" {
			req.Settings.CRF = nil
			req.RateControl, req.VideoKbps = "abr", 250
			if _, err := BuildArgs(req); err == nil || !strings.Contains(err.Error(), "conversion only") {
				t.Fatalf("%s accepted size mode: %v", codec, err)
			}
		}
	}
}

func TestContainerMuxersAndNormalization(t *testing.T) {
	for _, container := range config.Containers {
		codec := "h264"
		if container == "webm" {
			codec = "vp9"
		} else if container == "ogg" {
			codec = "theora"
		}
		s := config.Settings{Codec: codec, Container: container, Preset: "fast"}
		args, err := BuildArgs(Request{Source: "source", Output: "output.part", Settings: s, Encoder: Encoder{Name: softwareEncoders[codec]}})
		if err != nil || args[len(args)-2] != muxerByContainer[container] || slices.Contains(args, "-movflags") != (container == "mp4") {
			t.Fatalf("%s: %v, %v", container, args, err)
		}
	}
	for _, tc := range []struct{ format, path, want string }{
		{"mov,mp4,m4a,3gp,3g2,mj2", "output.mov", "mov"}, {"mov,mp4,m4a", "output.QT", "mov"},
		{"mov,mp4,m4a", "output.mp4", "mp4"}, {"avi", "output.part", "avi"},
		{"ogg", "output.ogv", "ogg"}, {"mpegts", "output.part", "ts"}, {"nut", "output.part", "nut"},
		{"matroska,webm", "output.webm", "webm"}, {"matroska,webm", "output.mkv", "mkv"},
		{"", "output.qt", "mov"}, {"", "output.ogv", "ogg"}, {"", "output.ts", "ts"},
	} {
		if got := normalizeContainer(tc.format, tc.path); got != tc.want {
			t.Fatalf("normalize(%s, %s) = %s; want %s", tc.format, tc.path, got, tc.want)
		}
	}
	for _, tc := range []struct{ container, codec string }{{"mov", "av1"}, {"webm", "h264"}, {"ogg", "vp9"}, {"invalid", "h264"}} {
		if _, err := BuildArgs(Request{Source: "source", Output: "output", Settings: config.Settings{Container: tc.container}, Encoder: Encoder{Name: softwareEncoders[tc.codec]}}); err == nil {
			t.Fatalf("accepted %s in %s", tc.codec, tc.container)
		}
	}
}

func TestAudioCodecArgsAndCopyPolicy(t *testing.T) {
	for _, tc := range []struct{ codec, flags string }{
		{"opus", "-c:a libopus -b:a 96k -vbr constrained"}, {"aac", "-c:a aac -b:a 96k"},
		{"mp3", "-c:a libmp3lame -b:a 96k"}, {"ac3", "-c:a ac3 -b:a 96k"}, {"flac", "-c:a flac"},
	} {
		s := config.Settings{AudioCodec: tc.codec, AudioKbps: 96}
		args, err := audioArgs("mkv", s, Media{HasAudio: true, AudioCodec: tc.codec}, nil)
		if err != nil || !slices.Equal(args, strings.Fields(tc.flags)) {
			t.Fatalf("%s: %v, %v", tc.codec, args, err)
		}
	}
	for _, tc := range []struct {
		name, container, target, source, want string
		explicit                              bool
	}{
		{"implicit-different", "mp4", "opus", "AAC", "-c:a copy", false},
		{"implicit-flac", "mkv", "opus", "flac", "-c:a copy", false},
		{"explicit-match", "mp4", "opus", "OPUS", "-c:a copy", true},
		{"explicit-different", "mp4", "opus", "aac", "-c:a libopus -b:a 128k -vbr constrained", true},
		{"explicit-flac", "mkv", "flac", "aac", "-c:a flac", true},
		{"empty-source", "mp4", "aac", "", "-c:a aac -b:a 128k", false},
		{"incompatible-source", "webm", "opus", "aac", "-c:a libopus -b:a 128k -vbr constrained", false},
		{"unknown-source", "mkv", "aac", "pcm_s16le", "-c:a aac -b:a 128k", false},
		{"empty-target", "mp4", "", "pcm_s16le", "-c:a libopus -b:a 128k -vbr constrained", false},
		{"mov-fallback", "mov", "", "pcm_s16le", "-c:a aac -b:a 128k", false},
		{"mov-explicit-incompatible", "mov", "opus", "aac", "error", true},
		{"unknown-target", "mkv", "invalid", "aac", "error", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			origins := config.Origins{"audio_codec": "preset"}
			if tc.explicit {
				origins["audio_codec"] = "explicit"
			}
			args, err := audioArgs(tc.container, config.Settings{AudioCodec: tc.target, AudioSource: true, AudioKbps: 512}, Media{HasAudio: true, AudioCodec: tc.source}, origins)
			if tc.want == "error" {
				if err == nil {
					t.Fatalf("accepted invalid audio: %v", args)
				}
			} else if err != nil || !slices.Equal(args, strings.Fields(tc.want)) {
				t.Fatalf("args = %v, %v; want %s", args, err, tc.want)
			}
		})
	}
	args, err := audioArgs("mkv", config.Settings{AudioCodec: "flac", AudioKbps: -1}, Media{HasAudio: true}, nil)
	if err != nil || !slices.Equal(args, []string{"-c:a", "flac"}) {
		t.Fatalf("FLAC bitrate must be ignored: %v, %v", args, err)
	}
	args, err = audioArgs("mkv", config.Settings{AudioCodec: "invalid"}, Media{}, nil)
	if err != nil || !slices.Equal(args, []string{"-an"}) {
		t.Fatalf("no audio: %v, %v", args, err)
	}
	req := Request{Source: "source", Output: "output", Settings: config.Settings{Container: "webm", Preset: "8", AudioCodec: "aac", AudioSource: true}, Encoder: Encoder{Name: "libvpx-vp9"}, Media: Media{HasAudio: true, AudioCodec: "opus"}, Origins: config.Origins{"audio_codec": "explicit"}}
	if _, err := BuildArgs(req); err == nil {
		t.Fatal("BuildArgs accepted explicit AAC in WebM")
	}
}

func TestChooseNewSoftwareAndUnsupportedHardware(t *testing.T) {
	for _, codec := range config.VideoCodecs {
		for _, hw := range []string{"off", "vulkan", "qsv"} {
			s := config.Settings{Codec: codec, HW: hw, Preset: "fast"}
			if hw == "off" {
				enc, err := ChooseEncoder(context.Background(), nil, "", s)
				if err != nil || enc.Name != softwareEncoders[codec] || enc.Hardware {
					t.Fatalf("%s: %+v, %v", codec, enc, err)
				}
			} else if !slices.Contains(config.CodecCaps(codec).HW, hw) {
				if _, err := ChooseEncoder(context.Background(), nil, "", s); err == nil || !strings.Contains(err.Error(), "does not support") {
					t.Fatalf("%s accepted %s: %v", codec, hw, err)
				}
			}
		}
	}
	if enc, err := ChooseEncoder(context.Background(), nil, "", config.Settings{Codec: "theora", HW: "auto"}); err != nil || enc.Name != "libtheora" {
		t.Fatalf("Theora auto must skip probing: %+v, %v", enc, err)
	}
	if _, err := ChooseEncoder(context.Background(), nil, "", config.Settings{Codec: "invalid"}); err == nil {
		t.Fatal("accepted unknown codec")
	}
}

func TestChooseVP9QSVAndFallback(t *testing.T) {
	s := config.Settings{Codec: "vp9", HW: "auto", Preset: "fast", Quality: "balanced"}
	for _, success := range []bool{true, false} {
		r := &fakeRunner{fn: func(_ context.Context, args []string, _ func(console.Event)) (Result, error) {
			if !slices.Contains(args, "vp9_qsv") || !hasFlagValue(args, "-preset", "fast") || !hasFlagValue(args, "-global_quality", "32") {
				t.Fatalf("QSV probe: %v", args)
			}
			if success {
				return Result{}, nil
			}
			return Result{ExitCode: 1, StderrTail: []string{"unsupported driver\nsecond line"}}, nil
		}}
		enc, err := ChooseEncoder(context.Background(), r, "ffmpeg", s)
		if err != nil || enc.Hardware != success || len(r.calls) != 1 {
			t.Fatalf("QSV success %v: %+v, %v, calls %d", success, enc, err, len(r.calls))
		}
		if !success && (enc.Name != "libvpx-vp9" || len(enc.Warnings) != 1 || strings.ContainsAny(enc.Warnings[0], "\r\n")) {
			t.Fatalf("QSV fallback: %+v", enc)
		}
		if !success {
			s.HW = "qsv"
			if _, err := ChooseEncoder(context.Background(), r, "ffmpeg", s); err == nil {
				t.Fatal("explicit QSV failure fell back")
			}
		}
	}
}

func TestNewVulkanProbeAndEncodeControls(t *testing.T) {
	for _, codec := range []string{"prores", "ffv1"} {
		s := config.Settings{Codec: codec, Container: config.Recommended(codec), HW: "vulkan"}
		r := &fakeRunner{}
		enc, err := ChooseEncoder(context.Background(), r, "ffmpeg", s)
		if err != nil || !enc.Hardware || enc.Name != vulkanEncoders[codec] {
			t.Fatalf("%s: %+v, %v", codec, enc, err)
		}
		args, err := BuildArgs(Request{Source: "source.mkv", Output: "output.part", Settings: s, Encoder: enc})
		if err != nil {
			t.Fatal(err)
		}
		for _, cmd := range [][]string{r.calls[0], args} {
			for _, flag := range []string{"-preset", "-crf", "-quality", "-rc_mode", "-qp", "-b:v"} {
				if slices.Contains(cmd, flag) {
					t.Fatalf("%s has unsupported %s: %v", codec, flag, cmd)
				}
			}
			if codec == "prores" && (!hasFlagValue(cmd, "-profile:v", "2") || !hasFlagValue(cmd, "-vf", "format=yuv422p10le,hwupload")) {
				t.Fatalf("ProRes controls: %v", cmd)
			}
			if codec == "ffv1" && (!hasFlagValue(cmd, "-level", "4") || !hasFlagValue(cmd, "-strict:v", "experimental") || !hasFlagValue(cmd, "-vf", "hwupload")) {
				t.Fatalf("FFV1 controls: %v", cmd)
			}
		}
	}
}

func TestProbeHardwareBackendsAndCancellation(t *testing.T) {
	for _, codec := range config.VideoCodecs {
		r := &fakeRunner{fn: func(_ context.Context, args []string, _ func(console.Event)) (Result, error) {
			if strings.HasSuffix(args[indexOf(args, "-c:v")+1], "_qsv") {
				return Result{ExitCode: 1, StderrTail: []string{"unsupported GPU\nmore details"}}, nil
			}
			return Result{}, nil
		}}
		availability, err := ProbeHardware(context.Background(), r, "ffmpeg", codec)
		if err != nil || len(availability) != len(config.CodecCaps(codec).HW) || len(r.calls) != len(availability) {
			t.Fatalf("%s: %v, %v, calls %d", codec, availability, err, len(r.calls))
		}
		for _, backend := range config.CodecCaps(codec).HW {
			reason, ok := availability[backend]
			if !ok || strings.ContainsAny(reason, "\r\n") || (backend == "qsv") != (reason != "") {
				t.Fatalf("%s %s: %q", codec, backend, reason)
			}
		}
	}
	if _, err := ProbeHardware(context.Background(), nil, "", "invalid"); err == nil {
		t.Fatal("accepted unknown codec")
	}
	if _, err := ProbeHardware(context.Background(), nil, "", "vp9"); err == nil {
		t.Fatal("accepted missing runner")
	}
	ctx, cancel := context.WithCancel(context.Background())
	r := &fakeRunner{fn: func(context.Context, []string, func(console.Event)) (Result, error) {
		cancel()
		return Result{}, nil
	}}
	if _, err := ProbeHardware(ctx, r, "ffmpeg", "av1"); !errors.Is(err, context.Canceled) || len(r.calls) != 1 {
		t.Fatalf("cancellation swallowed: %v, calls %d", err, len(r.calls))
	}
	r.calls = nil
	if _, err := ProbeHardware(ctx, r, "ffmpeg", "av1"); !errors.Is(err, context.Canceled) || len(r.calls) != 0 {
		t.Fatalf("pre-cancelled probe ran: %v", err)
	}
}
