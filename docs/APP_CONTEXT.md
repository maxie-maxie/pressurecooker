# Pressurecooker app context

Pressurecooker is a Go command-line tool that compresses and converts video through external FFmpeg. This document maps the codebase: package responsibilities, the flow from arguments to a published file, the on-disk configuration format, and the platform boundaries.

## Repository layout

```text
cmd/pressurecooker     entry point
internal/cli           parsing, resolution, run flow, help text
internal/config        settings, validation, JSON storage, presets
internal/console       terminal output, progress, diagnostics
internal/ffmpeg        probe, encoder selection, command construction, reference extraction
internal/output        destination resolution, temporary files, publishing
internal/target        fit check, Default encode, force-target search
internal/tui           alt-screen Setup and preset recorder
internal/e2e           end-to-end tests
```

The module is `github.com/maxie-maxie/pressurecooker`, built with Go 1.27. The user-visible TUI depends on `charmbracelet/bubbletea`, `charmbracelet/bubbles`, and `charmbracelet/lipgloss`; terminal sizing and input use `golang.org/x/term` and `golang.org/x/sys`. The media pipeline has no codec library dependency; it shells out to `ffmpeg` and `ffprobe`.

## Packages

| Package | Role |
| --- | --- |
| `cmd/pressurecooker` | Sets `version` (default `dev`, overridable with `-ldflags -X main.version=...`) and calls `cli.Main(os.Args[1:], version)`. |
| `internal/cli` | Parses `os.Args` into a `Run`, resolves effective settings, and drives the selected mode. Owns the help and details text and the exit codes. |
| `internal/config` | Defines `Settings`, factory defaults, validation, preset and resolution parsing, and the `Store` that loads and saves `presets.json` and `latest.json`. |
| `internal/console` | Formats stage lines, the result block, and the diagnostic footer for a terminal or a pipe; honors `NO_COLOR`; parses FFmpeg progress. |
| `internal/ffmpeg` | Probes media, selects an encoder and backend, builds and runs FFmpeg commands, resolves tool executables, extracts FFV1 references. |
| `internal/output` | Resolves the destination path and container, creates the temporary `.part` file, and publishes it atomically. |
| `internal/target` | Arithmetic fit check, Default encode with one retry, and the force-target candidate search. |
| `internal/tui` | Runs the alt-screen Setup and preset recorder, with a plain fallback. |
| `internal/e2e` | End-to-end tests. |

## The `cli` package

`internal/cli` is split across five source files.

- `parse.go` defines `Mode` (`default` or `conversion`), the `Run` struct, and `Parse`. `Parse` maps flags to fields through an `aliases` table. `--help`, `--help-details`, and `--version` are detected first and return before any other parsing, so they bypass invalid options and never read configuration or tools. A removed flag produces an error that names its replacement. Remaining tokens split into an option list and up to three positionals: `<source-video>`, `<destination>`, and an optional `[target-size-mb]`.
- `help.go` holds the `Usage`, `Help`, and `Details` strings shown by those flags.
- `resolve.go` defines `Resolve`, which layers factory, selected preset, and `--latest` values in that precedence, records each field's origin, and rejects combinations such as `--crf` with `--quality`, `-ft` without a size, and `--latest` with `-ps`. It also validates source and destination paths.
- `run.go` defines `executeRun` and the prompt helpers (`conversionQuestion`, `forceQuestion`, `sourceQuestion`), the dry-run printout, and the interactive prompts.
- `main.go` defines `Main` and `MainWith`, the injectable `Env` (streams, store, runner, session runner, clock), terminal detection that gates interactive setup, and the exit codes.

`executeRun` resolves tools, probes the source, resolves pending conversion and source-cap prompts (or records them as warnings in `--dry-run`), resolves the destination, and runs the fit check. `--dry-run` prints the plan and returns before encoder selection or writes. Otherwise it confirms force-target when fit fails, chooses the encoder, and dispatches Default or Conversion, saving `latest.json` only after a successful run that did not use force-target.

## Modes

`Mode` is `default` or `conversion`. `executeRun` decides from the presence of a size.

- Default mode: arithmetic fit check, then `target.FitEncode` (two-pass ABR or hardware VBR with 0.96 headroom, one retry on overshoot). If infeasible or still oversized, it asks the force-target question unless `-ft` was given.
- Conversion mode: one encode with the resolved settings, no size logic.
- Setup mode: `--setup` runs the TUI first, then Default or Conversion with the confirmed values; the conversion prompt is suppressed.

## Presets and latest settings (`internal/config`)

`Preset` fields: `id`, `label` (required, max 40 characters), `description` (max 120), `codec`, `container`, `preset` (encoder speed), `quality` or `crf` (never both), `audio_kbps` (null keeps source audio), `fps` (null means source; a number is a cap), `resolution` (empty means source; a value is a cap). Hardware mode is not stored; it stays `auto`.

`FactoryPreset()` returns the factory preset: AV1, MP4, speed `8`, quality `balanced`, 1080p cap, source audio and FPS.

`Store` methods: `LoadPresets`, `SavePreset`, `LoadLatest`, `SaveLatest`. The directory is `%APPDATA%\pressurecooker` or `$XDG_CONFIG_HOME/pressurecooker` (with home-directory fallbacks). `presets.json` and `latest.json` use schema version 3 and strict JSON, saved through a synced temporary file and atomic replacement.

`latest.json` stores encoding fields plus `hw`, without label, description, or size. Version 1 Python and version 2 latest settings migrate in memory; their bytes stay unchanged until an authorized save. The retired `defaults.json` is ignored. Corrupt or unknown-version files produce an error naming the path and are never silently overwritten.

In Go, `Settings` holds `Codec`, `Container`, `Preset`, `Quality`, `CRF` (nullable), `AudioKbps`, `AudioSource` (in-memory flag when keeping source audio), `FPS` (nullable), `Resolution` (empty means source), and `HW`. In `latest.json`, `audio_kbps` is nullable (`null` maps to `AudioSource = true`).

## Fit check and force-target (`internal/target`)

`Fit` holds `Fits`, `VideoKbps`, `AudioKbps`, `AudioCopy`, and a numeric `Reason`. `CheckFit` computes `budget_bits = target_bytes * 8 * 0.97`, subtracts audio bits, and divides by duration. A positive integer video bitrate means the request fits.

`FitEncode` preserves codec, container, speed, resolution, and FPS; uses software two-pass ABR or hardware VBR with 0.96 headroom; checks actual bytes; retries once with `target/actual * 0.98`; removes unsuccessful outputs.

`ForceFast` keeps codec, container, and explicit fields fixed. Adjustable inherited fields are bitrate, smaller resolution and FPS, audio bitrate, and the backend under `--hw auto`. It extracts one FFV1 reference (2-second windows, three for sources over three minutes, whole source for clips of 12 seconds or less), tests up to three resolution/FPS candidates with one-pass ABR, scores them at source display size with XPSNR (fallback SSIM) or VMAF, then encodes the original source with two-pass ABR and at most two scaled retries. Limits: 3 sample encodes and 3 full encodes. Progress shows `trial n/N`.

`score.go` detects XPSNR and falls back to SSIM; summarizes per-frame scores as a mean with a fifth-percentile guard. `calibrate.go` sweeps quality levels for the hardware quantizer table and is not on the normal CLI path.

## Media pipeline (`internal/ffmpeg`)

- `runner.go`: `Runner` interface and `ExecRunner`; `ResolveTool` finds `ffmpeg` and `ffprobe` on `PATH` or validates an explicit path; `.bat` and `.cmd` wrappers are rejected on Windows.
- `probe.go`: runs `ffprobe` for format, video, and audio facts; returns `Media`.
- `encoder.go`: `ChooseEncoder` maps `auto`, `vulkan`, `qsv`, `off` to backends; for `auto` and `vulkan` it enumerates Vulkan devices, probes encode support within a timeout, falls back to software, and reports the reason; `ProbeVulkanDecode` checks source decode on the GPU.
- `types.go`: `Encoder`, `Request`, `Media`, `Result`, encoder and muxer name tables, `FormatCommand`.
- `args.go`: `BuildArgs` assembles inputs, stream maps, encoder, preset, rate control, pixel format, filters, and muxer; MP4 output adds `+faststart`. `audioArgs` copies compatible source audio or re-encodes. `Encode` runs one- or two-pass encodes.
- `reference.go`: `ExtractReference` cuts the FFV1 reference clip.

## Terminal output (`internal/console`)

`console.go` defines `Writer`, `Event`, `Diagnostic`, and `Result`, stage labels, and the colored footer (warnings in yellow, errors in red), gated on a terminal and `NO_COLOR`. `vt_windows.go` and `vt_other.go` enable ANSI handling where needed. `progress.go` parses FFmpeg progress lines into clock and percentage values. `Writer.Prompt` writes a single yellow prompt line with no trailing newline. `Footer` prints each diagnostic as exactly one line.

## Interactive sessions (`internal/tui`)

`RunSession` runs a Bubble Tea program in the alternate screen (`tea.WithAltScreen()`) for color terminals, with a plain fallback (`tea.WithoutRenderer()`) when `NO_COLOR` is set, `TERM=dumb`, or the terminal lacks VT support. Layout: a header line, the current step, and a context bar pinned to the bottom row with keys, step count, and active preset label. Redraws on `tea.WindowSizeMsg`.

`Mode` is `ModeSetup` or `ModePreset`. `Session` holds `Settings`, `Preset`, `TargetBytes` (nil means Conversion), and `Edited`. `SessionOptions` holds presets, input, output, and terminal flag. Esc cancels (returns nil session); Ctrl+C and context cancellation return `ErrInterrupted`.

Setup steps: start-from preset, codec, container, encoder speed, quality or CRF, audio, FPS, resolution, hardware, size, review. Preset recorder steps: label, description, then the seven fields, review.

Golden frames for color and plain modes are compared in tests with `teatest`.

## Platform boundaries

Platform split files use `//go:build windows` and `//go:build !windows`:

- `replace_windows.go` / `replace_other.go`: atomic file replacement.
- `publish_windows.go` / `publish_other.go`: publishing the `.part` file.
- `vt_windows.go` / `vt_other.go`: terminal handling.
- `session_vt_windows.go` / `session_vt_other.go`: VT detection for the TUI.

## Exit codes

| Code | Meaning |
| --- | --- |
| `0` | Success |
| `1` | Cancelled by the user |
| `2` | Invalid input, usage, or configuration |
| `3` | Required FFmpeg or ffprobe tool missing |
| `4` | Could not fit; nothing published |
| `5` | Encode failure |
| `130` | Interrupted (`Ctrl-C`) |

## Testing

```text
go test ./...
go vet ./...
go build ./...
CGO_ENABLED=0 GOOS=windows GOARCH=386 go build ./...
CGO_ENABLED=0 GOOS=linux GOARCH=386 go build ./...
go test -tags integration ./...
```

Unit tests cover parsing, resolution, settings and storage, argument building, fit checking and force-target fitting, output handling, the session model, and end-to-end flows. Real-FFmpeg tests are behind the `integration` build tag. Format with `gofmt` before committing.