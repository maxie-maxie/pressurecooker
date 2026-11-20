//go:build integration

package e2e

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/maxie-maxie/pressurecooker/internal/cli"
	"github.com/maxie-maxie/pressurecooker/internal/config"
	"github.com/maxie-maxie/pressurecooker/internal/ffmpeg"
)

func TestRealCLI(t *testing.T) {
	ff, err := ffmpeg.ResolveTool("", "ffmpeg")
	if err != nil {
		t.Fatal(err)
	}
	fp, err := ffmpeg.ResolveTool("", "ffprobe")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	binary := filepath.Join(root, "pressurecooker")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	build := exec.Command("go", "build", "-trimpath", "-ldflags", "-s -w -X main.version=3.0.0", "-o", binary, "./cmd/pressurecooker")
	build.Dir = filepath.Join("..", "..")
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	t.Run("ReadOnlyAndInvalidCommands", func(t *testing.T) {
		store := isolatedStore(t)
		t.Setenv("PATH", "")
		for _, args := range [][]string{{"--help"}, {"--help-details"}, {"--version"}, {"--preset-list"}} {
			code, out := runBinary(t, binary, args)
			assertExit(t, code, 0, out)
			if out == "" {
				t.Fatalf("no output for %v", args)
			}
			assertEmptyDir(t, filepath.Dir(store.Dir))
		}
		for _, args := range invalidArguments() {
			code, out := runBinary(t, binary, args)
			assertExit(t, code, 2, out)
			assertEmptyDir(t, filepath.Dir(store.Dir))
		}
	})
	src := filepath.Join(root, "source.mp4")
	generateClip(t, ff, src, "320x180", "24", "5")
	sourceBefore := fileBytes(t, src)

	t.Run("DefaultFit", func(t *testing.T) {
		store := isolatedStore(t)
		seedPreset(t, store, sourcePreset(1))
		for _, target := range [][]string{{"0.3"}, {"-t", "0.3"}} {
			dst := filepath.Join(t.TempDir(), "fit.mp4")
			args := append([]string{src, dst}, target...)
			args = append(args, "-q", "off")
			code, out := runBinary(t, binary, args)
			assertExit(t, code, 0, out)
			m := assertPublished(t, fp, dst, 300000, "h264", "mp4", 320, 180)
			t.Logf("target=300000 bytes=%d", m.Bytes)
			latest, err := store.LoadLatest()
			if err != nil || latest == nil || latest.Codec != "h264" {
				t.Fatalf("latest: %v %v", latest, err)
			}
		}
	})

	t.Run("ConversionPrompt", func(t *testing.T) {
		store := isolatedStore(t)
		seedPreset(t, store, sourcePreset(1))
		before := configSnapshot(t, store)
		for _, answer := range []string{"nonterminal", "n\n", "\n", "y\n"} {
			dst := filepath.Join(t.TempDir(), "converted.mp4")
			args := []string{src, dst, "-q", "off"}
			var code int
			var out string
			if answer == "nonterminal" {
				code, out = runBinary(t, binary, args)
			} else {
				code, out = runInteractive(t, store, args, answer)
			}
			want := 1
			if answer == "nonterminal" {
				want = 2
			}
			if answer == "y\n" {
				want = 0
			}
			assertExit(t, code, want, out)
			if !strings.Contains(out, "convert without a size limit?") {
				t.Fatal(out)
			}
			if want == 0 {
				assertPublished(t, fp, dst, 0, "h264", "mp4", 320, 180)
				if latest, err := store.LoadLatest(); err != nil || latest == nil {
					t.Fatalf("latest: %v %v", latest, err)
				}
			} else {
				assertAbsent(t, dst)
				assertConfigUnchanged(t, store, before)
			}
			assertNoParts(t, filepath.Dir(dst))
		}
	})

	t.Run("SourceBelowPreset", func(t *testing.T) {
		store := isolatedStore(t)
		seedPreset(t, store, config.FactoryPreset())
		hd := filepath.Join(t.TempDir(), "720p.mp4")
		generateClip(t, ff, hd, "1280x720", "30", "3")
		before := configSnapshot(t, store)
		for _, answer := range []string{"nonterminal", "n\n", "\n", "y\n"} {
			dst := filepath.Join(t.TempDir(), "capped.mp4")
			args := []string{hd, dst, "0.5", "-c", "h264", "-p", "fast", "-q", "off", "-ps", "1"}
			var code int
			var out string
			if answer == "nonterminal" {
				code, out = runBinary(t, binary, args)
			} else {
				code, out = runInteractive(t, store, args, answer)
			}
			want := 1
			if answer == "nonterminal" {
				want = 2
			}
			if answer == "y\n" {
				want = 0
			}
			assertExit(t, code, want, out)
			if !strings.Contains(out, "1280x720 at 30 fps") || !strings.Contains(out, "1920x1080") {
				t.Fatal(out)
			}
			if want == 0 {
				m := assertPublished(t, fp, dst, 500000, "h264", "mp4", 1280, 720)
				if m.FPS != 30 {
					t.Fatalf("source FPS changed: %g", m.FPS)
				}
			} else {
				assertAbsent(t, dst)
				assertConfigUnchanged(t, store, before)
			}
			assertNoParts(t, filepath.Dir(dst))
		}
	})

	for _, metric := range []string{"XPSNR", "VMAF"} {
		t.Run("ForceTarget"+metric, func(t *testing.T) {
			store := isolatedStore(t)
			p := sourcePreset(1)
			audio := 128
			p.AudioKbps = &audio
			seedPreset(t, store, p)
			before := configSnapshot(t, store)
			dst := filepath.Join(t.TempDir(), "forced.mp4")
			args := []string{src, dst, "0.08", "-ft", "-q", "off"}
			if metric == "VMAF" {
				args = append(args, "--vmaf")
			}
			code, out := runBinary(t, binary, args)
			assertExit(t, code, 0, out)
			m := assertPublished(t, fp, dst, 80000, "h264", "mp4", 0, 0)
			if m.Width > 320 || m.Height > 180 || !m.HasAudio {
				t.Fatalf("force output: %+v", m)
			}
			if !strings.Contains(strings.ToLower(out), "metric:       "+strings.ToLower(metric)) || !strings.Contains(out, "trial ") {
				t.Fatal(out)
			}
			t.Logf("metric=%s target=80000 bytes=%d", metric, m.Bytes)
			assertConfigUnchanged(t, store, before)
			assertNoParts(t, filepath.Dir(dst))
		})
	}

	t.Run("Infeasible", func(t *testing.T) {
		store := isolatedStore(t)
		seedPreset(t, store, sourcePreset(1))
		before := configSnapshot(t, store)
		dst := filepath.Join(t.TempDir(), "impossible.mp4")
		code, out := runBinary(t, binary, []string{src, dst, "0.001", "-ft", "-a", "128", "-q", "off"})
		assertExit(t, code, 4, out)
		assertAbsent(t, dst)
		assertNoParts(t, filepath.Dir(dst))
		assertConfigUnchanged(t, store, before)
	})

	t.Run("PresetsLatestAndForceInvariance", func(t *testing.T) {
		store := isolatedStore(t)
		p1, p2 := sourcePreset(1), sourcePreset(2)
		p2.Codec, p2.Speed = "hevc", "ultrafast"
		seedPreset(t, store, p1)
		seedPreset(t, store, p2)
		dst := filepath.Join(t.TempDir(), "preset.mp4")
		code, out := runBinary(t, binary, []string{src, dst, "0.3", "-ps", "2", "-q", "off"})
		assertExit(t, code, 0, out)
		assertPublished(t, fp, dst, 300000, "hevc", "mp4", 320, 180)
		legacy := []byte("{\n  \"version\":2,\"target_bytes\":20000000,\"codec\":\"av1\",\"container\":\"mp4\",\"preset\":\"12\",\"quality\":\"balanced\",\"crf\":null,\"audio_kbps\":128,\"fps\":null,\"resolution\":\"\",\"hw\":\"off\",\"verbose\":false\n}\n")
		if err := os.WriteFile(filepath.Join(store.Dir, "latest.json"), legacy, 0600); err != nil {
			t.Fatal(err)
		}
		before := configSnapshot(t, store)
		for _, tc := range []struct {
			flags         []string
			format, speed string
		}{
			{[]string{"-ps", "2"}, "mp4 / hevc", "ultrafast"},
			{[]string{"--latest"}, "mp4 / av1", "12"},
			{[]string{"--latest", "-c", "h264", "-p", "fast"}, "mp4 / h264", "fast"},
		} {
			args := append([]string{src, filepath.Join(t.TempDir(), "planned", "output.mp4"), "0.3", "-d", "-q", "off"}, tc.flags...)
			code, out := runBinary(t, binary, args)
			assertExit(t, code, 0, out)
			if !strings.Contains(out, "Format:       "+tc.format) || !strings.Contains(out, "Speed:        "+tc.speed) {
				t.Fatal(out)
			}
			assertConfigUnchanged(t, store, before)
		}
		forced := filepath.Join(t.TempDir(), "forced.mp4")
		code, out = runBinary(t, binary, []string{src, forced, "0.08", "--latest", "-c", "h264", "-p", "fast", "-ft", "-q", "off"})
		assertExit(t, code, 0, out)
		assertPublished(t, fp, forced, 80000, "h264", "mp4", 0, 0)
		if !strings.Contains(strings.ToLower(out), "metric:       xpsnr") {
			t.Fatal(out)
		}
		assertConfigUnchanged(t, store, before)
		for name, data := range before {
			if data != nil {
				t.Logf("unchanged %s SHA-256=%x", name, sha256.Sum256(data))
			}
		}
	})

	t.Run("DryRun", func(t *testing.T) {
		for _, seeded := range []bool{false, true} {
			t.Run(fmt.Sprint(seeded), func(t *testing.T) {
				store := isolatedStore(t)
				if seeded {
					seedPreset(t, store, config.FactoryPreset())
				}
				before := configSnapshot(t, store)
				for _, size := range []string{"", "0.001"} {
					dst := filepath.Join(t.TempDir(), "new", "nested", "output.mp4")
					args := []string{src, dst, "-d"}
					if size != "" {
						args = append(args, size, "-a", "128")
					}
					code, out := runBinary(t, binary, args)
					assertExit(t, code, 0, out)
					if !strings.Contains(out, "Dry run") || !strings.Contains(out, "warning:") || !strings.Contains(out, "Keep the source size?") {
						t.Fatal(out)
					}
					if size == "" && !strings.Contains(out, "convert without a size limit?") {
						t.Fatal(out)
					}
					if size != "" && !strings.Contains(out, "Use it?") {
						t.Fatal(out)
					}
					assertAbsent(t, filepath.Dir(filepath.Dir(dst)))
					assertConfigUnchanged(t, store, before)
				}
			})
		}
	})
	if !bytes.Equal(sourceBefore, fileBytes(t, src)) {
		t.Fatal("source changed")
	}
	assertNoParts(t, root)
}

func isolatedStore(t *testing.T) config.Store {
	t.Helper()
	profile := t.TempDir()
	t.Setenv("APPDATA", profile)
	t.Setenv("XDG_CONFIG_HOME", profile)
	t.Setenv("NO_COLOR", "1")
	return config.Store{Dir: filepath.Join(profile, "pressurecooker")}
}

func sourcePreset(id int) config.Preset {
	return config.Preset{ID: id, Label: "Source", Codec: "h264", Container: "mp4", Speed: "fast", Quality: "balanced"}
}

func seedPreset(t *testing.T, store config.Store, p config.Preset) {
	t.Helper()
	if _, err := store.SavePreset(p); err != nil {
		t.Fatal(err)
	}
}

func runBinary(t *testing.T, binary string, args []string) (int, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Stdin = strings.NewReader("")
	data, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatal(ctx.Err())
	}
	if err != nil {
		if exit, ok := err.(*exec.ExitError); ok {
			return exit.ExitCode(), string(data)
		}
		t.Fatal(err)
	}
	return 0, string(data)
}

func runInteractive(t *testing.T, store config.Store, args []string, answer string) (int, string) {
	t.Helper()
	terminal := true
	var out bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	code := cli.MainWith(ctx, args, "3.0.0", cli.Env{
		In: strings.NewReader(answer), Out: &out, IsTerminalIn: &terminal, IsTerminalOut: &terminal,
		Store: func() (config.Store, error) { return store, nil }, Runner: ffmpeg.ExecRunner{},
	})
	return code, out.String()
}

func generateClip(t *testing.T, ff, path, size, fps, duration string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	res, err := (ffmpeg.ExecRunner{}).Run(ctx, []string{ff, "-nostdin", "-y", "-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", "testsrc2=size=" + size + ":rate=" + fps + ":duration=" + duration,
		"-f", "lavfi", "-i", "sine=frequency=520:sample_rate=48000:duration=" + duration,
		"-c:v", "libx264", "-preset", "ultrafast", "-crf", "18", "-pix_fmt", "yuv420p", "-c:a", "aac", "-b:a", "96k", "-shortest", path}, nil)
	if err != nil || res.ExitCode != 0 {
		t.Fatalf("generate: %v %+v", err, res)
	}
}

func assertExit(t *testing.T, got, want int, out string) {
	t.Helper()
	if got != want {
		t.Fatalf("exit %d, want %d:\n%s", got, want, out)
	}
}

func assertPublished(t *testing.T, fp, path string, target int64, codec, container string, width, height int) ffmpeg.Media {
	t.Helper()
	m, err := ffmpeg.Probe(context.Background(), ffmpeg.ExecRunner{}, fp, path)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if m.Bytes <= 0 || m.Bytes != info.Size() || target > 0 && m.Bytes > target || m.VideoCodec != codec || m.Container != container || width > 0 && (m.Width != width || m.Height != height) {
		t.Fatalf("output %+v, target=%d codec=%s container=%s dims=%dx%d", m, target, codec, container, width, height)
	}
	return m
}

func configSnapshot(t *testing.T, store config.Store) map[string][]byte {
	t.Helper()
	files := map[string][]byte{}
	err := filepath.WalkDir(store.Dir, func(path string, d os.DirEntry, err error) error {
		if os.IsNotExist(err) && path == store.Dir {
			return nil
		}
		if err != nil {
			return err
		}
		if d.IsDir() {
			files[path] = nil
		} else {
			files[path] = fileBytes(t, path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

func assertConfigUnchanged(t *testing.T, store config.Store, before map[string][]byte) {
	t.Helper()
	if !reflect.DeepEqual(before, configSnapshot(t, store)) {
		t.Fatal("config bytes changed")
	}
	if len(before) == 0 {
		assertAbsent(t, store.Dir)
	}
}

func fileBytes(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func assertNoParts(t *testing.T, root string) {
	t.Helper()
	if err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && (strings.Contains(d.Name(), ".part") || strings.Contains(d.Name(), "passlog")) {
			t.Errorf("temporary output remains: %s", path)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
