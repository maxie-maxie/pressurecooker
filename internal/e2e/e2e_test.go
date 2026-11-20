package e2e

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/maxie-maxie/pressurecooker/internal/cli"
	"github.com/maxie-maxie/pressurecooker/internal/config"
	"github.com/maxie-maxie/pressurecooker/internal/console"
	"github.com/maxie-maxie/pressurecooker/internal/ffmpeg"
)

func TestPathSafetyBeforeToolsAndWrites(t *testing.T) {
	for _, kind := range []string{"same-path", "existing", "empty-source", "directory-source", "missing-source"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			src, dst := filepath.Join(root, "input.mp4"), filepath.Join(root, "output.mp4")
			source := []byte("source unchanged")
			if err := os.WriteFile(src, source, 0600); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "same-path":
				dst = src
			case "existing":
				if err := os.WriteFile(dst, []byte("keep"), 0600); err != nil {
					t.Fatal(err)
				}
			case "empty-source":
				if err := os.WriteFile(src, nil, 0600); err != nil {
					t.Fatal(err)
				}
			case "directory-source":
				src = root
			case "missing-source":
				src += "missing"
			}
			configPath := filepath.Join(root, "isolated-config")
			var out bytes.Buffer
			code := cli.MainWith(context.Background(), []string{src, dst, "1"}, "test", cli.Env{
				Out: &out, Store: func() (config.Store, error) {
					t.Fatal("invalid path accessed config")
					return config.Store{Dir: configPath}, nil
				},
				ResolveTool: func(string, string) (string, error) { t.Fatal("invalid path invoked tool"); return "", nil },
			})
			if code != 2 || !strings.HasPrefix(out.String(), "error: ") {
				t.Fatalf("%d %s", code, out.String())
			}
			if _, err := os.Stat(configPath); !os.IsNotExist(err) {
				t.Fatal("created config")
			}
			if kind == "same-path" {
				after, _ := os.ReadFile(src)
				if !bytes.Equal(source, after) {
					t.Fatal("changed source")
				}
			}
			if kind == "existing" {
				after, _ := os.ReadFile(dst)
				if string(after) != "keep" {
					t.Fatal("changed destination")
				}
			}
			parts, _ := filepath.Glob(filepath.Join(root, "*.part"))
			if len(parts) > 0 {
				t.Fatalf("parts: %v", parts)
			}
		})
	}
}

func invalidArguments() [][]string {
	cases := [][]string{
		nil, {"--setup"}, {"input.mp4"}, {"input.mp4", "20"},
		{"input.mp4", "output.mp4", "1", "-t", "1"},
		{"input.mp4", "output.mp4", "1", "--crf", "30"},
		{"input.mp4", "output.mp4", "1", "-l", "small"},
		{"input.mp4", "output.mp4", "-ft"}, {"input.mp4", "output.mp4", "--vmaf"},
		{"input.mp4", "output.mp4", "--latest", "-ps", "2"},
		{"input.mp4", "output.mp4", "--crf", "30", "-l", "small"},
		{"--new-preset", "input.mp4", "output.mp4"}, {"--preset-list", "input.mp4", "output.mp4"},
		{"--new-preset", "-c", "h264"}, {"--preset-list", "-t", "1"},
	}
	for _, flag := range []string{"-v", "--verbose", "-co", "--conversion", "--defaults", "--force"} {
		cases = append(cases, []string{"input.mp4", "output.mp4", flag})
	}
	return cases
}

func TestInvalidArgumentsBeforeConfigAndTools(t *testing.T) {
	for i, args := range invalidArguments() {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			profile := t.TempDir()
			t.Setenv("APPDATA", profile)
			t.Setenv("XDG_CONFIG_HOME", profile)
			var out bytes.Buffer
			code := cli.MainWith(context.Background(), args, "test", cli.Env{
				Out:         &out,
				Store:       func() (config.Store, error) { t.Fatal("invalid arguments accessed config"); return config.Store{}, nil },
				ResolveTool: func(string, string) (string, error) { t.Fatal("invalid arguments accessed tools"); return "", nil },
			})
			if code != 2 || !strings.HasPrefix(out.String(), "error: ") {
				t.Fatalf("%v: exit %d, %s", args, code, out.String())
			}
			assertEmptyDir(t, profile)
		})
	}
}

type probeOnlyRunner struct{ t *testing.T }

func (r probeOnlyRunner) Run(_ context.Context, args []string, _ func(console.Event)) (ffmpeg.Result, error) {
	if args[0] != "ffprobe" {
		r.t.Fatalf("prompt invoked encoder: %v", args)
	}
	if strings.Contains(strings.Join(args, " "), "duration") {
		return ffmpeg.Result{Stdout: "5\n"}, nil
	}
	if strings.Contains(strings.Join(args, " "), "a:0") {
		return ffmpeg.Result{Stdout: `{"streams":[]}`}, nil
	}
	return ffmpeg.Result{Stdout: `{"format":{"duration":"5","size":"100","format_name":"mp4"},"streams":[{"codec_type":"video","codec_name":"h264","width":1280,"height":720,"avg_frame_rate":"30/1"}]}`}, nil
}

func TestNonTerminalPromptsPublishNothing(t *testing.T) {
	for _, kind := range []string{"conversion", "source-cap", "setup", "new-preset"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			profile := filepath.Join(root, "profile")
			if err := os.Mkdir(profile, 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("APPDATA", profile)
			t.Setenv("XDG_CONFIG_HOME", profile)
			src := filepath.Join(root, "source.mp4")
			if err := os.WriteFile(src, []byte("unchanged"), 0600); err != nil {
				t.Fatal(err)
			}
			dst := filepath.Join(root, "new", "output.mp4")
			args := []string{src, dst}
			switch kind {
			case "source-cap":
				args = append(args, "1")
			case "setup":
				args = append(args, "--setup")
			case "new-preset":
				args = []string{"--new-preset"}
			}
			var out bytes.Buffer
			code := cli.MainWith(context.Background(), args, "test", cli.Env{
				Out: &out, Runner: probeOnlyRunner{t},
				ResolveTool: func(_ string, name string) (string, error) { return name, nil },
			})
			if code != 2 || !strings.Contains(out.String(), "needs a terminal") {
				t.Fatalf("exit %d: %s", code, out.String())
			}
			assertAbsent(t, filepath.Dir(dst))
			assertEmptyDir(t, profile)
		})
	}
}

func assertAbsent(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("unexpected path %s: %v", path, err)
	}
}

func assertEmptyDir(t *testing.T, path string) {
	t.Helper()
	entries, err := os.ReadDir(path)
	if err != nil || len(entries) != 0 {
		t.Fatalf("unexpected entries in %s: %v, %v", path, entries, err)
	}
}
func TestReadOnlyCommandsWithIsolatedProfile(t *testing.T) {
	profile := t.TempDir()
	t.Setenv("APPDATA", profile)
	t.Setenv("XDG_CONFIG_HOME", profile)
	for _, args := range [][]string{{"--help"}, {"--help-details"}, {"--version"}, {"--preset-list"}} {
		var out bytes.Buffer
		if code := cli.MainWith(context.Background(), args, "test", cli.Env{Out: &out}); code != 0 {
			t.Fatalf("%v: %d %s", args, code, out.String())
		}
	}
	entries, err := os.ReadDir(profile)
	if err != nil || len(entries) != 0 {
		t.Fatalf("read-only command wrote config: %v %v", entries, err)
	}
}
