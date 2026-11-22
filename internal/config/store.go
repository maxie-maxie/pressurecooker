package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
)

type Store struct{ Dir string }

func UserStore() (Store, error) {
	return userStore(runtime.GOOS)
}

func userStore(goos string) (Store, error) {
	env, fallback := "XDG_CONFIG_HOME", ".config"
	if goos == "windows" {
		env, fallback = "APPDATA", filepath.Join("AppData", "Roaming")
	}
	base := os.Getenv(env)
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return Store{}, fmt.Errorf("configuration directory: %w", err)
		}
		base = filepath.Join(home, fallback)
	}
	return Store{Dir: filepath.Join(base, "pressurecooker")}, nil
}

func (s Store) LoadLatest() (*Settings, error) {
	path := filepath.Join(s.Dir, "latest.json")
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	settings, err := decodeLatest(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &settings, nil
}

var settingKeys = []string{"target_bytes", "codec", "container", "preset", "quality", "crf", "audio_kbps", "fps", "resolution", "hw", "verbose"}

func decodeSettings(data []byte, legacy bool) (Settings, error) {
	fields, err := decodeObject(data)
	if err != nil {
		return Settings{}, err
	}
	var version int
	if raw, ok := fields["version"]; !ok || json.Unmarshal(raw, &version) != nil || (version != 2 && !(legacy && version == 1)) {
		return Settings{}, fmt.Errorf("unsupported configuration version; expected 2")
	}
	keys := settingKeys
	if version == 1 {
		keys = []string{"target_mib", "codec", "container", "preset", "audio_kbps", "fps", "resolution", "hw", "verbose"}
	}
	allowed := append([]string{"version"}, keys...)
	if err := requireKeys(fields, keys, allowed); err != nil {
		return Settings{}, err
	}
	for _, key := range keys {
		value := fields[key]
		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) && key != "crf" && key != "fps" && key != "resolution" {
			return Settings{}, fmt.Errorf("configuration key %q cannot be null", key)
		}
	}
	if version == 2 {
		var target int64
		if err := json.Unmarshal(fields["target_bytes"], &target); err != nil || target <= 0 {
			return Settings{}, fmt.Errorf("target_bytes must be a positive integer")
		}
	}
	var verbose bool
	if err := json.Unmarshal(fields["verbose"], &verbose); err != nil {
		return Settings{}, fmt.Errorf("verbose must be a boolean")
	}
	var result Settings
	if err := json.Unmarshal(data, &result); err != nil {
		return Settings{}, fmt.Errorf("invalid settings: %w", err)
	}
	if version == 1 {
		var mib float64
		if err := json.Unmarshal(fields["target_mib"], &mib); err != nil {
			return Settings{}, fmt.Errorf("target_mib must be a finite positive number")
		}
		value := math.Round(mib * 1048576)
		if math.IsNaN(value) || math.IsInf(value, 0) || value <= 0 || value >= 0x1p63 {
			return Settings{}, fmt.Errorf("target_mib is outside the supported byte range")
		}
		result.Quality = "balanced"
	}
	if legacy {
		result.AudioCodec = "opus"
		if result.CRF != nil {
			result.Quality = ""
		}
	}
	return result, Validate(result)
}

func decodeLatest(data []byte) (Settings, error) {
	fields, err := decodeObject(data)
	if err != nil {
		return Settings{}, err
	}
	var version int
	if raw, ok := fields["version"]; !ok || json.Unmarshal(raw, &version) != nil {
		return Settings{}, fmt.Errorf("invalid latest version")
	}
	if version == 1 || version == 2 {
		result, err := decodeSettings(data, true)
		if err != nil {
			return Settings{}, err
		}
		result.AudioSource = false
		if err := validateLatest(result); err != nil {
			return Settings{}, err
		}
		return result, nil
	}
	if version != 3 && version != 4 {
		return Settings{}, fmt.Errorf("unsupported latest version; expected 4")
	}
	return decodeLatestV34(fields, version)
}

func decodeLatestV34(fields map[string]json.RawMessage, version int) (Settings, error) {
	required := []string{"version", "codec", "container", "preset", "audio_kbps", "fps", "resolution", "hw"}
	if version == 4 {
		required = append(required, "audio_codec")
	}
	allowed := append(append([]string{}, required...), "quality", "crf")
	if err := requireKeys(fields, required, allowed); err != nil {
		return Settings{}, err
	}
	result := Factory()
	result.Quality, result.CRF, result.AudioSource = "", nil, false
	targets := map[string]any{
		"codec": &result.Codec, "container": &result.Container, "preset": &result.Preset,
		"fps": &result.FPS, "resolution": &result.Resolution, "hw": &result.HW,
	}
	if version == 4 {
		targets["audio_codec"] = &result.AudioCodec
	} else {
		result.AudioCodec = "opus"
	}
	for key, destination := range targets {
		if key != "fps" && isJSONNull(fields[key]) {
			return Settings{}, fmt.Errorf("%s cannot be null", key)
		}
		if err := json.Unmarshal(fields[key], destination); err != nil {
			return Settings{}, fmt.Errorf("invalid %s: %w", key, err)
		}
	}
	if err := decodeQualityAndCRF(fields, result.Codec, &result.Quality, &result.CRF); err != nil {
		return Settings{}, fmt.Errorf("latest: %w", err)
	}
	if isJSONNull(fields["audio_kbps"]) {
		result.AudioKbps, result.AudioSource = 128, true
	} else if err := json.Unmarshal(fields["audio_kbps"], &result.AudioKbps); err != nil {
		return Settings{}, fmt.Errorf("invalid audio_kbps: %w", err)
	}
	if err := validateLatest(result); err != nil {
		return Settings{}, err
	}
	return result, nil
}

func validateLatest(s Settings) error {
	var audio *int
	if !s.AudioSource {
		audio = &s.AudioKbps
	}
	if err := ValidatePreset(Preset{
		Label: "latest", Codec: s.Codec, Container: s.Container, Speed: s.Preset,
		Quality: s.Quality, CRF: s.CRF, AudioCodec: s.AudioCodec, AudioKbps: audio, FPS: s.FPS, Resolution: s.Resolution,
	}); err != nil {
		return err
	}
	if s.HW != "auto" && s.HW != "vulkan" && s.HW != "qsv" && s.HW != "off" {
		return fmt.Errorf("hw must be auto, vulkan, qsv or off")
	}
	caps := CodecCaps(s.Codec)
	if (s.HW == "vulkan" || s.HW == "qsv") && !slices.Contains(caps.HW, s.HW) {
		return fmt.Errorf("%s does not support %s hardware encoding", s.Codec, s.HW)
	}
	if s.CRF != nil && (s.HW == "vulkan" || s.HW == "qsv") {
		return fmt.Errorf("raw CRF is unsupported on %s; use --quality or --hw off", s.HW)
	}
	if s.HW == "qsv" && caps.Speed {
		if _, err := ResolvePreset(s.Preset, s.Codec, true); err != nil {
			return err
		}
	}
	return nil
}

type latestFile struct {
	Version    int      `json:"version"`
	Codec      string   `json:"codec"`
	Container  string   `json:"container"`
	Preset     string   `json:"preset"`
	Quality    string   `json:"quality,omitempty"`
	CRF        *float64 `json:"crf,omitempty"`
	AudioCodec string   `json:"audio_codec"`
	AudioKbps  *int     `json:"audio_kbps"`
	FPS        *float64 `json:"fps"`
	Resolution string   `json:"resolution"`
	HW         string   `json:"hw"`
}

func (s Store) SaveLatest(settings Settings) error {
	path := filepath.Join(s.Dir, "latest.json")
	settings = Clone(settings)
	if settings.CRF != nil {
		settings.Quality = ""
	}
	if settings.AudioSource {
		settings.AudioKbps = 128
	}
	if err := validateLatest(settings); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	if existing, err := os.ReadFile(path); err == nil {
		if _, err := decodeLatest(existing); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("%s: %w", path, err)
	}
	var audio *int
	if !settings.AudioSource {
		audio = &settings.AudioKbps
	}
	data, err := json.MarshalIndent(latestFile{
		Version: 4, Codec: settings.Codec, Container: settings.Container, Preset: settings.Preset,
		Quality: settings.Quality, CRF: settings.CRF, AudioCodec: settings.AudioCodec, AudioKbps: audio, FPS: settings.FPS,
		Resolution: settings.Resolution, HW: settings.HW,
	}, "", "  ")
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return s.saveData("latest.json", append(data, '\n'), writeSynced, replaceFile)
}

func writeSynced(file *os.File, data []byte) error {
	if _, err := file.Write(data); err != nil {
		return err
	}
	return file.Sync()
}

func (s Store) saveData(name string, data []byte, write func(*os.File, []byte) error, replace func(string, string) error) error {
	target := filepath.Join(s.Dir, name)
	if err := os.MkdirAll(s.Dir, 0700); err != nil {
		return fmt.Errorf("%s: %w", target, err)
	}
	file, err := os.CreateTemp(s.Dir, "."+name+"-*.tmp")
	if err != nil {
		return fmt.Errorf("%s: %w", target, err)
	}
	defer os.Remove(file.Name())
	err = write(file, data)
	closeErr := file.Close()
	if err == nil {
		err = closeErr
	}
	if err == nil {
		err = replace(file.Name(), target)
	}
	if err != nil {
		return fmt.Errorf("%s: %w", target, err)
	}
	return nil
}

func decodeObject(data []byte) (map[string]json.RawMessage, error) {
	d := json.NewDecoder(bytes.NewReader(data))
	start, err := d.Token()
	if err != nil || start != json.Delim('{') {
		return nil, fmt.Errorf("configuration must be a JSON object")
	}
	fields := map[string]json.RawMessage{}
	for d.More() {
		token, err := d.Token()
		if err != nil {
			return nil, fmt.Errorf("invalid JSON: %w", err)
		}
		key, ok := token.(string)
		if !ok {
			return nil, fmt.Errorf("configuration keys must be text")
		}
		if _, exists := fields[key]; exists {
			return nil, fmt.Errorf("duplicate configuration key %q", key)
		}
		var value json.RawMessage
		if err := d.Decode(&value); err != nil {
			return nil, fmt.Errorf("invalid JSON: %w", err)
		}
		fields[key] = value
	}
	if _, err := d.Token(); err != nil {
		return nil, fmt.Errorf("invalid JSON: %w", err)
	}
	if _, err := d.Token(); err != io.EOF {
		return nil, fmt.Errorf("unexpected content after configuration object")
	}
	return fields, nil
}

func requireKeys(fields map[string]json.RawMessage, required, allowed []string) error {
	for _, key := range required {
		if _, ok := fields[key]; !ok {
			return fmt.Errorf("missing configuration key %q", key)
		}
	}
	allow := make(map[string]bool, len(allowed))
	for _, key := range allowed {
		allow[key] = true
	}
	var extra []string
	for key := range fields {
		if !allow[key] {
			extra = append(extra, key)
		}
	}
	if len(extra) > 0 {
		sort.Strings(extra)
		return fmt.Errorf("unknown configuration keys: %v", extra)
	}
	return nil
}

func isJSONNull(data []byte) bool {
	return bytes.Equal(bytes.TrimSpace(data), []byte("null"))
}

func firstNonSpace(data []byte) byte {
	data = bytes.TrimSpace(data)
	if len(data) == 0 {
		return 0
	}
	return data[0]
}
