# pressurecooker

Never have to pay for video upload limits again, pressurecook videos to your preferred size, using FFmpeg.

Uses Vulkan for GPU Detection, supports NVIDIA NVENC, AMD VCE, Intel QuickSync. Through a simple command-line interface, pressurecooker calculates a video and audio bitrate budget from the source duration. It reports the finished size and warns when the output exceeds the target. Encoder rate control and container overhead can cause the result to differ from the requested size.

hainan chicken
## Requirements

- FFmpeg and ffprobe, installed separately and on `PATH`, or passed with `--ffmpeg` and `--ffprobe`.
- Windows (`amd64`, `386`) or Linux (`amd64`, `386`).
- Optional GPU encoding: Vulkan Video (`h264_vulkan`, `hevc_vulkan`, `av1_vulkan`) or Intel Quick Sync (`h264_qsv`, `hevc_qsv`, `av1_qsv`). Under `--hw auto`, pressurecooker probes for Vulkan Video and falls back to the software encoders (`libx264`, `libx265`, `libsvtav1`) when the GPU cannot encode the selected codec. Quick Sync is used only when `--hw qsv` selects it.

FFmpeg is an external executable; the binary bundles no codec. List encoders with `ffmpeg -hide_banner -encoders` to confirm what your build provides.

## Installation

Prebuilt binaries are published per platform on the [releases page](https://github.com/maxie-maxie/pressurecooker/releases):

- `pressurecooker-windows-amd64.exe`, `pressurecooker-windows-386.exe`
- `pressurecooker-linux-amd64`, `pressurecooker-linux-386`

Install the newest build with Go:

```text
go install github.com/maxie-maxie/pressurecooker/cmd/pressurecooker@latest
```

Build from a checkout:

```text
git clone https://github.com/maxie-maxie/pressurecooker.git
cd pressurecooker
go build ./cmd/pressurecooker
```

The legacy Python package on PyPI (`pressurecooker-video`) is no longer updated.

## Usage

```text
pressurecooker <source-video> <destination> [target-size-mb] [options]
```

The destination is a directory or a file ending in `.mp4`, `.mkv`, or `.webm`. Its extension fixes the container unless `--container` is set, and an explicit `--container` must agree with the extension. A directory receives `<name>.fit<N>.<ext>` in Default and force-target modes, and `<name>.<codec>.<ext>` in Conversion mode. `./20` names a directory called `20`, so a bare number is never read as a destination. The optional target size is a strict byte limit in decimal MB (`1 MB = 1,000,000 bytes`); oversized files are never published.

Flag names are case-insensitive, and `--flag=value` and `-f=value` are accepted. Tokens after `--` are filenames. Preview the plan before encoding:

```text
pressurecooker input.mp4 output.mp4 20 --dry-run
```

## Modes

Pressurecooker runs in three modes. A size chooses between Default and Conversion.

**Default** (a size is given, positional or `-t/--target`): fits to the real target bytes with two-pass ABR (software) or VBR (hardware), an arithmetic fit check, one scaled retry (`target/actual * 0.98`), and never publishes an oversized output.

**Conversion** (no size given): encodes with the resolved `--quality` or `--crf`. Before encoding it asks `No size given: convert without a size limit? [y/N]`.

**Setup** (`--setup`, needs `<in> <out>`): checks paths first, then opens the alt-screen Setup TUI (bottom bar, label/description/fields, Esc cancels cleanly), then runs Default or Conversion with the confirmed values. The TUI size field starts from the command-line size; empty selects Conversion. The conversion prompt is not repeated after Setup.

Preset commands: `--new-preset [N]` opens an interactive TUI recorder (`N` replaces that slot), and `--preset-list` prints saved presets.

## Presets, `--latest`, and precedence

Factory Preset 1 is `Default`: AV1, MP4, speed `8`, quality `balanced`, 1080p maximum cap, source audio and FPS. The first interactive processing run opens the preset recorder prefilled with it, to confirm or customize slot 1. A non-terminal first run uses the factory preset in memory and saves nothing.

- `-ps N` / `--use-preset N` selects a saved slot (missing slot exits `2`).
- `--latest` loads `latest.json` (schema version 3; version 1 Python and version 2 JSON migrate in memory without altering disk bytes until an authorized save; the retired `defaults.json` is ignored). Force-target (`-ft`) never saves `latest.json`.
- Precedence: explicit flags > `--latest` > selected preset (`-ps N`, default 1) > factory. A recognized destination extension still fixes the container.

## Prompts and non-interactive rules

All prompts default to No (`[y/N]`), need both stdin and stdout to be terminals (a prompt without a terminal exits `2`), and `--dry-run` prints each pending question as a warning without prompting, encoding, or writing config or output.

Prompts:

- `No size given: convert without a size limit? [y/N]` (Conversion, before encoding)
- `Source is 1280x720 at 30 fps, below the preset's 1920x1080. Keep the source size? [y/N]` (preset or `--latest` resolution or FPS cap above the source; explicit `-s`/`-r` above the source also prompts). Yes uses source values and never upscales; No cancels the run.
- `<reason>. Force-target changes bitrate, resolution, FPS and audio bitrate to fit. Use it? [y/N]` (fit check fails, or output still over after the retry). `-ft` answers yes to this question only.

Declining Conversion or the source-cap prompt returns `1`; declining force-target returns `4`, with nothing published. Enter and EOF mean no.

## Force-target and VMAF

Force-target keeps codec, container, and every explicit field fixed. Adjustable inherited fields are bitrate, smaller resolution and FPS, audio bitrate, and the backend under `--hw auto`. It never exceeds source or preset caps, uses a slower speed, truncates, drops audio, or encodes a previous output.

Method: choose at most three resolution/FPS candidates ranked by bitrate per pixel; extract one FFV1 reference (two 2-second windows, three for sources over three minutes; clips of 12 seconds or less use the whole source); encode candidates one at a time with one-pass ABR; score at source display size; then encode the original source with two-pass ABR (hardware VBR) and check bytes, with at most two scaled retries. Limits: 3 sample encodes and 3 full encodes. Progress shows `trial n/N`. Still oversized or infeasible: exit `4` and delete the unfinished output.

Default metric is XPSNR, falling back to SSIM when XPSNR is missing. `--vmaf` ranks candidates with VMAF instead; it needs FFmpeg built with `libvmaf`, otherwise exit `2`. On this machine, VMAF scoring took about 2.4 times as long as XPSNR on a 720p60 sample. That ratio compares scoring time only, not total encoding time.

## Options

| Flag | Default | Values |
| --- | --- | --- |
| `-t`, `--target MB` | none | Target size in decimal MB |
| `-c`, `--codec` | `av1` | `av1`, `hevc`, `h264` |
| `-f`, `--container` | `mp4` | `mp4`, `mkv`, `webm` |
| `-p`, `--preset` | `8` | Encoder speed: `ultrafast`..`placebo`, or AV1 `0`-`13` |
| `-l`, `--quality` | `balanced` | `high`, `balanced`, `small` (Conversion only) |
| `--crf N` | none | Raw software CRF: `0`-`51` (h264/hevc), `1`-`63` (av1) (Conversion only) |
| `-a`, `--audio-kbps N` | source | Positive integer; fixes audio |
| `-r`, `--fps N` | source | Greater than `0`, at most `1000` |
| `-s`, `--resolution R` | `1080p` | Alias or positive even `WxH`, at most `16384` |
| `-q`, `--hw` | `auto` | `auto`, `vulkan`, `qsv`, `off` |
| `-ps`, `--use-preset N` | `1` | Saved preset slot |
| `--latest` | off | Reuse last run settings |
| `-ft`, `--force-target` | off | Answer yes to the force-target question |
| `--vmaf` | off | Rank force-target candidates with VMAF |
| `--setup` | off | Interactive setup, then encode |
| `--new-preset [N]` | next slot | Record a preset |
| `--preset-list` | off | List presets |
| `-d`, `--dry-run` | off | Print plan; no prompts, encodes, or writes |
| `--overwrite` | off | Replace an existing destination |
| `-ff`, `--ffmpeg PATH` | `PATH` | FFmpeg executable |
| `-fp`, `--ffprobe PATH` | `PATH` | ffprobe executable |
| `-h`, `--help`, `-?`, `/?` | | Short help |
| `--help-details` | | Detailed help |
| `--version` | | Print version |

`--crf` and `--quality` apply only without a size. `-ft` and `--vmaf` require a size. `--latest` and `-ps` cannot be combined. A bare numeric second positional without a size is rejected; use `./20` for a directory named `20`.

## Removed flags

| Old flag | Replacement |
| --- | --- |
| `-v`, `--verbose` | Progress is always shown |
| `-co`, `--conversion` | Omit the size to convert |
| `--defaults` | `--new-preset 1` |
| `--force` | `-ft` |

## Quality mapping

| Level | libx265 CRF | libx264 CRF | libsvtav1 CRF | Vulkan CQP | QSV ICQ |
| --- | --- | --- | --- | --- | --- |
| high | 26 | 21 | 30 | 26 | 26 |
| balanced | 32 | 27 | 38 | 32 | 32 |
| small | 36 | 31 | 46 | 36 | 36 |

Hardware values are approximate until calibrated; uncalibrated hardware uses the HEVC quantizer. `--crf` passes a raw software value. For hardware, use `--quality` or `--hw off`. Quality and CRF affect Conversion only; Default selects a bitrate.

## Speed and resolution

Named encoder speeds: `ultrafast` through `placebo`. AV1 names map to `12`..`3`; numeric SVT-AV1 presets are `0`-`13`. QSV maps `ultrafast`/`superfast` to `veryfast` and `placebo` to `veryslow`. Vulkan maps speeds to supported levels.

Resolution aliases: `sd`/`360p` = `640x360`, `480p` = `854x480`, `hd`/`720p` = `1280x720`, `fullhd`/`fhd`/`1080p` = `1920x1080`, `qhd`/`2k`/`1440p` = `2560x1440`, `uhd`/`4k`/`2160p` = `3840x2160`. Custom `WxH` must be positive, even, and at most `16384`. FPS must be greater than `0` and at most `1000`; audio kbps must be positive.

## Audio policy

Default mode: copy source audio only when its codec matches the container (AAC for MP4/MKV, Opus for WebM) and its bitrate is at most 20% of the mux-adjusted budget per second; otherwise re-encode at the highest fitting bitrate in the `128`/`96`/`64`/`48` kbps ladder, or `48` kbps if none fits. Conversion keeps compatible source audio or re-encodes at `128` kbps. Explicit `-a` always re-encodes at its fixed bitrate. Audio presence is preserved; force-target never drops audio.

## Fit check and Default encode

```text
budget_bits = target_bytes * 8 * 0.97
audio_bits  = audio_kbps * 1000 * duration
video_kbps  = (budget_bits - audio_bits) / duration / 1000
```

A positive integer video bitrate is attempted. Infeasible reasons report numeric sizes, bitrates, and durations. Software uses two-pass ABR; hardware uses VBR with 0.96 headroom. Codec, container, speed, resolution, and FPS stay at the resolved values. The encode writes a destination-directory `.part` file, checks actual bytes, and retries once if oversized, scaling bitrate by `target/actual * 0.98`. Persistent overshoot offers force-target. An oversized file is never published.

## Output and atomic publish

Each encode writes a unique temporary file and publishes it to the destination only after success. An existing destination is refused unless `--overwrite` is passed. The source file and its symlinks or hardlinks cannot be the destination. `Ctrl-C` removes temporary files and exits `130`. A dry run writes no files and runs no encodes. Output keeps one video and one audio stream, 8-bit 4:2:0, MP4 with `+faststart`. WebM requires AV1 and Opus. HDR, subtitles, and additional streams are not preserved.

## Config paths and saves

- Windows: `%APPDATA%\pressurecooker`, fallback `~\AppData\Roaming\pressurecooker`.
- POSIX: `$XDG_CONFIG_HOME/pressurecooker`, fallback `~/.config/pressurecooker`.

`presets.json` and `latest.json` use schema version 3 and strict JSON. `latest.json` stores encoding fields plus `hw`, without label, description, or size. Version 1 Python and version 2 latest settings migrate in memory; their bytes stay unchanged until a successful authorized save. The retired `defaults.json` is ignored. Successful Default, Conversion, and Setup runs save `latest`; a run that used force-target never saves it. A latest save failure warns after publication. Preset confirmation alone saves presets. Failed runs and cancelled drafts do not save `latest`. Saves use a synced temporary file and atomic replacement. Corrupt or unknown-version files produce an error naming the path and are never silently overwritten. Help, version, and parse errors access no config or tools. Paths, overwrite, dry-run, tool paths, size, modes, and origins are never saved.

## Exit codes

| Code | Meaning |
| --- | --- |
| `0` | Success |
| `1` | Cancelled by the user |
| `2` | Invalid input/configuration, or prompt without a terminal |
| `3` | Required FFmpeg/ffprobe tool missing |
| `4` | Could not fit; nothing published |
| `5` | Encode failure |
| `130` | Interrupted |

## Examples

Default fit to 20 MB:

```text
pressurecooker input.mp4 output.mp4 20
```

Conversion with H.264:

```text
pressurecooker input.mov output.mp4 --codec h264
```

Setup TUI:

```text
pressurecooker input.mp4 output.mp4 20 --setup
```

List presets:

```text
pressurecooker --preset-list
```

Force-target with VMAF:

```text
pressurecooker input.mp4 output.mp4 8 -ft --vmaf
```

Dry run:

```text
pressurecooker input.mp4 output.mp4 20 --dry-run
```

## Development

Requires Go 1.27 or newer and FFmpeg on `PATH` for the integration tests.

```text
go build ./cmd/pressurecooker
go test ./...
go vet ./...
go test -tags integration ./...
```

Cross-builds:

```text
CGO_ENABLED=0 GOOS=windows GOARCH=386 go build ./...
CGO_ENABLED=0 GOOS=linux GOARCH=386 go build ./...
```

The repository layout is described in [`docs/APP_CONTEXT.md`](docs/APP_CONTEXT.md).

## License

Distributed under the [MIT License](LICENSE). Third-party notices are in [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md). FFmpeg and its encoders are installed and licensed separately.
