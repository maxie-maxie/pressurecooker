//go:build integration

package ffmpeg

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/maxie-maxie/pressurecooker/internal/config"
	"github.com/maxie-maxie/pressurecooker/internal/console"
	"github.com/maxie-maxie/pressurecooker/internal/output"
)

func requireTools(t *testing.T) (string, string) {
	t.Helper()
	ff, err := ResolveTool("", "ffmpeg")
	if err != nil {
		t.Fatalf("ffmpeg required for integration tests: %v", err)
	}
	fp, err := ResolveTool("", "ffprobe")
	if err != nil {
		t.Fatalf("ffprobe required for integration tests: %v", err)
	}
	return ff, fp
}

func generateSampleClip(t *testing.T, ff, path string, duration string) {
	t.Helper()
	res, err := (ExecRunner{}).Run(context.Background(), []string{
		ff, "-nostdin", "-y", "-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "testsrc2=size=320x240:rate=24:duration=" + duration,
		"-f", "lavfi", "-i", "sine=frequency=440:duration=" + duration,
		"-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p",
		"-c:a", "aac", "-b:a", "96k",
		path,
	}, nil)
	if err != nil || res.ExitCode != 0 {
		t.Fatalf("failed to generate clip: err=%v exit=%d stderr=%v", err, res.ExitCode, res.StderrTail)
	}
}

func TestIntegrationRealCRFEncodeAndPublish(t *testing.T) {
	ff, fp := requireTools(t)
	root := t.TempDir()
	src := filepath.Join(root, "sample.mp4")
	generateSampleClip(t, ff, src, "2")

	r := ExecRunner{}
	ctx := context.Background()
	srcMedia, err := Probe(ctx, r, fp, src)
	if err != nil {
		t.Fatalf("Probe source failed: %v", err)
	}
	if srcMedia.Width != 320 || srcMedia.Height != 240 || !srcMedia.HasAudio || srcMedia.Duration < 1.5 {
		t.Fatalf("unexpected probed source media: %+v", srcMedia)
	}

	s := config.Factory()
	s.Codec = "h264"
	s.Preset = "ultrafast"
	s.HW = "off"
	s.Quality = "balanced"

	enc, err := ChooseEncoder(ctx, r, ff, s)
	if err != nil {
		t.Fatalf("ChooseEncoder failed: %v", err)
	}

	dest, err := output.Resolve(src, filepath.Join(root, "out.mp4"), s, "default", int64(20000000), false)
	if err != nil {
		t.Fatalf("output.Resolve failed: %v", err)
	}
	part, err := output.Temp(dest)
	if err != nil {
		t.Fatalf("output.Temp failed: %v", err)
	}
	defer os.Remove(part)

	var events int
	outMedia, err := Encode(ctx, r, Request{
		Source: src, Output: part, FFmpeg: ff, FFprobe: fp,
		Settings: s, Encoder: enc, Media: srcMedia, RateControl: "quality",
	}, func(ev console.Event) {
		events++
	})
	if err != nil {
		t.Fatalf("Encode CRF failed: %v", err)
	}
	if events == 0 {
		t.Fatalf("expected progress events from -progress pipe:1")
	}
	if outMedia.Bytes <= 0 || outMedia.Width != 320 || outMedia.Height != 240 || outMedia.VideoCodec != "h264" {
		t.Fatalf("unexpected encoded media: %+v", outMedia)
	}
	if err := output.Publish(part, dest, src, false); err != nil {
		t.Fatalf("Publish failed: %v", err)
	}
	if _, err := os.Stat(dest.Path); err != nil {
		t.Fatalf("published output missing: %v", err)
	}
}

func TestIntegrationRealTwoPassABREncode(t *testing.T) {
	ff, fp := requireTools(t)
	root := t.TempDir()
	src := filepath.Join(root, "sample.mp4")
	generateSampleClip(t, ff, src, "2")

	r := ExecRunner{}
	ctx := context.Background()
	srcMedia, err := Probe(ctx, r, fp, src)
	if err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		codec, encName string
	}{
		{"h264", "libx264"},
		{"hevc", "libx265"},
	} {
		t.Run(tc.codec, func(t *testing.T) {
			s := config.Factory()
			s.Codec = tc.codec
			s.Preset = "ultrafast"
			s.HW = "off"

			logDir := filepath.Join(root, "logs-"+tc.codec)
			if err := os.MkdirAll(logDir, 0o755); err != nil {
				t.Fatal(err)
			}
			passLog := filepath.Join(logDir, "pass")
			outPath := filepath.Join(root, "abr-"+tc.codec+".mp4")

			seenStages := map[string]bool{}
			media, err := Encode(ctx, r, Request{
				Source: src, Output: outPath, FFmpeg: ff, FFprobe: fp,
				Settings: s,
				Encoder:  Encoder{Name: tc.encName, Backend: "software", Preset: "ultrafast"},
				Media:    srcMedia, RateControl: "abr", VideoKbps: 250, PassLog: passLog,
			}, func(ev console.Event) {
				seenStages[ev.Stage] = true
			})
			if err != nil {
				t.Fatalf("2-pass %s ABR encode failed: %v", tc.encName, err)
			}
			if !seenStages["Pass 1/2"] || !seenStages["Pass 2/2"] {
				t.Fatalf("expected Pass 1/2 and Pass 2/2 progress stages, got %v", seenStages)
			}
			if media.Bytes <= 0 || media.Width != 320 || media.Height != 240 {
				t.Fatalf("unexpected 2-pass output media: %+v", media)
			}
			entries, err := os.ReadDir(logDir)
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 0 {
				t.Fatalf("expected pass logs cleaned up, remaining: %v", entries)
			}
		})
	}
}

func TestIntegrationContextCancelCleanup(t *testing.T) {
	ff, fp := requireTools(t)
	root := t.TempDir()
	src := filepath.Join(root, "long.mp4")
	generateSampleClip(t, ff, src, "15")

	r := ExecRunner{}
	srcMedia, err := Probe(context.Background(), r, fp, src)
	if err != nil {
		t.Fatal(err)
	}

	s := config.Factory()
	s.Codec = "hevc"
	s.Preset = "slow"
	s.HW = "off"

	dest, err := output.Resolve(src, filepath.Join(root, "cancelled.mp4"), s, "force-target", int64(20000000), false)
	if err != nil {
		t.Fatal(err)
	}
	part, err := output.Temp(dest)
	if err != nil {
		t.Fatal(err)
	}
	logDir := filepath.Join(root, "cancel-logs")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		t.Fatal(err)
	}
	passLog := filepath.Join(logDir, "run")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		time.Sleep(150 * time.Millisecond)
		cancel()
	}()

	_, err = Encode(ctx, r, Request{
		Source: src, Output: part, FFmpeg: ff, FFprobe: fp,
		Settings: s,
		Encoder:  Encoder{Name: "libx265", Backend: "software", Preset: "slow"},
		Media:    srcMedia, RateControl: "abr", VideoKbps: 300, PassLog: passLog,
	}, func(ev console.Event) {
		if strings.HasPrefix(ev.Stage, "Pass") {
			cancel()
		}
	})
	_ = os.Remove(part)

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
	if _, statErr := os.Stat(part); !os.IsNotExist(statErr) {
		t.Fatalf("expected .part cleaned up after cancel")
	}
	entries, err := os.ReadDir(logDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected pass logs cleaned up after cancel, remaining: %v", entries)
	}
}
