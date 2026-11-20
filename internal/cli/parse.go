package cli

import (
	"fmt"
	"math"
	"math/big"
	"regexp"
	"strconv"
	"strings"

	"github.com/maxie-maxie/pressurecooker/internal/config"
)

type Mode string

const (
	ModeDefault    Mode = "default"
	ModeConversion Mode = "conversion"
)

type Run struct {
	Settings                             config.Settings
	Origins                              config.Origins
	Source, Destination, FFmpeg, FFprobe string
	TargetBytes                          *int64
	Mode                                 Mode
	DryRun, Overwrite, Setup, Latest     bool
	ShowHelp, ShowDetails, ShowVersion   bool
	ForceTarget, VMAF, ListPresets       bool
	PresetID                             int
	NewPreset                            *int
}

var aliases = map[string]string{
	"-t": "target", "--target": "target",
	"-c": "codec", "--codec": "codec", "-f": "container", "--container": "container",
	"-p": "preset", "--preset": "preset", "-l": "quality", "--quality": "quality", "--crf": "crf",
	"-a": "audio", "--audio-kbps": "audio", "-r": "fps", "--fps": "fps",
	"-s": "resolution", "--resolution": "resolution", "-q": "hw", "--hw": "hw",
	"-ps": "use-preset", "--use-preset": "use-preset",
	"-d": "dry-run", "--dry-run": "dry-run",
	"-ff": "ffmpeg", "--ffmpeg": "ffmpeg", "-fp": "ffprobe", "--ffprobe": "ffprobe",
	"-ft": "force-target", "--force-target": "force-target", "--vmaf": "vmaf",
	"--setup": "setup", "--latest": "latest", "--overwrite": "overwrite",
	"--new-preset": "new-preset", "--preset-list": "preset-list",
}

var retired = map[string]string{
	"-v": "progress is always shown", "--verbose": "progress is always shown",
	"-co": "omit the size to convert", "--conversion": "omit the size to convert",
	"--defaults": "use --new-preset 1", "--force": "use -ft",
}

func flagName(token string) string {
	name, _, _ := strings.Cut(token, "=")
	return strings.ToLower(name)
}

func Parse(args []string) (Run, error) {
	run := Run{Settings: config.FactoryPreset().Settings(), Origins: config.Origins{}, Mode: ModeConversion, PresetID: 1}
	for _, token := range args {
		if token == "--" {
			break
		}
		switch flagName(token) {
		case "-h", "--help", "/?", "-?":
			run.ShowHelp = true
		case "--help-details":
			run.ShowDetails = true
		case "--version":
			run.ShowVersion = true
		}
	}
	// Information requests bypass invalid options and configuration access.
	if run.ShowHelp || run.ShowDetails || run.ShowVersion {
		return run, nil
	}
	var positional []string
	processing := false
	for i := 0; i < len(args); i++ {
		token := args[i]
		if token == "--" {
			positional = append(positional, args[i+1:]...)
			break
		}
		name := flagName(token)
		if replacement, ok := retired[name]; ok {
			return Run{}, fmt.Errorf("%s was removed; %s", name, replacement)
		}
		key, known := aliases[name]
		if !known {
			if strings.HasPrefix(token, "-") && token != "-" {
				return Run{}, fmt.Errorf("unknown option: %s", token)
			}
			positional = append(positional, token)
			continue
		}
		if key != "new-preset" && key != "preset-list" {
			processing = true
		}
		_, inline, hasInline := strings.Cut(token, "=")
		switch key {
		case "new-preset":
			n := 0
			if hasInline || (i+1 < len(args) && args[i+1] != "--" && !strings.HasPrefix(args[i+1], "-")) {
				value := inline
				if !hasInline {
					i++
					value = args[i]
				}
				var err error
				n, err = parsePresetID(value)
				if err != nil {
					return Run{}, err
				}
			}
			run.NewPreset = &n
		case "dry-run", "force-target", "vmaf", "setup", "latest", "overwrite", "preset-list":
			if hasInline {
				return Run{}, fmt.Errorf("%s does not take a value", name)
			}
			switch key {
			case "dry-run":
				run.DryRun = true
			case "force-target":
				run.ForceTarget = true
			case "vmaf":
				run.VMAF = true
			case "setup":
				run.Setup = true
			case "latest":
				run.Latest = true
			case "overwrite":
				run.Overwrite = true
			case "preset-list":
				run.ListPresets = true
			}
		default:
			value := inline
			if !hasInline {
				if i+1 == len(args) || args[i+1] == "--" || isOption(args[i+1]) {
					return Run{}, fmt.Errorf("%s requires a value", name)
				}
				i++
				value = args[i]
			}
			if value == "" {
				return Run{}, fmt.Errorf("%s requires a value", name)
			}
			if err := setOption(&run, key, value); err != nil {
				return Run{}, err
			}
			if key != "ffmpeg" && key != "ffprobe" {
				run.Origins[key] = "explicit"
			}
		}
	}
	if run.NewPreset != nil || run.ListPresets {
		if processing || len(positional) > 0 || (run.NewPreset != nil && run.ListPresets) {
			return Run{}, fmt.Errorf("--new-preset and --preset-list cannot be combined with paths or processing flags")
		}
		return run, nil
	}
	if len(positional) > 3 {
		return Run{}, fmt.Errorf("expected <in> <out> [size]")
	}
	if len(positional) == 1 || (len(positional) == 2 && bareNumber.MatchString(positional[1])) {
		return Run{}, fmt.Errorf("missing <destination>")
	}
	if len(positional) < 2 {
		if run.Setup {
			return Run{}, fmt.Errorf("--setup requires <in> <out>")
		}
		return Run{}, fmt.Errorf("missing <source-video> and <destination>")
	}
	if strings.TrimSpace(positional[0]) == "" || strings.TrimSpace(positional[1]) == "" {
		return Run{}, fmt.Errorf("source-video and destination cannot be empty")
	}
	run.Source, run.Destination = positional[0], positional[1]
	if len(positional) == 3 {
		if run.TargetBytes != nil {
			return Run{}, fmt.Errorf("cannot combine positional size and --target")
		}
		n, err := parseTarget(positional[2])
		if err != nil {
			return Run{}, err
		}
		run.TargetBytes, run.Origins["target"] = &n, "explicit"
	}
	if run.TargetBytes != nil {
		run.Mode = ModeDefault
		if run.Origins["crf"] == "explicit" || run.Origins["quality"] == "explicit" {
			return Run{}, fmt.Errorf("--crf and --quality apply only without a size")
		}
	} else if run.ForceTarget || run.VMAF {
		return Run{}, fmt.Errorf("-ft and --vmaf require a size")
	}
	if run.Latest && run.Origins["use-preset"] == "explicit" {
		return Run{}, fmt.Errorf("cannot combine --latest and --use-preset")
	}
	if run.Origins["crf"] == "explicit" && run.Origins["quality"] == "explicit" {
		return Run{}, fmt.Errorf("cannot combine --crf and --quality")
	}
	return run, nil
}

func isOption(token string) bool {
	_, known := aliases[flagName(token)]
	_, removed := retired[flagName(token)]
	return known || removed || strings.HasPrefix(token, "--")
}

func parsePresetID(value string) (int, error) {
	n, err := strconv.Atoi(value)
	if err != nil || n < 1 || !bareInteger.MatchString(value) {
		return 0, fmt.Errorf("preset number must be an integer >= 1")
	}
	return n, nil
}

var bareInteger = regexp.MustCompile(`^[0-9]+$`)
var bareNumber = regexp.MustCompile(`^[0-9]+(\.[0-9]+)?$`)
var decimalNumber = regexp.MustCompile(`^([0-9]+(\.[0-9]*)?|\.[0-9]+)([eE][+-]?[0-9]+)?$`)

func parseTarget(text string) (int64, error) {
	invalid := fmt.Errorf("target-size-mb must be a positive finite decimal number within the supported byte range")
	v, err := strconv.ParseFloat(text, 64)
	if err != nil || !decimalNumber.MatchString(text) || math.IsNaN(v) || math.IsInf(v, 0) || v <= 0 {
		return 0, invalid
	}
	// Decimal arithmetic keeps the last byte correct near the int64 limit.
	r, ok := new(big.Rat).SetString(text)
	if !ok {
		return 0, invalid
	}
	r.Mul(r, big.NewRat(1000000, 1))
	whole, remainder := new(big.Int), new(big.Int)
	whole.QuoRem(r.Num(), r.Denom(), remainder)
	if remainder.Lsh(remainder, 1).Cmp(r.Denom()) >= 0 {
		whole.Add(whole, big.NewInt(1))
	}
	if whole.Sign() <= 0 || !whole.IsInt64() {
		return 0, invalid
	}
	return whole.Int64(), nil
}

func setOption(run *Run, key, value string) error {
	lower := strings.ToLower(value)
	switch key {
	case "target":
		n, err := parseTarget(value)
		if err != nil {
			return err
		}
		run.TargetBytes = &n
	case "use-preset":
		n, err := parsePresetID(value)
		if err != nil {
			return err
		}
		run.PresetID = n
	case "codec":
		if lower != "h264" && lower != "hevc" && lower != "av1" {
			return fmt.Errorf("codec must be h264, hevc or av1")
		}
		run.Settings.Codec = lower
	case "container":
		if lower != "mp4" && lower != "mkv" && lower != "webm" {
			return fmt.Errorf("container must be mp4, mkv or webm")
		}
		run.Settings.Container = lower
	case "quality":
		if lower != "high" && lower != "balanced" && lower != "small" {
			return fmt.Errorf("quality must be high, balanced or small")
		}
		run.Settings.Quality = lower
	case "preset":
		if _, err := config.ResolvePreset(value, "av1", false); err != nil {
			return err
		}
		run.Settings.Preset = strings.ToLower(strings.TrimSpace(value))
	case "hw":
		if lower != "auto" && lower != "vulkan" && lower != "qsv" && lower != "off" {
			return fmt.Errorf("hw must be auto, vulkan, qsv or off")
		}
		run.Settings.HW = lower
	case "audio":
		n, err := strconv.Atoi(value)
		if err != nil || n <= 0 {
			return fmt.Errorf("audio-kbps must be a positive integer")
		}
		run.Settings.AudioKbps, run.Settings.AudioSource = n, false
	case "fps", "crf":
		n, err := strconv.ParseFloat(value, 64)
		decimal := strings.TrimPrefix(strings.TrimPrefix(value, "-"), "+")
		if err != nil || !decimalNumber.MatchString(decimal) || math.IsNaN(n) || math.IsInf(n, 0) {
			return fmt.Errorf("%s must be a finite number", key)
		}
		if key == "fps" {
			if n <= 0 || n > 1000 {
				return fmt.Errorf("fps must be greater than 0 and at most 1000")
			}
			run.Settings.FPS = &n
		} else {
			if n < 0 || n > 63 {
				return fmt.Errorf("crf must be from 0 to 51 for h264/hevc, or 1 to 63 for av1")
			}
			run.Settings.CRF = &n
		}
	case "resolution":
		if _, _, err := config.ParseResolution(value); err != nil {
			return err
		}
		run.Settings.Resolution = strings.ToLower(strings.TrimSpace(value))
	case "ffmpeg":
		run.FFmpeg = value
	case "ffprobe":
		run.FFprobe = value
	}
	return nil
}
