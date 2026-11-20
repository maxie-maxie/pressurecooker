package config

import (
	"reflect"
	"slices"
	"testing"
)

func TestCompatMatrixAllCells(t *testing.T) {
	wantContainers := []string{"mp4", "mkv", "webm", "mov", "avi", "ogg", "ts", "nut"}
	wantVideo := []string{"av1", "hevc", "h264", "vp9", "prores", "ffv1", "theora"}
	wantAudio := []string{"opus", "aac", "mp3", "flac", "ac3"}

	if !reflect.DeepEqual(Containers, wantContainers) {
		t.Fatalf("Containers = %v, want %v", Containers, wantContainers)
	}
	if !reflect.DeepEqual(VideoCodecs, wantVideo) {
		t.Fatalf("VideoCodecs = %v, want %v", VideoCodecs, wantVideo)
	}
	if !reflect.DeepEqual(AudioCodecs, wantAudio) {
		t.Fatalf("AudioCodecs = %v, want %v", AudioCodecs, wantAudio)
	}

	expected := map[string]struct{ video, audio []string }{
		"mp4":  {[]string{"av1", "hevc", "h264", "vp9", "ffv1"}, wantAudio},
		"mkv":  {wantVideo, wantAudio},
		"webm": {[]string{"av1", "vp9"}, []string{"opus"}},
		"mov":  {[]string{"hevc", "h264", "prores", "ffv1", "theora"}, []string{"aac", "mp3", "ac3"}},
		"avi":  {wantVideo, []string{"aac", "mp3", "flac", "ac3"}},
		"ogg":  {[]string{"theora"}, []string{"opus", "flac"}},
		"ts":   {wantVideo, wantAudio},
		"nut":  {wantVideo, wantAudio},
	}

	for _, c := range wantContainers {
		row := expected[c]
		for _, v := range wantVideo {
			wantV := slices.Contains(row.video, v)
			if got := Accepts(c, v, ""); got != wantV {
				t.Errorf("Accepts(%q, %q, \"\") = %v, want %v", c, v, got, wantV)
			}
			for _, a := range wantAudio {
				wantA := slices.Contains(row.audio, a)
				wantBoth := wantV && wantA
				if got := Accepts(c, v, a); got != wantBoth {
					t.Errorf("Accepts(%q, %q, %q) = %v, want %v", c, v, a, got, wantBoth)
				}
			}
		}
		for _, a := range wantAudio {
			wantA := slices.Contains(row.audio, a)
			if got := Accepts(c, "", a); got != wantA {
				t.Errorf("Accepts(%q, \"\", %q) = %v, want %v", c, a, got, wantA)
			}
		}
		if !Accepts(c, "", "") {
			t.Errorf("Accepts(%q, \"\", \"\") = false, want true", c)
		}
	}

	if Accepts("flv", "h264", "aac") {
		t.Error("Accepts accepted unknown container")
	}
	if Accepts("mkv", "mpeg2", "opus") {
		t.Error("Accepts accepted unknown video codec")
	}
	if Accepts("mkv", "av1", "vorbis") {
		t.Error("Accepts accepted unknown audio codec")
	}
}

func TestRecommendedContainers(t *testing.T) {
	want := map[string]string{
		"av1":     "mp4",
		"hevc":    "mp4",
		"h264":    "mp4",
		"vp9":     "webm",
		"prores":  "mov",
		"ffv1":    "mkv",
		"theora":  "ogg",
		"unknown": "",
		"":        "",
	}
	for codec, wantContainer := range want {
		if got := Recommended(codec); got != wantContainer {
			t.Errorf("Recommended(%q) = %q, want %q", codec, got, wantContainer)
		}
	}
}

func TestContainerForExt(t *testing.T) {
	cases := []struct {
		ext, want string
	}{
		{".mp4", "mp4"}, {"mp4", "mp4"}, {".MP4", "mp4"},
		{".mkv", "mkv"}, {"mkv", "mkv"}, {".MKV", "mkv"},
		{".webm", "webm"}, {"webm", "webm"}, {".WEBM", "webm"},
		{".mov", "mov"}, {"mov", "mov"}, {".QT", "mov"}, {"qt", "mov"}, {".qt", "mov"},
		{".avi", "avi"}, {"avi", "avi"}, {".AVI", "avi"},
		{".ogg", "ogg"}, {"ogg", "ogg"}, {".ogv", "ogg"}, {".OGV", "ogg"}, {"ogv", "ogg"},
		{".ts", "ts"}, {"ts", "ts"}, {".TS", "ts"},
		{".nut", "nut"}, {"nut", "nut"}, {".NUT", "nut"},
		{".wmv", ""}, {"wmv", ""}, {".", ""}, {"", ""},
	}
	for _, tc := range cases {
		if got := ContainerForExt(tc.ext); got != tc.want {
			t.Errorf("ContainerForExt(%q) = %q, want %q", tc.ext, got, tc.want)
		}
	}
}

func TestCodecCaps(t *testing.T) {
	cases := map[string]Caps{
		"av1":     {Speed: true, Quality: true, SizeMode: true, CRFLo: 1, CRFHi: 63, HW: []string{"vulkan", "qsv"}},
		"hevc":    {Speed: true, Quality: true, SizeMode: true, CRFLo: 0, CRFHi: 51, HW: []string{"vulkan", "qsv"}},
		"h264":    {Speed: true, Quality: true, SizeMode: true, CRFLo: 0, CRFHi: 51, HW: []string{"vulkan", "qsv"}},
		"vp9":     {Speed: true, Quality: true, SizeMode: true, CRFLo: 0, CRFHi: 63, HW: []string{"qsv"}},
		"prores":  {Speed: false, Quality: true, SizeMode: false, CRFLo: 0, CRFHi: 0, HW: []string{"vulkan"}},
		"ffv1":    {Speed: false, Quality: false, SizeMode: false, CRFLo: 0, CRFHi: 0, HW: []string{"vulkan"}},
		"theora":  {Speed: false, Quality: true, SizeMode: true, CRFLo: 0, CRFHi: 0, HW: nil},
		"unknown": {},
	}
	for codec, want := range cases {
		if got := CodecCaps(codec); !reflect.DeepEqual(got, want) {
			t.Errorf("CodecCaps(%q) = %+v, want %+v", codec, got, want)
		}
	}
}
