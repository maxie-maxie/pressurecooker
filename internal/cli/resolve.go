package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/maxie-maxie/pressurecooker/internal/config"
	"github.com/maxie-maxie/pressurecooker/internal/output"
)

var originKeys = []string{"codec", "container", "preset", "quality", "crf", "audio_codec", "audio", "fps", "resolution", "hw"}

func Resolve(parsed Run, preset *config.Settings, latest *config.Settings) (Run, error) {
	run := parsed
	run.Settings, run.Origins = config.FactoryPreset().Settings(), config.Origins{}
	for _, key := range originKeys {
		run.Origins[key] = "factory"
	}
	for _, layer := range []struct {
		settings *config.Settings
		origin   config.Origin
		use      bool
	}{{preset, "preset", true}, {latest, "latest", parsed.Latest}} {
		if !layer.use || layer.settings == nil {
			continue
		}
		settings := config.Clone(*layer.settings)
		if settings.AudioCodec == "" {
			settings.AudioCodec = "opus"
		}
		if err := config.Validate(settings); err != nil {
			return Run{}, fmt.Errorf("invalid %s settings: %w", layer.origin, err)
		}
		run.Settings = settings
		for _, key := range originKeys {
			run.Origins[key] = layer.origin
		}
	}
	for _, key := range originKeys {
		if parsed.Origins[key] != "explicit" {
			continue
		}
		run.Origins[key] = "explicit"
		switch key {
		case "codec":
			run.Settings.Codec = parsed.Settings.Codec
		case "container":
			run.Settings.Container = parsed.Settings.Container
		case "preset":
			run.Settings.Preset = parsed.Settings.Preset
		case "quality":
			run.Settings.Quality = parsed.Settings.Quality
			run.Settings.CRF, run.Origins["crf"] = nil, "factory"
		case "crf":
			run.Settings.CRF = config.Clone(parsed.Settings).CRF
			run.Settings.Quality, run.Origins["quality"] = "", "factory"
		case "audio":
			run.Settings.AudioKbps, run.Settings.AudioSource = parsed.Settings.AudioKbps, false
		case "audio_codec":
			run.Settings.AudioCodec = parsed.Settings.AudioCodec
		case "fps":
			run.Settings.FPS = config.Clone(parsed.Settings).FPS
		case "resolution":
			run.Settings.Resolution = parsed.Settings.Resolution
		case "hw":
			run.Settings.HW = parsed.Settings.HW
		}
	}
	if parsed.TargetBytes != nil {
		n := *parsed.TargetBytes
		run.TargetBytes, run.Origins["target"], run.Mode = &n, "explicit", ModeDefault
	} else {
		run.Mode = ModeConversion
	}
	return validateRun(run)
}

func destinationContainer(destination string) string {
	if strings.HasSuffix(destination, "/") || strings.HasSuffix(destination, "\\") {
		return ""
	}
	if info, err := os.Stat(destination); err == nil && info.IsDir() {
		return ""
	}
	return config.ContainerForExt(filepath.Ext(strings.ReplaceAll(destination, "\\", "/")))
}

func validateRun(run Run) (Run, error) {
	run.Warnings = nil
	run.Origins = cloneOrigins(run.Origins)
	if run.Settings.AudioCodec == "" {
		run.Settings.AudioCodec = "opus"
	}
	if container := destinationContainer(run.Destination); container != "" {
		if run.Origins["container"] == "explicit" && run.Settings.Container != container {
			return Run{}, fmt.Errorf("destination extension .%s conflicts with explicit container %s", container, run.Settings.Container)
		}
		run.Settings.Container = container
		if run.Origins["container"] != "explicit" {
			run.Origins["container"] = "extension"
		}
	}
	caps := config.CodecCaps(run.Settings.Codec)
	if run.Origins["codec"] == "explicit" {
		if run.Origins["preset"] != "explicit" && caps.Speed {
			if _, err := config.ResolvePreset(run.Settings.Preset, run.Settings.Codec, false); err != nil {
				for _, name := range []string{"ultrafast", "superfast", "veryfast", "faster", "fast", "medium", "slow", "slower", "veryslow", "placebo"} {
					if speed, err := config.ResolvePreset(name, "av1", false); err == nil && speed == run.Settings.Preset {
						run.Settings.Preset = name
						break
					}
				}
			}
		}
		if caps.CRFHi == 0 && run.Origins["crf"] != "explicit" && run.Settings.CRF != nil {
			run.Settings.CRF = nil
			if run.Origins["quality"] != "explicit" {
				run.Settings.Quality = ""
				if caps.Quality {
					run.Settings.Quality = "balanced"
				}
			}
		}
	}
	if run.TargetBytes != nil && !caps.SizeMode {
		return Run{}, fmt.Errorf("%s supports conversion only; size mode is unsupported", run.Settings.Codec)
	}
	if run.Origins["preset"] == "explicit" && !caps.Speed {
		return Run{}, fmt.Errorf("encoder speed is not supported for %s", run.Settings.Codec)
	}
	if run.Origins["quality"] == "explicit" && !caps.Quality {
		return Run{}, fmt.Errorf("quality is not supported for %s (lossless)", run.Settings.Codec)
	}
	if run.Origins["audio"] == "explicit" && run.Settings.AudioCodec == "flac" {
		return Run{}, fmt.Errorf("audio-kbps is not supported for flac (lossless)")
	}
	if !config.Accepts(run.Settings.Container, run.Settings.Codec, "") {
		return Run{}, config.Validate(run.Settings)
	}
	if !config.Accepts(run.Settings.Container, "", run.Settings.AudioCodec) && run.Origins["audio_codec"] != "explicit" {
		for _, codec := range []string{"opus", "aac", "mp3", "ac3"} {
			if config.Accepts(run.Settings.Container, "", codec) {
				run.Warnings = append(run.Warnings, fmt.Sprintf("%s does not accept %s; using %s", run.Settings.Container, run.Settings.AudioCodec, codec))
				run.Settings.AudioCodec = codec
				break
			}
		}
	}
	validation := config.Clone(run.Settings)
	// Size mode uses ABR, so inherited conversion quality cannot restrict hardware.
	if run.TargetBytes != nil {
		validation.CRF, validation.Quality = nil, "balanced"
	}
	if !caps.Quality {
		validation.Quality = ""
	}
	if err := config.Validate(validation); err != nil {
		return Run{}, err
	}
	return run, nil
}

// Check errors that do not depend on saved settings before opening the store.
func validateExplicitOptions(run Run) error {
	container := destinationContainer(run.Destination)
	if container != "" && run.Origins["container"] == "explicit" && container != run.Settings.Container {
		return fmt.Errorf("destination extension .%s conflicts with explicit container %s", container, run.Settings.Container)
	}
	if container == "" && run.Origins["container"] == "explicit" {
		container = run.Settings.Container
	}
	if run.Origins["audio"] == "explicit" && run.Origins["audio_codec"] == "explicit" && run.Settings.AudioCodec == "flac" {
		return fmt.Errorf("audio-kbps is not supported for flac (lossless)")
	}
	if run.Origins["codec"] == "explicit" {
		check := run
		check.Destination = ""
		check.Origins = cloneOrigins(run.Origins)
		if container == "" {
			check.Settings.Container = config.Recommended(run.Settings.Codec)
		} else {
			check.Settings.Container = container
		}
		_, err := validateRun(check)
		return err
	}
	if container != "" && run.Origins["audio_codec"] == "explicit" && !config.Accepts(container, "", run.Settings.AudioCodec) {
		check := run.Settings
		check.Container = container
		for _, codec := range config.VideoCodecs {
			if config.Accepts(container, codec, "") {
				check.Codec = codec
				break
			}
		}
		return config.Validate(check)
	}
	return nil
}

func resolveDestination(run Run) (output.Destination, error) {
	var bytes int64
	if run.TargetBytes != nil {
		bytes = *run.TargetBytes
	}
	return output.Resolve(run.Source, run.Destination, run.Settings, string(run.Mode), bytes, run.Overwrite)
}

func validateInitialPaths(run Run) error {
	if destinationContainer(run.Destination) != "" {
		return validatePaths(run)
	}
	if err := validateSource(run.Source); err != nil {
		return err
	}
	dir, err := filepath.Abs(run.Destination)
	if err != nil {
		return fmt.Errorf("invalid destination path: %w", err)
	}
	return validateDirectory(dir)
}

func validatePaths(run Run) error {
	if err := validateSource(run.Source); err != nil {
		return err
	}
	dest, err := resolveDestination(run)
	if err != nil {
		return err
	}
	if err := validateDirectory(dest.Dir); err != nil {
		return err
	}
	if info, err := os.Lstat(dest.Path); err == nil && !info.Mode().IsRegular() {
		return fmt.Errorf("destination must be a regular file: %s", dest.Path)
	} else if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("cannot inspect destination %s: %w", dest.Path, err)
	}
	return nil
}

func validateSource(source string) error {
	info, err := os.Stat(source)
	if err != nil {
		return fmt.Errorf("cannot inspect source file %s: %w", source, err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("source must be a regular file: %s", source)
	}
	if info.Size() == 0 {
		return fmt.Errorf("source file is empty: %s", source)
	}
	file, err := os.Open(source)
	if err != nil {
		return fmt.Errorf("cannot read source file %s: %w", source, err)
	}
	defer file.Close()
	info, err = file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
		return fmt.Errorf("source must be a readable, non-empty regular file: %s", source)
	}
	return nil
}

func validateDirectory(dir string) error {
	// Missing directories are allowed; every existing ancestor must be a directory.
	for parent := dir; ; parent = filepath.Dir(parent) {
		info, err := os.Stat(parent)
		if err == nil {
			if !info.IsDir() {
				return fmt.Errorf("destination parent is not a directory: %s", parent)
			}
			return nil
		}
		if !os.IsNotExist(err) {
			return fmt.Errorf("cannot inspect destination parent %s: %w", parent, err)
		}
		if next := filepath.Dir(parent); next == parent {
			return fmt.Errorf("destination parent does not exist: %s", parent)
		}
	}
}
