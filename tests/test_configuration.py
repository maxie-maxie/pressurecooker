"""Behavioural and regression tests for retained configuration and --latest.

These tests exercise configuration.py and the parts of cli.py that keep or
replay preferences. Every case points the resolved config at a throwaway
directory: wizard and merge helpers take an explicit ``path=``, and the
cli.main integration tests set APPDATA/XDG_CONFIG_HOME in setUp, so the real
user profile is never read or written and no external tool is launched.
"""

import contextlib
import io
import json
import os
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

from pressurecooker import cli, configuration

RETAINED = ["target_mib", "codec", "container", "preset", "audio_kbps",
            "fps", "resolution", "hw", "verbose"]


def settings(**overrides):
    base = {"target_mib": 25.0, "codec": "hevc", "container": "mp4", "preset": "fast",
            "audio_kbps": 128, "fps": None, "resolution": None, "hw": "auto", "verbose": False}
    base.update(overrides)
    return base


def silence(function, *args, **kwargs):
    buffer = io.StringIO()
    with contextlib.redirect_stdout(buffer):
        result = function(*args, **kwargs)
    return result, buffer.getvalue()


class ConfigTempHome(unittest.TestCase):
    """Redirect config_path() at a temp directory so the real profile is safe."""

    def setUp(self):
        self.home = tempfile.TemporaryDirectory(prefix="pc-config-home-")
        self.addCleanup(self.home.cleanup)
        env = patch.dict(os.environ, {"APPDATA": self.home.name,
                                      "XDG_CONFIG_HOME": os.path.join(self.home.name, "xdg")})
        env.start()
        self.addCleanup(env.stop)
        self.config = Path(configuration.config_path())
        root = Path(self.home.name).resolve()
        self.assertTrue(str(self.config).lower().startswith(str(root).lower()),
                        "config path must stay inside the temp home")


class ConfigPathTests(unittest.TestCase):
    def test_windows_uses_appdata_with_a_user_fallback(self):
        with patch.object(os, "name", "nt"), patch.dict(os.environ, {"APPDATA": "/tmp/pc-appdata"}):
            self.assertEqual(configuration.config_path(),
                             os.path.join("/tmp/pc-appdata", "pressurecooker", "latest.json"))
        with patch.object(os, "name", "nt"), patch.dict(os.environ, {}, clear=True):
            expected = os.path.join(os.path.expanduser("~"), "AppData", "Roaming",
                                    "pressurecooker", "latest.json")
            self.assertEqual(configuration.config_path(), expected)

    def test_posix_uses_xdg_config_home_with_a_user_fallback(self):
        with patch.object(os, "name", "posix"), patch.dict(os.environ, {"XDG_CONFIG_HOME": "/tmp/pc-xdg"}):
            self.assertEqual(configuration.config_path(),
                             os.path.join("/tmp/pc-xdg", "pressurecooker", "latest.json"))
        with patch.object(os, "name", "posix"), patch.dict(os.environ, {}, clear=True):
            self.assertEqual(configuration.config_path(),
                             os.path.join(os.path.expanduser("~"), ".config",
                                          "pressurecooker", "latest.json"))

    def test_an_explicit_path_is_honoured_and_isolated(self):
        with tempfile.TemporaryDirectory() as directory:
            config = Path(directory, "latest.json")
            self.assertTrue(configuration.save_latest(cli.Options(target_mib=30.0), path=str(config)))
            self.assertTrue(config.is_file())


class SaveLoadTests(unittest.TestCase):
    def test_round_trip_with_defaults(self):
        with tempfile.TemporaryDirectory() as directory:
            config = str(Path(directory, "latest.json"))
            opts = cli.Options(source="a.mp4", destination="b.mp4", target_mib=25.0)
            self.assertTrue(configuration.save_latest(opts, path=config))
            self.assertEqual(configuration.load_latest(config), settings())

    def test_round_trip_with_optional_values(self):
        with tempfile.TemporaryDirectory() as directory:
            config = str(Path(directory, "latest.json"))
            opts = cli.Options(destination="b.mkv", target_mib=8.5, codec="av1", container="mkv",
                               preset="slow", audio_kbps=96, fps=29.97, resolution="hd",
                               hw="qsv", verbose=True, overwrite=True, dry_run=True,
                               ffmpeg="C:/ffmpeg.exe", ffprobe="C:/ffprobe.exe")
            self.assertTrue(configuration.save_latest(opts, container="mkv", path=config))
            self.assertEqual(configuration.load_latest(config),
                             settings(target_mib=8.5, codec="av1", container="mkv", preset="slow",
                                      audio_kbps=96, fps=29.97, resolution="hd", hw="qsv",
                                      verbose=True))

    def test_container_comes_from_the_destination_when_unstated(self):
        with tempfile.TemporaryDirectory() as directory:
            config = str(Path(directory, "latest.json"))
            cases = (("out.webm", "webm"), ("out.mkv", "mkv"), ("out.mp4", "mp4"),
                     (directory, "mp4"), ("out.avi", "mp4"))
            for destination, expected in cases:
                with self.subTest(destination=destination):
                    codec = "av1" if expected == "webm" else "hevc"
                    opts = cli.Options(destination=destination, target_mib=10.0, codec=codec)
                    configuration.save_latest(opts, path=config)
                    self.assertEqual(configuration.load_latest(config)["container"], expected)

    def test_saved_json_holds_only_the_allowlisted_fields(self):
        with tempfile.TemporaryDirectory() as directory:
            config = str(Path(directory, "latest.json"))
            opts = cli.Options(source="secret-source.mp4", destination="C:/Videos/out.mp4",
                               target_mib=40.0, codec="av1", container="mp4", preset="medium",
                               audio_kbps=192, fps=50.0, resolution="1920x1080", hw="off",
                               verbose=True, dry_run=True, overwrite=True,
                               ffmpeg="/bin/ffmpeg", ffprobe="/bin/ffprobe",
                               show_help=True, show_version=True)
            configuration.save_latest(opts, path=config)
            payload = json.loads(Path(config).read_text(encoding="utf-8"))
            self.assertEqual(sorted(payload), sorted(RETAINED + ["version"]))
            self.assertEqual(payload["version"], 1)
            for forbidden in ("source", "destination", "ffmpeg", "ffprobe", "overwrite",
                              "dry_run", "dry-run", "show_help", "show_version", "setup",
                              "latest", "path"):
                self.assertNotIn(forbidden, payload)
            self.assertNotIn("secret-source.mp4", Path(config).read_text(encoding="utf-8"))

    def test_a_dict_of_settings_is_accepted(self):
        with tempfile.TemporaryDirectory() as directory:
            config = str(Path(directory, "latest.json"))
            self.assertTrue(configuration.save_latest(settings(target_mib=12.0), path=config))
            self.assertEqual(configuration.load_latest(config)["target_mib"], 12.0)

    def test_saving_rejects_invalid_settings_without_writing(self):
        with tempfile.TemporaryDirectory() as directory:
            config = str(Path(directory, "latest.json"))
            with self.assertRaises(cli.CliError) as caught:
                configuration.save_latest(settings(fps=2000.0), path=config)
            self.assertEqual(caught.exception.code, 2)
            self.assertFalse(Path(config).exists())

    def test_load_returns_none_for_an_absent_file(self):
        with tempfile.TemporaryDirectory() as directory:
            self.assertIsNone(configuration.load_latest(str(Path(directory, "gone.json"))))

    def test_load_reports_corrupt_or_unsupported_files(self):
        cases = [("", "empty file"), ("{oops", "broken json"), ("[]", "json array"),
                 ('"text"', "json string"), ("null", "json null"), ('{"version": 2}', "newer schema"),
                 ('{"target_mib": 25.0}', "no version"),
                 ('{"version": true, "target_mib": 25.0}', "boolean version"),
                 ('{"version": "1", "target_mib": 25.0}', "string version")]
        with tempfile.TemporaryDirectory() as directory:
            for index, (text, label) in enumerate(cases):
                config = str(Path(directory, "latest-%d.json" % index))
                Path(config).write_text(text, encoding="utf-8")
                with self.subTest(label=label):
                    with self.assertRaises(cli.CliError) as caught:
                        configuration.load_latest(config)
                    self.assertEqual(caught.exception.code, 2)

    def test_non_finite_numbers_are_rejected_after_reading(self):
        texts = ('{"version": 1, "target_mib": NaN}',
                 '{"version": 1, "target_mib": Infinity}',
                 '{"version": 1, "target_mib": 5.0, "codec": "hevc", "container": "mp4",'
                 ' "preset": "fast", "audio_kbps": 128, "fps": Infinity, "resolution": null,'
                 ' "hw": "auto", "verbose": false}')
        with tempfile.TemporaryDirectory() as directory:
            for text in texts:
                with self.subTest(text=text[:32]):
                    config = str(Path(directory, "latest.json"))
                    Path(config).write_text(text, encoding="utf-8")
                    with self.assertRaises(cli.CliError) as caught:
                        configuration.load_latest(config)
                    self.assertEqual(caught.exception.code, 2)

    def test_loading_runs_no_external_tool(self):
        with tempfile.TemporaryDirectory() as directory:
            config = str(Path(directory, "latest.json"))
            configuration.save_latest(cli.Options(target_mib=20.0), path=config)
            with patch.object(cli.subprocess, "run", side_effect=AssertionError):
                self.assertIsNotNone(configuration.load_latest(config))
                configuration.validate_settings(settings())


class ValidationTests(unittest.TestCase):
    def assert_rejected(self, cases):
        for overrides, _problem in cases:
            with self.subTest(problem=overrides):
                with self.assertRaises(cli.CliError) as caught:
                    configuration.validate_settings(settings(**overrides))
                self.assertEqual(caught.exception.code, 2)

    def test_bad_schema_keys(self):
        self.assert_rejected([({"unknown": 1}, "unexpected key"),
                              ({"source": "a.mp4"}, "operational key"),
                              ({"destination": "b.mp4"}, "operational key"),
                              ({"version": 3}, "unsupported version"),
                              ({"version": "1"}, "string version"),
                              ({"version": True}, "boolean version")])

    def test_missing_required_keys(self):
        for key in RETAINED:
            with self.subTest(missing=key):
                partial = settings()
                del partial[key]
                with self.assertRaises(cli.CliError) as caught:
                    configuration.validate_settings(partial)
                self.assertEqual(caught.exception.code, 2)

    def test_booleans_are_not_numbers(self):
        self.assert_rejected([({"target_mib": True}, "target bool"),
                              ({"audio_kbps": True}, "audio bool"),
                              ({"audio_kbps": False}, "audio bool false"),
                              ({"fps": False}, "fps bool"),
                              ({"verbose": 1}, "verbose number"),
                              ({"verbose": "yes"}, "verbose string"),
                              ({"target_mib": "25"}, "target string"),
                              ({"audio_kbps": 128.0}, "audio float"),
                              ({"fps": "30"}, "fps string")])

    def test_out_of_range_values(self):
        self.assert_rejected([({"target_mib": 0}, "zero target"),
                              ({"target_mib": -5}, "negative target"),
                              ({"target_mib": float("nan")}, "NaN target"),
                              ({"target_mib": float("inf")}, "infinity target"),
                              ({"audio_kbps": 0}, "zero audio"),
                              ({"audio_kbps": -1}, "negative audio"),
                              ({"fps": 0}, "zero fps"),
                              ({"fps": -5}, "negative fps"),
                              ({"fps": 1000.1}, "fps above the CLI range"),
                              ({"fps": float("inf")}, "infinite fps"),
                              ({"fps": float("nan")}, "NaN fps")])

    def test_bad_choice_values(self):
        self.assert_rejected([({"codec": "vp9"}, "codec"), ({"codec": "HEVC"}, "codec case"),
                              ({"container": "avi"}, "container"), ({"container": "mp4 "}, "container space"),
                              ({"hw": "cuda"}, "hw"), ({"hw": "AUTO"}, "hw case"),
                              ({"preset": "bogus"}, "preset"), ({"preset": 8}, "numeric preset type"),
                              ({"preset": ""}, "empty preset"),
                              ({"resolution": "wide"}, "resolution"),
                              ({"resolution": "0x0"}, "zero resolution"),
                              ({"resolution": "20000x10"}, "oversized resolution"),
                              ({"resolution": 1080}, "numeric resolution")])

    def test_incompatible_codec_container_and_preset(self):
        self.assert_rejected([({"container": "webm", "codec": "hevc"}, "webm with hevc"),
                              ({"container": "webm", "codec": "h264"}, "webm with h264"),
                              ({"codec": "hevc", "preset": "8"}, "AV1 preset with hevc"),
                              ({"codec": "h264", "preset": "13"}, "AV1 preset with h264"),
                              ({"codec": "av1", "preset": "14"}, "AV1 preset out of range"),
                              ({"codec": "av1", "preset": "banana", "hw": "qsv"}, "qsv name")])

    def test_valid_alternatives_are_accepted(self):
        for overrides in ({"codec": "av1", "container": "webm", "preset": "8"},
                          {"codec": "av1", "container": "webm", "preset": "medium"},
                          {"codec": "av1", "preset": "0"}, {"codec": "av1", "preset": "13"},
                          {"codec": "h264", "container": "mkv", "preset": "placebo"},
                          {"hw": "qsv", "preset": "placebo"},
                          {"fps": 1000.0, "target_mib": 0.5, "audio_kbps": 1,
                           "resolution": "sd", "verbose": True}):
            with self.subTest(overrides=overrides):
                self.assertEqual(configuration.validate_settings(settings(**overrides)),
                                 settings(**overrides))

    def test_a_non_dict_is_rejected(self):
        for value in (None, "text", 5, ["list"]):
            with self.subTest(value=value), self.assertRaises(cli.CliError) as caught:
                configuration.validate_settings(value)
            self.assertEqual(caught.exception.code, 2)


class SaveFailureTests(unittest.TestCase):
    def existing_config(self, directory):
        config = str(Path(directory, "latest.json"))
        configuration.save_latest(cli.Options(target_mib=33.0, codec="av1", container="webm",
                                              preset="7", fps=24.0), path=config)
        return config, Path(config).read_bytes()

    def test_a_failed_replace_keeps_the_previous_bytes_and_cleans_up(self):
        with tempfile.TemporaryDirectory() as directory:
            config, before = self.existing_config(directory)
            output = io.StringIO()
            with patch.object(configuration.os, "replace", side_effect=OSError("locked by another run")), \
                 contextlib.redirect_stdout(output):
                self.assertFalse(configuration.save_latest(cli.Options(target_mib=99.0), path=config))
            self.assertEqual(Path(config).read_bytes(), before)
            self.assertIn("WARNING:", output.getvalue())
            self.assertEqual(configuration.load_latest(config)["target_mib"], 33.0)
            self.assertEqual([entry.name for entry in Path(directory).iterdir()], ["latest.json"])

    def test_a_blocked_directory_warns_without_an_exception(self):
        with tempfile.TemporaryDirectory() as directory:
            blocker = Path(directory, "blocker.txt")
            blocker.write_text("not a directory", encoding="utf-8")
            output = io.StringIO()
            with contextlib.redirect_stdout(output):
                ok = configuration.save_latest(cli.Options(target_mib=10.0),
                                               path=str(blocker / "latest.json"))
            self.assertFalse(ok)
            self.assertIn("WARNING:", output.getvalue())
            self.assertEqual(blocker.read_text(encoding="utf-8"), "not a directory")

    def test_a_write_error_between_temp_and_replace_leaves_no_temp_file(self):
        with tempfile.TemporaryDirectory() as directory:
            config, before = self.existing_config(directory)
            real_mkstemp = configuration.tempfile.mkstemp

            def exploding_mkstemp(*args, **kwargs):
                fd, path = real_mkstemp(*args, **kwargs)
                os.close(fd)
                os.unlink(path)
                return fd, path

            with patch.object(configuration.tempfile, "mkstemp", side_effect=exploding_mkstemp), \
                 patch.object(configuration.os, "fdopen", side_effect=OSError("disk full")), \
                 contextlib.redirect_stdout(io.StringIO()) as output:
                self.assertFalse(configuration.save_latest(cli.Options(target_mib=11.0), path=config))
            self.assertEqual(Path(config).read_bytes(), before)
            self.assertEqual([entry.name for entry in Path(directory).iterdir()], ["latest.json"])
            self.assertIn("WARNING:", output.getvalue())


class ApplyLatestTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.config = str(Path(self.temp.name, "latest.json"))

    def saved(self, **overrides):
        configuration.save_latest(settings(**overrides), path=self.config)
        return self.config

    def test_saved_preferences_are_used(self):
        path = self.saved(target_mib=64.0, codec="av1", container="mkv", preset="6",
                          audio_kbps=160, fps=50.0, resolution="720p", hw="qsv", verbose=True)
        opts = configuration.apply_latest(["--latest", "in.mp4", "out"], path=path)
        self.assertEqual((opts.source, opts.destination), ("in.mp4", "out"))
        self.assertEqual(opts.target_mib, 64.0)
        self.assertEqual((opts.codec, opts.container, opts.preset), ("av1", "mkv", "6"))
        self.assertEqual((opts.audio_kbps, opts.fps, opts.resolution), (160, 50.0, "720p"))
        self.assertEqual((opts.hw, opts.verbose), ("qsv", True))
        self.assertFalse(opts.overwrite)
        self.assertFalse(opts.dry_run)
        self.assertIsNone(opts.ffmpeg)

    def test_explicit_flags_override_the_saved_values_at_any_position(self):
        path = self.saved(target_mib=64.0, codec="av1", container="mkv", preset="6",
                          audio_kbps=160, fps=50.0, resolution="720p", hw="qsv", verbose=True)
        first = configuration.apply_latest(
            ["--latest", "-t", "9", "-c", "h264", "-p", "slow", "-a", "64", "-r", "25",
             "-s", "sd", "-q", "off", "in.mp4", "out.mkv"], path=path)
        last = configuration.apply_latest(
            ["--latest", "in.mp4", "out.mkv", "--target-mb", "9", "--codec", "h264",
             "--preset", "slow", "--audio-kbps", "64", "--fps", "25",
             "--resolution", "sd", "--hw", "off"], path=path)
        inline = configuration.apply_latest(
            ["--latest", "in.mp4", "out.mkv", "--target-mb=9", "--codec=h264", "--preset=slow",
             "--audio-kbps=64", "--fps=25", "--resolution=sd", "--hw=off"], path=path)
        for opts in (first, last, inline):
            with self.subTest(opts=(opts.target_mib, opts.codec)):
                self.assertEqual(opts.target_mib, 9.0)
                self.assertEqual((opts.codec, opts.preset, opts.audio_kbps), ("h264", "slow", 64))
                self.assertEqual((opts.fps, opts.resolution, opts.hw), (25.0, "sd", "off"))
                self.assertIn(opts.container, (None, "mkv"))
                self.assertEqual(cli.resolve_destination(opts, os.path.abspath("in.mp4"))[0], "mkv")
                self.assertTrue(opts.verbose)

    def test_paths_and_operational_flags_are_never_replayed(self):
        path = self.saved()
        opts = configuration.apply_latest(["--latest", "in.mkv", "out.mkv", "--overwrite",
                                           "-d", "-ff", "/usr/bin/ffmpeg",
                                           "-fp", "/usr/bin/ffprobe"], path=path)
        self.assertEqual((opts.source, opts.destination), ("in.mkv", "out.mkv"))
        self.assertTrue(opts.overwrite and opts.dry_run)
        self.assertEqual((opts.ffmpeg, opts.ffprobe), ("/usr/bin/ffmpeg", "/usr/bin/ffprobe"))
        plain = configuration.apply_latest(["--latest", "in.mp4", "out.mp4"], path=path)
        self.assertFalse(plain.overwrite or plain.dry_run)
        self.assertIsNone(plain.ffmpeg)

    def test_destination_extension_beats_the_saved_container(self):
        path = self.saved(container="mp4")
        opts = configuration.apply_latest(["--latest", "in.mp4", "out.webm", "-c", "av1"], path=path)
        self.assertIn(opts.container, (None, "webm"))
        container = cli.resolve_destination(opts, os.path.abspath("in.mp4"))[0]
        self.assertEqual(container, "webm")

    def test_saved_container_names_the_output_in_a_destination_directory(self):
        path = self.saved(container="mkv")
        opts = configuration.apply_latest(["--latest", "in.mp4", self.temp.name], path=path)
        self.assertEqual(opts.container, "mkv")
        container, out_file = cli.resolve_destination(opts, os.path.abspath("in.mp4"))[:2]
        self.assertEqual(container, "mkv")
        self.assertTrue(out_file.endswith(".mkv"))

    def test_explicit_container_flag_wins(self):
        path = self.saved(container="mp4")
        opts = configuration.apply_latest(["--latest", "-f", "mkv", "in.mp4", "out"], path=path)
        self.assertEqual(opts.container, "mkv")
        opts = configuration.apply_latest(["--latest", "in.mp4", "out.mkv", "--container", "mkv"],
                                          path=path)
        self.assertEqual(opts.container, "mkv")

    def test_conflicting_extension_and_explicit_container_still_fails(self):
        path = self.saved(container="mp4")
        with self.assertRaises(cli.CliError) as caught:
            opts = configuration.apply_latest(["--latest", "in.mp4", "out.mkv", "-f", "mp4"], path=path)
            cli.resolve_destination(opts, os.path.abspath("in.mp4"))
        self.assertEqual(caught.exception.code, 2)

    def test_end_of_options_keeps_later_tokens_as_filenames(self):
        path = self.saved(codec="hevc")
        opts = configuration.apply_latest(["--latest", "-t", "12", "--", "-c", "out.mp4"], path=path)
        self.assertEqual((opts.source, opts.destination), ("-c", "out.mp4"))
        self.assertEqual(opts.codec, "hevc")
        self.assertEqual(opts.target_mib, 12.0)

    def test_end_of_options_treats_setup_and_latest_names_as_files(self):
        path = self.saved()
        opts = configuration.apply_latest(["--latest", "--", "--setup", "--latest"], path=path)
        self.assertEqual((opts.source, opts.destination), ("--setup", "--latest"))

    def test_codec_change_with_an_incompatible_saved_preset_is_rejected(self):
        path = self.saved(codec="av1", preset="8")
        with self.assertRaises(cli.CliError) as caught:
            configuration.apply_latest(["--latest", "in.mp4", "out.mp4", "-c", "hevc"], path=path)
        self.assertEqual(caught.exception.code, 2)
        self.assertIn("preset", str(caught.exception).lower())

    def test_qsv_preset_collapse_is_accepted(self):
        path = self.saved(hw="qsv", preset="placebo")
        opts = configuration.apply_latest(["--latest", "in.mp4", "out.mp4"], path=path)
        self.assertEqual(cli.resolve_preset(opts.preset, opts.codec, True), "veryslow")

    def test_missing_config_points_at_setup_without_running_tools(self):
        missing = str(Path(self.temp.name, "nothing.json"))
        self.assertIsNone(configuration.load_latest(missing))
        with patch.object(cli.subprocess, "run", side_effect=AssertionError), \
             self.assertRaises(cli.CliError) as caught:
            configuration.apply_latest(["--latest", "in.mp4", "out.mp4"], path=missing)
        self.assertEqual(caught.exception.code, 2)
        self.assertIn("--setup", str(caught.exception))

    def test_corrupt_config_fails_without_running_tools(self):
        Path(self.config).write_text("{ not json", encoding="utf-8")
        with patch.object(cli.subprocess, "run", side_effect=AssertionError), \
             self.assertRaises(cli.CliError) as caught:
            configuration.apply_latest(["--latest", "in.mp4", "out.mp4"], path=self.config)
        self.assertEqual(caught.exception.code, 2)

    def test_an_invalid_saved_dict_is_rejected_before_use(self):
        with self.assertRaises(cli.CliError) as caught:
            configuration.apply_latest(["--latest", "in.mp4", "out.mp4"], saved=settings(fps=0))
        self.assertEqual(caught.exception.code, 2)

    def test_a_preloaded_dict_needs_no_file(self):
        opts = configuration.apply_latest(["--latest", "in.mp4", "out.mp4"], saved=settings())
        self.assertEqual(opts.target_mib, 25.0)
        self.assertIn(opts.container, (None, "mp4"))
        self.assertEqual(cli.resolve_destination(opts, os.path.abspath("in.mp4"))[0], "mp4")

    def test_help_and_version_never_touch_the_config(self):
        missing = str(Path(self.temp.name, "absent.json"))
        for argv in (["--latest", "--help"], ["--version", "--latest"], ["-h", "--latest"],
                     ["--latest", "/?", "in.mp4", "out.mp4"]):
            with self.subTest(argv=argv):
                opts = configuration.apply_latest(argv, path=missing)
                self.assertTrue(opts.show_help or opts.show_version)
                self.assertFalse(Path(missing).exists())

    def test_setup_combined_with_latest_is_a_usage_error(self):
        path = self.saved()
        with self.assertRaises(cli.CliError) as caught:
            configuration.apply_latest(["--latest", "--setup", "in.mp4", "out.mp4"], path=path)
        self.assertEqual(caught.exception.code, 2)

    def test_missing_positionals_still_fail(self):
        path = self.saved()
        with self.assertRaises(cli.CliError) as caught:
            configuration.apply_latest(["--latest", "-t", "20"], path=path)
        self.assertEqual(caught.exception.code, 2)


class RetainDuringEncodeIntegration(ConfigTempHome):
    """The config write behind cli.main stays silent-safe through a real encode."""

    def encode_argv(self, *extra):
        return ["in.mp4", "out.mp4", "-t", "40"] + list(extra)

    def test_a_successful_encode_retains_preferences(self):
        with patch.object(cli, "run", return_value=cli.EXIT_OK):
            code, _output = silence(cli.main, self.encode_argv("-c", "av1", "-p", "6"))
        self.assertEqual(code, cli.EXIT_OK)
        saved = json.loads(self.config.read_text(encoding="utf-8"))
        self.assertEqual((saved["target_mib"], saved["codec"], saved["preset"]), (40.0, "av1", "6"))
        self.assertNotIn("in.mp4", self.config.read_text(encoding="utf-8"))

    def test_a_save_failure_does_not_turn_a_good_encode_into_an_error(self):
        with patch.object(cli, "run", return_value=cli.EXIT_OK), \
             patch.object(configuration.os, "replace", side_effect=OSError("read-only volume")):
            code, output = silence(cli.main, self.encode_argv())
        self.assertEqual(code, cli.EXIT_OK)
        self.assertIn("WARNING", output)
        self.assertFalse(self.config.exists(), "the failing save left no file behind")

    def test_dry_run_encodes_without_writing_config(self):
        with patch.object(cli, "run", return_value=cli.EXIT_OK):
            code, _output = silence(cli.main, self.encode_argv("-d"))
        self.assertEqual(code, cli.EXIT_OK)
        self.assertFalse(self.config.exists())

    def test_a_failed_encode_writes_no_config(self):
        with patch.object(cli, "run", return_value=cli.EXIT_ENCODE):
            code, _output = silence(cli.main, self.encode_argv())
        self.assertEqual(code, cli.EXIT_ENCODE)
        self.assertFalse(self.config.exists())


if __name__ == "__main__":
    unittest.main()
