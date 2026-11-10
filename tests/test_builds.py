import contextlib
import importlib.util
import io
import shutil
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]


def load_script(path):
    spec = importlib.util.spec_from_file_location(path.stem, path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


class ReleaseVersionTests(unittest.TestCase):
    def test_builds_follow_the_source_version(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            package = root / "src" / "pressurecooker"
            package.mkdir(parents=True)
            (root / "scripts").mkdir()
            (package / "__init__.py").write_text('__version__ = "0.2.0"\n')
            (package / "cli.py").write_text(
                "from . import __version__\ndef main():\n    print(__version__)\n")
            (root / "LICENSE").write_text("Test license\n")
            for name in ("build_binary.py", "build_portable.py"):
                shutil.copyfile(ROOT / "scripts" / name, root / "scripts" / name)

            native = load_script(root / "scripts" / "build_binary.py")
            self.assertEqual(native.VERSION, "0.2.0")
            self.assertIn("-0.2.0-", native.build_name())

            portable = load_script(root / "scripts" / "build_portable.py")
            with contextlib.redirect_stdout(io.StringIO()):
                self.assertEqual(portable.main(), 0)
            archive = root / "dist" / "pressurecooker-0.2.0.pyz"
            self.assertTrue(archive.is_file())
            result = subprocess.run([sys.executable, str(archive)], capture_output=True, text=True)
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertEqual(result.stdout.strip(), "0.2.0")


if __name__ == "__main__":
    unittest.main()
