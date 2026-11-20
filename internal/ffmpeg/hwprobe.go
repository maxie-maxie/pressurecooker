package ffmpeg

import (
	"context"
	"fmt"
	"strings"

	"github.com/maxie-maxie/pressurecooker/internal/config"
)

// HWAvailability maps a GPU backend to "" when it can encode, else a short reason.
type HWAvailability map[string]string

// ProbeHardware test-encodes each GPU backend the codec supports.
func ProbeHardware(ctx context.Context, r Runner, ffmpegBin, codec string) (HWAvailability, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	codec = strings.ToLower(strings.TrimSpace(codec))
	if _, ok := softwareEncoders[codec]; !ok {
		return nil, fmt.Errorf("unsupported video codec %q", codec)
	}
	backends := config.CodecCaps(codec).HW
	if len(backends) > 0 && r == nil {
		return nil, fmt.Errorf("runner is required for hardware probing")
	}
	availability := make(HWAvailability, len(backends))
	for _, backend := range backends {
		_, err := ChooseEncoder(ctx, r, ffmpegBin, config.Settings{
			Codec: codec, HW: backend, Preset: "fast", Quality: "balanced",
		})
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		availability[backend] = ""
		if err != nil {
			availability[backend] = firstMeaningfulLine(err.Error())
		}
	}
	return availability, nil
}
