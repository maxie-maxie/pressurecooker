"""Retain the last confirmed encoding preferences in a per-user JSON file."""

import json
import math
import os
import tempfile

CONFIG_VERSION = 1
RETAINED_FIELDS = ("target_mib", "codec", "container", "preset", "audio_kbps",
                   "fps", "resolution", "hw", "verbose")
HARDWARE_CHOICES = ("auto", "vulkan", "qsv", "off")
HELP_FLAGS = ("-h", "--help", "/?", "--version")
MISSING_CONFIG = "No saved configuration found. Run 'pressurecooker --setup' first."


def _cli():
    # Importing per call lets cli.py import this module for --latest routing
    # without the two files depending on each other at import time.
    from . import cli
    return cli


def _flag_name(token):
    """Return the option part of an argv token, lowercased the way parse_args does."""
    return token.split("=", 1)[0].lower()


def _split_at_end_of_options(tokens):
    """Split argv into option tokens and the filenames after the end-of-options marker."""
    cut = tokens.index("--") if "--" in tokens else len(tokens)
    return tokens[:cut], tokens[cut:]


def config_path():
    """Return the per-user file holding the latest confirmed preferences."""
    if os.name == "nt":
        base = os.environ.get("APPDATA") or os.path.join(os.path.expanduser("~"), "AppData", "Roaming")
    else:
        base = os.environ.get("XDG_CONFIG_HOME") or os.path.join(os.path.expanduser("~"), ".config")
    return os.path.join(base, "pressurecooker", "latest.json")


def _invalid(field, problem):
    """Report a rejected retained value and point at the way to replace it."""
    cli = _cli()
    return cli.CliError(cli.EXIT_USAGE, "Saved configuration is invalid: " + field + " " + problem
                        + ". Run 'pressurecooker --setup' to replace it.")


def _number(value):
    """Read a JSON number. Booleans are ints in Python, so refuse them first."""
    if isinstance(value, bool) or not isinstance(value, (int, float)):
        return None
    number = float(value)
    return number if math.isfinite(number) else None


def validate_settings(settings):
    """Check retained settings against the CLI's own rules and return the nine fields."""
    cli = _cli()
    if not isinstance(settings, dict):
        raise _invalid("content", "must be a JSON object")

    unexpected = sorted(key for key in settings if key != "version" and key not in RETAINED_FIELDS)
    if unexpected:
        raise _invalid("keys", "are not recognized: " + ", ".join(unexpected))
    missing = [key for key in RETAINED_FIELDS if key not in settings]
    if missing:
        raise _invalid("keys", "are missing: " + ", ".join(missing))
    version = settings.get("version", CONFIG_VERSION)
    if isinstance(version, bool) or version != CONFIG_VERSION:
        raise _invalid("version", "must be " + str(CONFIG_VERSION) + ", got " + repr(version))

    target_mib = _number(settings["target_mib"])
    if target_mib is None or target_mib <= 0:
        raise _invalid("target_mib", "must be a positive, finite number of MiB")

    codec = settings["codec"]
    if not isinstance(codec, str) or codec not in cli.SOFTWARE_ENCODERS:
        raise _invalid("codec", "must be h264, hevc or av1")
    container = settings["container"]
    if not isinstance(container, str) or container not in cli.MUXER_BY_CONTAINER:
        raise _invalid("container", "must be mp4, mkv or webm")
    if container == "webm" and codec != "av1":
        raise _invalid("container", "webm requires codec av1, got " + codec)
    hw = settings["hw"]
    if not isinstance(hw, str) or hw not in HARDWARE_CHOICES:
        raise _invalid("hw", "must be auto, vulkan, qsv or off")

    preset = settings["preset"]
    if not isinstance(preset, str):
        raise _invalid("preset", "must be text")
    # Numeric presets mean SVT-AV1 levels, so they are wrong for every other codec.
    cli.resolve_preset(preset, codec, False)
    if hw == "qsv":
        cli.resolve_preset(preset, codec, True)

    audio_kbps = settings["audio_kbps"]
    if isinstance(audio_kbps, bool) or not isinstance(audio_kbps, int) or audio_kbps <= 0:
        raise _invalid("audio_kbps", "must be a positive integer")

    fps = settings["fps"]
    if fps is not None:
        fps = _number(fps)
        if fps is None or fps <= 0 or fps > 1000:
            raise _invalid("fps", "must be a positive number up to 1000")

    resolution = settings["resolution"]
    if resolution is not None:
        if not isinstance(resolution, str):
            raise _invalid("resolution", "must be text or null")
        cli.parse_resolution(resolution)

    verbose = settings["verbose"]
    if not isinstance(verbose, bool):
        raise _invalid("verbose", "must be true or false")

    return {"target_mib": target_mib, "codec": codec, "container": container, "preset": preset,
            "audio_kbps": audio_kbps, "fps": fps, "resolution": resolution, "hw": hw,
            "verbose": verbose}


def load_latest(path=None):
    """Return the retained settings, or None when no configuration has been saved yet."""
    cli = _cli()
    target = str(path or config_path())
    try:
        with open(target, "r", encoding="utf-8") as handle:
            payload = json.load(handle)
    except FileNotFoundError:
        return None
    except (OSError, ValueError) as error:
        raise cli.CliError(cli.EXIT_USAGE, "Saved configuration at " + target
                           + " is unreadable or corrupt: " + str(error)
                           + ". Run 'pressurecooker --setup' to replace it.")
    if not isinstance(payload, dict):
        raise cli.CliError(cli.EXIT_USAGE, "Saved configuration at " + target
                           + " does not hold a JSON object."
                           + " Run 'pressurecooker --setup' to replace it.")
    version = payload.get("version")
    if isinstance(version, bool) or version != CONFIG_VERSION:
        raise _invalid("version", "must be " + str(CONFIG_VERSION) + ", got " + repr(version))
    return validate_settings(payload)


def _extension_container(destination):
    """Match resolve_destination so a saved container never fights an explicit extension."""
    cli = _cli()
    if not destination:
        return None
    try:
        full = os.path.abspath(destination)
    except (OSError, ValueError):
        return None
    if cli._looks_like_directory(destination, full):
        return None
    return cli.CONTAINER_BY_EXTENSION.get(os.path.splitext(full)[1].lower())


def _settings_from(opts, container):
    """Read the retained fields off an Options object or a plain dict."""
    read = opts.get if isinstance(opts, dict) else lambda key: getattr(opts, key, None)
    settings = {key: read(key) for key in RETAINED_FIELDS}
    if container is not None:
        settings["container"] = container
    elif not settings["container"]:
        settings["container"] = _extension_container(read("destination")) or "mp4"
    return settings


def save_latest(opts, container=None, path=None):
    """Atomically write the retained settings. Returns False after a save warning."""
    payload = {"version": CONFIG_VERSION}
    payload.update(validate_settings(_settings_from(opts, container)))
    target = str(path or config_path())
    directory = os.path.dirname(target)
    temp_path = None
    try:
        if directory:
            os.makedirs(directory, exist_ok=True)
        fd, temp_path = tempfile.mkstemp(dir=directory, prefix=".latest-", suffix=".tmp")
        with os.fdopen(fd, "w", encoding="utf-8", newline="\n") as handle:
            json.dump(payload, handle, indent=2)
            handle.write("\n")
        # A failed replace leaves the previous file alone, which is the point of the temp file.
        os.replace(temp_path, target)
        temp_path = None
    except OSError as error:
        print("WARNING: could not save the configuration to " + target + ": " + str(error)
              + " The encode result is unaffected.")
        return False
    finally:
        if temp_path and os.path.exists(temp_path):
            try:
                os.unlink(temp_path)
            except OSError:
                pass
    return True


def apply_latest(argv, saved=None, path=None):
    """Build Options from saved preferences with the explicit argv flags on top."""
    cli = _cli()
    tokens = list(argv)
    options_part, file_part = _split_at_end_of_options(tokens)
    explicit = [token for token in options_part if token != "--latest"]
    if any(_flag_name(token) in HELP_FLAGS for token in explicit):
        # Help and version never read or write the configuration.
        return cli.parse_args(explicit + file_part)

    settings = saved if saved is not None else load_latest(path)
    if settings is None:
        raise cli.CliError(cli.EXIT_USAGE, MISSING_CONFIG)
    settings = validate_settings(settings)

    defaults = ["-t", str(settings["target_mib"]), "-c", settings["codec"],
                "-p", settings["preset"], "-a", str(settings["audio_kbps"]), "-q", settings["hw"]]
    if settings["fps"] is not None:
        defaults += ["-r", str(settings["fps"])]
    if settings["resolution"] is not None:
        defaults += ["-s", settings["resolution"]]
    if settings["verbose"]:
        defaults.append("-v")
    # parse_args keeps the last value it sees, so saved values go first.
    opts = cli.parse_args(defaults + explicit + file_part)

    container_requested = any(_flag_name(token) in ("-f", "--container") for token in explicit)
    if not container_requested and not _extension_container(opts.destination):
        opts.container = settings["container"]

    cli.resolve_preset(opts.preset, opts.codec, opts.hw == "qsv")
    return opts
