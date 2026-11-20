package ffmpeg

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/maxie-maxie/pressurecooker/internal/console"
)

const (
	defaultStderrTail = 15
	verboseStderrTail = 512
)

// ResolveTool locates an executable by explicit path or PATH, refusing Windows batch wrappers.
func ResolveTool(explicit, defaultName string) (string, error) {
	return resolveToolForOS(explicit, defaultName, runtime.GOOS, exec.LookPath)
}

func resolveToolForOS(explicit, defaultName, goos string, lookPath func(string) (string, error)) (string, error) {
	trimmed := strings.TrimSpace(explicit)
	if trimmed != "" {
		if info, err := os.Stat(trimmed); err == nil && !info.IsDir() {
			return validateToolPath(trimmed, defaultName, goos)
		}
		if !strings.ContainsAny(trimmed, `/\`) {
			if found, err := lookPath(trimmed); err == nil && found != "" {
				return validateToolPath(found, defaultName, goos)
			}
		}
		return "", fmt.Errorf("%s not found at %q", defaultName, explicit)
	}
	found, err := lookPath(defaultName)
	if err != nil || found == "" {
		return "", fmt.Errorf("%s was not found on PATH; install ffmpeg or pass --%s <path>", defaultName, defaultName)
	}
	return validateToolPath(found, defaultName, goos)
}

func validateToolPath(path, defaultName, goos string) (string, error) {
	// Windows runs .bat/.cmd through cmd.exe even without a shell, so refuse batch wrappers.
	if goos == "windows" {
		ext := strings.ToLower(filepath.Ext(path))
		if ext == ".bat" || ext == ".cmd" {
			return "", fmt.Errorf("use %s.exe on Windows; batch wrappers are unsupported", defaultName)
		}
	}
	return filepath.Abs(path)
}

// Run executes args directly without a shell, streaming progress and draining stderr on separate goroutines.
func (ExecRunner) Run(ctx context.Context, args []string, onEvent func(console.Event)) (Result, error) {
	if len(args) == 0 {
		return Result{ExitCode: -1}, fmt.Errorf("command cannot be empty")
	}
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.WaitDelay = 3 * time.Second

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return Result{ExitCode: -1}, fmt.Errorf("stdout pipe: %w", err)
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return Result{ExitCode: -1}, fmt.Errorf("stderr pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return Result{ExitCode: -1}, err
	}

	maxTail := defaultStderrTail
	if hasFlagValue(args, "-loglevel", "verbose") {
		// Verbose hardware probes print GPU listings and capability lines before the final 15 lines.
		maxTail = verboseStderrTail
	}

	var (
		wg        sync.WaitGroup
		stdoutBuf bytes.Buffer
		stderr    []string
	)

	wg.Add(2)
	go func() {
		defer wg.Done()
		if onEvent != nil {
			_ = console.ParseProgress(stdoutPipe, onEvent)
			_, _ = io.Copy(io.Discard, stdoutPipe)
			return
		}
		_, _ = io.Copy(&stdoutBuf, stdoutPipe)
	}()

	go func() {
		defer wg.Done()
		stderr = readStderrTail(stderrPipe, maxTail)
	}()

	wg.Wait()
	waitErr := cmd.Wait()

	res := Result{
		Stdout:     stdoutBuf.String(),
		StderrTail: stderr,
		ExitCode:   0,
	}
	if waitErr != nil {
		var exitErr *exec.ExitError
		if errors.As(waitErr, &exitErr) {
			res.ExitCode = exitErr.ExitCode()
		} else {
			res.ExitCode = -1
		}
		if ctx.Err() != nil {
			return res, ctx.Err()
		}
		return res, waitErr
	}
	return res, nil
}

func readStderrTail(r io.Reader, limit int) []string {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	var tail []string
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		if len(tail) == limit {
			copy(tail, tail[1:])
			tail[limit-1] = line
		} else {
			tail = append(tail, line)
		}
	}
	return tail
}

func hasFlagValue(args []string, flag, value string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == flag && strings.EqualFold(args[i+1], value) {
			return true
		}
	}
	return false
}
