package ffmpeg

import (
	"context"
	"errors"
)

// HWAvailability maps a GPU backend to "" when it can encode, else a short reason.
type HWAvailability map[string]string

var errNotImplemented = errors.New("not implemented")

// ProbeHardware test-encodes each GPU backend the codec supports.
func ProbeHardware(ctx context.Context, r Runner, ffmpegBin, codec string) (HWAvailability, error) {
	return nil, errNotImplemented
}
