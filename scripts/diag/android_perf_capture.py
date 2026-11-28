"""Capture commands, parse frame counts, and save Android performance reports."""
import json
from pathlib import Path
import re
import subprocess
from tempfile import NamedTemporaryFile


def adb_command(serial, *args, timeout, capture=False):
    command = ['adb', '-s', serial, *args]
    try:
        return subprocess.run(command, stdout=subprocess.PIPE if capture else subprocess.DEVNULL,
                              stderr=subprocess.DEVNULL, text=True, timeout=timeout, check=True).stdout
    except subprocess.CalledProcessError as error:
        raise SystemExit(f"adb {' '.join(args[:2])} failed (exit {error.returncode})") from None
    except subprocess.TimeoutExpired:
        raise SystemExit(f"adb {' '.join(args[:2])} timed out after {timeout:g}s") from None
    except FileNotFoundError:
        raise SystemExit("adb not found on PATH") from None


def frame_counts(text):
    total = re.search(r'Total frames rendered: (\d+)', text)
    janky = re.search(r'Janky frames: (\d+)', text)
    if total and janky:
        frames, janky_frames = int(total[1]), int(janky[1])
        if 0 < frames and janky_frames <= frames:
            return frames, janky_frames
    raise SystemExit('gfxinfo did not report a usable frame sample')


def write_report(path, report):
    """Keep the previous capture until a complete JSON update has closed."""
    # Follow an existing output symlink, as opening the report directly did.
    path = Path(path).resolve()
    pending = None
    try:
        with NamedTemporaryFile(mode="w", encoding="utf-8", dir=path.parent, prefix=".android-perf-", delete=False) as stream:
            pending = Path(stream.name)
            json.dump(report, stream, ensure_ascii=False, indent=1)
        pending.replace(path)
    finally:
        if pending is not None:
            pending.unlink(missing_ok=True)
