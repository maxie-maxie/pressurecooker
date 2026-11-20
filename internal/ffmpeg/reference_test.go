package ffmpeg

import (
	"context"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/maxie-maxie/pressurecooker/internal/config"
	"github.com/maxie-maxie/pressurecooker/internal/console"
)

func TestExtractReferenceInputsAndCleanup(t *testing.T) {
	for _, windows := range [][]Window{nil, {{Start: 1, Length: 2}}, {{Start: 1, Length: 2}, {Start: 8, Length: 2}}, {{Start: 1, Length: 2}, {Start: 8, Length: 2}, {Start: 16, Length: 2}}} {
		dir := t.TempDir()
		src, out := filepath.Join(dir, "source space.mkv"), filepath.Join(dir, "ref.mkv")
		r := &fakeRunner{fn: func(_ context.Context, args []string, _ func(console.Event)) (Result, error) {
			if len(windows) > 0 {
				count := 0
				for i, arg := range args {
					if arg == "-i" {
						if i < 4 || args[i-4] != "-ss" || args[i-2] != "-t" || args[i+1] != src {
							t.Fatalf("windows must use input seeking: %v", args)
						}
						count++
					}
				}
				if count != len(windows) {
					t.Fatalf("inputs=%d windows=%d", count, len(windows))
				}
			}
			if !slices.Contains(args, "ffv1") || !slices.Contains(args, "-an") || (len(windows) > 1 && !strings.Contains(strings.Join(args, " "), "concat=n=")) {
				t.Fatalf("invalid reference command: %v", args)
			}
			return Result{}, os.WriteFile(out, []byte("reference"), 0o600)
		}}
		if err := ExtractReference(context.Background(), r, "ffmpeg", src, windows, out); err != nil {
			t.Fatal(err)
		}
		if len(r.calls) != 1 {
			t.Fatalf("reference must be cut once, calls=%d", len(r.calls))
		}
		if err := ExtractReference(context.Background(), r, "ffmpeg", src, []Window{{Start: math.NaN(), Length: 2}}, out); err == nil {
			t.Fatal("accepted non-finite window")
		}
		r.fn = func(context.Context, []string, func(console.Event)) (Result, error) { return Result{ExitCode: 1}, nil }
		if err := ExtractReference(context.Background(), r, "ffmpeg", src, windows, out); err == nil {
			t.Fatal("accepted reference failure")
		}
		if _, err := os.Stat(out); !os.IsNotExist(err) {
			t.Fatalf("failed reference retained: %v", err)
		}
	}
}

func TestAudioSourceAndExplicitBitrate(t *testing.T) {
	for _, container := range []string{"mp4", "mkv", "webm"} {
		codec, encoder := "aac", "aac"
		if container == "webm" {
			codec, encoder = "opus", "libopus"
		}
		for _, sourceKbps := range []float64{0, 48, 256} {
			m := Media{HasAudio: true, AudioCodec: codec, AudioKbps: sourceKbps}
			s := config.Settings{AudioSource: true}
			if args := audioArgs(container, s, m); !slices.Equal(args, []string{"-c:a", "copy"}) {
				t.Fatalf("source audio must copy: %v", args)
			}
			s.AudioSource, s.AudioKbps = false, 96
			args := audioArgs(container, s, m)
			if !slices.Contains(args, encoder) || !slices.Contains(args, "96k") || slices.Contains(args, "copy") {
				t.Fatalf("explicit audio must encode: %v", args)
			}
			s.AudioSource = true
			m.AudioCodec = "flac"
			if args = audioArgs(container, s, m); !slices.Contains(args, "128k") {
				t.Fatalf("incompatible source must use 128k: %v", args)
			}
		}
	}
}

func TestVideoCapsNeverIncreaseSource(t *testing.T) {
	fps := 60.0
	s := config.Settings{Resolution: "1920x1080", FPS: &fps}
	filters, err := buildVideoFilters(s, Media{Width: 720, Height: 1280, FPS: 24})
	if err != nil || !slices.Equal(filters, []string{"scale=608:1080", "fps=24"}) {
		t.Fatalf("portrait and FPS caps: %v, %v", filters, err)
	}
}

func TestBuildArgsRejectsInvalidFixedAudio(t *testing.T) {
	for _, kbps := range []int{0, -1} {
		req := Request{Source: "source.mp4", Output: "output.mp4", Settings: config.Factory(), Encoder: Encoder{Name: "libx264", Preset: "fast"}, Media: Media{HasAudio: true}}
		req.Settings.AudioKbps = kbps
		if _, err := BuildArgs(req); err == nil {
			t.Fatalf("accepted fixed audio at %d kbps", kbps)
		}
	}
}
