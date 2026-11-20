package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/maxie-maxie/pressurecooker/internal/config"
	"github.com/maxie-maxie/pressurecooker/internal/console"
	"github.com/maxie-maxie/pressurecooker/internal/ffmpeg"
	"github.com/maxie-maxie/pressurecooker/internal/output"
	"github.com/maxie-maxie/pressurecooker/internal/target"
)

const conversionQuestion = "No size given: convert without a size limit? [y/N]"

func forceQuestion(reason string, dirDest bool) string {
	fields := "codec, encoder, bitrate, resolution, FPS and audio"
	if dirDest {
		fields = "codec, encoder, container, bitrate, resolution, FPS and audio"
	}
	return strings.TrimSuffix(reason, ".") + ". Force-target may change " + fields + " to fit. Use it? [y/N]"
}

func sourceQuestion(m ffmpeg.Media, s config.Settings) (string, bool, bool) {
	w, h, _ := config.ParseResolution(s.Resolution)
	resAbove := w > m.Width || h > m.Height
	fpsAbove := s.FPS != nil && *s.FPS > m.FPS
	var caps []string
	if resAbove {
		caps = append(caps, fmt.Sprintf("%dx%d", w, h))
	}
	if fpsAbove {
		caps = append(caps, trimFloat(*s.FPS)+" fps")
	}
	if len(caps) == 0 {
		return "", false, false
	}
	return fmt.Sprintf("Source is %dx%d at %s fps, below the preset's %s. Keep the source size? [y/N]", m.Width, m.Height, trimFloat(m.FPS), strings.Join(caps, " and ")), resAbove, fpsAbove
}

func executeRun(ctx context.Context, env Env, w *console.Writer, storage config.Store, run Run, interactive bool) int {
	var diags []console.Diagnostic
	for _, msg := range run.Warnings {
		diags = append(diags, console.Diagnostic{Severity: "warning", Message: msg})
	}
	dirDest := destinationContainer(run.Destination) == ""
	ffmpegBin, err := env.ResolveTool(run.FFmpeg, "ffmpeg")
	if err != nil {
		return failFooter(w, diags, err.Error(), exitMissingTool)
	}
	ffprobeBin, err := env.ResolveTool(run.FFprobe, "ffprobe")
	if err != nil {
		return failFooter(w, diags, err.Error(), exitMissingTool)
	}
	media, err := ffmpeg.Probe(ctx, env.Runner, ffprobeBin, run.Source)
	if err != nil {
		return runError(ctx, w, diags, err)
	}
	if run.Mode == ModeConversion && (!run.ManualSetup || run.DryRun) {
		if run.DryRun {
			diags = append(diags, console.Diagnostic{Severity: "warning", Message: conversionQuestion})
		} else {
			if code := ask(ctx, env.In, w, conversionQuestion, interactive, exitCancelled, diags); code != exitOK {
				return code
			}
		}
	}
	question, resAbove, fpsAbove := sourceQuestion(media, run.Settings)
	if question != "" {
		if run.DryRun {
			diags = append(diags, console.Diagnostic{Severity: "warning", Message: question})
		} else {
			if code := ask(ctx, env.In, w, question, interactive, exitCancelled, diags); code != exitOK {
				return code
			}
		}
		if resAbove {
			run.Settings.Resolution = ""
		}
		if fpsAbove {
			run.Settings.FPS = nil
		}
	}
	dest, err := resolveDestination(run)
	if err != nil {
		return failFooter(w, diags, err.Error(), exitUsage)
	}
	var fit target.Fit
	if run.TargetBytes != nil {
		fit = target.CheckFit(media, run.Settings, *run.TargetBytes)
	}
	if run.DryRun {
		w.Line("Dry run (no files or configuration written)")
		w.Line("Mode:         " + string(run.Mode))
		w.Line("Output:       " + dest.Path)
		w.Line(fmt.Sprintf("Format:       %s / %s / %s", run.Settings.Container, run.Settings.Codec, run.Settings.AudioCodec))
		speed := run.Settings.Preset
		if !config.CodecCaps(run.Settings.Codec).Speed {
			speed = "n/a"
		}
		w.Line("Speed:        " + speed)
		quality := run.Settings.Quality
		if !config.CodecCaps(run.Settings.Codec).Quality {
			quality = "lossless"
		}
		if run.Settings.CRF != nil {
			quality = "crf " + trimFloat(*run.Settings.CRF)
		}
		w.Line("Quality:      " + quality)
		audio, fps, res := "source", "source", "source"
		if !run.Settings.AudioSource {
			audio = fmt.Sprintf("%dk", run.Settings.AudioKbps)
			if run.Settings.AudioCodec == "flac" {
				audio = "flac lossless"
			}
		}
		if run.Settings.FPS != nil {
			fps = trimFloat(*run.Settings.FPS)
		}
		if run.Settings.Resolution != "" {
			res = run.Settings.Resolution
		}
		w.Line(fmt.Sprintf("Audio:        %s\nFPS:          %s\nResolution:   %s\nHardware:     %s", audio, fps, res, run.Settings.HW))
		if run.TargetBytes != nil {
			w.Line(fmt.Sprintf("Target:       %g MB (%d bytes)", float64(*run.TargetBytes)/1e6, *run.TargetBytes))
			w.Line(fmt.Sprintf("Video:        %d kbps\nAudio budget: %d kbps (copy: %t)", fit.VideoKbps, fit.AudioKbps, fit.AudioCopy))
			if !fit.Fits && !run.ForceTarget {
				diags = append(diags, console.Diagnostic{Severity: "warning", Message: forceQuestion(fit.Reason, dirDest)})
			}
		}
		w.Footer(diags)
		return exitOK
	}
	forced := false
	if run.TargetBytes != nil && !fit.Fits {
		if !run.ForceTarget {
			if code := ask(ctx, env.In, w, forceQuestion(fit.Reason, dirDest), interactive, exitTargetSmall, diags); code != exitOK {
				return code
			}
		}
		forced = true
	}
	selection := config.Clone(run.Settings)
	if run.TargetBytes != nil {
		selection.CRF, selection.Quality = nil, "balanced"
	}
	enc, err := ffmpeg.ChooseEncoder(ctx, env.Runner, ffmpegBin, selection)
	if err != nil {
		return runError(ctx, w, diags, err)
	}
	for _, msg := range enc.Warnings {
		diags = append(diags, console.Diagnostic{Severity: "warning", Message: msg})
	}
	if strings.EqualFold(enc.Backend, "vulkan") {
		decodeOK, reason := ffmpeg.ProbeVulkanDecode(ctx, env.Runner, ffmpegBin, run.Source, enc, run.Settings, media)
		enc.Decode = decodeOK
		if !decodeOK && reason != "" {
			diags = append(diags, console.Diagnostic{Severity: "warning", Message: fmt.Sprintf("Vulkan decode unavailable (%s); using software decode", reason)})
		}
	}
	if ctx.Err() != nil {
		return runError(ctx, w, diags, ctx.Err())
	}
	part, err := output.Temp(dest)
	if err != nil {
		return runError(ctx, w, diags, err)
	}
	defer os.Remove(part)
	req := ffmpeg.Request{Source: run.Source, Output: part, FFmpeg: ffmpegBin, FFprobe: ffprobeBin, Settings: config.Clone(run.Settings), Origins: cloneOrigins(run.Origins), Encoder: enc, Media: media, RateControl: "quality"}
	started := env.Now()
	outcome := target.Outcome{Settings: config.Clone(run.Settings)}
	if run.Mode == ModeConversion {
		outcome.Media, err = ffmpeg.Encode(ctx, env.Runner, req, w.Event)
		if err != nil {
			return runError(ctx, w, diags, err)
		}
	} else if !forced {
		outcome, err = target.FitEncode(ctx, env.Runner, req, fit, *run.TargetBytes, w.Event)
		if err != nil {
			return runError(ctx, w, diags, err)
		}
		if outcome.Oversize || outcome.Infeasible != "" {
			if !run.ForceTarget {
				if code := ask(ctx, env.In, w, forceQuestion(outcome.Infeasible, dirDest), interactive, exitTargetSmall, diags); code != exitOK {
					return code
				}
			}
			forced = true
		}
	}
	if forced {
		if req.Settings.Quality == "" {
			req.Settings.Quality = "balanced"
		}
		metric := "xpsnr"
		if run.VMAF {
			metric = "vmaf"
		}
		outcome, err = target.ForceFast(ctx, env.Runner, req, *run.TargetBytes, metric, w.Event)
		if err != nil {
			if strings.Contains(err.Error(), "--vmaf needs FFmpeg built with libvmaf") {
				return failFooter(w, diags, err.Error(), exitUsage)
			}
			return runError(ctx, w, diags, err)
		}
		if outcome.Oversize || outcome.Infeasible != "" {
			return failFooter(w, diags, outcome.Infeasible, exitTargetSmall)
		}
		if outcome.Settings.Container != dest.Container {
			dest, err = output.Resolve(run.Source, run.Destination, outcome.Settings, string(run.Mode), *run.TargetBytes, run.Overwrite)
			if err != nil {
				return failFooter(w, diags, err.Error(), exitUsage)
			}
		}
		selection = config.Clone(outcome.Settings)
		selection.CRF, selection.Quality = nil, "balanced"
		if selection.HW != "vulkan" && selection.HW != "qsv" {
			selection.HW = "off"
		}
		enc, err = ffmpeg.ChooseEncoder(ctx, env.Runner, ffmpegBin, selection)
		if err != nil {
			return runError(ctx, w, diags, err)
		}
	}
	info, err := os.Stat(part)
	if err != nil || info.Size() == 0 {
		if err == nil {
			err = fmt.Errorf("encoded output is empty")
		}
		return runError(ctx, w, diags, err)
	}
	if run.TargetBytes != nil && info.Size() > *run.TargetBytes {
		return failFooter(w, diags, fmt.Sprintf("Output is %g MB; target is %g MB", float64(info.Size())/1e6, float64(*run.TargetBytes)/1e6), exitTargetSmall)
	}
	if ctx.Err() != nil {
		return runError(ctx, w, diags, ctx.Err())
	}
	if err := output.Publish(part, dest, run.Source, run.Overwrite); err != nil {
		return failFooter(w, diags, err.Error(), publishExitCode(err))
	}
	published, err := ffmpeg.Probe(ctx, env.Runner, ffprobeBin, dest.Path)
	if err != nil {
		return runError(ctx, w, diags, err)
	}
	if ctx.Err() != nil {
		return runError(ctx, w, diags, ctx.Err())
	}
	if !media.HasAudio {
		outcome.Settings.AudioKbps, outcome.Settings.AudioSource = run.Settings.AudioKbps, run.Settings.AudioSource
	}
	if err := env.SaveLatest(storage, outcome.Settings); err != nil {
		diags = append(diags, console.Diagnostic{Severity: "warning", Message: fmt.Sprintf("could not save latest settings: %v", err)})
	}
	rateMode, targetFit := "quality", "none"
	if run.TargetBytes != nil {
		rateMode, targetFit = "abr", "yes"
	}
	w.Result(buildResult(dest, published, enc, outcome.Settings, rateMode, targetFit, env.Now().Sub(started), outcome.Metric, outcome.MetricVersion))
	w.Footer(diags)
	return exitOK
}

func ask(ctx context.Context, in io.Reader, w *console.Writer, question string, interactive bool, declined int, diags []console.Diagnostic) int {
	if !interactive {
		return failFooter(w, diags, question+" needs a terminal to answer", exitUsage)
	}
	for {
		w.Prompt(question + " ")
		line, eof, err := readLine(ctx, in)
		w.Line("")
		if err != nil {
			return runError(ctx, w, diags, err)
		}
		if strings.ContainsRune(line, '\x03') {
			return failFooter(w, diags, "interrupted", exitInterrupted)
		}
		if eof {
			w.Footer(diags)
			return declined
		}
		switch strings.ToLower(strings.TrimSpace(line)) {
		case "", "n", "no":
			w.Footer(diags)
			return declined
		case "y", "yes":
			return exitOK
		}
	}
}

func runError(ctx context.Context, w *console.Writer, diags []console.Diagnostic, err error) int {
	if ctx.Err() != nil || errors.Is(err, context.Canceled) {
		return failFooter(w, diags, "interrupted", exitInterrupted)
	}
	return failFooter(w, diags, err.Error(), exitEncode)
}

func readLine(ctx context.Context, r io.Reader) (string, bool, error) {
	if err := ctx.Err(); err != nil {
		return "", false, err
	}
	type readResult struct {
		line string
		eof  bool
		err  error
	}
	ch := make(chan readResult, 1)
	go func() {
		var buf []byte
		var b [1]byte
		for {
			n, err := r.Read(b[:])
			if n > 0 {
				if b[0] == '\n' {
					ch <- readResult{line: strings.TrimSuffix(string(buf), "\r")}
					return
				}
				buf = append(buf, b[0])
			}
			if err != nil {
				if errors.Is(err, io.EOF) {
					if len(buf) > 0 {
						ch <- readResult{line: strings.TrimSuffix(string(buf), "\r")}
					} else {
						ch <- readResult{eof: true}
					}
					return
				}
				ch <- readResult{err: err}
				return
			}
		}
	}()
	select {
	case <-ctx.Done():
		return "", false, ctx.Err()
	case res := <-ch:
		return res.line, res.eof, res.err
	}
}

func buildResult(
	dest output.Destination,
	media ffmpeg.Media,
	enc ffmpeg.Encoder,
	settings config.Settings,
	rateMode string,
	targetFit string,
	elapsed time.Duration,
	metric string,
	metricVersion string,
) console.Result {
	container := media.Container
	if container == "" {
		container = dest.Container
	}
	codec := media.VideoCodec
	if codec == "" {
		codec = settings.Codec
	}
	dims := "unknown"
	if media.Width > 0 && media.Height > 0 {
		dims = fmt.Sprintf("%dx%d", media.Width, media.Height)
	}
	audio := "none"
	if media.HasAudio {
		switch {
		case media.AudioCodec != "" && media.AudioKbps > 0:
			audio = fmt.Sprintf("%s %dk", media.AudioCodec, int(math.Round(media.AudioKbps)))
		case media.AudioCodec != "":
			audio = media.AudioCodec
		default:
			audio = fmt.Sprintf("%dk", settings.AudioKbps)
		}
	}
	return console.Result{
		Path:          dest.Path,
		Container:     container,
		Codec:         codec,
		Encoder:       enc.Name,
		RateControl:   qualityLabel(settings, enc, rateMode),
		Dimensions:    dims,
		Audio:         audio,
		TargetFit:     targetFit,
		Bytes:         media.Bytes,
		FPS:           media.FPS,
		Elapsed:       elapsed,
		Metric:        metric,
		MetricVersion: metricVersion,
	}
}

func qualityLabel(settings config.Settings, enc ffmpeg.Encoder, rateMode string) string {
	if !config.CodecCaps(settings.Codec).Quality {
		return "lossless"
	}
	if rateMode == "abr" {
		if enc.Hardware {
			return "hardware abr"
		}
		return "2-pass abr"
	}
	if settings.CRF != nil {
		return "crf " + trimFloat(*settings.CRF)
	}
	lvl := settings.Quality
	if lvl == "" {
		lvl = "balanced"
	}
	if settings.Codec == "prores" {
		profile := map[string]string{"high": "hq", "balanced": "standard", "small": "lt"}[lvl]
		return fmt.Sprintf("%s (%s)", lvl, profile)
	}
	q := enc.Quantizer
	if q == 0 {
		if mapped, err := ffmpeg.QualityQuantizer(lvl, enc.Name); err == nil {
			q = mapped
		}
	}
	if settings.Codec == "theora" {
		return fmt.Sprintf("%s (q:v %s)", lvl, trimFloat(q))
	}
	switch strings.ToLower(enc.Backend) {
	case "vulkan":
		return fmt.Sprintf("%s (cqp %s)", lvl, trimFloat(q))
	case "qsv":
		return fmt.Sprintf("%s (icq %s)", lvl, trimFloat(q))
	default:
		return fmt.Sprintf("%s (crf %s)", lvl, trimFloat(q))
	}
}

func softwareEncoderName(codec string) string {
	switch strings.ToLower(strings.TrimSpace(codec)) {
	case "h264":
		return "libx264"
	case "av1":
		return "libsvtav1"
	case "hevc":
		return "libx265"
	case "vp9":
		return "libvpx-vp9"
	case "prores":
		return "prores_ks"
	case "ffv1":
		return "ffv1"
	case "theora":
		return "libtheora"
	}
	return ""
}

func publishExitCode(err error) int {
	msg := err.Error()
	if strings.Contains(msg, "destination appeared during encoding") ||
		strings.Contains(msg, "output path must not be the same file") ||
		strings.Contains(msg, "link or alias") {
		return exitUsage
	}
	return exitEncode
}

func failFooter(w *console.Writer, diags []console.Diagnostic, msg string, code int) int {
	diags = append(diags, console.Diagnostic{Severity: "error", Message: msg})
	w.Footer(diags)
	return code
}

func trimFloat(v float64) string {
	return strconv.FormatFloat(v, 'f', -1, 64)
}
