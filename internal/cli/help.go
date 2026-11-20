package cli

const Usage = "pressurecooker <in> <out> [size] [options]"

const Help = `Usage:
  pressurecooker <in> <out> <size> [options]
  pressurecooker <in> <out> -t <size> [options]
  pressurecooker <in> <out> [options]
  pressurecooker <in> <out> [size] --setup [options]
  pressurecooker --new-preset [N]
  pressurecooker --preset-list
  pressurecooker --help | -h | /?
  pressurecooker --help-details
  pressurecooker --version

Options:
  -t, --target MB       Target size (default: none)
  -c, --codec C         av1, hevc, h264 (default: av1)
  -f, --container C     mp4, mkv, webm (default: mp4)
  -p, --preset P        Encoder speed, name or AV1 0-13 (default: 8)
  -l, --quality L       high, balanced, small (default: balanced)
      --crf N          Software CRF (default: none)
  -a, --audio-kbps N    Audio bitrate (default: source)
  -r, --fps N           Frame rate cap (default: source)
  -s, --resolution R    Resolution cap (default: 1080p)
  -q, --hw M            auto, vulkan, qsv, off (default: auto)
  -ps, --use-preset N   Saved preset (default: 1)
      --latest         Last successful settings (default: off)
  -ft, --force-target   Accept force-target when needed (default: off)
  --vmaf  Rank force-target candidates with VMAF (slower)
      --setup          Interactive setup (default: off)
      --new-preset [N] Record preset (default: next slot)
      --preset-list    List presets (default: off)
  -d, --dry-run         Print plan (default: off)
      --overwrite      Replace destination (default: off)
  -ff, --ffmpeg PATH    FFmpeg executable (default: PATH)
  -fp, --ffprobe PATH   FFprobe executable (default: PATH)
  -h, --help, -?, /?    Short help (default: off)
      --help-details   Detailed help (default: off)
      --version        Version (default: off)

Examples:
  pressurecooker input.mp4 output.mp4 20
  pressurecooker input.mov output.mkv --setup
  pressurecooker input.mp4 output.mp4 8 -ft --vmaf
`

const Details = `Modes and prompts:
Default: a positional size or -t/--target selects a bitrate encode to that size.
Conversion: omit the size to use the resolved quality or CRF without a size limit.
  No size given: convert without a size limit? [y/N]
Setup: --setup requires <in> <out> and a terminal, checks paths before opening the TUI,
and runs with the confirmed settings. Its size field starts from the CLI size;
empty selects Conversion. Setup confirmation replaces the conversion prompt.
Source resolution or FPS below a resolved cap asks separately:
  Source is 1280x720 at 30 fps, below the preset's 1920x1080. Keep the source size? [y/N]
Yes clears the exceeded cap; output never upscales or raises source FPS.
A failed fit check or persistent overshoot asks:
  <reason>. Force-target changes bitrate, resolution, FPS and audio bitrate to fit. Use it? [y/N]
-ft/--force-target answers yes only to the force-target question.
Answer y or n. Enter and EOF mean no. Declining Conversion or source-cap
confirmation returns 1; declining force-target returns 4, with nothing published.
A prompt needs both stdin and stdout to be terminals; otherwise exit 2.
Dry-run prints resolved settings and each pending question as a warning.
It opens no TUI, encodes nothing, and writes no config or output.

Presets and precedence:
Preset fields: id, label (required, max 40 characters), description (max 120),
codec, container, encoder speed, quality or crf, audio_kbps, fps, resolution.
Null audio means source audio; null FPS and empty resolution mean source.
Hardware is not saved in presets and defaults to auto.
Factory preset 1: Default; AV1, MP4, speed 8 (measured SVT-AV1 default),
balanced (CRF 38), source audio and FPS, 1080p maximum.
--use-preset/-ps N selects a saved slot, default 1. --preset-list lists them.
--new-preset appends; --new-preset N prefills and replaces slot N on confirmation.
First terminal processing use records preset 1 after checking paths.
Non-terminal first use and dry-run use the factory preset without saving.
Esc cancels with exit 1 and Ctrl+C returns 130; neither saves the draft.
Resolution order: explicit flags > --latest > selected preset > factory.
--latest and -ps conflict. A missing latest file falls back to the selected preset.
Explicit quality clears inherited CRF; explicit CRF clears inherited quality.
Recognized destination extensions fix the container; explicit -f must agree.
-a fixes audio bitrate. Size with explicit --crf or --quality is an error.
-ft or --vmaf without size is an error. No arguments and missing paths are errors.
Removed flags: -v/--verbose (progress is always shown),
-co/--conversion (omit size), --defaults (use --new-preset 1), --force (use -ft).

Audio policy:
AudioSource keeps compatible AAC for MP4/MKV or Opus for WebM in Conversion;
otherwise audio is encoded at 128 kbps. Default copies compatible source audio
only if its bitrate uses at most 20% of the mux-adjusted budget per second.
Otherwise use the highest fitting bitrate in the 128/96/64/48 kbps ladder,
or 48 kbps if none fits the 20% share. Explicit -a always re-encodes at its
fixed bitrate. Preserve audio presence; force-target never drops audio.

Fit check and Default encode:
budget_bits = target_bytes * 8 * 0.97 (starting mux allowance).
Subtract audio_bits = audio_kbps * 1000 * duration, then divide by duration
and 1000 to obtain video_kbps. A positive integer video bitrate is attempted.
Infeasible reasons report numeric sizes, bitrates and durations.
Software uses two-pass ABR; hardware uses VBR with 0.96 headroom.
Codec, container, speed, resolution and FPS stay at the resolved values.
Encode into a destination-directory .part, check actual bytes, and retry once
if oversized, scaling bitrate by target/actual * 0.98. Persistent overshoot
offers force-target. An oversized file is never published.

Force-target method and bounds:
Codec, container and every explicit encoding field stay fixed. Adjustable
inherited fields: bitrate, smaller resolution and FPS, audio bitrate, and
backend under hw=auto. Never exceed source or preset caps, use a slower speed,
truncate, drop audio, or encode a previous output.
Choose at most three resolution/FPS candidates ranked by bitrate per pixel.
Extract one FFV1 reference: two two-second windows, three for sources over
three minutes; clips <=12 seconds use the whole source. Encode candidates
sequentially with one-pass ABR, score at source display size, and choose the
best measured candidate. Default metric: XPSNR, falling back to SSIM if missing.
Encode the original source with two-pass ABR (hardware VBR), then check bytes
and allow at most two scaled retries. Limits: 3 sample encodes, 3 full encodes.
A software pass pair counts as one full encode. Progress shows trial n/N.
Still oversized or infeasible: exit 4 and delete the unfinished output.

VMAF:
VMAF predicts how viewers rate quality and is slower than the default XPSNR. Use it when quality matters more than time. Needs FFmpeg built with libvmaf.
On this machine, VMAF scoring took about 2.4 times as long as XPSNR on a 720p60 sample.
This measurement compares scoring time, not total encoding time.
Missing libvmaf with --vmaf returns 2 when force-target needs scoring.

Quality mapping:
  Level       libx265 CRF  libx264 CRF  libsvtav1 CRF  Vulkan CQP  QSV ICQ
  high        26          21          30             26          26
  balanced    32          27          38             32          32
  small       36          31          46             36          36
Hardware quality mapping approximate until calibrated; hardware uses HEVC
values as quantizers. --crf is raw software CRF: h264/hevc 0-51, AV1 1-63.
Use --quality or --hw off with hardware; auto can select software for raw CRF.
Quality and CRF affect Conversion only; Default selects a bitrate.

Speed and resolution:
Named encoder speeds: ultrafast through placebo. AV1 names map to 12..3;
numeric SVT-AV1 presets are 0-13. QSV maps ultrafast/superfast to veryfast
and placebo to veryslow. Vulkan maps speeds to supported levels.
Resolution aliases: sd/360p=640x360, 480p=854x480, hd/720p=1280x720,
fullhd/fhd/1080p=1920x1080, qhd/2k/1440p=2560x1440,
uhd/4k/2160p=3840x2160. Custom WxH must be positive, even and <=16384.
FPS must be greater than 0 and at most 1000; audio kbps must be positive.

Units, paths and saves:
1 MB = 1,000,000 bytes. Public sizes changed from MiB (1,048,576 bytes).
Windows: %APPDATA%/pressurecooker, or ~/AppData/Roaming/pressurecooker.
POSIX: $XDG_CONFIG_HOME/pressurecooker, or ~/.config/pressurecooker.
presets.json and latest.json use schema version 3 and strict JSON.
latest.json stores encoding fields plus hw, without label, description or size.
Version-1 Python and version-2 latest settings migrate in memory; their bytes
stay unchanged until a successful authorized save. Retired defaults.json is ignored.
Successful Default, Conversion and Setup runs save latest; a run that used
force-target never saves it. A latest save failure warns after publication.
Preset confirmation alone saves presets. Failed runs and cancelled drafts do not
save latest. Saves use a synced temporary file and atomic replacement.
Corrupt or unknown-version files produce an error naming the path, and are never
silently overwritten. Help, version and parse errors access no config or tools.
Paths, overwrite, dry-run, tool paths, size, modes and origins are never saved.

Exit codes:
  0    Success
  1    Cancelled by the user
  2    Invalid input/configuration, or prompt without a terminal
  3    Required FFmpeg/FFprobe tool missing
  4    Could not fit; nothing published
  5    Encode failure
  130  Interrupted

Hardware and output limits:
FFmpeg and FFprobe are external executables. Software needs libx264, libx265
or libsvtav1. auto tries Vulkan then software; QSV is explicitly selected.
Encoder listings do not prove GPU support: live encodes probe it. Explicit
unsupported hardware combinations fail. Software decoding is used as needed.
Windows .bat/.cmd wrappers are unsupported. Use executable tool paths.
Output keeps one video and one audio stream, 8-bit 4:2:0, MP4 +faststart.
WebM requires AV1 and Opus. HDR, subtitles and additional streams are not preserved.
Source/destination aliases and existing outputs are rejected; --overwrite allows
destination replacement. Verified .part files are published atomically.
Directory names: Default <name>.fit<N>.<ext>; Conversion <name>.<codec>.<ext>.
Paths with spaces or Unicode must be passed as one argument.
Flag names are case-insensitive, --flag=value is accepted, and -ps, -ft, -ff,
-fp are whole flags. -- ends option parsing. A bare numeric second positional
without size is rejected; use ./20 to name that directory.
`
