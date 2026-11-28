"""Python script suites keep their isolated HOME while Rust shims find the installed toolchain."""
import importlib.util
from pathlib import Path
import tempfile
import unittest
from unittest import mock


ROOT = Path(__file__).resolve().parents[2]
SPEC = importlib.util.spec_from_file_location("ci_python_tests_under_test", ROOT / "scripts/ci/python-tests.py")


def load_module():
    saved = list(__import__("sys").path)
    try:
        module = importlib.util.module_from_spec(SPEC)
        SPEC.loader.exec_module(module)
        return module
    finally:
        __import__("sys").path[:] = saved


class ToolchainHomeTests(unittest.TestCase):
    def test_rust_shims_keep_their_real_toolchain_and_cargo_cache(self):
        with tempfile.TemporaryDirectory() as parent, tempfile.TemporaryDirectory(dir=ROOT) as suite_home:
            module = load_module()
            environment = {"HOME": parent, "RUSTUP_DIST_SERVER": "https://example.com"}
            self.assertEqual(module.toolchain_homes(environment), {
                "RUSTUP_HOME": str(Path(parent) / ".rustup"),
                "CARGO_HOME": str(Path(parent) / ".cargo"),
            })
            captured = {}

            class Process:
                returncode = 0

                def __init__(self, argv, **kwargs):
                    captured.update(kwargs["env"])

                def wait(self, timeout):
                    return self.returncode

            suite = Path(suite_home) / "test_fixture.py"
            with mock.patch.object(module.os, "environ", environment), \
                    mock.patch.object(module.subprocess, "Popen", Process):
                label, code = module.run_suite(suite, suite_home, __import__("threading").Lock(), module.toolchain_homes(environment))
            self.assertEqual((Path(label).name, code), ("test_fixture.py", 0))
            self.assertEqual(captured["RUSTUP_HOME"], str(Path(parent) / ".rustup"))
            self.assertEqual(captured["CARGO_HOME"], str(Path(parent) / ".cargo"))
            self.assertEqual(captured["RUSTUP_DIST_SERVER"], "https://example.com")
            self.assertEqual(Path(captured["HOME"]).parent, Path(suite_home))
            self.assertNotEqual(captured["HOME"], parent)

    def test_explicit_rust_homes_are_not_replaced(self):
        module = load_module()
        with tempfile.TemporaryDirectory() as directory:
            rust, cargo = Path(directory) / "rust", Path(directory) / "cargo"
            environment = {"HOME": directory, "RUSTUP_HOME": str(rust), "CARGO_HOME": str(cargo)}
            self.assertEqual(module.toolchain_homes(environment), {
                "RUSTUP_HOME": str(rust), "CARGO_HOME": str(cargo),
            })


if __name__ == "__main__":
    unittest.main()
