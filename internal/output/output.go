package output

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	"github.com/maxie-maxie/pressurecooker/internal/config"
)

type Destination struct {
	Path, Dir, Container string
}

// Resolve picks the final output path and container without creating directories.
func Resolve(source, destination string, settings config.Settings, mode string, targetBytes int64, overwrite bool) (Destination, error) {
	if strings.TrimSpace(source) == "" {
		return Destination{}, fmt.Errorf("source path cannot be empty")
	}
	if strings.TrimSpace(destination) == "" {
		return Destination{}, fmt.Errorf("destination path cannot be empty")
	}
	sourceFull, err := filepath.Abs(source)
	if err != nil {
		return Destination{}, fmt.Errorf("invalid source path %q: %w", source, err)
	}
	destFull, err := filepath.Abs(destination)
	if err != nil {
		return Destination{}, fmt.Errorf("invalid destination path %q: %w", destination, err)
	}

	isDir := looksLikeDir(destination, destFull)
	ext := strings.ToLower(filepath.Ext(destFull))
	extContainer := ""
	if !isDir {
		extContainer = config.ContainerForExt(ext)
	}

	reqContainer := strings.ToLower(strings.TrimSpace(settings.Container))
	if reqContainer != "" {
		if !slices.Contains(config.Containers, reqContainer) {
			return Destination{}, fmt.Errorf("container must be one of %s; got %q", strings.Join(config.Containers, ", "), settings.Container)
		}
		if extContainer != "" && extContainer != reqContainer {
			return Destination{}, fmt.Errorf("destination extension %q conflicts with container %s", ext, reqContainer)
		}
	}

	codec := strings.ToLower(strings.TrimSpace(settings.Codec))
	container := reqContainer
	if container == "" {
		container = extContainer
	}
	if container == "" {
		container = config.Recommended(codec)
	}
	if container == "" {
		container = "mp4"
	}
	if codec != "" && !config.Accepts(container, codec, "") {
		return Destination{}, fmt.Errorf("%s does not accept %s", container, codec)
	}

	var outPath, outDir string
	if isDir {
		base := filepath.Base(sourceFull)
		stem := strings.TrimSuffix(base, filepath.Ext(base))
		if stem == "" {
			stem = "output"
		}
		name, err := autoName(stem, container, settings, mode, targetBytes)
		if err != nil {
			return Destination{}, err
		}
		outDir = destFull
		outPath = filepath.Join(destFull, name)
	} else {
		outPath = destFull
		outDir = filepath.Dir(outPath)
	}

	if err := RejectAlias(sourceFull, outPath); err != nil {
		return Destination{}, err
	}
	if _, err := os.Lstat(outPath); err == nil && !overwrite {
		return Destination{}, fmt.Errorf("destination already exists: %s; pass --overwrite to replace it", outPath)
	}

	return Destination{Path: outPath, Dir: outDir, Container: container}, nil
}

func looksLikeDir(raw, absPath string) bool {
	if strings.HasSuffix(raw, "/") || strings.HasSuffix(raw, `\`) {
		return true
	}
	if info, err := os.Stat(absPath); err == nil && info.IsDir() {
		return true
	}
	return config.ContainerForExt(filepath.Ext(absPath)) == ""
}

func autoName(stem, container string, settings config.Settings, mode string, targetBytes int64) (string, error) {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "conversion":
		codec := strings.ToLower(strings.TrimSpace(settings.Codec))
		if codec == "" {
			codec = "hevc"
		}
		return fmt.Sprintf("%s.%s.%s", stem, codec, container), nil
	case "", "default":
		if targetBytes <= 0 {
			return "", fmt.Errorf("target bytes must be positive for directory auto-naming")
		}
		return fmt.Sprintf("%s.fit%s.%s", stem, formatMBTag(targetBytes), container), nil
	default:
		return "", fmt.Errorf("unknown mode %q", mode)
	}
}

func formatMBTag(bytes int64) string {
	text := strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.3f", float64(bytes)/1e6), "0"), ".")
	if text == "" || text == "-0" {
		return "0"
	}
	return text
}

// RejectAlias fails if source and destination refer to the same file or path alias.
func RejectAlias(source, destination string) error {
	srcAbs, err := filepath.Abs(source)
	if err != nil {
		return fmt.Errorf("invalid source path %q: %w", source, err)
	}
	dstAbs, err := filepath.Abs(destination)
	if err != nil {
		return fmt.Errorf("invalid destination path %q: %w", destination, err)
	}

	srcReal := resolveReal(srcAbs)
	dstReal := resolveReal(dstAbs)
	if sameNormalizedPath(srcReal, dstReal) {
		return fmt.Errorf("output path must not be the same file as the source")
	}

	srcInfo, srcErr := os.Stat(srcAbs)
	dstInfo, dstErr := os.Stat(dstAbs)
	if srcErr == nil && dstErr == nil && os.SameFile(srcInfo, dstInfo) {
		return fmt.Errorf("output path refers to the source file (link or alias)")
	}
	return nil
}

func resolveReal(absPath string) string {
	if resolved, err := filepath.EvalSymlinks(absPath); err == nil {
		return resolved
	}
	// Resolve the parent directory when the file does not exist yet to catch symlinked dirs.
	dir, base := filepath.Dir(absPath), filepath.Base(absPath)
	if resolvedDir, err := filepath.EvalSymlinks(dir); err == nil {
		return filepath.Join(resolvedDir, base)
	}
	return filepath.Clean(absPath)
}

func sameNormalizedPath(a, b string) bool {
	a, b = filepath.Clean(a), filepath.Clean(b)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

// Temp creates the destination directory if needed and returns a unique .part file path inside it.
func Temp(dest Destination) (string, error) {
	if dest.Dir != "" {
		if err := os.MkdirAll(dest.Dir, 0o755); err != nil {
			return "", fmt.Errorf("cannot prepare output directory %s: %w", dest.Dir, err)
		}
	}
	f, err := os.CreateTemp(dest.Dir, ".pressurecooker-*.part")
	if err != nil {
		return "", fmt.Errorf("cannot create temporary output file: %w", err)
	}
	name := f.Name()
	if err := f.Close(); err != nil {
		_ = os.Remove(name)
		return "", fmt.Errorf("cannot close temporary output file: %w", err)
	}
	return name, nil
}

// Publish atomically moves part to destination after re-checking source aliasing.
func Publish(part string, destination Destination, source string, overwrite bool) error {
	if err := RejectAlias(source, destination.Path); err != nil {
		return err
	}
	return publishFile(part, destination.Path, overwrite)
}

func publishLinkNoOverwrite(part, dest string, linkFn func(string, string) error, removeFn func(string) error) error {
	if err := linkFn(part, dest); err != nil {
		if errors.Is(err, os.ErrExist) {
			return fmt.Errorf("destination appeared during encoding: %s; pass --overwrite to replace it", dest)
		}
		return fmt.Errorf("cannot finalize output: %w", err)
	}
	if err := removeFn(part); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("published output but failed to remove temp file %s: %w", part, err)
	}
	return nil
}
