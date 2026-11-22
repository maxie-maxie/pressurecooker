package ffmpeg

import (
	"context"
	"fmt"
	"math"
	"os"
	"strings"

	"github.com/maxie-maxie/pressurecooker/internal/output"
)

type Window struct {
	Start, Length float64
}

func ExtractReference(ctx context.Context, r Runner, ffmpegBin, src string, w []Window, out string) error {
	if r == nil || strings.TrimSpace(src) == "" || strings.TrimSpace(out) == "" {
		return fmt.Errorf("runner, source and reference output are required")
	}
	if err := output.RejectAlias(src, out); err != nil {
		return err
	}
	for _, win := range w {
		if win.Start < 0 || win.Length <= 0 || math.IsNaN(win.Start) || math.IsNaN(win.Length) || math.IsInf(win.Start, 0) || math.IsInf(win.Length, 0) {
			return fmt.Errorf("reference windows need finite non-negative starts and positive lengths")
		}
	}
	if strings.TrimSpace(ffmpegBin) == "" {
		ffmpegBin = "ffmpeg"
	}
	args := []string{ffmpegBin, "-nostdin", "-y", "-hide_banner"}
	if len(w) == 0 {
		args = append(args, "-i", src, "-map", "0:v:0")
	} else {
		var graph, inputs strings.Builder
		for i, win := range w {
			args = append(args, "-ss", formatFloat(win.Start), "-t", formatFloat(win.Length), "-i", src)
			fmt.Fprintf(&graph, "[%d:v:0]setpts=PTS-STARTPTS[v%d];", i, i)
			fmt.Fprintf(&inputs, "[v%d]", i)
		}
		if len(w) == 1 {
			args = append(args, "-map", "0:v:0")
		} else {
			fmt.Fprintf(&graph, "%sconcat=n=%d:v=1:a=0[v]", inputs.String(), len(w))
			args = append(args, "-filter_complex", graph.String(), "-map", "[v]")
		}
	}
	args = append(args, "-c:v", "ffv1", "-an", "-f", "matroska", out)
	res, err := r.Run(ctx, args, nil)
	if err != nil || res.ExitCode != 0 {
		_ = os.Remove(out)
		return formatEncodeError("extract reference", ctx, res, err)
	}
	info, err := os.Stat(out)
	if err != nil || info.IsDir() || info.Size() == 0 {
		_ = os.Remove(out)
		return fmt.Errorf("reference extraction produced no output")
	}
	return nil
}
