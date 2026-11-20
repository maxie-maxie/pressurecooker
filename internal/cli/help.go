package cli

const Usage = "pressurecooker <in> <out> [size] [options]"

const Help = `Usage:
  pressurecooker <in> <out> <size> [options]
  pressurecooker <in> <out> -t <size> [options]
  pressurecooker <in> <out> [options]
  pressurecooker <in> <out> [size] -p N [options]
  pressurecooker <in> <out> [size] --setup [options]
  pressurecooker -p | --preset
  pressurecooker -p N | --preset N
  pressurecooker --preset-list
  pressurecooker --help | -h | /?
  pressurecooker --help-details
  pressurecooker --version

Options:
  -t, --target MB       Target size (default: none)
  -c, --codec C         av1, hevc, h264, vp9, prores, ffv1, theora (default: av1)
  -f, --container C     mp4, mkv, webm, mov/quicktime, avi, ogg, ts/mpegts, nut (default: mp4)
  -ac, --audio-codec C  opus, aac, mp3, flac, ac3 (default: opus)
  -e, --enc-speed P     Encoder speed, ultrafast..placebo or 0-13 (default: 8)
  -p, --preset [N]      Run preset N with paths; edit without paths (default: 1)
  -l, --quality L       high, balanced, small (default: balanced)
      --crf N          Software CRF (default: none)
  -a, --audio-kbps N    Audio bitrate (default: source)
  -r, --fps N           Frame rate cap (default: source)
  -s, --resolution R    Resolution cap (default: 1080p)
  -q, --hw M            auto, vulkan, qsv, off (default: auto)
      --latest         Last successful settings (default: off)
  -ft, --force-target   Accept force-target when needed (default: off)
  --vmaf  Rank force-target candidates with VMAF (slower)
      --setup          Interactive setup (default: off)
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
Setup: --setup requires <in> <out> and a terminal, checks paths before opening the TUI.
Choose a saved preset to run in one click, with no review and the CLI size preserved.
Without a size, the conversion prompt applies to a one-click preset.
Setup manually starts from Preset 1 and opens all supported controls and one review.
Its size field starts from the CLI size; empty selects Conversion. Manual review
confirmation replaces the conversion prompt.
Source resolution or FPS below a resolved cap asks separately:
  Source is 1280x720 at 30 fps, below the preset's 1920x1080. Keep the source size? [y/N]
Yes clears the exceeded cap; output never upscales or raises source FPS.
A failed fit check or persistent overshoot asks:
  <reason>. Force-target may change codec, encoder, container, bitrate, resolution, FPS and audio to fit. Use it? [y/N]
The word container appears only for directory destinations.
-ft/--force-target answers yes only to the force-target question.
Answer y or n. Enter and EOF mean no. Declining Conversion or source-cap
confirmation returns 1; declining force-target returns 4, with nothing published.
A prompt needs both stdin and stdout to be terminals; otherwise exit 2.
Dry-run prints resolved settings and each pending question as a warning.
It opens no TUI, encodes nothing, and writes no config or output.

Presets and precedence:
Preset fields: id, label (required, max 40 characters), description (max 120),
codec, container, encoder speed, quality or crf, audio_codec, audio_kbps, fps, resolution.
Null audio means source audio; null FPS and empty resolution mean source.
Hardware is not saved in presets and defaults to auto.
Factory preset 1: Default; AV1, MP4, speed 8 (measured SVT-AV1 default),
balanced (CRF 38), opus when re-encoding, source audio and FPS, 1080p maximum.
-p/--preset N with paths selects a saved slot, default 1. --preset-list lists them.
-p without paths opens the editor picker, with saved slots and New preset.
-p N without paths edits slot N, creating it if missing; confirmation alone saves.
N is a bare integer >=1. Put size before -p: in out 25 -p 2 means 25 MB with preset 2;
in out -p 25 means preset 25 without a size. Encoder speed uses -e/--enc-speed.
The editor and --preset-list cannot be combined with paths or processing flags.
First terminal processing use records preset 1 after checking paths.
Non-terminal first use and dry-run use the factory preset without saving.
Esc cancels with exit 1 and Ctrl+C returns 130; neither saves the draft.
Resolution order: explicit flags > --latest > selected preset > factory.
--latest or --setup with -p N is an error. A missing latest file falls back to the selected preset.
Explicit quality clears inherited CRF; explicit CRF clears inherited quality.
Recognized destination extensions fix the container; explicit -f must agree.
-a fixes audio bitrate. Size with explicit --crf or --quality is an error.
-ft or --vmaf without size is an error. No arguments and missing paths are errors.
Removed flags: -v/--verbose (progress is always shown),
-co/--conversion (omit size), --defaults (use --preset 1), --force (use -ft),
-ps/--use-preset (use -p N), --new-preset (use --preset).

Container compatibility (video / audio):
  mp4  .mp4       av1, hevc, h264, vp9, ffv1 / opus, aac, mp3, flac, ac3
  mkv  .mkv       all 7 video codecs / all 5 audio codecs
  webm .webm      av1, vp9 / opus
  mov  .mov, .qt  hevc, h264, prores, ffv1, theora / aac, mp3, ac3
  avi  .avi       all 7 video codecs / aac, mp3, flac, ac3
  ogg  .ogg, .ogv  theora / opus, flac
  ts   .ts        all 7 video codecs / all 5 audio codecs
  nut  .nut       all 7 video codecs / all 5 audio codecs
Aliases quicktime and mpegts normalize to mov and ts.
Video mismatch is an error with the accepted-codec hint; video is never swapped silently.
Incompatible inherited audio uses the first accepted of opus, aac, mp3, ac3 and warns
in yellow, for example: mov does not accept opus; using aac. Explicit -ac conflicts
are errors. Recommended containers: av1/hevc/h264 mp4, vp9 webm, prores mov,
ffv1 mkv, theora ogg. Containers were measured for muxing, not player compatibility.

Per-codec controls:
  av1     speed 0-13 or names; levels or CRF 1-63; size: 2-pass ABR; GPU: vulkan, qsv
  hevc    named speeds; levels or CRF 0-51; size: 2-pass ABR; GPU: vulkan, qsv
  h264    named speeds; levels or CRF 0-51; size: 2-pass ABR; GPU: vulkan, qsv
  vp9     cpu-used 0-8 or names; levels or CRF 0-63; size: 2-pass ABR; GPU: qsv
  prores  speed n/a; high=hq, balanced=standard, small=lt; Conversion only; GPU: vulkan
  ffv1    speed and quality n/a; lossless level 3; Conversion only; GPU: vulkan
  theora  speed n/a; high q:v 8, balanced 6, small 4; size: 2-pass ABR; GPU: none
--crf and -e with prores, ffv1 or theora are errors. Size with prores or ffv1 is an
error; -l with ffv1 is an error. An explicit codec change adapts inherited numeric
speed to a name when needed, and clears inherited CRF for codecs without CRF.
Explicit -e, -l and --crf are always validated, never discarded.
Unsupported GPU choices are dimmed and skipped. The hardware step test-encodes
each supported backend, shows checking... and caches results per codec.
Manual steps skip speed, quality or audio bitrate controls a codec does not support.

Audio policy:
Source audio is copied in Conversion when the container accepts its codec and,
with explicit -ac, only when it matches that codec. Otherwise encode with the
resolved audio codec at 128 kbps. Default copies compatible source audio
only if its bitrate uses at most 20% of the mux-adjusted budget per second.
Otherwise use the highest fitting bitrate in the 128/96/64/48 kbps ladder,
or 48 kbps if none fits the 20% share. Explicit -a always re-encodes at its
fixed bitrate. FLAC is lossless, ignores inherited kbps and rejects explicit -a.
In size mode, encode FLAC audio first and budget its exact byte count.
Preserve audio presence; force-target never drops audio.

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
Every explicit encoding field stays fixed; extension or -f locks the container.
With an unlocked directory destination, use the chosen codec's recommended container.
Select available lossy codecs in quality order av1, hevc, vp9, h264, theora;
theora is selected only for ogg. prores and ffv1 are never force candidates.
Inherited codecs, audio codec, bitrate, smaller resolution and FPS, audio bitrate,
and backend under hw=auto are adjustable. Use the first compatible audio codec
from opus, aac, mp3, ac3; FLAC only when explicit. A GPU trial under auto wins only
within 0.5 dB XPSNR or 1 VMAF point of the best software trial.
Never exceed source or preset caps, use a slower speed,
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
  Level       libx265 CRF  libx264 CRF  libsvtav1 CRF  VP9 CRF  Vulkan CQP  QSV ICQ
  high        26          21          30             24       26          26
  balanced    32          27          38             32       32          32
  small       36          31          46             40       36          36
Hardware quality mapping approximate until calibrated; hardware uses HEVC
values as quantizers (VP9 QSV uses VP9 values). --crf is raw software CRF:
h264/hevc 0-51, AV1 1-63, VP9 0-63.
Use --quality or --hw off with hardware; auto can select software for raw CRF.
Quality and CRF affect Conversion only; Default selects a bitrate.

Speed and resolution:
Named encoder speeds: ultrafast through placebo. AV1 names map to 12..3;
numeric SVT-AV1 presets are 0-13. VP9 names map to cpu-used 8..0.
QSV maps ultrafast/superfast to veryfast
and placebo to veryslow. Vulkan maps speeds to supported levels.
Resolution aliases: sd/360p=640x360, 480p=854x480, hd/720p=1280x720,
fullhd/fhd/1080p=1920x1080, qhd/2k/1440p=2560x1440,
uhd/4k/2160p=3840x2160. Custom WxH must be positive, even and <=16384.
FPS must be greater than 0 and at most 1000; audio kbps must be positive.

Units, paths and saves:
1 MB = 1,000,000 bytes. Public sizes changed from MiB (1,048,576 bytes).
Windows: %APPDATA%/pressurecooker, or ~/AppData/Roaming/pressurecooker.
POSIX: $XDG_CONFIG_HOME/pressurecooker, or ~/.config/pressurecooker.
presets.json and latest.json use schema version 4 and strict JSON, including audio_codec.
latest.json stores encoding fields plus hw, without label, description or size.
Version-1 Python, version-2 and version-3 settings migrate in memory with opus; their bytes
stay unchanged until a successful authorized save. Retired defaults.json is ignored.
Successful Default, Conversion and Setup runs save latest only after the
published file passes the post-publish probe. A successful -ft run saves the
force-target settings that produced the file. Dry-run, error, cancel and overshoot
never update latest.json. A latest save failure warns after publication.
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
FFmpeg and FFprobe are external executables. Software needs libx264, libx265,
libsvtav1, libvpx-vp9, prores_ks, ffv1 or libtheora for the chosen codec.
auto tries the codec's GPU backend then software; QSV is also explicitly selectable.
Encoder listings do not prove GPU support: live encodes probe it. Explicit
unsupported hardware combinations fail. Software decoding is used as needed.
Windows .bat/.cmd wrappers are unsupported. Use executable tool paths.
Output keeps one video and one audio stream; pixel format follows the codec
(ProRes 10-bit 4:2:2, FFV1 lossless format, otherwise 8-bit 4:2:0). MP4 uses +faststart.
WebM accepts AV1/VP9 and Opus. HDR, subtitles and additional streams are not preserved.
Source/destination aliases and existing outputs are rejected; --overwrite allows
destination replacement. Verified .part files are published atomically.
Directory names: Default <name>.fit<N>.<ext>; Conversion <name>.<codec>.<ext>.
Paths with spaces or Unicode must be passed as one argument.
Flag names are case-insensitive, --flag=value is accepted, and -ac, -ft, -ff,
-fp are whole flags. -- ends option parsing. A bare numeric second positional
without size is rejected; use ./20 to name that directory.
`
