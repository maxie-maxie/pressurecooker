package target

import (
	"context"
	"errors"
	"math"
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

func TestCheckFitAudioBudget(t *testing.T) {
	m := ffmpeg.Media{Duration: 600, HasAudio: true, AudioCodec: "aac", AudioKbps: 32.4}
	s := config.Settings{Container: "mp4", AudioSource: true, AudioKbps: 128}
	for _, tc := range []struct {
		name         string
		media        ffmpeg.Media
		settings     config.Settings
		target       int64
		audio, video int
		copy, fits   bool
	}{
		{"copy exact fractional audio", m, s, 20_000_000, 32, 226, true, true},
		{"copy MKV", m, config.Settings{Container: "mkv", AudioSource: true}, 20_000_000, 32, 226, true, true},
		{"copy Opus", ffmpeg.Media{Duration: 600, HasAudio: true, AudioCodec: "opus", AudioKbps: 32.4}, config.Settings{Container: "webm", AudioSource: true}, 20_000_000, 32, 226, true, true},
		{"one video kbps", ffmpeg.Media{Duration: 600}, s, 77_320, 0, 1, false, true},
		{"unknown audio reencode", ffmpeg.Media{Duration: 600, HasAudio: true, AudioCodec: "aac"}, s, 20_000_000, 48, 210, false, true},
		{"incompatible audio reencode", m, config.Settings{Container: "webm", AudioSource: true}, 20_000_000, 48, 210, false, true},
		{"128 share", ffmpeg.Media{Duration: 60, HasAudio: true, AudioCodec: "flac"}, s, 5_000_000, 128, 518, false, true},
		{"96 share", ffmpeg.Media{Duration: 60, HasAudio: true, AudioCodec: "flac"}, s, 4_000_000, 96, 421, false, true},
		{"64 share", ffmpeg.Media{Duration: 60, HasAudio: true, AudioCodec: "flac"}, s, 3_000_000, 64, 324, false, true},
		{"explicit audio exceeds budget", m, config.Settings{Container: "mp4", AudioKbps: 128}, 8_000_000, 128, 0, false, false},
		{"no audio", ffmpeg.Media{Duration: 600}, s, 20_000_000, 0, 258, false, true},
		{"48 floor exceeds budget", ffmpeg.Media{Duration: 600, HasAudio: true, AudioCodec: "flac"}, s, 100_000, 48, 0, false, false},
		{"less than one video kbps", ffmpeg.Media{Duration: 600}, s, 50_000, 0, 0, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fit := CheckFit(tc.media, tc.settings, tc.target)
			if fit.AudioKbps != tc.audio || fit.VideoKbps != tc.video || fit.AudioCopy != tc.copy || fit.Fits != tc.fits {
				t.Fatalf("got %+v", fit)
			}
			if (fit.Reason == "") != fit.Fits || strings.ContainsAny(fit.Reason, "\r\n") || strings.Contains(fit.Reason, "quality") {
				t.Fatalf("invalid numeric reason %q", fit.Reason)
			}
		})
	}
	fit := CheckFit(m, config.Settings{AudioKbps: 128}, 8_000_000)
	if fit.Reason != "Audio at 128 kbps needs 9.6 MB for 10:00; target is 8 MB" {
		t.Fatal(fit.Reason)
	}
	for _, duration := range []float64{0, -1, math.NaN(), math.Inf(1)} {
		if fit := CheckFit(ffmpeg.Media{Duration: duration}, s, 1_000_000); fit.Fits {
			t.Fatalf("accepted duration %v", duration)
		}
	}
}

func TestFitEncodeRetryAndCleanup(t *testing.T) {
	for _, mode := range []string{"success", "oversize", "failure", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			req := baseTestRequest(t.TempDir(), 60)
			req.Settings.Resolution = "720p"
			req.Settings.FPS = f64ForFit(30)
			original := cloneRequest(req)
			r := newFakeRunner(false)
			var bitrates []int
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			r.onEncode = func(args []string, path string) (int64, int64, int64, float64, error) {
				bitrates = append(bitrates, parseVideoKbpsFromArgs(args))
				if mode == "failure" {
					_ = os.WriteFile(path, []byte("partial"), 0o600)
					return 0, 0, 0, 0, errors.New("encoder failed")
				}
				if mode == "cancel" {
					cancel()
				}
				bytes := int64(1_200_000)
				if len(bitrates) == 2 && mode == "success" {
					bytes = 950_000
				}
				return bytes, bytes - 3000, 0, 60, nil
			}
			fit := Fit{Fits: true, VideoKbps: 100, AudioKbps: 64}
			out, err := FitEncode(ctx, r, req, fit, 1_000_000, nil)
			if (err != nil) != (mode == "failure" || mode == "cancel") {
				t.Fatalf("mode %s: %+v %v", mode, out, err)
			}
			if !reflect.DeepEqual(req, original) {
				t.Fatal("caller request changed")
			}
			if mode == "success" || mode == "oversize" {
				if !slices.Equal(bitrates, []int{100, 81}) || out.FullEncodes != 2 {
					t.Fatalf("retry: %v outcome %+v", bitrates, out)
				}
			}
			if mode == "success" {
				if out.File != req.Output || out.Oversize || out.ActualBytes != 950_000 || out.Settings.AudioSource || out.Settings.AudioKbps != 64 {
					t.Fatalf("success: %+v", out)
				}
			} else {
				if _, statErr := os.Stat(req.Output); !os.IsNotExist(statErr) || out.File != "" {
					t.Fatalf("unfinished part remains: %v %+v", statErr, out)
				}
				if mode == "oversize" && (!out.Oversize || out.ActualBytes != 1_200_000 || out.Infeasible != "Output is 1.20 MB after retry; target is 1.00 MB") {
					t.Fatalf("oversize: %+v", out)
				}
			}
		})
	}
}

func f64ForFit(v float64) *float64 { return &v }

func TestForceFastMetricsBoundsAndSource(t *testing.T) {
	for _, metric := range []string{"", "xpsnr", "vmaf", "fallback", "missing-vmaf"} {
		t.Run(metric, func(t *testing.T) {
			req := baseTestRequest(t.TempDir(), 200)
			req.Settings.Resolution, req.Settings.FPS = "720p", f64ForFit(25)
			req.Settings.AudioSource = true
			original := cloneRequest(req)
			r := newFakeRunner(metric != "missing-vmaf")
			r.hasXPSNR = metric != "fallback" && metric != "missing-vmaf"
			requested := metric
			if requested == "fallback" || requested == "missing-vmaf" {
				requested = ""
			}
			if metric == "missing-vmaf" {
				requested = "vmaf"
			}
			var events []console.Event
			out, err := ForceFast(context.Background(), r, req, 5_000_000, requested, func(ev console.Event) { events = append(events, ev) })
			if metric == "missing-vmaf" {
				if err == nil || err.Error() != "--vmaf needs FFmpeg built with libvmaf" || r.encodeCalls != 0 {
					t.Fatalf("missing VMAF: %+v %v calls=%d", out, err, r.encodeCalls)
				}
				return
			}
			if err != nil || out.File != req.Output || out.SampleEncodes != 3 || out.FullEncodes != 1 || out.MetricVersion == "" {
				t.Fatalf("outcome %+v %v", out, err)
			}
			wantMetric := metricXPSNR
			if metric == "fallback" {
				wantMetric = metricSSIM
			}
			if metric == "vmaf" {
				wantMetric = metricVMAF
			}
			if out.Metric != wantMetric || r.filterCalls != 1 || !reflect.DeepEqual(req, original) {
				t.Fatalf("metric or mutation: %+v", out)
			}
			var reference string
			samples, finals, cuts := 0, 0, 0
			for _, args := range r.recordedArgs {
				if slices.Contains(args, "ffv1") {
					reference = args[len(args)-1]
					cuts++
					if strings.Count(strings.Join(args, " "), "-ss") != 3 {
						t.Fatal("long source needs 3 windows")
					}
				}
				if slices.Contains(args, "-b:v") {
					src := args[slices.Index(args, "-i")+1]
					if args[len(args)-1] == req.Output || isPass1Args(args) {
						finals++
						if src != req.Source {
							t.Fatal("final did not use original")
						}
					} else {
						samples++
						if src != reference || slices.Contains(args, "-pass:v") || slices.Contains(args, "-x265-params") || !slices.Contains(args, "-an") {
							t.Fatalf("sample must be one-pass on reference: %v", args)
						}
					}
					w, h := parseScaleFromArgs(args, req.Media.Width, req.Media.Height)
					fps := parseFPSFromArgs(args, req.Media.FPS)
					if w > 1280 || h > 720 || fps > 25 {
						t.Fatalf("cap exceeded %dx%d %g fps", w, h, fps)
					}
				}
			}
			if cuts != 1 || samples != 3 || finals != 2 {
				t.Fatalf("cuts=%d samples=%d finals=%d", cuts, samples, finals)
			}
			if _, err := os.Stat(filepath.Dir(reference)); !os.IsNotExist(err) {
				t.Fatalf("reference scratch retained: %v", err)
			}
			trials := 0
			for _, ev := range events {
				if strings.HasPrefix(ev.Stage, "trial ") {
					trials++
					if ev.Trial != trials || ev.Trials != 3 {
						t.Fatal(ev)
					}
				}
			}
			if trials != 3 {
				t.Fatalf("trial events=%d", trials)
			}
		})
	}
}

func TestForceFastRetriesAndExplicitLocks(t *testing.T) {
	req := baseTestRequest(t.TempDir(), 5)
	req.Settings.Resolution, req.Settings.FPS = "720p", f64ForFit(30)
	req.Origins = config.Origins{"codec": "explicit", "container": "explicit", "audio_codec": "explicit", "resolution": "explicit", "fps": "explicit", "audio": "explicit", "preset": "explicit", "hw": "explicit"}
	r := newFakeRunner(false)
	r.onEncode = func(args []string, path string) (int64, int64, int64, float64, error) {
		return 200_000, 190_000, 6000, 5, nil
	}
	out, err := ForceFast(context.Background(), r, req, 150_000, "", nil)
	if err != nil || !out.Oversize || out.FullEncodes != 3 || out.SampleEncodes != 1 || out.ActualBytes != 200_000 || len(out.Changed) != 0 {
		t.Fatalf("bounded locked retries %+v %v", out, err)
	}
	if _, err := os.Stat(req.Output); !os.IsNotExist(err) {
		t.Fatal("oversized part remains")
	}
	for _, args := range r.recordedArgs {
		if slices.Contains(args, "ffv1") && slices.Contains(args, "-ss") {
			t.Fatal("short clip must use whole source")
		}
	}
}

func TestSizeEngineRejectsSourceAliases(t *testing.T) {
	dir := t.TempDir()
	req := baseTestRequest(dir, 60)
	if err := os.WriteFile(req.Source, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	req.Output = req.Source
	r := newFakeRunner(false)
	if _, err := FitEncode(context.Background(), r, req, Fit{Fits: true, VideoKbps: 100}, 100_000, nil); err == nil {
		t.Fatal("accepted alias")
	}
	if _, err := ForceFast(context.Background(), r, req, 100_000, "", nil); err == nil {
		t.Fatal("accepted alias")
	}
	data, _ := os.ReadFile(req.Source)
	if string(data) != "original" || r.encodeCalls != 0 {
		t.Fatal("source changed")
	}
}

func TestXPSNRFrameParsing(t *testing.T) {
	frames, err := parseXPSNRStats([]byte("n: 1 XPSNR y: 32.50 u: 40.00 v: 38.00\nn: 2 XPSNR y: inf u: inf v: inf\nn: 3 XPSNR y: 41.00 u: 34.00 v: 39.00\n"))
	if err != nil || !slices.Equal(frames, []float64{32.5, 100, 34}) {
		t.Fatalf("%v %v", frames, err)
	}
	if _, err := parseXPSNRStats([]byte("no scores")); err == nil {
		t.Fatal("accepted missing scores")
	}
}

func TestForceFastCapsMatchEncodedDimensions(t *testing.T) {
	for _, explicit := range []bool{false, true} {
		req := baseTestRequest(t.TempDir(), 60)
		req.Media.Width, req.Media.Height, req.Media.FPS = 720, 1280, 24
		req.Settings.Resolution, req.Settings.FPS = "1080p", f64ForFit(60)
		if explicit {
			req.Origins = config.Origins{"resolution": "explicit", "fps": "explicit"}
		}
		res, fps, err := fastRungs(req)
		if err != nil || res[0].width != 608 || res[0].height != 1080 || fps[0].fps != 24 {
			t.Fatalf("cap: %v %v %v", res, fps, err)
		}
		if explicit && (len(res) != 1 || len(fps) != 1) {
			t.Fatal("explicit caps changed")
		}
		for _, cand := range selectABRCandidates(res, fps, 48, 200, bandForCodec("hevc"), 3) {
			sample := cloneRequest(req)
			sample.Settings = applyCandidate(req.Settings, cand, req.Media)
			args, err := ffmpeg.BuildArgs(sample)
			w, h := parseScaleFromArgs(args, req.Media.Width, req.Media.Height)
			if err != nil || w != cand.width || h != cand.height || w > 608 || h > 1080 || parseFPSFromArgs(args, req.Media.FPS) != cand.fps {
				t.Fatalf("candidate and encode disagree: %+v %v %v", cand, args, err)
			}
		}
	}
}

func TestSizeEngineHardwareHeadroomAndFallback(t *testing.T) {
	for _, mode := range []string{"fit", "sample-fallback", "final-fallback", "explicit"} {
		t.Run(mode, func(t *testing.T) {
			req := baseTestRequest(t.TempDir(), 60)
			req.Settings.HW = "auto"
			req.Origins["codec"] = "explicit"
			req.Encoder = ffmpeg.Encoder{Name: "hevc_vulkan", Backend: "Vulkan", Hardware: true}
			if mode == "explicit" {
				req.Origins["hw"] = "explicit"
			}
			r := newFakeRunner(false)
			r.hwEncoders = map[string]bool{"hevc_vulkan": true}
			var hardwareBitrates []int
			r.onEncode = func(args []string, path string) (int64, int64, int64, float64, error) {
				if slices.Contains(args, "hevc_vulkan") {
					hardwareBitrates = append(hardwareBitrates, parseVideoKbpsFromArgs(args))
					if mode == "sample-fallback" || mode == "explicit" || (mode == "final-fallback" && path == req.Output) {
						_ = os.WriteFile(path, []byte("partial"), 0o600)
						return 0, 0, 0, 0, errors.New("device lost")
					}
				}
				return 900_000, 880_000, 10_000, 60, nil
			}
			var out Outcome
			var err error
			if mode == "fit" {
				out, err = FitEncode(context.Background(), r, req, Fit{Fits: true, VideoKbps: 100, AudioKbps: 128}, 1_000_000, nil)
				if !slices.Equal(hardwareBitrates, []int{96}) || out.FullEncodes != 1 {
					t.Fatalf("hardware headroom: %v %+v", hardwareBitrates, out)
				}
			} else {
				out, err = ForceFast(context.Background(), r, req, 5_000_000, "", nil)
			}
			if mode == "explicit" {
				if err == nil || out.File != "" {
					t.Fatal("explicit backend fell back")
				}
				if _, statErr := os.Stat(req.Output); !os.IsNotExist(statErr) {
					t.Fatal("failed output retained")
				}
				return
			}
			if err != nil || out.File != req.Output || out.SampleEncodes > 3 || out.FullEncodes > 3 {
				t.Fatalf("outcome: %+v %v", out, err)
			}
			if (mode == "sample-fallback" || mode == "final-fallback") && (out.Settings.HW != "off" || !slices.Contains(out.Changed, "hw")) {
				t.Fatalf("missing fallback result: %+v", out)
			}
		})
	}
}

func TestForceFastFailureCleanup(t *testing.T) {
	for _, stage := range []string{"reference", "sample", "score", "final", "cancel", "infeasible"} {
		t.Run(stage, func(t *testing.T) {
			req := baseTestRequest(t.TempDir(), 60)
			if err := os.WriteFile(req.Output, []byte("part"), 0o600); err != nil {
				t.Fatal(err)
			}
			r := newFakeRunner(false)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var scratch string
			r.onEncode = func(args []string, path string) (int64, int64, int64, float64, error) {
				if slices.Contains(args, "ffv1") {
					scratch = filepath.Dir(path)
				}
				fail := (stage == "reference" && slices.Contains(args, "ffv1")) || (stage == "sample" && slices.Contains(args, "-b:v") && path != req.Output) || (stage == "final" && path == req.Output)
				if fail {
					_ = os.WriteFile(path, []byte("partial"), 0o600)
					return 0, 0, 0, 0, errors.New("encode failed")
				}
				if stage == "cancel" {
					cancel()
				}
				return 100_000, 90_000, 0, 60, nil
			}
			if stage == "score" {
				r.onScore = func([]string, string, string, string) ([]float64, error) { return nil, errors.New("score failed") }
			}
			target := int64(5_000_000)
			if stage == "infeasible" {
				target = 1000
			}
			out, err := ForceFast(ctx, r, req, target, "", nil)
			if (err == nil) != (stage == "infeasible") || out.File != "" || (stage == "infeasible" && out.Infeasible == "") {
				t.Fatalf("outcome: %+v %v", out, err)
			}
			for _, path := range []string{req.Output, scratch} {
				if path != "" {
					if _, err := os.Stat(path); !os.IsNotExist(err) {
						t.Fatalf("unfinished path retained: %s", path)
					}
				}
			}
		})
	}
}
