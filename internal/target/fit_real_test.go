package target

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/maxie-maxie/pressurecooker/internal/config"
	"github.com/maxie-maxie/pressurecooker/internal/ffmpeg"
)

func TestSizeEngineRealFFmpeg(t *testing.T) {
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
	src := filepath.Join(dir, "source.mp4")
	res, err := r.Run(ctx, []string{ff, "-nostdin", "-y", "-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", "testsrc2=size=320x180:rate=24:duration=14", "-f", "lavfi", "-i", "sine=frequency=440:duration=14", "-c:v", "libx264", "-preset", "ultrafast", "-crf", "20", "-c:a", "aac", "-b:a", "96k", src}, nil)
	if err != nil || res.ExitCode != 0 {
		t.Fatalf("generate: %v %+v", err, res)
	}
	m, err := ffmpeg.Probe(ctx, r, fp, src)
	if err != nil {
		t.Fatal(err)
	}
	s := config.Factory()
	s.Codec, s.Container, s.Preset, s.HW, s.AudioSource = "h264", "mp4", "fast", "off", true
	s.CRF = nil
	req := ffmpeg.Request{Source: src, Output: filepath.Join(dir, "fit.part"), FFmpeg: ff, FFprobe: fp, Media: m, Settings: s}
	const target = int64(450_000)
	fit := CheckFit(m, s, target)
	out, err := FitEncode(ctx, r, req, fit, target, nil)
	if err != nil || out.File != req.Output || out.ActualBytes <= 0 || out.ActualBytes > target || out.FullEncodes > 2 || out.Media.Width != m.Width || out.Media.Height != m.Height || !out.Media.HasAudio {
		t.Fatalf("fit %+v %v", out, err)
	}
	video, audio, ok := probePacketBytes(ctx, r, fp, out.File)
	if !ok {
		t.Fatal("missing packet measurement")
	}
	t.Logf("FitEncode: %d/%d bytes, %d encodes, mux overhead %.3f%%, audio=%dk copy=%v", out.ActualBytes, target, out.FullEncodes, float64(out.ActualBytes-video-audio)/float64(out.ActualBytes)*100, fit.AudioKbps, fit.AudioCopy)
	for _, codec := range []string{"hevc", "av1"} {
		other := req
		other.Settings = config.Clone(s)
		other.Settings.Codec, other.Settings.Preset = codec, "ultrafast"
		other.Output = filepath.Join(dir, codec+".part")
		out, err := FitEncode(ctx, r, other, CheckFit(m, other.Settings, target), target, nil)
		if err != nil || out.File != other.Output || out.Media.VideoCodec != codec || out.ActualBytes > target {
			t.Fatalf("fit %s: %+v %v", codec, out, err)
		}
		t.Logf("FitEncode %s: %d/%d bytes, %d encodes", codec, out.ActualBytes, target, out.FullEncodes)
	}
	copyReq := req
	copyReq.Settings = config.Clone(s)
	copyReq.Settings.Resolution = "720p"
	copyReq.Settings.FPS = f64ForFit(60)
	copyReq.Output = filepath.Join(dir, "copy.part")
	copyFit := CheckFit(m, copyReq.Settings, 2_000_000)
	copyOut, err := FitEncode(ctx, r, copyReq, copyFit, 2_000_000, nil)
	if err != nil || !copyFit.AudioCopy || !copyOut.Settings.AudioSource || copyOut.File != copyReq.Output || copyOut.ActualBytes > 2_000_000 || copyOut.Media.Width != m.Width || copyOut.Media.Height != m.Height || copyOut.Media.FPS != m.FPS || copyOut.Media.AudioKbps != m.AudioKbps {
		t.Fatalf("copy/caps: %+v %+v %v", copyFit, copyOut, err)
	}
	t.Logf("FitEncode copy/caps: %d/2000000 bytes, AAC %.3f kbps unchanged, %dx%d at %g fps", copyOut.ActualBytes, copyOut.Media.AudioKbps, copyOut.Media.Width, copyOut.Media.Height, copyOut.Media.FPS)
	for _, metric := range []string{"xpsnr", "vmaf"} {
		scorer, err := detectFastMetric(ctx, r, ff, metric)
		if err != nil && metric == "vmaf" {
			t.Log(err)
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		req.Output = filepath.Join(dir, "force-"+metric+".part")
		out, err := ForceFast(ctx, r, req, target, metric, nil)
		if err != nil || out.File != req.Output || out.ActualBytes <= 0 || out.ActualBytes > target || out.SampleEncodes < 1 || out.SampleEncodes > 3 || out.FullEncodes > 3 || out.Metric != scorer.metric || !out.Media.HasAudio {
			t.Fatalf("force %+v %v", out, err)
		}
		t.Logf("ForceFast: metric=%s bytes=%d/%d samples=%d full=%d", out.Metric, out.ActualBytes, target, out.SampleEncodes, out.FullEncodes)
	}
	req.Output = filepath.Join(dir, "oversize.part")
	fit = Fit{Fits: true, VideoKbps: 100, AudioKbps: 128}
	const overshootTarget = int64(100_000)
	out, err = FitEncode(ctx, r, req, fit, overshootTarget, nil)
	if err != nil || !out.Oversize || out.FullEncodes != 2 || out.ActualBytes <= overshootTarget || out.File != "" {
		t.Fatalf("real overshoot: %+v %v", out, err)
	}
	if _, err := os.Stat(req.Output); !os.IsNotExist(err) {
		t.Fatalf("oversized part retained: %v", err)
	}
	t.Logf("FitEncode cleanup: %d bytes > %d after %d encodes, part removed", out.ActualBytes, overshootTarget, out.FullEncodes)
}
