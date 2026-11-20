package ffmpeg

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestExtractReferenceRealLosslessWindows(t *testing.T) {
	ff, err := ResolveTool("", "ffmpeg")
	if err != nil {
		t.Skip(err)
	}
	fp, err := ResolveTool("", "ffprobe")
	if err != nil {
		t.Skip(err)
	}
	ctx, r := context.Background(), ExecRunner{}
	dir := t.TempDir()
	src := filepath.Join(dir, "source.mkv")
	res, err := r.Run(ctx, []string{ff, "-nostdin", "-y", "-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", "testsrc2=size=160x90:rate=12:duration=5", "-c:v", "ffv1", "-an", src}, nil)
	if err != nil || res.ExitCode != 0 {
		t.Fatalf("generate: %v %+v", err, res)
	}
	hashes := func(path string, windows []Window) []string {
		if len(windows) == 0 {
			windows = []Window{{Length: 5}}
		}
		var result []string
		for _, win := range windows {
			res, err := r.Run(ctx, []string{ff, "-nostdin", "-hide_banner", "-loglevel", "error", "-ss", formatFloat(win.Start), "-t", formatFloat(win.Length), "-i", path, "-map", "0:v:0", "-f", "framemd5", "-"}, nil)
			if err != nil || res.ExitCode != 0 {
				t.Fatalf("hash: %v %+v", err, res)
			}
			for _, line := range strings.Split(res.Stdout, "\n") {
				if strings.HasPrefix(line, "#") {
					continue
				}
				parts := strings.Split(line, ",")
				if len(parts) == 6 {
					result = append(result, strings.TrimSpace(parts[5]))
				}
			}
		}
		return result
	}
	for _, windows := range [][]Window{nil, {{Start: 1, Length: 1}}, {{Start: 0, Length: 1}, {Start: 3, Length: 1}}, {{Start: 0, Length: 1}, {Start: 2, Length: 1}, {Start: 4, Length: 1}}} {
		out := filepath.Join(dir, "reference.mkv")
		if err := ExtractReference(ctx, r, ff, src, windows, out); err != nil {
			t.Fatal(err)
		}
		media, err := Probe(ctx, r, fp, out)
		if err != nil || media.VideoCodec != "ffv1" || media.HasAudio || media.Bytes == 0 {
			t.Fatalf("reference metadata: %+v %v", media, err)
		}
		want := hashes(src, windows)
		got := hashes(out, []Window{{Length: media.Duration}})
		if len(want) == 0 || !slices.Equal(want, got) {
			t.Fatalf("lossless frame mismatch: want %d got %d", len(want), len(got))
		}
	}
	if err := ExtractReference(ctx, r, ff, src, nil, src); err == nil {
		t.Fatal("source alias accepted")
	}
	if info, err := os.Stat(src); err != nil || info.Size() == 0 {
		t.Fatal("source lost")
	}
}
