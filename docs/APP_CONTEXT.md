# Pressurecooker app context

Pressurecooker is a Go command-line tool that compresses and converts video through external FFmpeg. This document maps the codebase: package responsibilities, the flow from arguments to a published file, the on-disk configuration format, and the platform boundaries.

## Repository layout

```text
cmd/pressurecooker     entry point
internal/cli           parsing, resolution, run flow, help text
internal/config        settings, compatibility table, validation, JSON storage, presets
internal/console       terminal output, progress, diagnostics
internal/ffmpeg        probe, encoder selection, hardware probe, command construction, reference extraction
internal/output        destination resolution, temporary files, publishing
internal/target        fit check, Default encode, force-target search
internal/tui           alt-screen Setup and preset editor
internal/e2e           end-to-end tests
```

The module is `github.com/maxie-maxie/pressurecooker`, built with Go 1.27. The user-visible TUI depends on `charmbracelet/bubbletea`, `charmbracelet/bubbles`, and `charmbracelet/lipgloss`; terminal sizing and input use `golang.org/x/term` and `golang.org/x/sys`. The media pipeline has no codec library dependency; it shells out to `ffmpeg` and `ffprobe`.

## Packages

| Package | Role |
| --- | --- |
| `cmd/pressurecooker` | Sets `version` (default `dev`, overridable with `-ldflags -X main.version=...`) and calls `cli.Main(os.Args[1:], version)`. |
| `internal/cli` | Parses `os.Args` into a `Run`, resolves effective settings, and drives the selected mode. Owns the help and details text and the exit codes. |
| `internal/config` | Holds the compatibility table (video, audio, container), `Settings`, factory defaults, validation, preset and resolution parsing, and the `Store` that loads and saves `presets.json` and `latest.json`. |
| `internal/console` | Formats stage lines, the result block, and the diagnostic footer for a terminal or a pipe; honors `NO_COLOR`; parses FFmpeg progress. |
| `internal/ffmpeg` | Probes media, selects an encoder and backend, probes GPU availability, builds and runs FFmpeg commands, resolves tool executables, extracts FFV1 references. |
| `internal/output` | Resolves the destination path and container, creates the temporary `.part` file, and publishes it atomically. |
| `internal/target` | Arithmetic fit check, Default encode with one retry, and the force-target candidate search. |
| `internal/tui` | Runs the alt-screen Setup and preset editor, with a plain fallback. |
| `internal/e2e` | End-to-end tests. |

## The `cli` package

`internal/cli` is split across five source files.

- `parse.go` defines `Mode` (`default` or `conversion`), the `Run` struct, and `Parse`. `Parse` maps flags to fields through an `aliases` table. `--help`, `--help-details`, and `--version` are detected first and return before any other parsing, so they bypass invalid options and never read configuration or tools. The flags are `-c` (7 codecs), `-f` (8 containers, with `quicktime` and `mpegts` aliases), `-ac` (audio codec), `-e`/`--enc-speed` (encoder speed), and `-p`/`--preset [N]`. `-p` takes the next token as `N` only when it is a bare integer of 1 or more; with paths `N` is required, and `-p fast` fails with a message pointing to `-e`. Removed flags (`-ps`/`--use-preset`, `--new-preset`, `--defaults`, `-v`, `-co`, `--force`) produce an error that names the replacement. Remaining tokens split into an option list and up to three positionals: `<source-video>`, `<destination>`, and an optional `[target-size-mb]`. `Run.ManualSetup` records whether `--setup` ended in `Setup manually`.
- `help.go` holds the `Usage`, `Help`, and `Details` strings shown by those flags. `Details` carries the compatibility table, per-codec controls, and the quality mapping.
- `resolve.go` defines `Resolve`, which layers factory, selected preset, and `--latest` values in that precedence and records each field's origin. It rejects combinations such as `--crf` with `--quality`, `-ft` without a size, `--latest` with `-p N`, and `-p N` with `--setup`. It enforces the compatibility table through `config`:
  - The container comes from the destination extension, else `-f`, else the preset. An extension that conflicts with an explicit `-f` is an error (the extension lock).
  - A video codec the container rejects is an error with the accepted-codec hint, whatever its origin (flag, preset or `--latest`).
  - An inherited audio codec the container rejects falls back to the first accepted of `opus`, `aac`, `mp3`, `ac3` and adds a yellow warning. An explicit `-ac` that conflicts is an error. `flac` with an explicit `-a` is an error.
  - A codec change adapts inherited values: a numeric speed becomes a name when the new codec needs one, and an inherited CRF is cleared for codecs without CRF. Explicit `-e`, `-l` and `--crf` values are validated and never discarded.
  - It also validates source and destination paths.
- `run.go` defines `executeRun`, the prompt helpers (`conversionQuestion`, `forceQuestion`, `sourceQuestion`), the dry-run printout, and the interactive prompts.
- `main.go` defines `Main` and `MainWith`, the injectable `Env` (streams, store, runner, session runner, clock, `SaveLatest`), terminal detection that gates interactive setup, and the exit codes. It also builds the `HWProbe` callback that the TUI receives.

`executeRun` resolves tools, probes the source, resolves pending conversion and source-cap prompts (or records them as warnings in `--dry-run`), resolves the destination, and runs the fit check. `--dry-run` prints the plan and returns before encoder selection or writes. Otherwise it confirms force-target when fit fails, chooses the encoder, and dispatches Default or Conversion. When force-target changes the container (a directory destination with no `-f`), it re-resolves the destination from the chosen settings. `latest.json` is saved with `SaveLatest` only after the published file passes the post-publish probe, and it stores the settings that produced the file, including after a successful `-ft` run. It is never saved on error, cancel, dry run or overshoot.

## Modes

`Mode` is `default` or `conversion`. `executeRun` decides from the presence of a size.

- Default mode: arithmetic fit check, then `target.FitEncode` (two-pass ABR or hardware VBR with 0.96 headroom, one retry on overshoot). If infeasible or still oversized, it asks the force-target question unless `-ft` was given.
- Conversion mode: one encode with the resolved settings, no size logic. `prores` and `ffv1` run only here.
- Setup mode: `--setup` runs the TUI first, then Default or Conversion with the confirmed values. A one-click preset still gets the conversion prompt when no size was given; `Setup manually` replaces that prompt with its review screen.

## Compatibility table and presets (`internal/config`)

`compat.go` is the single source of truth for what can be combined. Other packages read it and keep no tables of their own.

- `VideoCodecs`: `av1`, `hevc`, `h264`, `vp9`, `prores`, `ffv1`, `theora`. `AudioCodecs`: `opus`, `aac`, `mp3`, `flac`, `ac3`. `Containers`: `mp4`, `mkv`, `webm`, `mov`, `avi`, `ogg`, `ts`, `nut`. Each is an ordered list.
- `Accepts(container, videoCodec, audioCodec)` reports whether a container can hold the pair; an empty codec is not checked. The mux rules were measured against FFmpeg `N-124716`.
- `Recommended(videoCodec)` returns `mp4` for `av1`, `hevc` and `h264`, `webm` for `vp9`, `mov` for `prores`, `mkv` for `ffv1`, and `ogg` for `theora`.
- `ContainerForExt` maps an extension, with or without the dot, to a container: `.mov` and `.qt` map to `mov`, `.ogg` and `.ogv` to `ogg`, and the others map to themselves.
- `CodecCaps(codec)` returns `Caps{Speed, Quality, SizeMode, CRFLo, CRFHi, HW}`. `CRFHi == 0` means no CRF. `av1` has CRF 1-63 and `vulkan`, `qsv`; `hevc` and `h264` have 0-51 and `vulkan`, `qsv`; `vp9` has 0-63 and `qsv`; `prores` has quality only and `vulkan`; `ffv1` has `vulkan` only; `theora` has quality and size mode but no GPU backend.
- Validation errors for a mismatch use the form `mov does not accept av1; use -c hevc, h264, prores, ffv1 or theora`, or the `-ac` equivalent for audio.

`Preset` fields: `id`, `label` (required, max 40 characters), `description` (max 120), `codec`, `container`, `preset` (encoder speed), `quality` or `crf` (never both), `audio_codec`, `audio_kbps` (null keeps source audio), `fps` (null means source; a number is a cap), `resolution` (empty means source; a value is a cap). Hardware mode is not stored; it stays `auto`.

`FactoryPreset()` returns the factory preset: AV1, MP4, speed `8`, quality `balanced`, 1080p cap, source audio and FPS, audio codec `opus`.

`Store` methods: `LoadPresets`, `SavePreset`, `LoadLatest`, `SaveLatest`. The directory is `%APPDATA%\pressurecooker` or `$XDG_CONFIG_HOME/pressurecooker` (with home-directory fallbacks). `presets.json` and `latest.json` use schema version 4 (`audio_codec` was added) and strict JSON, saved through a synced temporary file and atomic replacement. Runs never change `presets.json`.

`latest.json` stores encoding fields plus `hw`, without label, description, or size. Files from versions 1 to 3 migrate in memory with `audio_codec: opus`, and their bytes stay unchanged until an authorized save. The retired `defaults.json` is ignored. Corrupt or unknown-version files produce an error naming the path and are never silently overwritten.

In Go, `Settings` holds `Codec`, `Container`, `AudioCodec`, `Preset`, `Quality`, `CRF` (nullable), `AudioKbps`, `AudioSource` (in-memory flag when keeping source audio), `FPS` (nullable), `Resolution` (empty means source), and `HW`. In `latest.json`, `audio_kbps` is nullable (`null` maps to `AudioSource = true`). With `flac`, `AudioKbps` is not used.

## Fit check and force-target (`internal/target`)

`Fit` holds `Fits`, `VideoKbps`, `AudioKbps`, `AudioCopy`, and a numeric `Reason`. `CheckFit` computes `budget_bits = target_bytes * 8 * 0.97`, subtracts audio bits, and divides by duration. A positive integer video bitrate means the request fits. For `flac`, the audio kbps is unknown in advance, so `fit.go` encodes the source audio to a temporary FLAC file, measures its exact byte count, and subtracts those bytes from the budget. If the FLAC audio alone exceeds the target, the reason reports its size in MB. `prores` and `ffv1` fail the size-mode guard.

`FitEncode` preserves codec, container, speed, resolution, and FPS; uses software two-pass ABR or hardware VBR with 0.96 headroom; checks actual bytes; retries once with `target/actual * 0.98`; removes unsuccessful outputs. `vp9` and `theora` also use two-pass ABR.

`ForceFast` keeps every explicit field fixed and adjusts the inherited ones. `selectForceFormats` chooses the format fields:

- Container: locked by the extension or `-f`. For a directory destination without `-f`, it uses `config.Recommended` of the chosen codec.
- Video codec: lossy codecs that the container accepts and the FFmpeg build can encode, in the order `av1`, `hevc`, `vp9`, `h264`, `theora`. `theora` is chosen only for `ogg`; `prores` and `ffv1` are never chosen.
- Audio codec: the first accepted of `opus`, `aac`, `mp3`, `ac3`; `flac` only when explicit.
- Encoder: an explicit `--hw` is kept. Under `auto`, one of the three trial candidates uses a GPU encoder that passes the probe. It wins only within 0.5 dB XPSNR or 1 VMAF point of the best software candidate.

The search then extracts one FFV1 reference (2-second windows, three for sources over three minutes, whole source for clips of 12 seconds or less), tests up to three resolution/FPS candidates with one-pass ABR, scores them at source display size with XPSNR (fallback SSIM) or VMAF, and encodes the original source with two-pass ABR and at most two scaled retries. Limits: 3 sample encodes and 3 full encodes. Progress shows `trial n/N`.

`score.go` detects XPSNR and falls back to SSIM; summarizes per-frame scores as a mean with a fifth-percentile guard. `calibrate.go` sweeps quality levels for the hardware quantizer table and is not on the normal CLI path.

## Media pipeline (`internal/ffmpeg`)

- `runner.go`: `Runner` interface and `ExecRunner`; `ResolveTool` finds `ffmpeg` and `ffprobe` on `PATH` or validates an explicit path; `.bat` and `.cmd` wrappers are rejected on Windows.
- `probe.go`: runs `ffprobe` for format, video, and audio facts; returns `Media`.
- `encoder.go`: `ChooseEncoder` maps `auto`, `vulkan`, `qsv`, `off` to backends for the 7 video codecs (`libsvtav1`, `libx265`, `libx264`, `libvpx-vp9`, `prores_ks`, `ffv1`, `libtheora` in software). For `auto` and `vulkan` it enumerates Vulkan devices, probes encode support within a timeout, falls back to software, and reports the reason. The GPU set covers Vulkan for AV1, HEVC, H.264, ProRes (`prores_ks_vulkan`) and FFV1 (`ffv1_vulkan`), and QSV for AV1, HEVC, H.264 and VP9. `ProbeVulkanDecode` checks source decode on the GPU.
- `hwprobe.go`: `ProbeHardware(ctx, runner, ffmpegBin, codec)` returns `HWAvailability`, a map from each backend in `CodecCaps(codec).HW` to `""` when it can encode, or a short reason. It test-encodes through `ChooseEncoder`, so the TUI and the real run agree.
- `types.go`: `Encoder`, `Request`, `Media`, `Result`, encoder and muxer name tables, `FormatCommand`.
- `args.go`: `BuildArgs` assembles inputs, stream maps, encoder, preset, rate control, pixel format, filters, and muxer; MP4 output adds `+faststart`. VP9 uses `-cpu-used` and `-b:v 0` for CRF, ProRes uses profiles `hq`, `standard` and `lt`, FFV1 uses `-level 3`, and Theora uses `-q:v`. Pixel format follows the codec. `audioArgs` covers the 5 audio codecs (`libopus`, `aac`, `libmp3lame`, `flac`, `ac3`) and the copy policy: source audio is copied when the container accepts its codec and, with explicit `-ac`, only when it matches. `Encode` runs one- or two-pass encodes; VP9 and Theora use two-pass ABR in size mode.
- `reference.go`: `ExtractReference` cuts the FFV1 reference clip.

## Output (`internal/output`)

`output.Resolve` turns the destination into a `Destination`. A file extension maps to one of the 8 containers through `config.ContainerForExt` (including `.qt` and `.ogv`), and an extension that conflicts with the requested container is an error. For a directory, it builds the file name as `<name>.fit<N>.<ext>` in Default mode or `<name>.<codec>.<ext>` in Conversion mode; when no container was requested it falls back to `config.Recommended(codec)`. `Temp` creates the destination-directory `.part` file, and `Publish` replaces or links it atomically, refusing existing files unless `--overwrite` is set. `RejectAlias` stops a destination that resolves to the source.

## Terminal output (`internal/console`)

`console.go` defines `Writer`, `Event`, `Diagnostic`, and `Result`, stage labels, and the colored footer (warnings in yellow, errors in red), gated on a terminal and `NO_COLOR`. `vt_windows.go` and `vt_other.go` enable ANSI handling where needed. `progress.go` parses FFmpeg progress lines into clock and percentage values. `Writer.Prompt` writes a single yellow prompt line with no trailing newline. `Footer` prints each diagnostic as exactly one line.

## Interactive sessions (`internal/tui`)

`RunSession` runs a Bubble Tea program in the alternate screen (`tea.WithAltScreen()`) for color terminals, with a plain fallback (`tea.WithoutRenderer()`) when `NO_COLOR` is set, `TERM=dumb`, or the terminal lacks VT support. Layout: a header line, the current step, and a context bar pinned to the bottom row with keys, step count, and active preset label. Redraws on `tea.WindowSizeMsg`.

`Mode` is `ModeSetup` or `ModePreset`. `Session` holds `Settings`, `Preset`, `TargetBytes` (nil means Conversion), `Edited`, and `ManualSetup`. `SessionOptions` holds presets, input, output, a terminal flag, and `HWProbe`. Esc cancels (returns nil session); Ctrl+C and context cancellation return `ErrInterrupted`.

- **Setup start screen:** lists saved presets and then `Setup manually`. Choosing a preset returns it at once with `ManualSetup == false`, with no review. `Setup manually` sets `ManualSetup = true` and opens these steps from Preset 1's values: codec, container, encoder speed, quality or CRF, audio codec, audio kbps, FPS, resolution, hardware, target, review.
- **Preset editor (`ModePreset`):** without a slot number the first screen lists presets plus `New preset` (the next free ID). Steps: label, description, codec, container, encoder speed, quality, audio codec, audio kbps, FPS, resolution, review. Presets store neither hardware nor target.
- **Greyed choices:** options that do not fit are dimmed, or shown as `[unavailable: <reason>]` in plain mode, and the cursor skips them. They cover containers that reject the video codec, audio codecs the container rejects, GPU backends the codec lacks or the machine failed to run, and `target` for `prores` and `ffv1`.
- **Skipped steps:** encoder speed for `prores`, `ffv1` and `theora`, quality for `ffv1`, audio kbps for `flac`. The review screen shows `n/a` for them.
- **Recommended label:** the container that `config.Recommended` returns for the codec carries a `(recommended)` suffix.
- **Back-navigation:** `resetAfterCodecChange`, `resetAfterContainerChange` and `resetAfterAudioCodecChange` move later choices that became invalid to the recommended or first valid option, and the review screen marks them `(reset)`.
- **Hardware probe:** when the hardware step opens, the model runs `SessionOptions.HWProbe` as a Bubble Tea command. It shows `checking...` until `hwProbeResultMsg` arrives and caches the result per codec for the session. Tests inject a fake probe.

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

Unit tests cover parsing, resolution, the compatibility table (every accepted and rejected cell), settings and storage including v1 to v3 migration, argument building for each codec, fit checking and force-target selection, output handling, and the session model. `internal/e2e` holds the end-to-end suite. Its default tests check path safety, invalid arguments before any config or tool access, non-terminal prompts, and read-only commands with an isolated profile. Its `integration` test builds the binary and runs it against real FFmpeg: Conversion and a 5 MB Default fit for each codec, ProRes with a size, a preset that mismatches `mov`, the `mov` audio fallback, FLAC at 5 MB, force-target (including to a directory), dry run, and the `latest.json` and `presets.json` invariants. Real-FFmpeg tests are behind the `integration` build tag. Format with `gofmt` before committing.
