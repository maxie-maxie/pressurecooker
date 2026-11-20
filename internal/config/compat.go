package config

import (
	"fmt"
	"slices"
	"strings"
)

var (
	Containers  = []string{"mp4", "mkv", "webm", "mov", "avi", "ogg", "ts", "nut"}
	VideoCodecs = []string{"av1", "hevc", "h264", "vp9", "prores", "ffv1", "theora"}
	AudioCodecs = []string{"opus", "aac", "mp3", "flac", "ac3"}
)

type muxRule struct{ video, audio []string }

// Measured against FFmpeg N-124716: every listed pair muxes, every other pair fails.
var muxRules = map[string]muxRule{
	"mp4":  {[]string{"av1", "hevc", "h264", "vp9", "ffv1"}, AudioCodecs},
	"mkv":  {VideoCodecs, AudioCodecs},
	"webm": {[]string{"av1", "vp9"}, []string{"opus"}},
	"mov":  {[]string{"hevc", "h264", "prores", "ffv1", "theora"}, []string{"aac", "mp3", "ac3"}},
	"avi":  {VideoCodecs, []string{"aac", "mp3", "flac", "ac3"}},
	"ogg":  {[]string{"theora"}, []string{"opus", "flac"}},
	"ts":   {VideoCodecs, AudioCodecs},
	"nut":  {VideoCodecs, AudioCodecs},
}

// Accepts reports whether container can hold the codecs; an empty codec is not checked.
func Accepts(container, videoCodec, audioCodec string) bool {
	rule, ok := muxRules[container]
	if !ok {
		return false
	}
	if videoCodec != "" && !slices.Contains(rule.video, videoCodec) {
		return false
	}
	return audioCodec == "" || slices.Contains(rule.audio, audioCodec)
}

func formatChoiceList(items []string) string {
	if len(items) <= 1 {
		return strings.Join(items, "")
	}
	return strings.Join(items[:len(items)-1], ", ") + " or " + items[len(items)-1]
}

func videoMismatchError(container, codec string) error {
	return fmt.Errorf("%s does not accept %s; use -c %s", container, codec, formatChoiceList(muxRules[container].video))
}

func audioMismatchError(container, audioCodec string) error {
	return fmt.Errorf("%s does not accept %s; use -ac %s", container, audioCodec, formatChoiceList(muxRules[container].audio))
}

func Recommended(videoCodec string) string {
	switch videoCodec {
	case "vp9":
		return "webm"
	case "prores":
		return "mov"
	case "ffv1":
		return "mkv"
	case "theora":
		return "ogg"
	case "av1", "hevc", "h264":
		return "mp4"
	}
	return ""
}

// ContainerForExt maps a file extension (with or without the dot) to a container name.
func ContainerForExt(ext string) string {
	switch strings.TrimPrefix(strings.ToLower(ext), ".") {
	case "mp4":
		return "mp4"
	case "mkv":
		return "mkv"
	case "webm":
		return "webm"
	case "mov", "qt":
		return "mov"
	case "avi":
		return "avi"
	case "ogg", "ogv":
		return "ogg"
	case "ts":
		return "ts"
	case "nut":
		return "nut"
	}
	return ""
}

// Caps lists the controls a video codec supports. CRFHi == 0 means no CRF.
type Caps struct {
	Speed, Quality, SizeMode bool
	CRFLo, CRFHi             float64
	HW                       []string
}

func CodecCaps(codec string) Caps {
	switch codec {
	case "av1":
		return Caps{Speed: true, Quality: true, SizeMode: true, CRFLo: 1, CRFHi: 63, HW: []string{"vulkan", "qsv"}}
	case "hevc", "h264":
		return Caps{Speed: true, Quality: true, SizeMode: true, CRFLo: 0, CRFHi: 51, HW: []string{"vulkan", "qsv"}}
	case "vp9":
		return Caps{Speed: true, Quality: true, SizeMode: true, CRFLo: 0, CRFHi: 63, HW: []string{"qsv"}}
	case "prores":
		return Caps{Quality: true, HW: []string{"vulkan"}}
	case "ffv1":
		return Caps{HW: []string{"vulkan"}}
	case "theora":
		return Caps{Quality: true, SizeMode: true}
	}
	return Caps{}
}
