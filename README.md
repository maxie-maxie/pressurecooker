# pressurecooker

Compresses and converts video through an external FFmpeg installation. Target sizes are decimal megabytes (`1 MB = 1,000,000 bytes`).

## Requirements

- FFmpeg and ffprobe, installed separately and on `PATH`, or passed with `--ffmpeg` and `--ffprobe`.
- Windows (`amd64`, `386`) or Linux (`amd64`, `386`).
- Software encoders for the codecs you choose: `libsvtav1`, `libx265`, `libx264`, `libvpx-vp9`, `prores_ks`, `ffv1`, `libtheora` for video, and `libopus`, `aac`, `libmp3lame`, `flac`, `ac3` for audio.
- Optional GPU encoding: Vulkan Video (`h264_vulkan`, `hevc_vulkan`, `av1_vulkan`, `prores_ks_vulkan`, `ffv1_vulkan`) or Intel Quick Sync (`h264_qsv`, `hevc_qsv`, `av1_qsv`, `vp9_qsv`). Under `--hw auto`, pressurecooker test-encodes with the codec's GPU backend and falls back to software when the GPU cannot encode it. Quick Sync is used only when `--hw qsv` selects it.

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
pressurecooker <input source> <output file/path> [target-size-mb] [options]
pressurecooker <input source> <output file/path> [target-size-mb] -p N [options]
pressurecooker <input source> <output file/path> [target-size-mb] --setup [options]
pressurecooker -p | --preset
pressurecooker -p N | --preset N
pressurecooker --preset-list
```

The destination is a directory or a file whose extension names one of the supported containers (see the table below). The extension fixes the container unless `-f`/`--container` is set, and an explicit `--container` must agree with the extension. A directory receives `<name>.fit<N>.<ext>` in Default and force-target modes, and `<name>.<codec>.<ext>` in Conversion mode. `./20` names a directory called `20`, so a bare number is never read as a destination. The optional target size is a strict byte limit in decimal MB (`1 MB = 1,000,000 bytes`); oversized files are never published.

Flag names are case-insensitive, and `--flag=value` and `-f=value` are accepted. Tokens after `--` are filenames. Preview the plan before encoding:

```text
pressurecooker input.mp4 output.mp4 20 --dry-run
```

## Codecs, containers, and compatibility

Video codecs: `av1`, `hevc`, `h264`, `vp9`, `prores`, `ffv1`, `theora`. Audio codecs: `opus`, `aac`, `mp3`, `flac`, `ac3`. Containers: `mp4`, `mkv`, `webm`, `mov`, `avi`, `ogg`, `ts`, `nut`. The names `quicktime` (for `mov`) and `mpegts` (for `ts`) are accepted by `-f`.

Every pair below was muxed with FFmpeg `N-124716`. The tool allows exactly these combinations and rejects the rest.

| Container | Extensions | Video | Audio |
| --- | --- | --- | --- |
| `mp4` | `.mp4` | av1, hevc, h264, vp9, ffv1 | opus, aac, mp3, flac, ac3 |
| `mkv` | `.mkv` | all 7 | all 5 |
| `webm` | `.webm` | av1, vp9 | opus |
| `mov` | `.mov`, `.qt` | hevc, h264, prores, ffv1, theora | aac, mp3, ac3 |
| `avi` | `.avi` | all 7 | aac, mp3, flac, ac3 |
| `ogg` | `.ogg`, `.ogv` | theora | opus, flac |
| `ts` | `.ts` | all 7 | all 5 |
| `nut` | `.nut` | all 7 | all 5 |

Recommended container per video codec: `av1`, `hevc` and `h264` use `mp4`; `vp9` uses `webm`; `prores` uses `mov`; `ffv1` uses `mkv`; `theora` uses `ogg`. The setup screens mark it `(recommended)`, and force-target uses it for a directory destination. The matrix was measured for muxing, not player support. For example, HEVC in `mp4` and AV1 in `avi` mux fine but many players reject them.

Per-codec controls:

| Codec | Encoder speed | Quality | CRF range | Size mode | GPU backends |
| --- | --- | --- | --- | --- | --- |
| `av1` | `0`-`13` or names | levels or CRF | `1`-`63` | 2-pass ABR | `vulkan`, `qsv` |
| `hevc` | named speeds | levels or CRF | `0`-`51` | 2-pass ABR | `vulkan`, `qsv` |
| `h264` | named speeds | levels or CRF | `0`-`51` | 2-pass ABR | `vulkan`, `qsv` |
| `vp9` | `cpu-used` `0`-`8` or names | levels or CRF | `0`-`63` | 2-pass ABR | `qsv` |
| `prores` | n/a | `hq`, `standard`, `lt` | none | Conversion only | `vulkan` |
| `ffv1` | n/a | n/a (lossless) | none | Conversion only | `vulkan` |
| `theora` | n/a | `-q:v` levels | none | 2-pass ABR | none |

`--crf` or `-e` with `prores`, `ffv1` or `theora` exits `2`. A size with `prores` or `ffv1` exits `2`, and so does `-l` with `ffv1`.

When the container does not accept the video codec, the run exits `2` before any tool runs, and the message lists the accepted codecs: `mov does not accept av1; use -c hevc, h264, prores, ffv1 or theora`. This applies to codecs from flags, presets and `--latest`. The video codec is never swapped silently.

Audio differs by origin. If the audio codec was not passed with `-ac` and the container rejects it, pressurecooker uses the first accepted codec of `opus`, `aac`, `mp3`, `ac3` and prints a yellow warning such as `mov does not accept opus; using aac`. If you passed `-ac` and the container rejects it, the run exits `2`.

A change of codec adapts inherited settings. A numeric speed becomes a name when the new codec needs one, and an inherited CRF is cleared for codecs without CRF. Explicit `-e`, `-l` and `--crf` values are validated, never dropped.

## Modes

Pressurecooker runs in three modes. A size chooses between Default and Conversion.

**Default** (a size is given, positional or `-t/--target`): fits to the real target bytes with two-pass ABR (software) or VBR (hardware), an arithmetic fit check, one scaled retry (`target/actual * 0.98`), and never publishes an oversized output.

**Conversion** (no size given): encodes with the resolved `--quality` or `--crf`. Before encoding it asks `No size given: convert without a size limit? [y/N]`.

**Setup** (`--setup`, needs `<input source> <output file/path>`): checks paths first, then opens the alt-screen Setup TUI (bottom bar, label/description/fields, Esc cancels cleanly), then runs Default or Conversion with the confirmed values. See [Presets and `--setup`](#presets-and---setup).

## Presets and `--setup`

Factory Preset 1 is `Default`: AV1, MP4, speed `8`, quality `balanced`, 1080p maximum cap, source audio and FPS, audio codec `opus`. Source audio is copied when the container accepts it; `opus` applies when audio is re-encoded. The first interactive processing run opens the preset editor prefilled with it, to confirm or customize slot 1. A non-terminal first run uses the factory preset in memory and saves nothing.

`--setup` starts with a list of saved presets (`1. Default`, ...) followed by `Setup manually`.

- **A saved preset:** Enter runs it at once, with no review screen and no edits. The size comes from the command line. With no size, the conversion prompt applies.
- **Setup manually:** opens every step, starting from Preset 1's values: codec, container, encoder speed, quality, audio codec, audio kbps, FPS, resolution, hardware, target, review. The size field starts from the command-line size, and an empty size selects Conversion. Confirming the review replaces the conversion prompt.

Setup screens:

- The container list marks the recommended one with `(recommended)`.
- Choices that do not fit are dimmed, or shown as `[unavailable: <reason>]` in plain mode, and the cursor skips them. They are containers that reject the video codec, audio codecs the container rejects, GPU backends the codec lacks or this machine failed to run, and the target size for `prores` and `ffv1`.
- The hardware step test-encodes each supported backend and shows `checking...` until the result arrives. Results are cached per codec for the session.
- Steps a codec does not support are skipped and show `n/a` on the review screen: encoder speed for `prores`, `ffv1` and `theora`, quality for `ffv1`, and audio kbps for `flac`.
- If you go back and change a value, later choices that become invalid reset to the recommended or first valid option, and the review screen marks them `(reset)`.

Preset commands:

- `-p N` / `--preset N` with paths runs saved preset `N` (a missing slot exits `2`). `N` is a bare integer of 1 or more and is required with paths. Put the size before it: `in out 25 -p 2` means 25 MB with preset 2, while `in out -p 25` means preset 25 with no size.
- `-p` / `--preset` without paths opens the preset editor. Its first screen lists saved presets and `New preset` (the next free slot). Steps: label, description, codec, container, encoder speed, quality, audio codec, audio kbps, FPS, resolution, review. Presets store neither hardware nor target.
- `-p N` / `--preset N` without paths edits slot `N`, creating it if missing.
- `--preset-list` prints saved presets.
- The editor and `--preset-list` cannot be combined with paths or processing flags. `-p N` cannot be combined with `--setup` or `--latest`.

Files and precedence:

- `presets.json` and `latest.json` use schema version 4, which adds `audio_codec`. Files from versions 1 to 3 migrate in memory with `audio_codec: opus`, and their bytes stay unchanged until an authorized save.
- `--latest` loads `latest.json`. It is saved after the published file passes the post-publish probe, including after a successful `-ft` run, which stores the settings that produced the file. It is never saved on error, cancel, dry run or overshoot. Runs never change `presets.json`.
- Precedence: explicit flags > `--latest` > selected preset (`-p N`, default 1) > factory. A recognized destination extension still fixes the container.

## Prompts and non-interactive rules

All prompts default to No (`[y/N]`), need both stdin and stdout to be terminals (a prompt without a terminal exits `2`), and `--dry-run` prints each pending question as a warning without prompting, encoding, or writing config or output.

Prompts:

- `No size given: convert without a size limit? [y/N]` (Conversion, before encoding)
- `Source is 1280x720 at 30 fps, below the preset's 1920x1080. Keep the source size? [y/N]` (preset or `--latest` resolution or FPS cap above the source; explicit `-s`/`-r` above the source also prompts). Yes uses source values and never upscales; No cancels the run.
- `<reason>. Force-target may change codec, encoder, container, bitrate, resolution, FPS and audio to fit. Use it? [y/N]` (fit check fails, or output still over after the retry). The word `container` appears only for directory destinations. `-ft` answers yes to this question only.

Declining Conversion or the source-cap prompt returns `1`; declining force-target returns `4`, with nothing published. Enter and EOF mean no.

## Force-target and VMAF

Force-target keeps every field you passed explicitly. It may change the fields that came from a preset, `--latest` or the factory, which are:

- **Container:** locked by the destination extension or `-f`. For a directory destination without `-f`, it uses the recommended container of the chosen codec.
- **Video codec:** the best-quality lossy codec that the container accepts and your FFmpeg can encode, in the order `av1`, `hevc`, `vp9`, `h264`, `theora`. `theora` is chosen only for `ogg`. `prores` and `ffv1` are never chosen.
- **Encoder:** an explicit `--hw` is kept. Under `--hw auto`, one of the three trial candidates uses the GPU encoder when it passes the probe, and it wins only if its score is within 0.5 dB XPSNR (or 1 VMAF point) of the best software candidate. Otherwise software is used.
- **Audio codec:** the first accepted of `opus`, `aac`, `mp3`, `ac3`. `flac` only when passed with `-ac`.
- **Rates:** bitrate, smaller resolution and FPS, and audio bitrate.

It never exceeds source or preset caps, uses a slower speed, truncates, drops audio, or encodes a previous output.

Method: choose at most three resolution/FPS candidates ranked by bitrate per pixel; extract one FFV1 reference (two 2-second windows, three for sources over three minutes; clips of 12 seconds or less use the whole source); encode candidates one at a time with one-pass ABR; score at source display size; then encode the original source with two-pass ABR (hardware VBR) and check bytes, with at most two scaled retries. Limits: 3 sample encodes and 3 full encodes. Progress shows `trial n/N`. Still oversized or infeasible: exit `4` and delete the unfinished output.

Default metric is XPSNR, falling back to SSIM when XPSNR is missing. `--vmaf` ranks candidates with VMAF instead; it needs FFmpeg built with `libvmaf`, otherwise exit `2`. On this machine, VMAF scoring took about 2.4 times as long as XPSNR on a 720p60 sample. That ratio compares scoring time only, not total encoding time.

## Options

| Flag | Default | Values |
| --- | --- | --- |
| `-t`, `--target MB` | none | Target size in decimal MB |
| `-c`, `--codec` | `av1` | `av1`, `hevc`, `h264`, `vp9`, `prores`, `ffv1`, `theora` |
| `-f`, `--container` | `mp4` | `mp4`, `mkv`, `webm`, `mov` (`quicktime`), `avi`, `ogg`, `ts` (`mpegts`), `nut` |
| `-ac`, `--audio-codec` | `opus` | `opus`, `aac`, `mp3`, `flac`, `ac3` |
| `-e`, `--enc-speed` | `8` | Encoder speed: `ultrafast`..`placebo`, or numeric `0`-`13` |
| `-p`, `--preset [N]` | `1` | Run preset `N` with paths; edit without paths |
| `-l`, `--quality` | `balanced` | `high`, `balanced`, `small` (Conversion only) |
| `--crf N` | none | Raw software CRF; range depends on the codec (Conversion only) |
| `-a`, `--audio-kbps N` | source | Positive integer; fixes audio; rejected with `flac` |
| `-r`, `--fps N` | source | Greater than `0`, at most `1000` |
| `-s`, `--resolution R` | `1080p` | Alias or positive even `WxH`, at most `16384` |
| `-q`, `--hw` | `auto` | `auto`, `vulkan`, `qsv`, `off` |
| `--latest` | off | Reuse last run settings |
| `-ft`, `--force-target` | off | Answer yes to the force-target question |
| `--vmaf` | off | Rank force-target candidates with VMAF |
| `--setup` | off | Interactive setup, then encode |
| `--preset-list` | off | List presets |
| `-d`, `--dry-run` | off | Print plan; no prompts, encodes, or writes |
| `--overwrite` | off | Replace an existing destination |
| `-ff`, `--ffmpeg PATH` | `PATH` | FFmpeg executable |
| `-fp`, `--ffprobe PATH` | `PATH` | ffprobe executable |
| `-h`, `--help`, `-?`, `/?` | | Short help |
| `--help-details` | | Detailed help |
| `--version` | | Print version |

`--crf` and `--quality` apply only without a size. `-ft` and `--vmaf` require a size. `--latest` cannot be combined with `-p N` or `--setup`. A bare numeric second positional without a size is rejected; use `./20` for a directory named `20`.

## Changed and removed flags

In v1.3.0, `-e`/`--enc-speed` sets the encoder speed. `-p`/`--preset` now selects a preset, so an old `-p fast` exits `2` with `preset number must be an integer; encoder speed is -e/--enc-speed`.

| Old flag | Replacement |
| --- | --- |
| `-p`, `--preset` (speed) | `-e`, `--enc-speed` |
| `-ps`, `--use-preset N` | `-p N` |
| `--new-preset [N]` | `--preset [N]` |
| `--defaults` | `--preset 1` |
| `-v`, `--verbose` | Progress is always shown |
| `-co`, `--conversion` | Omit the size to convert |
| `--force` | `-ft` |

## Quality mapping

| Level | libx265 CRF | libx264 CRF | libsvtav1 CRF | VP9 CRF | Vulkan CQP | QSV ICQ |
| --- | --- | --- | --- | --- | --- | --- |
| high | 26 | 21 | 30 | 24 | 26 | 26 |
| balanced | 32 | 27 | 38 | 32 | 32 | 32 |
| small | 36 | 31 | 46 | 40 | 36 | 36 |

Other codecs:

| Level | ProRes profile | Theora `-q:v` |
| --- | --- | --- |
| high | `hq` | 8 |
| balanced | `standard` | 6 |
| small | `lt` | 4 |

FFV1 is lossless (`-level 3`) and has no quality setting. Pixel format follows the codec: ProRes is 10-bit 4:2:2, FFV1 uses a lossless format, and the rest are 8-bit 4:2:0.

Hardware values are approximate until calibrated; hardware uses the HEVC quantizer, and VP9 on QSV uses the VP9 values. `--crf` passes a raw software value: `0`-`51` for h264 and hevc, `1`-`63` for av1, `0`-`63` for vp9. For hardware, use `--quality` or `--hw off`; `auto` can select software for a raw CRF. Quality and CRF affect Conversion only; Default selects a bitrate. The Theora `-q:v` values are starting points.

## Speed and resolution

Named encoder speeds: `ultrafast` through `placebo`. AV1 names map to `12`..`3`; numeric SVT-AV1 presets are `0`-`13`. VP9 names map to `cpu-used` `8`..`0`. QSV maps `ultrafast`/`superfast` to `veryfast` and `placebo` to `veryslow`. Vulkan maps speeds to supported levels.

Resolution aliases: `sd`/`360p` = `640x360`, `480p` = `854x480`, `hd`/`720p` = `1280x720`, `fullhd`/`fhd`/`1080p` = `1920x1080`, `qhd`/`2k`/`1440p` = `2560x1440`, `uhd`/`4k`/`2160p` = `3840x2160`. Custom `WxH` must be positive, even, and at most `16384`. FPS must be greater than `0` and at most `1000`; audio kbps must be positive.

## Audio policy

- **Conversion:** compatible source audio is copied when the container accepts its codec. With an explicit `-ac`, source audio is copied only when it already uses that codec. Otherwise the audio is encoded with the resolved audio codec at `128` kbps.
- **Default:** compatible source audio is copied only when its bitrate is at most 20% of the mux-adjusted budget per second. Otherwise it is re-encoded at the highest fitting bitrate in the `128`/`96`/`64`/`48` kbps ladder, or `48` kbps if none fits.
- **Explicit `-a`:** always re-encodes at the fixed bitrate.
- **FLAC:** lossless, so it ignores inherited kbps and rejects an explicit `-a` (exit `2`). In size mode, pressurecooker encodes the FLAC audio first and subtracts its exact byte count from the budget. If the audio alone does not fit, the run reports the numeric reason.
- Audio presence is preserved; force-target never drops audio.

## Fit check and Default encode

```text
budget_bits = target_bytes * 8 * 0.97
audio_bits  = audio_kbps * 1000 * duration
video_kbps  = (budget_bits - audio_bits) / duration / 1000
```

A positive integer video bitrate is attempted. Infeasible reasons report numeric sizes, bitrates, and durations. Software uses two-pass ABR; hardware uses VBR with 0.96 headroom. Codec, container, speed, resolution, and FPS stay at the resolved values. The encode writes a destination-directory `.part` file, checks actual bytes, and retries once if oversized, scaling bitrate by `target/actual * 0.98`. Persistent overshoot offers force-target. An oversized file is never published.

## Output and atomic publish

Each encode writes a unique temporary file and publishes it to the destination only after success. An existing destination is refused unless `--overwrite` is passed. The source file and its symlinks or hardlinks cannot be the destination. `Ctrl-C` removes temporary files and exits `130`. A dry run writes no files and runs no encodes. Output keeps one video and one audio stream. MP4 gets `+faststart`. WebM accepts AV1 or VP9 video and Opus audio. HDR, subtitles, and additional streams are not preserved.

## Config paths and saves

- Windows: `%APPDATA%\pressurecooker`, fallback `~\AppData\Roaming\pressurecooker`.
- POSIX: `$XDG_CONFIG_HOME/pressurecooker`, fallback `~/.config/pressurecooker`.

`presets.json` and `latest.json` use schema version 4 and strict JSON, including `audio_codec`. `latest.json` stores encoding fields plus `hw`, without label, description, or size. Versions 1 to 3 migrate in memory with `opus`; their bytes stay unchanged until a successful authorized save. The retired `defaults.json` is ignored. Successful Default, Conversion, Setup and `-ft` runs save `latest` after the published file passes the post-publish probe. A latest save failure warns after publication. Preset confirmation alone saves presets. Failed, cancelled, dry-run and overshoot runs do not save `latest`. Saves use a synced temporary file and atomic replacement. Corrupt or unknown-version files produce an error naming the path and are never silently overwritten. Help, version, and parse errors access no config or tools. Paths, overwrite, dry-run, tool paths, size, modes, and origins are never saved.

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

Conversion with H.264 into a QuickTime container (the inherited Opus audio falls back to AAC with a warning):

```text
pressurecooker input.mov output.mov --codec h264
```

Preview a VP9 conversion:

```text
pressurecooker input.mov output.mkv -c vp9 --dry-run
```

Run saved preset 2 with a 25 MB target:

```text
pressurecooker input.mp4 output.mp4 25 -p 2
```

Force-target to a directory, which uses the recommended container of the chosen codec:

```text
pressurecooker input.mp4 outdir/ 8 -ft
```

Setup TUI:

```text
pressurecooker input.mp4 output.mp4 20 --setup
```

List presets, or open the preset editor:

```text
pressurecooker --preset-list
pressurecooker --preset
```

Force-target with VMAF:

```text
pressurecooker input.mp4 output.mp4 8 -ft --vmaf
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
