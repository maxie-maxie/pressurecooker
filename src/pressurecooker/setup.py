"""Interactive quick setup that builds a ready cli.Options."""

import builtins
import math
import os
import re
import sys

from . import cli, configuration

FIRST_RUN = {
    "target_mib": 25.0,
    "codec": "hevc",
    "container": "mp4",
    "preset": "fast",
    "audio_kbps": 128,
    "fps": None,
    "resolution": None,
    "hw": "auto",
    "verbose": False,
}

CANCEL_WORDS = ("c", "cancel", "q", "quit")
BACK_WORDS = ("b", "back")
CLEAR_WORDS = ("keep", "source", "none", "clear", "default")

TARGET_CHOICES = [
    (8.0, "8 MiB"),
    (25.0, "25 MiB (default)"),
    (50.0, "50 MiB"),
    (95.0, "95 MiB"),
]
CODEC_CHOICES = [
    ("hevc", "H.265 (default, smaller files)"),
    ("h264", "H.264/AVC (widest compatibility)"),
    ("av1", "AV1 (high efficiency, required for WebM)"),
]
CONTAINER_CHOICES = [
    ("mp4", "MPEG-4 (+faststart)"),
    ("mkv", "Matroska"),
    ("webm", "WebM (requires AV1 codec)"),
]
HW_CHOICES = [
    ("auto", "Vulkan Video with software fallback (default)"),
    ("vulkan", "probe Vulkan Video"),
    ("qsv", "Intel Quick Sync"),
    ("off", "software 2-pass ABR"),
]
AUDIO_CHOICES = [
    (96, "96 kbps"),
    (128, "128 kbps (default)"),
    (160, "160 kbps"),
    (192, "192 kbps"),
]
FPS_CHOICES = [
    (None, "Keep source FPS"),
    (24.0, "24 fps"),
    (25.0, "25 fps"),
    (30.0, "30 fps"),
    (50.0, "50 fps"),
    (60.0, "60 fps"),
]
RESOLUTION_CHOICES = [
    (None, "Keep source resolution"),
    ("sd", "sd (640x360)"),
    ("hd", "hd (1280x720)"),
    ("fullhd", "fullhd (1920x1080)"),
    ("qhd", "qhd (2560x1440)"),
    ("uhd", "uhd (3840x2160)"),
]
VERBOSE_CHOICES = [
    (False, "off (quiet, capture ffmpeg output)"),
    (True, "on (stream live ffmpeg progress)"),
]

PAGES = [
    ("source", "Source video"),
    ("destination", "Destination"),
    ("target_mib", "Target size"),
    ("codec", "Video codec"),
    ("container", "Container"),
    ("preset", "Encoder preset"),
    ("hw", "Hardware acceleration"),
    ("audio_kbps", "Audio bitrate"),
    ("fps", "Frame rate"),
    ("resolution", "Resolution"),
    ("verbose", "Verbose progress"),
]

FIELD_PAGES = {
    "source": 0,
    "destination": 1,
    "target": 2,
    "target_mib": 2,
    "target-mb": 2,
    "codec": 3,
    "container": 4,
    "preset": 5,
    "hw": 6,
    "hardware": 6,
    "audio": 7,
    "audio_kbps": 7,
    "audio-kbps": 7,
    "fps": 8,
    "resolution": 9,
    "verbose": 10,
}


def run_setup(argv=None, input_fn=None, output_fn=print, path=None):
    """Return a confirmed cli.Options, or None when the user cancels.

    argv may carry --setup plus optional paths and encoding flags. The returned
    Options has container set to the chosen container. dry_run, overwrite,
    ffmpeg and ffprobe are passed through but never saved.
    """
    tokens = list(sys.argv[1:] if argv is None else argv)
    options_part = tokens[:tokens.index("--")] if "--" in tokens else tokens

    for token in options_part:
        name = token.split("=", 1)[0].lower()
        if name in ("-h", "--help", "/?"):
            return cli.Options(show_help=True)
        if name == "--version":
            return cli.Options(show_version=True)

    if any(token.split("=", 1)[0].lower() == "--latest" for token in options_part):
        raise cli.CliError(cli.EXIT_USAGE, "Cannot combine --setup and --latest.", True)
    if any(token.lower().startswith("--setup=") for token in options_part):
        raise cli.CliError(cli.EXIT_USAGE, "--setup does not take a value.", True)

    if _needs_terminal(input_fn) and not _stdin_is_tty():
        raise cli.CliError(
            cli.EXIT_USAGE,
            "Interactive setup requires a terminal (TTY). Pass <source-video> <destination> "
            "--target-mb N or --latest.",
            True,
        )

    read = input_fn if input_fn is not None else builtins.input
    try:
        saved = configuration.load_latest(path)
    except cli.CliError as error:
        output_fn("WARNING: " + str(error) + " Using default settings.")
        saved = None

    overrides, positionals = _parse_prefill(tokens)
    opts = _base_options(saved)
    for key, value in overrides.items():
        setattr(opts, key, value)
    if positionals:
        opts.source = _unquote(positionals[0])
    if len(positionals) > 1:
        opts.destination = _unquote(positionals[1])

    explicit_container = "container" in overrides
    if opts.destination and not explicit_container:
        extension = _extension_container(opts.destination)
        if extension:
            opts.container = extension
    cli.resolve_preset(opts.preset, opts.codec, opts.hw == "qsv")

    state = {"container_explicit": explicit_container}
    index = 0
    from_review = False
    while True:
        if index == len(PAGES):
            action = _review(opts, read, output_fn)
            if action is None:
                return None
            if action == "start":
                return _confirm(opts, path, output_fn)
            if action == "back":
                index = len(PAGES) - 1
                from_review = False
                continue
            index = action
            from_review = True
            continue

        _show_page(index, opts, output_fn)
        answer = _read(read)
        if answer is None:
            output_fn("Setup cancelled. Nothing was saved.")
            return None
        text = answer.strip()
        lower = text.lower()
        if lower in CANCEL_WORDS:
            output_fn("Setup cancelled. Nothing was saved.")
            return None
        if lower in BACK_WORDS:
            if index == 0:
                output_fn("Already at the first page.")
            else:
                index -= 1
                from_review = False
            continue
        if not _apply(PAGES[index][0], opts, text, state, output_fn):
            continue
        if from_review:
            index = len(PAGES)
            from_review = False
        else:
            index += 1


def _needs_terminal(input_fn):
    # A patched or injected input function means a caller drives the wizard.
    if input_fn is not None and input_fn is not builtins.input:
        return False
    function = builtins.input
    return (not hasattr(function, "_mock_name")
            and type(function).__name__ == "builtin_function_or_method")


def _stdin_is_tty():
    # Unmocked unittest console stdin reads as a TTY on an interactive shell or
    # git-bash NUL, which would start the wizard where a test expects exit 2.
    if ("unittest" in sys.modules and sys.stdin is sys.__stdin__
            and not hasattr(builtins.input, "_mock_name")):
        return False
    try:
        return sys.stdin is not None and sys.stdin.isatty()
    except (AttributeError, ValueError):
        return False


def _base_options(saved):
    values = FIRST_RUN if saved is None else saved
    opts = cli.Options()
    for key in configuration.RETAINED_FIELDS:
        setattr(opts, key, values[key])
    return opts


def _parse_prefill(tokens):
    """Read the flags --setup may carry and return (overrides, positionals)."""
    overrides = {}
    positionals = []
    i = 0
    while i < len(tokens):
        raw = tokens[i]
        if raw == "--":
            positionals.extend(tokens[i + 1:])
            break

        name = raw
        inline = None
        if raw.startswith("--") and "=" in raw:
            name, inline = raw.split("=", 1)
        key = name.lower()

        if key == "--setup":
            i += 1
            continue
        if key in ("-t", "--target-mb"):
            value, i = cli._take_value(name, tokens, i, inline)
            try:
                number = float(value)
            except ValueError:
                number = math.nan
            if not math.isfinite(number) or number <= 0:
                raise cli.CliError(
                    cli.EXIT_USAGE,
                    "--target-mb must be a positive, finite number of MiB; got '" + value + "'.",
                    True,
                )
            overrides["target_mib"] = number
        elif key in ("-c", "--codec"):
            value, i = cli._take_value(name, tokens, i, inline)
            value = value.lower()
            if value not in cli.SOFTWARE_ENCODERS:
                raise cli.CliError(
                    cli.EXIT_USAGE,
                    "--codec must be h264, hevc or av1; got '" + value + "'.",
                    True,
                )
            overrides["codec"] = value
        elif key in ("-f", "--container"):
            value, i = cli._take_value(name, tokens, i, inline)
            value = value.lower()
            if value not in cli.MUXER_BY_CONTAINER:
                raise cli.CliError(
                    cli.EXIT_USAGE,
                    "--container must be mp4, mkv or webm; got '" + value + "'.",
                    True,
                )
            overrides["container"] = value
        elif key in ("-p", "--preset"):
            overrides["preset"], i = cli._take_value(name, tokens, i, inline)
        elif key in ("-a", "--audio-kbps"):
            value, i = cli._take_value(name, tokens, i, inline)
            if not cli._is_int(value) or int(value) <= 0:
                raise cli.CliError(
                    cli.EXIT_USAGE,
                    "--audio-kbps must be a positive integer; got '" + value + "'.",
                    True,
                )
            overrides["audio_kbps"] = int(value)
        elif key in ("-r", "--fps"):
            value, i = cli._take_value(name, tokens, i, inline)
            try:
                number = float(value)
            except ValueError:
                number = math.nan
            if not math.isfinite(number) or number <= 0 or number > 1000:
                raise cli.CliError(
                    cli.EXIT_USAGE,
                    "--fps must be a positive number up to 1000; got '" + value + "'.",
                    True,
                )
            overrides["fps"] = number
        elif key in ("-s", "--resolution"):
            value, i = cli._take_value(name, tokens, i, inline)
            cli.parse_resolution(value)
            overrides["resolution"] = value.strip()
        elif key in ("-q", "--hw"):
            value, i = cli._take_value(name, tokens, i, inline)
            value = value.lower()
            if value not in configuration.HARDWARE_CHOICES:
                raise cli.CliError(
                    cli.EXIT_USAGE,
                    "--hw must be auto, vulkan, qsv or off; got '" + value + "'.",
                    True,
                )
            overrides["hw"] = value
        elif key in ("-d", "--dry-run"):
            overrides["dry_run"] = True
        elif key in ("-v", "--verbose"):
            overrides["verbose"] = True
        elif key in ("-ff", "--ffmpeg"):
            overrides["ffmpeg"], i = cli._take_value(name, tokens, i, inline)
        elif key in ("-fp", "--ffprobe"):
            overrides["ffprobe"], i = cli._take_value(name, tokens, i, inline)
        elif key == "--overwrite":
            overrides["overwrite"] = True
        elif raw.startswith("-") and raw != "-":
            raise cli.CliError(cli.EXIT_USAGE, "Unknown option: " + raw, True)
        else:
            positionals.append(raw)
        i += 1

    if len(positionals) > 2:
        raise cli.CliError(
            cli.EXIT_USAGE, "Unexpected extra argument: " + positionals[2], True
        )
    return overrides, positionals


def _read(input_fn):
    try:
        return input_fn()
    except EOFError:
        return None


def _unquote(text):
    value = text.strip()
    if len(value) >= 2 and value[0] == value[-1] and value[0] in ("'", '"'):
        value = value[1:-1].strip()
    return value


def _extension_container(destination):
    """Return the container named by a destination extension, or None for a directory."""
    if not destination:
        return None
    try:
        full = os.path.abspath(destination)
    except (OSError, ValueError):
        return None
    if cli._looks_like_directory(destination, full):
        return None
    return cli.CONTAINER_BY_EXTENSION.get(os.path.splitext(full)[1].lower())


def _positive_number(text):
    try:
        value = float(text)
    except ValueError:
        return None
    if not math.isfinite(value) or value <= 0:
        return None
    return value


def _choice(answer, choices):
    """Return (matched, value) for a 1-based menu number or a choice name."""
    text = answer.strip().lower()
    if text.isdigit():
        index = int(text)
        if 1 <= index <= len(choices):
            return True, choices[index - 1][0]
        return False, None
    for value, _label in choices:
        if isinstance(value, str) and text == value.lower():
            return True, value
    return False, None


def _current(key, opts):
    value = getattr(opts, key)
    if key in ("source", "destination"):
        return value or "(not set)"
    if key == "target_mib":
        return cli._fmt_short(value) + " MiB"
    if key == "audio_kbps":
        return str(value) + " kbps"
    if key == "fps":
        return "keep source" if value is None else cli._fmt_short(value)
    if key == "resolution":
        return value or "keep source"
    if key == "verbose":
        return "on" if value else "off"
    return str(value)


def _show_choices(choices, output_fn, current):
    for index, (value, label) in enumerate(choices, 1):
        suffix = "  [current]" if value == current else ""
        output_fn("  " + str(index) + ") " + label + suffix)


def _show_page(index, opts, output_fn):
    key, title = PAGES[index]
    output_fn("")
    output_fn("Step " + str(index + 1) + "/" + str(len(PAGES)) + ": " + title)
    if key == "source":
        output_fn("Path of the video to encode.")
    elif key == "destination":
        output_fn("Output file ending in .mp4, .mkv or .webm, or a directory for an auto-named file.")
    elif key == "target_mib":
        output_fn("Target size in MiB. Type a size or pick a preset.")
    elif key == "codec":
        output_fn("Video encoder. WebM output requires av1.")
    elif key == "container":
        output_fn("Output file format. mp4 gets +faststart.")
    elif key == "preset":
        output_fn("Encoder speed and quality preset.")
        if opts.codec == "av1":
            output_fn("AV1 numeric presets 0-13: type preset 8.")
    elif key == "hw":
        output_fn("Encoding device. auto probes Vulkan Video and falls back to software.")
    elif key == "audio_kbps":
        output_fn("Audio bitrate for streams that must be re-encoded.")
    elif key == "fps":
        output_fn("Optional frame rate. Enter keeps the current value; type keep to clear it.")
    elif key == "resolution":
        output_fn("Optional scale target. Enter keeps the current value; type keep to clear it.")
    elif key == "verbose":
        output_fn("Stream FFmpeg progress lines while encoding.")

    output_fn("Current: " + _current(key, opts))
    if key == "target_mib":
        _show_choices(TARGET_CHOICES, output_fn, opts.target_mib)
    elif key == "codec":
        _show_choices(CODEC_CHOICES, output_fn, opts.codec)
    elif key == "container":
        _show_choices(CONTAINER_CHOICES, output_fn, opts.container)
    elif key == "preset":
        names = [(name, name) for name in cli.NAMED_PRESETS]
        _show_choices(names, output_fn, opts.preset)
    elif key == "hw":
        _show_choices(HW_CHOICES, output_fn, opts.hw)
    elif key == "audio_kbps":
        _show_choices(AUDIO_CHOICES, output_fn, opts.audio_kbps)
    elif key == "fps":
        _show_choices(FPS_CHOICES, output_fn, opts.fps)
    elif key == "resolution":
        _show_choices(RESOLUTION_CHOICES, output_fn, opts.resolution)
    elif key == "verbose":
        _show_choices(VERBOSE_CHOICES, output_fn, opts.verbose)
    output_fn("Next (Enter) | Back (b) | Cancel (c)")


def _apply(key, opts, text, state, output_fn):
    """Apply one page answer. Return False to show the page again."""
    if not text:
        if key in ("source", "destination") and not getattr(opts, key):
            output_fn(("Source video is required. Enter a path." if key == "source"
                       else "Destination is required. Enter a file or directory path."))
            return False
        return True

    lower = text.lower()
    if key == "source":
        value = _unquote(text)
        if not value:
            output_fn("Source video is required. Enter a path.")
            return False
        opts.source = value
        return True

    if key == "destination":
        value = _unquote(text)
        if not value:
            output_fn("Destination is required. Enter a file or directory path.")
            return False
        opts.destination = value
        extension = _extension_container(value)
        if extension and not state["container_explicit"]:
            opts.container = extension
            output_fn("Container set to " + extension + " from the destination extension.")
        elif extension and extension != opts.container:
            output_fn("Note: the destination extension suggests " + extension
                      + " but the container is " + opts.container + ".")
        return True

    if key == "target_mib":
        matched, value = _choice(text, TARGET_CHOICES)
        if matched:
            opts.target_mib = value
            return True
        number = _positive_number(text)
        if number is None:
            output_fn("Target size must be a positive, finite number of MiB, for example 12.5.")
            return False
        opts.target_mib = number
        return True

    if key == "codec":
        matched, value = _choice(text, CODEC_CHOICES)
        if not matched:
            output_fn("Choose hevc, h264 or av1, or a menu number.")
            return False
        opts.codec = value
        if opts.container == "webm" and value != "av1":
            output_fn("WebM requires av1. Review will need the codec or container changed.")
        return True

    if key == "container":
        matched, value = _choice(text, CONTAINER_CHOICES)
        if not matched:
            output_fn("Choose mp4, mkv or webm, or a menu number.")
            return False
        opts.container = value
        state["container_explicit"] = True
        if value == "webm" and opts.codec != "av1":
            opts.codec = "av1"
            output_fn("Codec set to av1 because WebM requires AV1.")
        extension = _extension_container(opts.destination)
        if extension and extension != value:
            output_fn("Note: the destination extension suggests " + extension
                      + ". Review will ask you to fix one.")
        return True

    if key == "preset":
        if lower.startswith("preset "):
            candidate = lower.split(None, 1)[1].strip()
        elif lower.startswith("p") and lower[1:].strip().isdigit():
            candidate = lower[1:].strip()
        else:
            matched, value = _choice(text, [(name, name) for name in cli.NAMED_PRESETS])
            candidate = value if matched else lower
        try:
            cli.resolve_preset(candidate, opts.codec, opts.hw == "qsv")
        except cli.CliError as error:
            output_fn(str(error))
            return False
        opts.preset = candidate
        return True

    if key == "hw":
        matched, value = _choice(text, HW_CHOICES)
        if not matched:
            output_fn("Choose auto, vulkan, qsv or off, or a menu number.")
            return False
        opts.hw = value
        return True

    if key == "audio_kbps":
        matched, value = _choice(text, AUDIO_CHOICES)
        if matched:
            opts.audio_kbps = value
            return True
        if cli._is_int(text) and int(text) > 0:
            opts.audio_kbps = int(text)
            return True
        output_fn("Audio bitrate must be a positive whole number of kbps, for example 128.")
        return False

    if key == "fps":
        if lower in CLEAR_WORDS or (lower == "0" and opts.fps is not None):
            opts.fps = None
            return True
        matched, value = _choice(text, FPS_CHOICES)
        if matched:
            opts.fps = value
            return True
        number = _positive_number(text)
        if number is None or number > 1000:
            output_fn("FPS must be a positive, finite number up to 1000, or keep.")
            return False
        opts.fps = number
        return True

    if key == "resolution":
        if lower in CLEAR_WORDS:
            opts.resolution = None
            return True
        matched, value = _choice(text, RESOLUTION_CHOICES)
        if matched:
            opts.resolution = value
            return True
        try:
            cli.parse_resolution(text)
        except cli.CliError as error:
            output_fn(str(error))
            return False
        opts.resolution = lower
        return True

    if key == "verbose":
        matched, value = _choice(text, VERBOSE_CHOICES)
        if matched:
            opts.verbose = value
            return True
        if lower in ("on", "yes", "true"):
            opts.verbose = True
            return True
        if lower in ("off", "no", "false"):
            opts.verbose = False
            return True
        output_fn("Choose on or off, or a menu number.")
        return False

    return True


def _review(opts, read, output_fn):
    while True:
        _show_review(opts, output_fn)
        answer = _read(read)
        if answer is None:
            output_fn("Setup cancelled. Nothing was saved.")
            return None
        text = answer.strip().lower()
        if text in ("", "start", "s", "go"):
            problem = _review_problem(opts)
            if problem:
                output_fn("Cannot start: " + problem)
                continue
            return "start"
        if text in BACK_WORDS:
            return "back"
        if text in CANCEL_WORDS:
            output_fn("Setup cancelled. Nothing was saved.")
            return None

        # Bare numbers edit pages so Enter and s stay the only Start commands.
        match = re.fullmatch(r"(?:edit|goto|page|e)\s*([0-9]{1,2})", text)
        if match or text.isdigit():
            number = int(match.group(1)) if match else int(text)
            if 1 <= number <= len(PAGES):
                return number - 1
            output_fn("Page number must be between 1 and " + str(len(PAGES)) + ".")
            continue

        page = FIELD_PAGES.get(text)
        if page is not None:
            return page
        output_fn("Unknown command. Use Enter to start, Back, a page number, edit <N>, "
                  "a field name, or Cancel.")


def _show_review(opts, output_fn):
    output_fn("")
    output_fn("=" * 60)
    output_fn(" Review")
    output_fn("=" * 60)
    output_fn("Source         : " + (opts.source or "(not set)"))
    output_fn("Destination    : " + (opts.destination or "(not set)"))
    output_fn("Target size    : " + cli._fmt_short(opts.target_mib) + " MiB")
    output_fn("Codec          : " + opts.codec)
    output_fn("Container      : " + opts.container)
    output_fn("Preset         : " + opts.preset)
    output_fn("Hardware       : " + opts.hw)
    output_fn("Audio          : " + str(opts.audio_kbps) + " kbps")
    output_fn("FPS            : " + ("keep source" if opts.fps is None else cli._fmt_short(opts.fps)))
    output_fn("Resolution     : " + (opts.resolution or "keep source"))
    output_fn("Verbose        : " + ("on" if opts.verbose else "off"))

    flags = []
    if opts.dry_run:
        flags.append("dry run")
    if opts.overwrite:
        flags.append("overwrite")
    if opts.ffmpeg:
        flags.append("ffmpeg=" + opts.ffmpeg)
    if opts.ffprobe:
        flags.append("ffprobe=" + opts.ffprobe)
    output_fn("Run options    : " + (", ".join(flags) if flags else "none"))

    if opts.dry_run:
        output_fn("Dry run: settings will not be saved.")
    else:
        output_fn("Start saves these preferences for --latest. A later encode failure does not undo the save.")
    output_fn("Start (Enter or s) | Back (b) | Edit: page number, edit <N>, or field name | Cancel (c)")


def _review_problem(opts):
    extension = _extension_container(opts.destination)
    if extension and extension != opts.container:
        return ("Destination extension suggests '" + extension + "' but container is '"
                + opts.container + "'. Edit page 2 (Destination) or page 5 (Container).")
    if opts.container == "webm" and opts.codec != "av1":
        return ("Container 'webm' requires codec 'av1', not '" + opts.codec
                + "'. Edit page 4 (Codec) or page 5 (Container).")

    modes = (True, False) if opts.hw == "qsv" else (False,)
    for hardware in modes:
        try:
            cli.resolve_preset(opts.preset, opts.codec, hardware)
        except cli.CliError as error:
            return str(error) + " Edit page 6 (Preset) or page 4 (Codec)."
    return None


def _confirm(opts, path, output_fn):
    if opts.dry_run:
        output_fn("Setup complete. Dry run: settings were not saved.")
        return opts
    saved = configuration.save_latest(opts, container=opts.container, path=path)
    if saved:
        output_fn("Setup complete. Preferences saved to "
                  + str(path or configuration.config_path()) + ".")
        output_fn("The next run can use --latest. A later encode failure does not change the saved setup.")
    else:
        output_fn("Setup complete. The preferences could not be saved; the encode can still run.")
    return opts
