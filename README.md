# pressurecooker
Never have to pay for video upload limits again, **pressurecook** videos to your preferred size, **using FFmpeg**.

Uses Vulkan for GPU Detection, supports **NVIDIA NVENC, AMD VCE, Intel QuickSync**. Through a simple command-line interface, pressurecooker calculates a video and audio bitrate budget from the source duration. It reports the finished size and warns when the output exceeds the target. Encoder rate control and container overhead can cause the result to differ from the requested size.

hainan chicken
## Quick start

Install Python 3.10 or newer, [FFmpeg with ffprobe](#requirements), and [pipx](https://pipx.pypa.io/stable/installation/). Install the package from PyPI:

```text
pipx install pressurecooker-video
pipx ensurepath
```

Open a new terminal so PATH changes take effect, then encode a video with a 25 MiB target:

```text
pressurecooker video.mp4 output.mp4 -t 25
```

Replace `video.mp4` with your file. Use `pressurecooker --help` to list flags. These commands work on Windows and Linux. See [Installation](#installation) for virtual environments and native executables.

To avoid command-line flags, run the guided setup on an interactive terminal, then reuse your saved choices on the next file:

```text
pressurecooker --setup
pressurecooker --latest video.mp4 output.mp4
```

See [Quick setup](#quick-setup) for the full walkthrough and saved preferences.

## Contents

- [Requirements](#requirements)
- [Installation](#installation)
- [Release files](#release-files)
- [Usage](#usage)
- [Quick setup](#quick-setup)
- [Options](#options)
- [Encoding behavior](#encoding-behavior)
- [Compatibility](#compatibility)
- [Development](#development)
- [Publishing releases](#publishing-releases)
- [License](#license)

## Requirements

| Dependency | Required for |
| --- | --- |
| FFmpeg and ffprobe | Every installation; installed separately. |
| Python 3.10 or newer | Python package and portable executable. Native executables include Python. |
| GPU and driver with Vulkan Video encoding support | GPU encoding. Software encoding remains available. |

The CLI has no third-party Python runtime dependencies. The FFmpeg build must contain the selected encoder: `libx264`, `libx265`, or `libsvtav1` for software; `h264_vulkan`, `hevc_vulkan`, or `av1_vulkan` for GPU encoding. Check `ffmpeg -encoders` for the available encoders. Vulkan Video uses the GPU's video hardware through its driver; support depends on the device, driver, and codec. General Vulkan graphics support alone is insufficient.

Software AV1 requires an FFmpeg build with SVT-AV1 two-pass integration; older builds may include the encoder without supporting two passes.

Install FFmpeg with your system package manager, for example:

```bash
# Debian or Ubuntu
sudo apt install ffmpeg
```

```powershell
# Windows
winget install --id Gyan.FFmpeg --exact
```

Check that both commands resolve:

```text
ffmpeg -version
ffprobe -version
```

Use `--ffmpeg` and `--ffprobe` to provide explicit executable paths.

## Installation

The Python distribution is named `pressurecooker-video` and installs the `pressurecooker` command. With [pipx](https://pipx.pypa.io/stable/installation/) installed, install the release from [PyPI](https://pypi.org/project/pressurecooker-video/):

```text
pipx install pressurecooker-video
pipx ensurepath
```

Open a new terminal after `pipx ensurepath`, then run `pressurecooker --help`. FFmpeg and ffprobe must be installed separately. Source installations and release files are described below.

### Python package

For an installation inside a virtual environment, use pip instead of pipx.

Linux:

```bash
python3 -m venv .venv
source .venv/bin/activate
python -m pip install pressurecooker-video
pressurecooker --help
```

Windows PowerShell:

```powershell
py -m venv .venv
.\.venv\Scripts\python.exe -m pip install pressurecooker-video
.\.venv\Scripts\pressurecooker.exe --help
```

To install a downloaded wheel:

```text
pipx install ./pressurecooker_video-0.1.0-py3-none-any.whl
```

The wheel can also be installed with `python -m pip install` inside a virtual environment.

### Source checkout

To install from GitHub source, install Git and run:

```text
git clone https://github.com/maxie-maxie/pressurecooker.git
cd pressurecooker
pipx install .
```

For development, run `python -m pip install .` inside a virtual environment instead.

### Updating

Update an installation managed by pipx:

```text
pipx upgrade pressurecooker-video
```

For pip, run this inside the environment where the package is installed:

```text
python -m pip install --upgrade pressurecooker-video
```

For a native executable, download the new archive and replace the installed executable and its license directory.

### Native executable

Download the matching archive from [GitHub Releases](https://github.com/maxie-maxie/pressurecooker/releases/latest). Each native archive contains the executable and its `.licenses` directory. Keep both when redistributing it; Python is included, while FFmpeg and ffprobe are installed separately.

For Linux x64, extract `pressurecooker-0.1.0-linux64.tar.gz` into `dist/`, then run:

```bash
mkdir -p "$HOME/.local/bin" "$HOME/.local/share/pressurecooker/licenses"
install -m 755 dist/pressurecooker-0.1.0-linux64 "$HOME/.local/bin/pressurecooker"
cp -R dist/pressurecooker-0.1.0-linux64.licenses/. "$HOME/.local/share/pressurecooker/licenses/"
export PATH="$HOME/.local/bin:$PATH"
pressurecooker --version
```

Add the `export PATH` line to your shell's startup file to keep this setting.

Use the `linux32` files for a 32-bit Linux installation, replacing `linux64` in these commands.

For Windows x64, extract `pressurecooker-0.1.0-windows64.zip` into `dist/`, then run in PowerShell:

```powershell
$installDir = Join-Path $env:LOCALAPPDATA 'Programs\pressurecooker'
New-Item -ItemType Directory -Path $installDir -Force | Out-Null
Copy-Item -LiteralPath '.\dist\pressurecooker-0.1.0-windows64.exe' -Destination (Join-Path $installDir 'pressurecooker.exe')
Copy-Item -LiteralPath '.\dist\pressurecooker-0.1.0-windows64.licenses' -Destination (Join-Path $installDir 'licenses') -Recurse -Force
$userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
if (($userPath -split ';') -notcontains $installDir) {
    $newUserPath = if ($userPath) { $userPath.TrimEnd(';') + ';' + $installDir } else { $installDir }
    [Environment]::SetEnvironmentVariable('Path', $newUserPath, 'User')
}
if (($env:Path -split ';') -notcontains $installDir) {
    $env:Path = $env:Path.TrimEnd(';') + ';' + $installDir
}
pressurecooker --version
```

Use the `windows32` files for a 32-bit installation. The commands above rename the executable to `pressurecooker.exe` and update the user PATH without replacing existing entries.

### Portable executable

Download the `.pyz` file from [GitHub Releases](https://github.com/maxie-maxie/pressurecooker/releases/latest). It runs on a system with Python 3.10 or newer:

```text
python pressurecooker-0.1.0.pyz --help
```

On Linux, it can also be copied to a directory on PATH as `pressurecooker` and made executable with `chmod +x`. Its shebang uses `python3`.

## Release files

| File | Description |
| --- | --- |
| [pressurecooker_video-0.1.0-py3-none-any.whl](https://github.com/maxie-maxie/pressurecooker/releases/download/v0.1.0/pressurecooker_video-0.1.0-py3-none-any.whl) | Python package for either interpreter architecture. |
| [pressurecooker_video-0.1.0.tar.gz](https://github.com/maxie-maxie/pressurecooker/releases/download/v0.1.0/pressurecooker_video-0.1.0.tar.gz) | Source distribution, including tests and build scripts. |
| [pressurecooker-0.1.0.pyz](https://github.com/maxie-maxie/pressurecooker/releases/download/v0.1.0/pressurecooker-0.1.0.pyz) | Portable executable; requires Python. |
| [pressurecooker-0.1.0-windows64.zip](https://github.com/maxie-maxie/pressurecooker/releases/download/v0.1.0/pressurecooker-0.1.0-windows64.zip) | Windows x64 executable and license directory. |
| [pressurecooker-0.1.0-windows32.zip](https://github.com/maxie-maxie/pressurecooker/releases/download/v0.1.0/pressurecooker-0.1.0-windows32.zip) | Windows x86 executable and license directory. |
| [pressurecooker-0.1.0-linux64.tar.gz](https://github.com/maxie-maxie/pressurecooker/releases/download/v0.1.0/pressurecooker-0.1.0-linux64.tar.gz) | Linux x64 executable and license directory. |
| [pressurecooker-0.1.0-linux32.tar.gz](https://github.com/maxie-maxie/pressurecooker/releases/download/v0.1.0/pressurecooker-0.1.0-linux32.tar.gz) | Linux x86 executable and license directory. |
| [SHA256SUMS](https://github.com/maxie-maxie/pressurecooker/releases/download/v0.1.0/SHA256SUMS) | SHA256 hashes of the downloadable release files. |

FFmpeg and ffprobe are external dependencies for every release file. Native executables must be built on the target operating system with a matching interpreter architecture.

## Usage

```text
pressurecooker <source-video> <destination> --target-mb N [options]
```

Sizes use MiB; bitrates use kbps. The destination can be a directory or an output filename ending in `.mp4`, `.mkv`, or `.webm`. A directory receives `<sourceName>.fit<N>.<container-ext>`. A path without a recognized extension is treated as a directory.

Relative paths resolve against the current working directory. For example, from a Windows directory containing percent signs:

```powershell
Set-Location -LiteralPath 'D:\Rar%44211323%'
pressurecooker video.mp4 . -t 150
```

Encode H.264/AAC in MP4 for broad playback compatibility, including Discord clients that support that format:

```text
pressurecooker input.mov output.mp4 -t 25 -c h264 -s hd
```

Preview commands without creating output files or directories:

```text
pressurecooker video.mp4 output/ -t 50 --dry-run
```

## Quick setup

Quick setup walks through the encoding choices in the terminal, so you do not have to remember the flags. It is a line-based wizard: read each page, type a value or a menu number, and press Enter to continue. There is no arrow-key navigation.

**Unreleased.** `--setup` and `--latest` live in the repository source only. They are not in the published `v0.1.0` PyPI packages or GitHub Release assets, which remain at version `0.1.0` without quick setup.

### Entry points

Running `pressurecooker` with no arguments on an interactive terminal, or passing `--setup`, opens an 11-step setup wizard followed by a Review page:

```text
pressurecooker
pressurecooker --setup video.mp4 output.mp4
```

Source and destination paths (and normal encoding flags) after `--setup` prefill the matching pages. `--setup` and `--latest` cannot be combined and exit with code 2. On a non-interactive terminal, a no-argument or `--setup` call prints usage and exits with code 2 instead of waiting for input.

### Controls

| Input | Effect |
| --- | --- |
| Menu number or typed value | Sets the field. Typed values cover target size (MiB), FPS, audio bitrate (kbps) and a custom `WxH` resolution; codec, container, preset, hardware and verbose use numbered choices. |
| `Enter` | Keeps the current or default value and moves to the next step. |
| `b` or `back` | Returns to the previous step and keeps your draft edits. |
| `c`, `cancel`, `q`, `quit`, or end of input | Cancels without saving or encoding. |
| `Ctrl-C` | Exits with code 130 without saving. |

Clear an optional FPS or resolution back to the source default with `keep`, `source`, `none`, `clear`, `default`, or `0` for FPS when one is already set.

The Review page lists every choice and accepts `Start` (`Enter` or `s`), `Back` (`b`), `Cancel` (`c`), a jump to any step by page number, `edit <N>`, or a field name.

### Retained preferences and `--latest`

```text
pressurecooker --latest video.mp4 output.mp4
```

`--latest` loads the saved preferences and encodes without prompting. Flags given on the command line override the saved values, wherever they appear in the argument list. `--help` and `--version` neither read nor write configuration and never prompt.

### Configuration file

Preferences are stored as UTF-8 JSON (schema version 1) and written with an atomic file replacement:

| Platform | Path |
| --- | --- |
| Windows | `%APPDATA%\pressurecooker\latest.json` (fallback `~\AppData\Roaming\pressurecooker\latest.json`) |
| Linux | `$XDG_CONFIG_HOME/pressurecooker/latest.json` (fallback `~/.config/pressurecooker/latest.json`) |

Retained fields: `target_mib`, `codec`, `container`, `preset`, `audio_kbps`, `fps`, `resolution`, `hw`, `verbose`. Source and destination paths, `--overwrite`, `--dry-run`, `--ffmpeg` and `--ffprobe` are never saved or replayed.

### When preferences are saved

A run writes the file only when setup is confirmed at `Start` on a non-dry-run invocation (the save remains even if the encode that follows it fails), or after a successful ordinary non-dry-run encode. `--dry-run` never writes configuration.

A missing file is normal for `--setup`. `--latest` with no saved configuration exits with code 2 and points at `--setup`. A corrupt or incompatible file makes `--latest` fail with code 2 before any external tool runs, while `--setup` prints a warning and falls back to defaults until you confirm a new selection.

## Options

| Option | Default | Description |
| --- | --- | --- |
| `-t`, `--target-mb N` | Required | Positive, finite target size in MiB. |
| `-c`, `--codec C` | `hevc` | `h264`, `hevc`, or `av1`. |
| `-f`, `--container C` | Extension, then `mp4` | `mp4`, `mkv`, or `webm`; WebM requires AV1. |
| `-p`, `--preset P` | `fast` | Named encoder preset; AV1 also accepts 0-13. |
| `-a`, `--audio-kbps N` | `128` | Positive audio bitrate for re-encoding. |
| `-r`, `--fps N` | Source | Positive frame rate, up to 1000. |
| `-s`, `--resolution R` | Source | Resolution alias or `WxH`. |
| `-q`, `--hw M` | `auto` | `auto` or `vulkan`: detect Vulkan Video GPUs; `off`: software; `qsv`: legacy backend. |
| `-d`, `--dry-run` | Off | Print the plan and FFmpeg commands. |
| `-v`, `--verbose` | Off | Display FFmpeg progress. |
| `-ff`, `--ffmpeg PATH` | PATH | FFmpeg executable path. |
| `-fp`, `--ffprobe PATH` | PATH | ffprobe executable path. |
| `--setup` | | Interactive quick setup, then encode. Optional paths and encoding flags prefill the pages. |
| `--latest` | | Encode with saved preferences; command-line flags override them. |
| `--overwrite` | Off | Replace an existing destination after successful encoding. |
| `--version` | | Print the version. |
| `-h`, `--help`, `/?` | | Show help. |

An explicit container must agree with the destination's recognized extension. Use `--` before positional paths whose names begin with a hyphen.

## Encoding behavior

### Presets and scaling

Named presets are `ultrafast`, `superfast`, `veryfast`, `faster`, `fast`, `medium`, `slow`, `slower`, `veryslow`, and `placebo`. Software H.264 and HEVC use these names directly. For SVT-AV1 they map to 12, 11, 10, 9, 8, 7, 6, 5, 4, and 3 respectively.

Vulkan uses FFmpeg's `-quality` option. `fast` and faster presets map to level 0; `medium`, `slow`, `slower`, `veryslow`, and `placebo` request levels 1 through 5. AV1 numeric presets map to `max(0, 8 - N)`. The requested level is capped at the GPU's highest supported level. Higher levels trade speed for quality, with the available levels defined by the driver. The encoding plan reports the selected level and GPU.

Resolution aliases are `sd`/`360p` (640x360), `hd`/`720p` (1280x720), `fullhd`/`1080p` (1920x1080), `qhd`/`1440p` (2560x1440), and `uhd`/`2160p` (3840x2160). Explicit dimensions round down to even numbers. Requests exceeding either source dimension keep the original resolution. Frame rate stays unchanged unless requested.

### Audio and size budget

The first video and first audio streams are used. Compatible AAC audio in MP4/MKV, or Opus in WebM, is copied when its bitrate is known and at most 160 kbps. Other audio is re-encoded as AAC or Opus, with a minimum of 48 kbps and a cap based on 15% of the total budget. Video-only sources reserve no audio budget.

```text
total_kbps = target_MiB * 8388.608 / duration
video_kbps = floor((total_kbps - audio_kbps) * 0.97)
```

A video budget below 50 kbps is rejected. Software encoding uses two passes. GPU encoding uses one pass with an additional 0.96 bitrate factor. MP4 output includes `faststart`.

The default `auto` mode probes the GPUs listed by FFmpeg and selects the first that can encode the chosen codec through Vulkan Video. If none succeeds, it selects the corresponding software encoder and prints the reason. `--hw vulkan` follows the same selection and fallback policy. `--hw off` skips GPU probes.

With Vulkan encoding, a short decode-and-encode probe checks the source on the selected GPU. Supported sources use Vulkan decoding and scaling. If that probe fails, encoding stays on the GPU while decoding and scaling use software. Both probe types discard their output. HEVC and AV1 playback depends on the receiving application and device.

### Output handling and exit codes

Existing output is refused unless `--overwrite` is set. Source files and their symlink or hardlink aliases cannot be destinations. Encoding uses unique temporary output and statistics files; output is finalized after success, and failures or Ctrl-C remove temporary files. A dry run creates no files or directories; GPU probes write to the null device.

| Exit code | Meaning |
| --- | --- |
| 0 | Success. |
| 2 | Input or usage error. |
| 3 | Missing FFmpeg or ffprobe. |
| 4 | Target too small for the duration. |
| 5 | Encoding failure. |
| 130 | Interrupted. |

Running without arguments opens quick setup on an interactive terminal. When standard input is not a terminal, it prints usage and exits with code 2. See [Quick setup](#quick-setup).

## Compatibility

The Python package and portable executable require Python 3.10 or newer and a compatible FFmpeg installation. A universal wheel does not establish that every platform has been tested. Native Linux builds also depend on the target system's C library.

Windows x64, Windows x86, Linux x64, and Linux x86 build routes are tested in GitHub Actions. Linux x86 uses Debian Bookworm i386 in a chroot on an x64 runner; CI verifies 32-bit Python, runs the test suite, and starts the built executable. Local Linux x86 execution is unavailable on the WSL 1 environment used for release preparation.

## Development

Install build tools and run checks from the repository root:

```text
python -m pip install build ".[build]"
python -m unittest discover -s tests -v
python -m build
python scripts/build_portable.py
python scripts/build_binary.py
python scripts/package_release.py
```

Outputs are written to `dist/`. Native builds use the operating system and pointer width of the Python interpreter running the script. A 32-bit build requires a 32-bit interpreter. Build Linux executables on the oldest Linux distribution you intend to support.

The native builder discovers CPython and PyInstaller licenses from the installed runtime and distribution metadata. For a custom Python installation, pass `--python-license PATH`. Supplemental dependency notices are supplied for Windows CPython 3.12.10/3.14.4 and discovered from Debian/Ubuntu system packages. Other runtimes require `--runtime-license-dir PATH`. Missing license files stop the build. `--clean` removes the repository's build outputs before rebuilding.

## Publishing releases

Version `0.1.0` is available on [PyPI](https://pypi.org/project/pressurecooker-video/) and [GitHub Releases](https://github.com/maxie-maxie/pressurecooker/releases/tag/v0.1.0). Publishing a new GitHub release runs [publish-pypi.yml](.github/workflows/publish-pypi.yml), which builds, checks, tests, and uploads the Python packages through [PyPI Trusted Publishing](https://docs.pypi.org/trusted-publishers/).

You choose when to publish by creating a GitHub release. Drafts and edits to an existing release do not upload to PyPI. The separate [build-binaries.yml](.github/workflows/build-binaries.yml) workflow builds all four native variants on demand.

### One-time setup

This repository's publisher and `pypi` environment are already configured. For another repository, create a PyPI account, verify its email address, enable two-factor authentication, and add a publisher under [Publishing](https://pypi.org/manage/account/publishing/). The registered values for this repository are:

| Field | Value |
| --- | --- |
| PyPI project name | `pressurecooker-video` |
| Owner | `maxie-maxie` |
| Repository | `pressurecooker` |
| Workflow filename | `publish-pypi.yml` |
| Environment | `pypi` |

For a new PyPI project, add a pending publisher; for an existing project, use its Publishing settings. Authentication uses GitHub's identity token, so the workflow needs no API token or repository secret.

### Prepare the next version

Start from an up-to-date checkout of `main`. The examples below use `0.1.1`; replace it with the version you intend to publish. Update `version` in `pyproject.toml` and `__version__` in `src/pressurecooker/__init__.py` to the same value. Native and portable build filenames use the source version automatically.

From the repository root, with a Python virtual environment active, run:

```text
python -m pip install --upgrade build twine ".[build]"
python -m unittest discover -s tests -v
python -m build --outdir dist/pypi/0.1.1
python -m twine check --strict "dist/pypi/0.1.1/*"
```

Use a new output directory for each version so an upload contains only that version's wheel and source archive. Commit the version changes along with any code or documentation changes, then push and tag:

```text
git add pyproject.toml src/pressurecooker/__init__.py
git commit -m "chore: release v0.1.1"
git push origin main
git tag -a v0.1.1 -m "v0.1.1"
git push origin v0.1.1
```

Wait for the [test workflow](https://github.com/maxie-maxie/pressurecooker/actions/workflows/tests.yml) to pass before publishing.

### Publish on GitHub and PyPI

Build native release files from the tag:

```text
gh workflow run build-binaries.yml --ref v0.1.1
```

Open the [binary-build run](https://github.com/maxie-maxie/pressurecooker/actions/workflows/build-binaries.yml), wait for all four jobs to pass, and download its artifacts. Each contains a native archive with license notices; the Linux x64 artifact also contains the Python packages and portable executable.

Write the release notes in `RELEASE_NOTES.md`, then publish the tagged GitHub release:

```text
gh release create v0.1.1 --verify-tag --title "v0.1.1" --notes-file RELEASE_NOTES.md
```

This starts the PyPI publishing workflow. Check its result on the [Actions page](https://github.com/maxie-maxie/pressurecooker/actions/workflows/publish-pypi.yml), then confirm the version on [PyPI](https://pypi.org/project/pressurecooker-video/).

Attach the downloaded archives through GitHub's **Edit release** page. The CLI can also upload files; for example, from a directory containing two downloaded archives:

```text
gh release upload v0.1.1 pressurecooker-0.1.1-windows64.zip pressurecooker-0.1.1-linux64.tar.gz
```

Include the other platform archives, portable executable, wheel, and source archive as appropriate. For a local build, `python scripts/build_binary.py` builds the current platform, `python scripts/build_portable.py` builds the portable executable, and `python scripts/package_release.py` creates native archives and a checksum manifest in `dist/`.

Attaching files to an existing release does not start another PyPI upload.

### Upload directly to PyPI

To publish from your computer instead of GitHub Actions, create a PyPI API token scoped to `pressurecooker-video` in your [account settings](https://pypi.org/manage/account/#api-tokens). After the preparation checks, run:

```text
python -m twine upload --username __token__ "dist/pypi/0.1.1/*"
```

Paste the token when Twine prompts for the password. Keep tokens out of Git and release notes.

If Twine has already published this version, prevent the GitHub release from uploading it again:

```text
gh workflow disable publish-pypi.yml
gh release create v0.1.1 --verify-tag --title "v0.1.1" --notes-file RELEASE_NOTES.md
gh workflow enable publish-pypi.yml
```

Re-enable the workflow even if release creation fails. Upload the GitHub assets as described above. This sequence is only needed for a version uploaded directly with Twine; the normal GitHub release procedure publishes to both services.

### Check publishing without uploading

To check the workflow without uploading, run it manually from the Actions page or use:

```text
gh workflow run publish-pypi.yml --ref main
```

Manual workflow runs build and test the packages, then skip publishing. PyPI versions cannot be replaced, even after deletion; use a new version for each later release. Keep published Git tags attached to their original commits.

## License

pressurecooker is distributed under the [MIT License](LICENSE). Native executables include CPython and the PyInstaller bootloader; their license texts accompany each build. FFmpeg and its encoders are installed and licensed separately. See [Third-party notices](THIRD_PARTY_NOTICES.md).
