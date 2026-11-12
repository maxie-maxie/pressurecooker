"""Behavioural and regression tests for the interactive quick-setup wizard.

These tests drive setup.run_setup with a scripted input function and an explicit
``path=``, and drive cli.main end to end with the config redirected to a throwaway
directory through APPDATA/XDG_CONFIG_HOME. The real user profile is never read or
written and no external tool is launched: every encode-level test patches cli.run or
cli._run_encode away.
"""

import builtins
import contextlib
import io
import json
import os
import sys
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

from pressurecooker import cli, configuration, setup


def scripted(*answers):
    """Return an input function that fails the test when the script runs out."""
    iterator = iter(answers)

    def fake_input():
        try:
            return next(iterator)
        except StopIteration:
            raise AssertionError("the wizard asked for more input than scripted")
    return fake_input


def config_argv(*extra):
    """A --setup command line that pre-fills paths so no prompts are needed."""
    return ["--setup", "clip.mp4", "out.mp4"] + list(extra)


class WizardHarness(unittest.TestCase):
    """Drive the wizard against a throwaway config file, never the real profile."""

    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory(prefix="pc-setup-")
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        self.config = self.root / "config" / "latest.json"

    def isolated_env(self):
        """Point the real config path at a temp home; return its resolved file."""
        env = patch.dict(os.environ, {"APPDATA": str(self.root),
                                      "XDG_CONFIG_HOME": str(self.root / "xdg")})
        env.start()
        self.addCleanup(env.stop)
        base = self.root if os.name == "nt" else self.root / "xdg"
        return base / "pressurecooker" / "latest.json"

    def run_setup(self, answers, argv=None, path=None):
        lines = []
        opts = setup.run_setup(
            ["--setup"] if argv is None else argv,
            input_fn=scripted(*answers),
            output_fn=lines.append,
            path=str(path or self.config),
        )
        return opts, "\n".join(lines)

    def write_config(self, **overrides):
        payload = {
            "version": 1, "target_mib": 50.0, "codec": "h264", "container": "mkv",
            "preset": "slow", "audio_kbps": 160, "fps": 30.0, "resolution": "hd",
            "hw": "off", "verbose": True,
        }
        payload.update(overrides)
        self.config.parent.mkdir(parents=True, exist_ok=True)
        self.config.write_text(json.dumps(payload), encoding="utf-8")
        return payload

    def saved_payload(self):
        return json.loads(self.config.read_text(encoding="utf-8"))


class NavigationTests(WizardHarness):
    def test_first_run_walks_all_pages_and_saves_defaults(self):
        opts, output = self.run_setup(["clip.mp4", "out.mp4"] + [""] * 10)
        self.assertIsNotNone(opts)
        self.assertEqual(opts.source, "clip.mp4")
        self.assertEqual(opts.destination, "out.mp4")
        self.assertEqual(opts.container, "mp4")
        self.assertEqual(opts.target_mib, 25.0)
        self.assertEqual(opts.codec, "hevc")
        self.assertEqual(opts.preset, "fast")
        self.assertEqual(opts.hw, "auto")
        self.assertEqual(opts.audio_kbps, 128)
        self.assertIsNone(opts.fps)
        self.assertIsNone(opts.resolution)
        self.assertFalse(opts.verbose)
        self.assertFalse(opts.dry_run)
        self.assertIn("Step 1/11: Source video", output)
        self.assertIn("Next (Enter) | Back (b) | Cancel (c)", output)
        payload = self.saved_payload()
        self.assertEqual(payload["target_mib"], 25.0)
        self.assertEqual(payload["codec"], "hevc")
        self.assertEqual(payload["container"], "mp4")

    def test_back_and_next_keep_draft_edits_across_pages(self):
        answers = [
            "clip.mp4",       # 1 source
            "out.mkv",        # 2 destination, syncs container to mkv
            "2",              # 3 target: 25 MiB
            "2",              # 4 codec: h264
            "b",              # 5 back to codec
            "",               # 4 keeps h264
            "1",              # 5 container: mp4
            "", "", "", "",   # 6-9 preset, hw, audio, fps
            "", "",           # 10-11 resolution, verbose
            "edit 2",         # review: edit destination
            "renamed.mp4",
            "edit 9",         # review: edit fps
            "30",
            "edit 3",         # review: edit target
            "12.5",
            "",               # review: start
        ]
        opts, _ = self.run_setup(answers)
        self.assertEqual(opts.source, "clip.mp4")
        self.assertEqual(opts.destination, "renamed.mp4")
        self.assertEqual(opts.container, "mp4")
        self.assertEqual(opts.target_mib, 12.5)
        self.assertEqual(opts.codec, "h264")
        self.assertEqual(opts.fps, 30.0)
        self.assertEqual(opts.audio_kbps, 128)
        self.assertIsNone(opts.resolution)

    def test_back_on_the_first_page_stays_put(self):
        opts, output = self.run_setup(["b", "clip.mp4", "out.mp4"] + [""] * 10)
        self.assertEqual(opts.source, "clip.mp4")
        self.assertIn("Already at the first page.", output)


class SavedDefaultsTests(WizardHarness):
    def test_saved_defaults_are_reused_on_the_next_run(self):
        self.write_config()
        opts, _ = self.run_setup(["clip.mp4", str(self.root / "outdir")] + [""] * 10)
        self.assertEqual(opts.target_mib, 50.0)
        self.assertEqual(opts.codec, "h264")
        self.assertEqual(opts.container, "mkv")
        self.assertEqual(opts.preset, "slow")
        self.assertEqual(opts.audio_kbps, 160)
        self.assertEqual(opts.fps, 30.0)
        self.assertEqual(opts.resolution, "hd")
        self.assertEqual(opts.hw, "off")
        self.assertTrue(opts.verbose)

    def test_corrupt_saved_config_warns_and_falls_back(self):
        self.config.parent.mkdir(parents=True, exist_ok=True)
        self.config.write_text("{not json", encoding="utf-8")
        opts, output = self.run_setup(["clip.mp4", "out.mp4"] + [""] * 10)
        self.assertIn("WARNING: ", output)
        self.assertIn("Using default settings.", output)
        self.assertEqual(opts.target_mib, 25.0)
        self.assertEqual(opts.codec, "hevc")
        self.assertFalse(opts.verbose)

    def test_unsupported_version_warns_and_falls_back(self):
        self.write_config(version=2)
        opts, output = self.run_setup(["clip.mp4", "out.mp4"] + [""] * 10)
        self.assertIn("WARNING: ", output)
        self.assertIn("Using default settings.", output)
        self.assertEqual(opts.target_mib, 25.0)


class RepromptTests(WizardHarness):
    def test_invalid_typed_fps_and_target_reprompt_with_messages(self):
        answers = [
            "clip.mp4", "out.mp4",
            "nan", "inf", "0", "-5", "25",          # target: all invalid then a preset
            "", "", "", "",                           # codec, container, preset, hw
            "",                                        # audio
            "1001", "0", "-1", "nan", "inf", "30",   # fps: out of range then a valid choice
            "", "",                                  # resolution, verbose
            "",                                      # review: start
        ]
        opts, output = self.run_setup(answers)
        self.assertEqual(opts.target_mib, 25.0)
        self.assertEqual(opts.fps, 30.0)
        self.assertIn("Target size must be a positive, finite number of MiB", output)
        self.assertIn("FPS must be a positive, finite number up to 1000", output)

    def test_invalid_audio_and_resolution_reprompt(self):
        answers = [
            "clip.mp4", "out.mp4", "", "", "", "", "",
            "0", "-1", "abc", "1.5", "96",           # audio: invalid then 96
            "",                                        # fps
            "abc", "640x", "0x720", "hd",             # resolution: invalid then hd
            "",                                        # verbose
            "",                                        # review: start
        ]
        opts, output = self.run_setup(answers)
        self.assertEqual(opts.audio_kbps, 96)
        self.assertEqual(opts.resolution, "hd")
        self.assertIn("Audio bitrate must be a positive whole number of kbps", output)


class ClearOptionalTests(WizardHarness):
    def test_every_clear_word_resets_saved_optional_fields(self):
        for word in setup.CLEAR_WORDS:
            with self.subTest(word=word):
                self.write_config()
                opts, _ = self.run_setup(
                    ["clip.mp4", str(self.root / "outdir")] + [""] * 6
                    + [word, word] + [""] * 2)
                self.assertIsNone(opts.fps)
                self.assertIsNone(opts.resolution)
                payload = self.saved_payload()
                self.assertIsNone(payload["fps"])
                self.assertIsNone(payload["resolution"])

    def test_zero_clears_a_saved_fps_but_not_an_unset_one(self):
        self.write_config()
        opts, _ = self.run_setup(
            ["clip.mp4", str(self.root / "outdir")] + [""] * 6 + ["0"] + [""] * 3)
        self.assertIsNone(opts.fps)
        self.assertEqual(opts.resolution, "hd")
        self.assertIsNone(self.saved_payload()["fps"])

    def test_enter_keeps_saved_optional_values(self):
        self.write_config()
        opts, _ = self.run_setup(["clip.mp4", str(self.root / "outdir")] + [""] * 10)
        self.assertEqual(opts.fps, 30.0)
        self.assertEqual(opts.resolution, "hd")


class PrefillTests(WizardHarness):
    def test_flags_and_paths_are_kept_but_paths_are_never_saved(self):
        argv = ["--setup", "clip.mp4", "out.webm",
                "-t", "12", "-c", "av1", "-p", "8", "-a", "160",
                "-r", "30", "-s", "hd", "-q", "off", "-v", "--overwrite",
                "-ff", "C:/tools/ffmpeg.exe", "-fp", "C:/tools/ffprobe.exe"]
        opts, _ = self.run_setup([""] * 12, argv=argv)
        self.assertEqual(opts.source, "clip.mp4")
        self.assertEqual(opts.destination, "out.webm")
        self.assertEqual(opts.container, "webm")
        self.assertEqual(opts.target_mib, 12.0)
        self.assertEqual(opts.codec, "av1")
        self.assertEqual(opts.preset, "8")
        self.assertEqual(opts.audio_kbps, 160)
        self.assertEqual(opts.fps, 30.0)
        self.assertEqual(opts.resolution, "hd")
        self.assertEqual(opts.hw, "off")
        self.assertTrue(opts.verbose)
        self.assertTrue(opts.overwrite)
        self.assertEqual(opts.ffmpeg, "C:/tools/ffmpeg.exe")
        self.assertEqual(opts.ffprobe, "C:/tools/ffprobe.exe")
        payload = self.saved_payload()
        self.assertEqual(set(payload),
                         {"version", "target_mib", "codec", "container", "preset",
                          "audio_kbps", "fps", "resolution", "hw", "verbose"})
        for key in ("source", "destination", "ffmpeg", "ffprobe", "overwrite", "dry_run"):
            self.assertNotIn(key, payload)

    def test_quoted_paths_are_unquoted(self):
        opts, _ = self.run_setup(['"clip.mp4"', "'out.mp4'"] + [""] * 10)
        self.assertEqual(opts.source, "clip.mp4")
        self.assertEqual(opts.destination, "out.mp4")

    def test_end_of_options_keeps_dash_prefixed_filenames(self):
        argv = ["--setup", "-t", "5", "--", "-clip.mp4", "out.mkv"]
        opts, _ = self.run_setup([""] * 12, argv=argv)
        self.assertEqual(opts.source, "-clip.mp4")
        self.assertEqual(opts.destination, "out.mkv")
        self.assertEqual(opts.container, "mkv")
        self.assertEqual(opts.target_mib, 5.0)

    def test_bad_prefill_values_are_usage_errors_before_any_prompt(self):
        cases = [
            config_argv("--bogus"), config_argv("a.mp4", "b.mp4", "c.mp4"),
            config_argv("-t", "nan"), config_argv("-t", "0"), config_argv("-t"),
            config_argv("-c", "vp9"), config_argv("-a", "1.5"), config_argv("-r", "0"),
            config_argv("-s", "abc"), config_argv("-q", "cuda")]
        for argv in cases:
            with self.subTest(argv=argv), self.assertRaises(cli.CliError) as caught:
                setup.run_setup(argv, input_fn=scripted(), output_fn=lambda line: None,
                                path=str(self.config))
            self.assertEqual(caught.exception.code, cli.EXIT_USAGE)
        self.assertFalse(self.config.exists())


class ReviewProblemTests(WizardHarness):
    def test_webm_with_hevc_is_blocked_until_codec_is_fixed(self):
        calls = []
        original = configuration.save_latest

        def spy(opts, **kwargs):
            calls.append(opts.codec)
            return original(opts, **kwargs)

        argv = ["--setup", "clip.mp4", "out.webm", "-c", "hevc"]
        answers = [""] * 11 + ["", "edit 4", "3", ""]
        with patch.object(configuration, "save_latest", side_effect=spy):
            opts, output = self.run_setup(answers, argv=argv)
        self.assertIn("Cannot start:", output)
        self.assertIn("requires codec 'av1'", output)
        self.assertEqual(opts.codec, "av1")
        self.assertEqual(opts.container, "webm")
        self.assertEqual(calls, ["av1"])
        self.assertEqual(self.saved_payload()["codec"], "av1")

    def test_destination_extension_mismatch_is_blocked_at_review(self):
        argv = ["--setup", "clip.mp4", "out.mkv", "-f", "mp4"]
        opts, output = self.run_setup([""] * 11 + ["", "cancel"], argv=argv)
        self.assertIsNone(opts)
        self.assertIn("Destination extension suggests 'mkv' but container is 'mp4'", output)
        self.assertFalse(self.config.exists())

    def test_av1_numeric_preset_after_codec_change_caught_and_fixed(self):
        calls = []
        original = configuration.save_latest

        def spy(opts, **kwargs):
            calls.append((opts.codec, opts.preset))
            return original(opts, **kwargs)

        argv = ["--setup", "clip.mp4", "out.mp4", "-c", "av1", "-p", "8"]
        answers = ["", "", "", "1"] + [""] * 7 + ["", "edit 6", "fast", ""]
        with patch.object(configuration, "save_latest", side_effect=spy):
            opts, output = self.run_setup(answers, argv=argv)
        self.assertIn("Preset '8' is not valid for hevc", output)
        self.assertIn("Edit page 6 (Preset) or page 4 (Codec)", output)
        self.assertEqual(opts.codec, "hevc")
        self.assertEqual(opts.preset, "fast")
        self.assertEqual(calls, [("hevc", "fast")])
        payload = self.saved_payload()
        self.assertEqual(payload["codec"], "hevc")
        self.assertEqual(payload["preset"], "fast")

    def test_unknown_review_command_is_reported(self):
        argv = config_argv()
        opts, output = self.run_setup([""] * 11 + ["frobnicate", "cancel"], argv=argv)
        self.assertIsNone(opts)
        self.assertIn("Unknown command.", output)


class InterruptionTests(WizardHarness):
    def test_cancel_words_eof_and_interrupt_preserve_config_and_run_nothing(self):
        self.write_config()
        before = self.config.read_bytes()
        for word in ("cancel", "c", "q", "quit"):
            with self.subTest(word=word):
                opts, output = self.run_setup([word])
                self.assertIsNone(opts)
                self.assertIn("Setup cancelled. Nothing was saved.", output)
                self.assertEqual(self.config.read_bytes(), before)

        lines = []
        opts = setup.run_setup(["--setup"], input_fn=raise_eof,
                               output_fn=lines.append, path=str(self.config))
        self.assertIsNone(opts)
        self.assertIn("Setup cancelled. Nothing was saved.", "\n".join(lines))
        self.assertEqual(self.config.read_bytes(), before)

        with patch.object(cli, "run", side_effect=AssertionError("encode ran")):
            with self.assertRaises(KeyboardInterrupt):
                setup.run_setup(["--setup"], input_fn=raise_interrupt,
                                output_fn=lambda line: None, path=str(self.config))
        self.assertEqual(self.config.read_bytes(), before)

    def test_cancel_at_review_writes_nothing(self):
        opts, output = self.run_setup([""] * 11 + ["cancel"], argv=config_argv())
        self.assertIsNone(opts)
        self.assertIn("Setup cancelled. Nothing was saved.", output)
        self.assertFalse(self.config.exists())

    def test_main_returns_130_on_keyboard_interrupt_without_writing_config(self):
        config = self.isolated_env()
        self.assertFalse(config.exists(), "isolated home starts empty")
        with patch.object(builtins, "input", side_effect=KeyboardInterrupt), \
             patch.object(cli, "run", side_effect=AssertionError("encode ran")), \
             contextlib.redirect_stdout(io.StringIO()):
            code = cli.main(["--setup"])
        self.assertEqual(code, cli.EXIT_INTERRUPTED)
        self.assertFalse(config.exists(), "an interrupt before review saves nothing")


def raise_eof():
    raise EOFError


def raise_interrupt():
    raise KeyboardInterrupt


class TerminalRequirementTests(WizardHarness):
    def test_noninteractive_main_fails_immediately_without_prompts(self):
        config = self.isolated_env()
        buffer = io.StringIO()
        with patch.object(sys, "stdin", NotATerminal()), \
             patch.object(cli, "run", side_effect=AssertionError("encode ran")), \
             contextlib.redirect_stdout(buffer):
            empty = cli.main([])
            explicit = cli.main(["--setup"])
        output = buffer.getvalue()
        self.assertEqual(empty, cli.EXIT_USAGE)
        self.assertEqual(explicit, cli.EXIT_USAGE)
        self.assertIn("requires a terminal (TTY)", output)
        self.assertNotIn("Step 1/11", output)
        self.assertFalse(config.exists())


class NotATerminal:
    def isatty(self):
        return False

    def readline(self, *_args):
        raise AssertionError("setup read from a noninteractive stdin")


class RetainAfterFailureTests(WizardHarness):
    def test_confirmed_preferences_survive_a_later_failed_encode(self):
        config = self.isolated_env()
        with patch.object(builtins, "input", return_value=""), \
             contextlib.redirect_stdout(io.StringIO()):
            cli.main(config_argv("-t", "18", "-c", "h264", "--hw", "off"))
        self.assertTrue(config.is_file(), "setup confirmation saved the preferences")
        saved = json.loads(config.read_text(encoding="utf-8"))
        self.assertEqual((saved["target_mib"], saved["codec"], saved["hw"]), (18.0, "h264", "off"))
        with patch.object(cli, "run", return_value=cli.EXIT_ENCODE), \
             contextlib.redirect_stdout(io.StringIO()):
            code = cli.main(["clip.mp4", "out.mp4", "-t", "18", "-c", "h264", "--hw", "off"])
        self.assertEqual(code, cli.EXIT_ENCODE)
        self.assertEqual(json.loads(config.read_text(encoding="utf-8"))["target_mib"], 18.0)


if __name__ == "__main__":
    unittest.main()
