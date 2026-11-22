package ffmpeg

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/maxie-maxie/pressurecooker/internal/config"
	"github.com/maxie-maxie/pressurecooker/internal/console"
)

func TestRealCodecEncodes(t *testing.T) {
	ff, err := ResolveTool("", "ffmpeg")
	if err != nil {
		t.Skip(err)
	}
	fp, err := ResolveTool("", "ffprobe")
	if err != nil {
		t.Skip(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	r := ExecRunner{}
	root := t.TempDir()
	source := filepath.Join(root, "source.mkv")
	res, err := r.Run(ctx, []string{ff, "-nostdin", "-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "testsrc2=size=320x240:rate=12:duration=1",
		"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000:duration=1",
		"-c:v", "ffv1", "-level", "3", "-c:a", "pcm_s16le", source}, nil)
	if err != nil || res.ExitCode != 0 {
		t.Fatalf("generate source: %v, %+v", err, res)
	}
	m, err := Probe(ctx, r, fp, source)
	if err != nil {
		t.Fatal(err)
	}
	sourceBytes, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	pixelFormat := func(path string) string {
		t.Helper()
		res, err := r.Run(ctx, []string{fp, "-v", "error", "-select_streams", "v:0", "-show_entries", "stream=pix_fmt", "-of", "default=nw=1:nk=1", path}, nil)
		if err != nil || res.ExitCode != 0 {
			t.Fatalf("pixel format: %v, %+v", err, res)
		}
		return strings.TrimSpace(res.Stdout)
	}
	frameHashes := func(path string) []string {
		t.Helper()
		res, err := r.Run(ctx, []string{ff, "-nostdin", "-hide_banner", "-loglevel", "error", "-i", path, "-map", "0:v:0", "-f", "framemd5", "-"}, nil)
		if err != nil || res.ExitCode != 0 {
			t.Fatalf("frame hashes: %v, %+v", err, res)
		}
		var hashes []string
		for _, line := range strings.Split(res.Stdout, "\n") {
			if parts := strings.Split(line, ","); len(parts) == 6 && !strings.HasPrefix(line, "#") {
				hashes = append(hashes, strings.TrimSpace(parts[5]))
			}
		}
		return hashes
	}
	for _, codec := range config.VideoCodecs {
		t.Run("video/"+codec, func(t *testing.T) {
			s := config.Settings{Codec: codec, Container: config.Recommended(codec), Preset: "fast", Quality: "balanced", HW: "off"}
			enc, err := ChooseEncoder(ctx, r, ff, s)
			if err != nil {
				t.Fatal(err)
			}
			out := filepath.Join(root, codec+"."+s.Container)
			videoOnly := m
			videoOnly.HasAudio = false
			media, err := Encode(ctx, r, Request{Source: source, Output: out, FFmpeg: ff, FFprobe: fp, Settings: s, Encoder: enc, Media: videoOnly}, nil)
			if err != nil {
				t.Fatal(err)
			}
			if media.VideoCodec != codec || media.Container != s.Container || media.Width != 320 || media.Height != 240 || media.HasAudio || media.Duration < 0.9 || media.Duration > 1.1 {
				t.Fatalf("unexpected output: %+v", media)
			}
			pix := pixelFormat(out)
			if codec == "prores" && pix != "yuv422p10le" {
				t.Fatalf("ProRes pixel format = %s", pix)
			}
			if codec == "ffv1" {
				want, got := frameHashes(source), frameHashes(out)
				if len(want) != 12 || !slices.Equal(want, got) {
					t.Fatalf("FFV1 frame hashes differ: source %d, output %d", len(want), len(got))
				}
			}
			t.Logf("codec=%s encoder=%s container=%s duration=%.3fs dimensions=%dx%d pix_fmt=%s bytes=%d", media.VideoCodec, enc.Name, media.Container, media.Duration, media.Width, media.Height, pix, media.Bytes)
		})
	}
	for _, codec := range []string{"vp9", "theora"} {
		t.Run("two-pass/"+codec, func(t *testing.T) {
			s := config.Settings{Codec: codec, Container: config.Recommended(codec), Preset: "8", Quality: "balanced", HW: "off"}
			enc, err := ChooseEncoder(ctx, r, ff, s)
			if err != nil {
				t.Fatal(err)
			}
			logDir := t.TempDir()
			videoOnly := m
			videoOnly.HasAudio = false
			var stages []string
			media, err := Encode(ctx, r, Request{Source: source, Output: filepath.Join(root, codec+"-abr."+s.Container), FFmpeg: ff, FFprobe: fp, Settings: s, Encoder: enc, Media: videoOnly, RateControl: "abr", VideoKbps: 250, PassLog: filepath.Join(logDir, "pass")}, func(ev console.Event) {
				if !slices.Contains(stages, ev.Stage) {
					stages = append(stages, ev.Stage)
				}
			})
			if err != nil {
				t.Fatal(err)
			}
			if media.VideoCodec != codec || media.Container != s.Container || media.Duration < 0.9 || !slices.Equal(stages, []string{"Pass 1/2", "Pass 2/2"}) {
				t.Fatalf("two-pass media=%+v stages=%v", media, stages)
			}
			if entries, err := os.ReadDir(logDir); err != nil || len(entries) != 0 {
				t.Fatalf("pass logs retained: %v, %v", entries, err)
			}
			t.Logf("codec=%s two-pass ABR=250k container=%s duration=%.3fs bytes=%d; pass logs removed", codec, media.Container, media.Duration, media.Bytes)
		})
	}
	for _, codec := range config.AudioCodecs {
		t.Run("audio/"+codec, func(t *testing.T) {
			s := config.Settings{Codec: "h264", Container: "mkv", Preset: "ultrafast", Quality: "balanced", AudioCodec: codec, AudioKbps: 96, HW: "off"}
			enc, err := ChooseEncoder(ctx, r, ff, s)
			if err != nil {
				t.Fatal(err)
			}
			media, err := Encode(ctx, r, Request{Source: source, Output: filepath.Join(root, "audio-"+codec+".mkv"), FFmpeg: ff, FFprobe: fp, Settings: s, Encoder: enc, Media: m}, nil)
			if err != nil || !media.HasAudio || media.AudioCodec != codec || media.Container != "mkv" || media.Duration < 0.9 || media.Duration > 1.1 {
				t.Fatalf("audio %s: %+v, %v", codec, media, err)
			}
			t.Logf("audio=%s container=%s duration=%.3fs bytes=%d", media.AudioCodec, media.Container, media.Duration, media.Bytes)
		})
	}
	for _, codec := range config.VideoCodecs {
		t.Run("hardware/"+codec, func(t *testing.T) {
			availability, err := ProbeHardware(ctx, r, ff, codec)
			if err != nil || len(availability) != len(config.CodecCaps(codec).HW) {
				t.Fatalf("hardware availability: %v, %v", availability, err)
			}
			for _, backend := range config.CodecCaps(codec).HW {
				reason, ok := availability[backend]
				if !ok || strings.ContainsAny(reason, "\r\n") {
					t.Fatalf("invalid %s probe reason: %q", backend, reason)
				}
				t.Logf("codec=%s backend=%s unavailable_reason=%q", codec, backend, reason)
				if reason == "" && (codec == "prores" || codec == "ffv1" || codec == "vp9") {
					s := config.Settings{Codec: codec, Container: config.Recommended(codec), Preset: "fast", Quality: "balanced", HW: backend}
					enc, err := ChooseEncoder(ctx, r, ff, s)
					if err != nil {
						t.Fatal(err)
					}
					videoOnly := m
					videoOnly.HasAudio = false
					out := filepath.Join(root, codec+"-"+backend+"."+s.Container)
					media, err := Encode(ctx, r, Request{Source: source, Output: out, FFmpeg: ff, FFprobe: fp, Settings: s, Encoder: enc, Media: videoOnly}, nil)
					if err != nil || media.VideoCodec != codec || media.Duration < 0.9 || media.Width != 320 || media.Height != 240 {
						t.Fatalf("available hardware encode: %+v, %v", media, err)
					}
					pix := pixelFormat(out)
					if codec == "prores" && pix != "yuv422p10le" {
						t.Fatalf("Vulkan ProRes pixel format = %s", pix)
					}
					if codec == "ffv1" && !slices.Equal(frameHashes(source), frameHashes(out)) {
						t.Fatal("Vulkan FFV1 frame hashes differ")
					}
					t.Logf("hardware encoder=%s device=%d container=%s duration=%.3fs pix_fmt=%s bytes=%d", enc.Name, enc.Device, media.Container, media.Duration, pix, media.Bytes)
				}
			}
		})
	}
	if got, err := os.ReadFile(source); err != nil || !slices.Equal(sourceBytes, got) {
		t.Fatalf("source changed: %v", err)
	}
}
