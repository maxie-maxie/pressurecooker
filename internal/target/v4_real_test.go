package target

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/maxie-maxie/pressurecooker/internal/config"
	"github.com/maxie-maxie/pressurecooker/internal/ffmpeg"
)

func realSizeSource(t *testing.T) (context.Context, ffmpeg.ExecRunner, ffmpeg.Request) {
	t.Helper()
	ff, err := ffmpeg.ResolveTool("", "ffmpeg")
	if err != nil {
		t.Skip(err)
	}
	fp, err := ffmpeg.ResolveTool("", "ffprobe")
	if err != nil {
		t.Skip(err)
	}
	ctx, r := context.Background(), ffmpeg.ExecRunner{}
	dir := t.TempDir()
	src := filepath.Join(dir, "source.mkv")
	res, err := r.Run(ctx, []string{ff, "-nostdin", "-y", "-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", "testsrc2=size=320x180:rate=24:duration=4", "-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000:duration=4", "-c:v", "ffv1", "-c:a", "pcm_s16le", src}, nil)
	if err != nil || res.ExitCode != 0 {
		t.Fatalf("generate source: %v %+v", err, res)
	}
	m, err := ffmpeg.Probe(ctx, r, fp, src)
	if err != nil {
		t.Fatal(err)
	}
	s := config.Factory()
	s.HW, s.AudioSource = "off", true
	return ctx, r, ffmpeg.Request{Source: src, Output: filepath.Join(dir, "out.part"), FFmpeg: ff, FFprobe: fp, Settings: s, Media: m, Origins: config.Origins{}}
}

func TestForceFormatsRealFFmpeg(t *testing.T) {
	ctx, r, base := realSizeSource(t)
	for _, tc := range []struct {
		name, container, codec, audio string
		flac                          bool
		target                        int64
	}{
		{"directory", "mp4", "av1", "opus", false, 180_000},
		{"ogg", "ogg", "theora", "opus", false, 180_000},
		{"flac", "mp4", "av1", "flac", true, 250_000},
		{"flac infeasible", "mp4", "av1", "flac", true, 1_000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := cloneRequest(base)
			req.Output = filepath.Join(filepath.Dir(base.Output), tc.name+".part")
			if tc.name == "ogg" {
				req.Settings.Container = "ogg"
				req.Origins["container"] = "extension"
			}
			if tc.flac {
				req.Settings.AudioCodec, req.Settings.AudioKbps = "flac", 0
				req.Origins["audio_codec"] = "explicit"
			}
			out, err := ForceFast(ctx, r, req, tc.target, "xpsnr", nil)
			if err != nil {
				t.Fatalf("%+v %v", out, err)
			}
			if tc.target == 1_000 {
				if out.Infeasible == "" || out.SampleEncodes != 0 || out.FullEncodes != 0 || out.File != "" {
					t.Fatal(out)
				}
				if _, err := os.Stat(req.Output); !os.IsNotExist(err) {
					t.Fatal("infeasible output retained")
				}
				t.Log(out.Infeasible)
				return
			}
			if out.File != req.Output || out.ActualBytes > tc.target || out.Media.VideoCodec != tc.codec || out.Media.AudioCodec != tc.audio || out.Settings.Container != tc.container || out.Settings.AudioCodec != tc.audio || out.Settings.Codec != tc.codec || out.SampleEncodes > 3 || out.FullEncodes > 3 {
				t.Fatalf("unexpected result: %+v", out)
			}
			t.Logf("%s: %s/%s/%s %d/%d bytes samples=%d full=%d audio=%dk copy=%v", tc.name, out.Settings.Codec, out.Settings.Container, out.Settings.AudioCodec, out.ActualBytes, tc.target, out.SampleEncodes, out.FullEncodes, out.Settings.AudioKbps, out.Settings.AudioSource)
		})
	}
	req := cloneRequest(base)
	req.Settings.AudioCodec, req.Settings.AudioKbps, req.Settings.AudioSource = "flac", 0, false
	req.Origins["audio_codec"] = "explicit"
	bytes, err := exactAudioBytes(ctx, r, req)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("exact FLAC: %d bytes for %g seconds", bytes, req.Media.Duration)
	for _, target := range []int64{250_000, 1_000} {
		req.Output = filepath.Join(filepath.Dir(base.Output), "fit-flac.part")
		out, err := FitEncode(ctx, r, req, CheckFit(req.Media, req.Settings, target), target, nil)
		if err != nil {
			t.Fatal(err)
		}
		if target == 1_000 {
			if out.Infeasible == "" || out.FullEncodes != 0 || out.File != "" {
				t.Fatal(out)
			}
		} else if out.File != req.Output || out.ActualBytes > target || out.FullEncodes > 2 || out.Media.AudioCodec != "flac" {
			t.Fatal(out)
		}
		t.Logf("FitEncode FLAC: target=%d bytes=%d full=%d reason=%s", target, out.ActualBytes, out.FullEncodes, out.Infeasible)
	}
}

func TestQualityBandsAndGPURealFFmpeg(t *testing.T) {
	ctx, r, base := realSizeSource(t)
	base.Media.HasAudio = false
	scorer, err := detectFastMetric(ctx, r, base.FFmpeg, "xpsnr")
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Dir(base.Output)
	id := 0
	score := func(req ffmpeg.Request, metric metricScorer) (float64, float64) {
		t.Helper()
		id++
		frames, err := metric.scoreSample(ctx, r, req.FFmpeg, req.Output, base.Source, base.Media, sampleWindow{}, dir, id)
		if err != nil {
			t.Fatal(err)
		}
		return summarizeFrames(frames)
	}
	for _, codec := range []string{"vp9", "theora"} {
		kbps := []int{70, 120, 166}
		if codec == "theora" {
			kbps = []int{138, 200, 332}
		}
		for _, bitrate := range kbps {
			req := cloneRequest(base)
			req.Settings.Codec, req.Settings.Container = codec, config.Recommended(codec)
			req.Encoder, err = ffmpeg.ChooseEncoder(ctx, r, req.FFmpeg, req.Settings)
			if err != nil {
				t.Fatal(err)
			}
			req.Output = filepath.Join(dir, codec+"-"+formatSec(float64(bitrate))+"."+req.Settings.Container)
			req.RateControl, req.SinglePass, req.VideoKbps = "abr", true, bitrate
			m, err := ffmpeg.Encode(ctx, r, req, nil)
			if err != nil {
				t.Fatal(err)
			}
			mean, p05 := score(req, scorer)
			t.Logf("BPP %s: requested=%dk bpp=%.4f bytes=%d %s mean=%.3f p05=%.3f guarded=%.3f", codec, bitrate, computeBPP(bitrate, base.Media.Width, base.Media.Height, base.Media.FPS), m.Bytes, scorer.metric, mean, p05, guardedScore(mean, p05))
		}
	}
	for _, level := range []string{"small", "balanced", "high"} {
		req := cloneRequest(base)
		req.Settings.Codec, req.Settings.Container, req.Settings.Quality = "theora", "ogg", level
		req.Encoder, err = ffmpeg.ChooseEncoder(ctx, r, req.FFmpeg, req.Settings)
		if err != nil {
			t.Fatal(err)
		}
		req.Output = filepath.Join(dir, "theora-"+level+".ogg")
		m, err := ffmpeg.Encode(ctx, r, req, nil)
		if err != nil {
			t.Fatal(err)
		}
		mean, p05 := score(req, scorer)
		t.Logf("Theora quality %s: q=%g bytes=%d %s mean=%.3f p05=%.3f", level, req.Encoder.Quantizer, m.Bytes, scorer.metric, mean, p05)
	}
	measured := 0
	for _, codec := range []string{"hevc", "vp9"} {
		avail, err := ffmpeg.ProbeHardware(ctx, r, base.FFmpeg, codec)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("GPU availability %s: %v", codec, avail)
		for _, backend := range config.CodecCaps(codec).HW {
			if reason, ok := avail[backend]; !ok || reason != "" {
				continue
			}
			metrics := []metricScorer{scorer}
			if vmaf, err := detectFastMetric(ctx, r, base.FFmpeg, "vmaf"); err == nil {
				metrics = append(metrics, vmaf)
			}
			for _, hw := range []string{"off", backend} {
				req := cloneRequest(base)
				req.Settings.Codec, req.Settings.Container, req.Settings.HW = codec, config.Recommended(codec), hw
				req.Encoder, err = ffmpeg.ChooseEncoder(ctx, r, req.FFmpeg, req.Settings)
				if err != nil {
					t.Fatal(err)
				}
				req.Output = filepath.Join(dir, codec+"-"+hw+"."+req.Settings.Container)
				req.RateControl, req.SinglePass = "abr", true
				req.VideoKbps = sizeVideoKbps(req.Encoder, 200)
				m, err := ffmpeg.Encode(ctx, r, req, nil)
				if err != nil {
					t.Fatal(err)
				}
				for _, metric := range metrics {
					mean, p05 := score(req, metric)
					t.Logf("GPU comparison %s/%s: requested=%dk bytes=%d metric=%s mean=%.3f p05=%.3f guarded=%.3f", codec, hw, req.VideoKbps, m.Bytes, metric.metric, mean, p05, guardedScore(mean, p05))
				}
			}
			measured++
			break
		}
	}
	if measured == 0 {
		t.Log("No GPU backend passed; real GPU quality could not be measured")
	}
}
