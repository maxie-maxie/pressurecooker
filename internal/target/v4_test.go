package target

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/maxie-maxie/pressurecooker/internal/config"
	"github.com/maxie-maxie/pressurecooker/internal/console"
	"github.com/maxie-maxie/pressurecooker/internal/ffmpeg"
)

func TestCheckFitAllAudioCodecs(t *testing.T) {
	for _, codec := range config.AudioCodecs {
		m := ffmpeg.Media{Duration: 60, HasAudio: true, AudioCodec: strings.ToUpper(codec), AudioKbps: 32.4}
		s := config.Factory()
		s.AudioSource, s.AudioCodec = true, codec
		fit := CheckFit(m, s, 2_000_000)
		if !fit.Fits || !fit.AudioCopy || fit.AudioKbps != 32 || fit.VideoKbps != 226 {
			t.Fatalf("%s: %+v", codec, fit)
		}
	}
}

func TestSizeModeCodecGuards(t *testing.T) {
	for _, codec := range []string{"prores", "ffv1"} {
		req := baseTestRequest(t.TempDir(), 60)
		req.Settings.Codec = codec
		r := newFakeRunner(false)
		want := codec + " supports conversion only; size mode is unsupported"
		fit := CheckFit(req.Media, req.Settings, 1_000_000)
		if fit.Fits || fit.Reason != want {
			t.Fatal(fit)
		}
		if _, err := FitEncode(context.Background(), r, req, Fit{Fits: true, VideoKbps: 100}, 1_000_000, nil); err == nil || err.Error() != want {
			t.Fatal(err)
		}
		if _, err := ForceFast(context.Background(), r, req, 1_000_000, "", nil); err == nil || err.Error() != want {
			t.Fatal(err)
		}
		if len(r.recordedArgs) != 0 {
			t.Fatal("conversion-only codec ran a tool")
		}
	}
}

func TestSizeExplicitAudioCodecCopyRule(t *testing.T) {
	for _, force := range []bool{false, true} {
		for _, explicit := range []bool{false, true} {
			for _, codec := range []string{"opus", "aac"} {
				req := baseTestRequest(t.TempDir(), 60)
				req.Settings.AudioSource, req.Settings.AudioCodec = true, codec
				req.Media.AudioKbps = 32
				req.Origins["audio_codec"] = "preset"
				if explicit {
					req.Origins["audio_codec"] = "explicit"
				}
				r := newFakeRunner(false)
				var out Outcome
				var err error
				if force {
					out, err = ForceFast(context.Background(), r, req, 2_000_000, "", nil)
				} else {
					out, err = FitEncode(context.Background(), r, req, CheckFit(req.Media, req.Settings, 2_000_000), 2_000_000, nil)
				}
				wantCopy := !explicit || codec == "aac"
				if err != nil || out.Settings.AudioSource != wantCopy {
					t.Fatalf("force=%v explicit=%v codec=%s: %+v %v", force, explicit, codec, out, err)
				}
				for _, args := range r.recordedArgs {
					if args[len(args)-1] == req.Output && slices.Contains(args, "-c:a") {
						ac := args[slices.Index(args, "-c:a")+1]
						if (ac == "copy") != wantCopy {
							t.Fatal(args)
						}
					}
				}
			}
		}
	}
}

func TestExactFLACBudgetAndCleanup(t *testing.T) {
	for _, force := range []bool{false, true} {
		for _, mode := range []string{"fits", "infeasible", "unknown source", "known source", "failure", "cancel"} {
			t.Run(fmt.Sprintf("force=%v/%s", force, mode), func(t *testing.T) {
				req := baseTestRequest(t.TempDir(), 60)
				req.Settings.AudioCodec, req.Settings.AudioKbps = "flac", 0
				req.Origins["audio_codec"] = "explicit"
				if strings.Contains(mode, "source") {
					req.Settings.AudioSource = true
					req.Media.AudioCodec, req.Media.AudioKbps = "flac", 0
					if mode == "known source" {
						req.Media.AudioKbps = 16.4
					}
				}
				original := cloneRequest(req)
				r := newFakeRunner(false)
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				flacBytes := int64(100_000)
				if mode == "infeasible" {
					flacBytes = 1_000_000
				}
				var measuredPath string
				var bitrates []int
				r.onEncode = func(args []string, path string) (int64, int64, int64, float64, error) {
					if slices.Contains(args, "-vn") {
						measuredPath = path
						if mode == "failure" || mode == "cancel" {
							_ = os.WriteFile(path, []byte("partial"), 0o600)
							if mode == "cancel" {
								cancel()
							}
							return 0, 0, 0, 0, errors.New("measurement failed")
						}
						return flacBytes, 0, flacBytes, 60, nil
					}
					if slices.Contains(args, "-b:v") {
						bitrates = append(bitrates, parseVideoKbpsFromArgs(args))
					}
					return 900_000, 800_000, 100_000, 60, nil
				}
				var out Outcome
				var err error
				if force {
					out, err = ForceFast(ctx, r, req, 1_000_000, "", nil)
				} else {
					out, err = FitEncode(ctx, r, req, CheckFit(req.Media, req.Settings, 1_000_000), 1_000_000, nil)
				}
				if !reflect.DeepEqual(req, original) {
					t.Fatal("caller changed")
				}
				if measuredPath != "" {
					if _, err := os.Stat(measuredPath); !os.IsNotExist(err) {
						t.Fatal("FLAC measurement retained")
					}
				}
				if mode == "failure" || mode == "cancel" {
					if err == nil || out.FullEncodes != 0 || out.SampleEncodes != 0 {
						t.Fatalf("%+v %v", out, err)
					}
					if mode == "cancel" && !errors.Is(err, context.Canceled) {
						t.Fatal(err)
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				if mode == "infeasible" {
					if out.Infeasible != "FLAC audio needs 1.00 MB for 1:00; target is 1.00 MB" || out.FullEncodes != 0 || out.SampleEncodes != 0 || len(bitrates) != 0 {
						t.Fatal(out)
					}
					return
				}
				wantVideo := 116
				if mode == "known source" {
					wantVideo = 112
					if measuredPath != "" || !out.Settings.AudioSource {
						t.Fatal("known FLAC copy needs only stream bitrate")
					}
				} else if measuredPath == "" {
					t.Fatal("missing exact FLAC measurement")
				}
				if out.File != req.Output || len(bitrates) == 0 || bitrates[len(bitrates)-1] != wantVideo || out.FullEncodes > 3 || out.SampleEncodes > 3 {
					t.Fatalf("bitrates=%v %+v", bitrates, out)
				}
			})
		}
	}
}

func TestForceFormatSelection(t *testing.T) {
	for _, tc := range []struct {
		name, container, codec, audio       string
		origins                             config.Origins
		encoders                            string
		wantCodec, wantContainer, wantAudio string
	}{
		{name: "directory", container: "mkv", codec: "h264", audio: "flac", origins: config.Origins{"codec": "latest", "container": "preset", "audio_codec": "latest"}, wantCodec: "av1", wantContainer: "mp4", wantAudio: "opus"},
		{name: "ogg", container: "ogg", origins: config.Origins{"container": "extension"}, wantCodec: "theora", wantContainer: "ogg", wantAudio: "opus"},
		{name: "mov", container: "mov", origins: config.Origins{"container": "locked"}, wantCodec: "hevc", wantContainer: "mov", wantAudio: "aac"},
		{name: "webm", container: "webm", origins: config.Origins{"container": "explicit"}, wantCodec: "av1", wantContainer: "webm", wantAudio: "opus"},
		{name: "flags", container: "avi", codec: "vp9", audio: "mp3", origins: config.Origins{"codec": "explicit", "container": "explicit", "audio_codec": "explicit"}, wantCodec: "vp9", wantContainer: "avi", wantAudio: "mp3"},
		{name: "explicit codec directory", container: "mkv", codec: "h264", origins: config.Origins{"codec": "explicit"}, wantCodec: "h264", wantContainer: "mp4", wantAudio: "opus"},
		{name: "missing av1", container: "mp4", encoders: " V..... libx265 HEVC\n V..... libx264 H264\n", wantCodec: "hevc", wantContainer: "mp4", wantAudio: "opus"},
		{name: "missing av1 hevc", container: "mp4", encoders: " V..... libvpx-vp9 VP9\n V..... libx264 H264\n", wantCodec: "vp9", wantContainer: "webm", wantAudio: "opus"},
		{name: "only h264", container: "mp4", encoders: " V..... libx264 H264\n", wantCodec: "h264", wantContainer: "mp4", wantAudio: "opus"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := baseTestRequest(t.TempDir(), 60)
			req.Settings.Container = tc.container
			if tc.codec != "" {
				req.Settings.Codec = tc.codec
			}
			if tc.audio != "" {
				req.Settings.AudioCodec = tc.audio
			}
			req.Origins = tc.origins
			original := cloneRequest(req)
			r := newFakeRunner(false)
			r.encoderList = tc.encoders
			var summary string
			out, err := ForceFast(context.Background(), r, req, 5_000_000, "", func(ev console.Event) {
				if strings.HasPrefix(ev.Stage, "Force-target fixed:") {
					summary = ev.Stage
				}
			})
			if err != nil || out.File != req.Output || out.Settings.Codec != tc.wantCodec || out.Settings.Container != tc.wantContainer || out.Settings.AudioCodec != tc.wantAudio || out.Settings.HW != "off" {
				t.Fatalf("%+v %v", out, err)
			}
			if !reflect.DeepEqual(req, original) || out.FullEncodes > 3 || out.SampleEncodes > 3 || r.encoderCalls > 1 {
				t.Fatal("mutation or bounds")
			}
			for _, field := range []string{"codec", "container", "audio_codec"} {
				if !strings.Contains(summary, field) {
					t.Fatal(summary)
				}
			}
			for field, changed := range map[string]bool{"codec": req.Settings.Codec != out.Settings.Codec, "container": req.Settings.Container != out.Settings.Container, "audio_codec": req.Settings.AudioCodec != out.Settings.AudioCodec} {
				if slices.Contains(out.Changed, field) != changed {
					t.Fatalf("%s: %v", field, out.Changed)
				}
			}
			for _, args := range r.recordedArgs {
				if slices.Contains(args, "-b:v") && !isPass1Args(args) && args[len(args)-1] != req.Output && filepath.Ext(args[len(args)-1]) != "."+tc.wantContainer {
					t.Fatal(args)
				}
			}
		})
	}
}

func TestForceGPUThresholds(t *testing.T) {
	for _, metric := range []string{"xpsnr", "vmaf", "ssim"} {
		threshold := 0.5
		if metric == "vmaf" {
			threshold = 1
		}
		for _, oneCandidate := range []bool{false, true} {
			for _, gpuWins := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/one=%v/gpu=%v", metric, oneCandidate, gpuWins), func(t *testing.T) {
					req := baseTestRequest(t.TempDir(), 60)
					req.Settings.HW = "auto"
					req.Origins["codec"] = "explicit"
					req.Origins["audio"] = "explicit"
					if oneCandidate {
						req.Origins["resolution"], req.Origins["fps"] = "explicit", "explicit"
					}
					r := newFakeRunner(metric == "vmaf")
					r.hasXPSNR = metric == "xpsnr"
					r.hwEncoders = map[string]bool{"hevc_qsv": true}
					gpuSamples := 0
					r.onEncode = func(args []string, path string) (int64, int64, int64, float64, error) {
						if slices.Contains(args, "hevc_qsv") && path != req.Output {
							gpuSamples++
							if parseVideoKbpsFromArgs(args) != sizeVideoKbps(ffmpeg.Encoder{Hardware: true}, checkFit(req.Media, req.Settings, 5_000_000, req.Origins, nil).VideoKbps) || parseFPSFromArgs(args, req.Media.FPS) != req.Media.FPS {
								t.Fatal("GPU must use top rung and hardware headroom")
							}
						}
						return 900_000, 880_000, 10_000, 60, nil
					}
					r.onScore = func(args []string, sample, metric, log string) ([]float64, error) {
						gpu := false
						for _, encode := range r.recordedArgs {
							if encode[len(encode)-1] == sample && slices.Contains(encode, "hevc_qsv") {
								gpu = true
							}
						}
						score := 40.0
						if gpu {
							score -= threshold
							if !gpuWins {
								score -= 0.01
							}
						}
						return []float64{score, score, score}, nil
					}
					requested := metric
					if metric == "ssim" {
						requested = ""
					}
					out, err := ForceFast(context.Background(), r, req, 5_000_000, requested, nil)
					wantHW := "off"
					if gpuWins {
						wantHW = "qsv"
					}
					wantSamples := 3
					if oneCandidate {
						wantSamples = 2
					}
					if err != nil || out.File != req.Output || out.Settings.HW != wantHW || gpuSamples != 1 || out.SampleEncodes != wantSamples || out.FullEncodes > 3 {
						t.Fatalf("%+v %v GPU samples=%d", out, err, gpuSamples)
					}
					for _, args := range r.recordedArgs {
						if args[len(args)-1] == req.Output && slices.Contains(args, "-b:v") && slices.Contains(args, "hevc_qsv") != gpuWins {
							t.Fatal(args)
						}
					}
				})
			}
		}
	}
}

func TestGPUThresholdUsesBestGuardedSoftwareScore(t *testing.T) {
	sw := ffmpeg.Encoder{Name: "libx265"}
	gpu := ffmpeg.Encoder{Name: "hevc_qsv", Hardware: true}
	scored := []scoredCandidate{
		{cand: candidate{fps: 60}, mean: 40, p05: 40, encoder: sw},
		{cand: candidate{fps: 30}, mean: 40.9, p05: 40.9, encoder: sw},
		{cand: candidate{fps: 60}, mean: 40.3, p05: 40.3, encoder: gpu},
	}
	if winner := rankEncoderCandidates(scored, metricXPSNR); winner.encoder.Hardware {
		t.Fatal("GPU compared against the software FPS tie-break instead of best quality")
	}
	scored[2].mean, scored[2].p05 = 50, 20
	if winner := rankEncoderCandidates(scored, metricXPSNR); winner.encoder.Hardware {
		t.Fatal("GPU escaped the percentile guard")
	}
}

func TestForceExplicitFormatsAndHWErrors(t *testing.T) {
	for _, mode := range []string{"video mismatch", "audio mismatch", "missing encoder", "explicit GPU", "explicit off"} {
		req := baseTestRequest(t.TempDir(), 60)
		r := newFakeRunner(false)
		req.Encoder = ffmpeg.Encoder{}
		switch mode {
		case "video mismatch":
			req.Settings.Container = "ogg"
			req.Origins = config.Origins{"codec": "explicit", "container": "explicit"}
		case "audio mismatch":
			req.Settings.Container, req.Settings.AudioCodec = "ogg", "aac"
			req.Origins = config.Origins{"container": "explicit", "audio_codec": "explicit"}
		case "missing encoder":
			r.encoderList = " V..... rawvideo RAW\n"
		case "explicit GPU":
			req.Settings.HW = "qsv"
			req.Origins = config.Origins{"codec": "explicit", "hw": "explicit"}
		case "explicit off":
			req.Settings.HW = "off"
			req.Origins["hw"] = "explicit"
		}
		out, err := ForceFast(context.Background(), r, req, 5_000_000, "", nil)
		if mode == "explicit off" {
			if err != nil || out.Settings.HW != "off" || out.File != req.Output {
				t.Fatalf("%+v %v", out, err)
			}
		} else if err == nil || out.SampleEncodes != 0 || out.FullEncodes != 0 {
			t.Fatalf("%s: %+v %v", mode, out, err)
		}
	}
}
