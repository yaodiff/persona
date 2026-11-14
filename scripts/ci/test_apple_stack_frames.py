"""A view function's fixed stack frame in a Debug iOS build stays under 64 KB, or under its baseline (#6735).

The cases decode hand-assembled arm64 prologues and run the ratchet on made-up sizes; one case checks the committed
baseline's shape. The scan of a real build runs in the apple lane (scripts/ci/apple.sh).
"""
import json
from pathlib import Path
import sys
import unittest

sys.path.insert(0, str(Path(__file__).resolve().parent))
import apple_stack_frames as frames

ROOT = Path(__file__).resolve().parents[2]


def sub_sp(imm, shifted=False):
    """sub sp, sp, #imm{, lsl #12}"""
    return 0xD10003FF | ((1 if shifted else 0) << 22) | (imm << 10)


def movz_x15(imm, hw=0):
    return 0xD280000F | (hw << 21) | (imm << 5)


def movk_x15(imm, hw):
    return 0xF280000F | (hw << 21) | (imm << 5)


STP_FP_LR = 0xA9BF7BFD  # stp x29, x30, [sp, #-16]!
MOV_FP = 0x910003FD     # mov x29, sp
BL = 0x94000000         # bl (to ___chkstk_darwin)
SUB_SP_X15 = 0xCB2F73FF  # sub sp, sp, x15, uxtx #4


class FrameSizeTests(unittest.TestCase):
    def test_small_immediate_frame(self):
        self.assertEqual(0x6A0, frames.frame_size([STP_FP_LR, MOV_FP, sub_sp(0x6A0)]))

    def test_shifted_immediates_add_up(self):
        # 0x19 << 12 then 0x10: a 100 KB frame, as `CodeBlockStack.block` reserves.
        self.assertEqual((0x19 << 12) + 0x10, frames.frame_size([STP_FP_LR, sub_sp(0x19, True), sub_sp(0x10)]))

    def test_chkstk_probe_counts_x15_times_16(self):
        size = 0x123450
        words = [STP_FP_LR, movz_x15((size // 16) & 0xFFFF), movk_x15((size // 16) >> 16, 1), BL, SUB_SP_X15]
        self.assertEqual(size, frames.frame_size(words))

    def test_other_instructions_do_not_count(self):
        sub_x8 = 0xD1000108  # sub x8, x8, #0 (not sp)
        mov_sp_x8 = 0x9100011F  # mov sp, x8: a run-time allocation, invisible to the fixed frame by design
        self.assertEqual(0, frames.frame_size([STP_FP_LR, sub_x8, mov_sp_x8]))


class ViewFunctionTests(unittest.TestCase):
    def test_view_functions_by_mangled_name(self):
        self.assertTrue(frames.is_view_function("_$s7Persona10AgentsListV4bodyQrvg"))
        self.assertTrue(frames.is_view_function("_$s7Persona4CardV4body7SwiftUI4TextVvg"))
        self.assertFalse(frames.is_view_function("_$s7Persona18ExtensionComponentO4fromACs7Decoder_p_tKcfC"))
        self.assertFalse(frames.is_view_function("_main"))


class RatchetTests(unittest.TestCase):
    T = 64 * 1024

    def test_new_function_over_threshold_fails(self):
        problems, _ = frames.check({"a": 70 * 1024}, {}, self.T)
        self.assertEqual(["a"], [p[0] for p in problems])

    def test_baselined_function_may_stay_or_shrink(self):
        problems, lowered = frames.check({"a": 90 * 1024}, {"a": 100 * 1024}, self.T)
        self.assertEqual([], problems)
        self.assertEqual({"a": 90 * 1024}, lowered)

    def test_baselined_function_may_not_grow(self):
        problems, _ = frames.check({"a": 100 * 1024 + frames.SLACK + 16}, {"a": 100 * 1024}, self.T)
        self.assertEqual(["a"], [p[0] for p in problems])

    def test_toolchain_noise_within_slack_passes(self):
        problems, _ = frames.check({"a": 100 * 1024 + 512}, {"a": 100 * 1024}, self.T)
        self.assertEqual([], problems)

    def test_function_now_under_threshold_leaves_the_baseline(self):
        _, lowered = frames.check({"a": 10 * 1024}, {"a": 100 * 1024}, self.T)
        self.assertEqual({}, lowered)

    def test_under_threshold_is_fine(self):
        problems, _ = frames.check({"a": 60 * 1024}, {}, self.T)
        self.assertEqual([], problems)


class BaselineTests(unittest.TestCase):
    def test_committed_baseline_is_over_threshold_view_functions_only(self):
        data = json.loads((ROOT / frames.BASELINE_REL).read_text())
        for name, size in data.items():
            self.assertTrue(frames.is_view_function(name), name)
            self.assertGreater(size, frames.THRESHOLD, name)


if __name__ == "__main__":
    unittest.main()
