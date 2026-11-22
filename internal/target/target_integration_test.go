//go:build integration

package target

import (
	"context"
	"path/filepath"
	"sort"
	"testing"

	"github.com/maxie-maxie/pressurecooker/internal/ffmpeg"
)

func requireTools(t *testing.T) (string, string) {
	t.Helper()
	ff, err := ffmpeg.ResolveTool("", "ffmpeg")
	if err != nil {
		t.Fatalf("ffmpeg required for integration tests: %v", err)
	}
	fp, err := ffmpeg.ResolveTool("", "ffprobe")
	if err != nil {
		t.Fatalf("ffprobe required for integration tests: %v", err)
	}
	return ff, fp
}

func makeSyntheticClip(t *testing.T, ff, path, size, rate, dur string) {
	t.Helper()
	res, err := (ffmpeg.ExecRunner{}).Run(context.Background(), []string{
		ff, "-nostdin", "-y", "-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "testsrc2=size=" + size + ":rate=" + rate + ":duration=" + dur,
		"-f", "lavfi", "-i", "sine=frequency=440:duration=" + dur,
		"-c:v", "libx264", "-preset", "ultrafast", "-crf", "20", "-pix_fmt", "yuv420p",
		"-c:a", "aac", "-b:a", "128k",
		path,
	}, nil)
	if err != nil || res.ExitCode != 0 {
		t.Fatalf("generate clip failed: err=%v exit=%d stderr=%v", err, res.ExitCode, res.StderrTail)
	}
}

func TestIntegrationCalibrationProbe(t *testing.T) {
	ff, fp := requireTools(t)
	dir := t.TempDir()
	r := ffmpeg.ExecRunner{}
	ctx := context.Background()

	clip := filepath.Join(dir, "cal-ref.mp4")
	makeSyntheticClip(t, ff, clip, "640x360", "24", "3")

	rep, err := Calibrate(ctx, r, ff, fp, clip, []string{"h264", "hevc", "av1"})
	if err != nil {
		t.Fatalf("Calibrate failed: %v", err)
	}
	if rep.Metric == "" || len(rep.References) == 0 {
		t.Fatalf("expected non-empty metric and software references, got %+v", rep)
	}
	t.Logf("CALIBRATION METRIC: %s (%s)", rep.Metric, rep.MetricVersion)
	for _, ref := range rep.References {
		t.Logf("REF   codec=%-4s encoder=%-10s level=%-8s q=%4.1f mean=%6.2f p05=%6.2f bytes=%d",
			ref.Codec, ref.Encoder, ref.Level, ref.Quantizer, ref.Score, ref.P05, ref.Bytes)
	}
	for _, pt := range rep.Probes {
		t.Logf("PROBE codec=%-4s backend=%-7s encoder=%-12s q=%4.1f mean=%6.2f p05=%6.2f bytes=%d",
			pt.Codec, pt.Backend, pt.Encoder, pt.Quantizer, pt.Score, pt.P05, pt.Bytes)
	}
	for _, m := range rep.Matches {
		t.Logf("MATCH codec=%-4s backend=%-7s encoder=%-12s level=%-8s refQ=%4.1f(%.2f) -> hwQ=%4.1f(%.2f, diff=%+.2f) refB=%d hwB=%d",
			m.Codec, m.Backend, m.Encoder, m.Level, m.RefQuantizer, m.RefScore, m.MatchedQuantizer, m.MatchedScore, m.ScoreDiff, m.RefBytes, m.MatchedBytes)
	}
	keys := make([]string, 0, len(rep.Unavailable))
	for k := range rep.Unavailable {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		t.Logf("UNAVAILABLE %s: %s", k, rep.Unavailable[k])
	}
}
