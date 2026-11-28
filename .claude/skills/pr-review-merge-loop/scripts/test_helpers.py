"""Tests for configurable PR review helpers; no real Herdr or GitHub writes."""
import json
import os
from pathlib import Path
import runpy
import subprocess
import sys
import tempfile
import time
import unittest
from unittest.mock import patch

HERE = Path(__file__).resolve().parent
OPERATIONS = HERE.parents[3] / "scripts" / "operations"
sys.path.insert(0, str(OPERATIONS))
import pr_watch_common
import pr_watch_repair


class HelperTests(unittest.TestCase):
    def test_interactive_stalled_prompt_keeps_accepted_work_on_same_pane(self):
        module = runpy.run_path(str(HERE / "herdr_review.py"))
        calls = []
        def fake_command(*args, **kwargs):
            calls.append(args)
            if args[:2] == ("agent", "prompt"):
                raise RuntimeError("agent prompt produced no observed state change within 5000 ms")
            if args[:2] == ("agent", "list"):
                return {"agents": [{"pane_id": "w3:p1", "agent_status": "working"}]}
            return {}
        with patch.dict(module["submit_prompt"].__globals__,
                        {"_config": {"lifetime_seconds": 7200}, "command": fake_command,
                         "scope_running": unittest.mock.Mock(return_value=True)}):
            module["submit_prompt"]("w3:p1", "large prompt", "transcript", "marker", "schema", "unit")
        self.assertEqual(sum(args[:2] == ("agent", "prompt") for args in calls), 1)

    def test_interactive_stalled_prompt_rejects_unstarted_work(self):
        module = runpy.run_path(str(HERE / "herdr_review.py"))
        def fake_command(*args, **kwargs):
            if args[:2] == ("agent", "prompt"):
                raise RuntimeError("agent prompt produced no observed state change within 5000 ms")
            if args[:2] == ("agent", "list"):
                return {"agents": [{"pane_id": "w3:p1", "agent_status": "idle"}]}
            return {}
        with patch.dict(module["submit_prompt"].__globals__,
                        {"_config": {"lifetime_seconds": 7200, "startup_grace_seconds": 1},
                         "command": fake_command, "scope_running": unittest.mock.Mock(return_value=True),
                         "transcript_result": unittest.mock.Mock(side_effect=RuntimeError("no final result"))}), \
                patch.object(module["time"], "monotonic", side_effect=[0, 0, 0, 1]), \
                patch.object(module["time"], "sleep", return_value=None):
            with self.assertRaisesRegex(RuntimeError, "did not start"):
                module["submit_prompt"]("w3:p1", "large prompt", "transcript", "marker", "schema", "unit")

    def test_interactive_stalled_idle_prompt_allows_delayed_working_state(self):
        module = runpy.run_path(str(HERE / "herdr_review.py"))
        responses = iter([
            {"agents": [{"pane_id": "w3:p1", "agent_status": "idle"}]},
            {"agents": [{"pane_id": "w3:p1", "agent_status": "working"}]},
        ])
        calls = []
        def fake_command(*args, **kwargs):
            calls.append(args)
            if args[:2] == ("agent", "prompt"):
                raise RuntimeError("agent prompt produced no observed state change within 5000 ms")
            if args[:2] == ("agent", "list"):
                return next(responses)
            return {}
        with patch.dict(module["submit_prompt"].__globals__,
                        {"_config": {"lifetime_seconds": 7200}, "command": fake_command,
                         "scope_running": unittest.mock.Mock(return_value=True),
                         "transcript_result": unittest.mock.Mock(side_effect=RuntimeError("no final result"))}), \
                patch.object(module["time"], "monotonic", side_effect=[0, 0, 0, 1]), \
                patch.object(module["time"], "sleep", return_value=None):
            self.assertTrue(module["submit_prompt"](
                "w3:p1", "large prompt", "transcript", "marker", "schema", "unit"))
        self.assertEqual(sum(args[:2] == ("agent", "prompt") for args in calls), 1)

    def test_interactive_stalled_startup_rejects_scope_exit(self):
        module = runpy.run_path(str(HERE / "herdr_review.py"))
        def fake_command(*args, **kwargs):
            if args[:2] == ("agent", "prompt"):
                raise RuntimeError("agent prompt produced no observed state change within 5000 ms")
            return {"agents": [{"pane_id": "w3:p1", "agent_status": "idle"}]}
        with patch.dict(module["submit_prompt"].__globals__,
                        {"_config": {"lifetime_seconds": 7200}, "command": fake_command,
                         "scope_running": unittest.mock.Mock(return_value=False)}):
            with self.assertRaisesRegex(RuntimeError, "scope exited during prompt startup"):
                module["submit_prompt"]("w3:p1", "large prompt", "transcript", "marker", "schema", "unit")

    def test_interactive_stalled_prompt_accepts_completed_work(self):
        module = runpy.run_path(str(HERE / "herdr_review.py"))
        def fake_command(*args, **kwargs):
            if args[:2] == ("agent", "prompt"):
                raise RuntimeError("agent prompt produced no observed state change within 5000 ms")
            if args[:2] == ("agent", "list"):
                return {"agents": [{"pane_id": "w3:p1", "agent_status": "done"}]}
            return {}
        with patch.dict(module["submit_prompt"].__globals__,
                        {"_config": {"lifetime_seconds": 7200}, "command": fake_command,
                         "scope_running": unittest.mock.Mock(return_value=True),
                         "transcript_result": unittest.mock.Mock(return_value={"findings": []})}):
            module["submit_prompt"]("w3:p1", "large prompt", "transcript", "marker", "schema", "unit")

    def test_interactive_stalled_idle_result_is_validated_before_accepting(self):
        module = runpy.run_path(str(HERE / "herdr_review.py"))
        results = []
        def fake_result(path, marker, schema):
            results.append((path, marker, schema))
            return {"findings": []}
        def fake_command(*args, **kwargs):
            if args[:2] == ("agent", "prompt"):
                raise RuntimeError("agent prompt produced no observed state change within 5000 ms")
            if args[:2] == ("agent", "list"):
                return {"agents": [{"pane_id": "w3:p1", "agent_status": "idle"}]}
            return {}
        with patch.dict(module["submit_prompt"].__globals__,
                        {"_config": {"lifetime_seconds": 7200}, "command": fake_command,
                         "scope_running": unittest.mock.Mock(return_value=True),
                         "transcript_result": fake_result}):
            self.assertTrue(module["submit_prompt"](
                "w3:p1", "large prompt", "transcript", "marker", "schema", "unit"))
        self.assertEqual(results, [("transcript", "marker", "schema")])

    def test_interactive_stalled_blocked_prompt_waits_on_same_pane(self):
        module = runpy.run_path(str(HERE / "herdr_review.py"))
        def fake_command(*args, **kwargs):
            if args[:2] == ("agent", "prompt"):
                raise RuntimeError("agent prompt produced no observed state change within 5000 ms")
            if args[:2] == ("agent", "list"):
                return {"agents": [{"pane_id": "w3:p1", "agent_status": "blocked"}]}
            return {}
        with patch.dict(module["submit_prompt"].__globals__,
                        {"_config": {"lifetime_seconds": 7200}, "command": fake_command,
                         "scope_running": unittest.mock.Mock(return_value=True)}):
            self.assertTrue(module["submit_prompt"](
                "w3:p1", "large prompt", "transcript", "marker", "schema", "unit"))
        with patch.dict(module["wait_transcript"].__globals__,
                        {"command": fake_command, "scope_running": unittest.mock.Mock(return_value=True)}), \
                patch.object(module["time"], "sleep", return_value=None), \
                patch.object(module["time"], "monotonic", side_effect=[0, 0, 2]):
            with self.assertRaises(TimeoutError):
                module["wait_transcript"](Path("/no/result"), "marker", "schema", "w3:p1", "unit", 1,
                                          accepted_stall=True)

    def test_interactive_stall_blocked_only_tolerates_initial_observation(self):
        module = runpy.run_path(str(HERE / "herdr_review.py"))
        responses = iter([
            {"agents": [{"pane_id": "w3:p1", "agent_status": "blocked"}]},
            {"agents": [{"pane_id": "w3:p1", "agent_status": "working"}]},
            {"agents": [{"pane_id": "w3:p1", "agent_status": "blocked"}]},
        ])
        with patch.dict(module["wait_transcript"].__globals__, {
                "command": unittest.mock.Mock(side_effect=lambda *args, **kwargs: next(responses)),
                "scope_running": unittest.mock.Mock(return_value=True)}), \
                patch.object(module["time"], "sleep", return_value=None), \
                patch.object(module["time"], "monotonic", side_effect=[0, 0, 1, 1.1]):
            with self.assertRaisesRegex(RuntimeError, "unexpected interaction"):
                module["wait_transcript"](Path("/no/result"), "marker", "schema", "w3:p1", "unit", 2,
                                          accepted_stall=True)

    def test_interactive_working_stall_does_not_suppress_later_block(self):
        module = runpy.run_path(str(HERE / "herdr_review.py"))
        with patch.dict(module["wait_transcript"].__globals__, {
                "command": unittest.mock.Mock(return_value={
                    "agents": [{"pane_id": "w3:p1", "agent_status": "blocked"}]}),
                "scope_running": unittest.mock.Mock(return_value=True)}):
            with self.assertRaisesRegex(RuntimeError, "unexpected interaction"):
                module["wait_transcript"](Path("/no/result"), "marker", "schema", "w3:p1", "unit", 2,
                                          accepted_stall=False)

    def test_interactive_reviewer_forwards_prompt_json_and_read_boundary(self):
        module = runpy.run_path(str(HERE / "herdr_review.py"))
        flags = module["forwarded_flags"](
            ["-p", "--output-format", "json", "--json-schema", '{"type":"object"}',
             "--tools", "Read", "--permission-mode", "plan"], {"--model"})
        self.assertEqual(flags[:3], ["-p", "--output-format", "json"])
        self.assertEqual(flags[flags.index("--tools") + 1], "Read")
        with self.assertRaises(RuntimeError):
            module["forwarded_flags"](["--model", "other"], {"--model"})

    def test_interactive_socket_keeps_large_prompt_out_of_argv(self):
        module = runpy.run_path(str(HERE / "herdr_review.py"))
        payload = (("Review exact diff\n" + "+ changed 行\n") * 9000) + "final schema and marker"
        self.assertGreater(len(payload.encode("utf-8")), 131_072)
        connection = unittest.mock.Mock()
        connection.recv.side_effect = [b'{"id":"probe","result":{"type":"submitted"}}\n']
        transport = module["socket_request"]
        def fake_request(request, timeout=90):
            return transport(request, timeout=timeout, socket_factory=lambda *_: connection)
        with patch.dict(module["command"].__globals__,
                        {"_config": {"socket_path": "/test/herdr.sock"}, "socket_request": fake_request}):
            result = module["command"](
                "agent", "prompt", "w6:p1", payload, "--wait", "--until", "working",
                "--until", "blocked", "--timeout", "120000")
        self.assertEqual(result, {"type": "submitted"})
        request = json.loads(connection.sendall.call_args.args[0])
        self.assertEqual(request["method"], "agent.prompt")
        self.assertEqual(request["params"]["text"], payload)
        self.assertEqual(request["params"]["wait"],
                         {"timeout_ms": 120000, "until": ["working", "blocked"]})
        connection.connect.assert_called_once_with("/test/herdr.sock")
        connection.close.assert_called_once_with()

    def test_interactive_socket_translates_existing_cli_arguments(self):
        module = runpy.run_path(str(HERE / "herdr_review.py"))
        cases = [
            (("tab", "create", "--workspace", "w6", "--cwd", "/work", "--label", "pr-r00", "--no-focus"),
             {"method": "tab.create", "params": {"workspace_id": "w6", "cwd": "/work",
                                                   "label": "pr-r00", "focus": False}}),
            (("tab", "close", "w6:t1"), {"method": "tab.close", "params": {"tab_id": "w6:t1"}}),
            (("pane", "send-keys", "w6:p1", "down", "enter"),
             {"method": "pane.send_keys", "params": {"pane_id": "w6:p1", "keys": ["down", "enter"]}}),
            (("pane", "read", "w6:p1", "--source", "recent-unwrapped", "--lines", "200", "--format", "text"),
             {"method": "pane.read", "params": {"pane_id": "w6:p1", "source": "recent_unwrapped",
                                                 "lines": 200, "format": "text"}}),
            (("agent", "list"), {"method": "agent.list", "params": {}}),
        ]
        for args, expected in cases:
            with self.subTest(args=args):
                request = unittest.mock.Mock(return_value={})
                with patch.dict(module["command"].__globals__, {"socket_request": request}):
                    module["command"](*args)
                sent = request.call_args.args[0]
                self.assertEqual(sent["method"], expected["method"])
                self.assertEqual(sent["params"], expected["params"])

    def test_interactive_terminal_reads_typed_socket_result(self):
        module = runpy.run_path(str(HERE / "herdr_review.py"))
        with patch.dict(module["terminal_text"].__globals__,
                        {"_config": {"result_lines": 200}, "command": unittest.mock.Mock(
                            return_value={"type": "pane_read", "read": {"text": "review result\n"}})}) as mocked:
            self.assertEqual(module["terminal_text"]("w6:p1"), "review result\n")
            self.assertEqual(mocked["command"].call_args.args[:3], ("pane", "read", "w6:p1"))

    def test_interactive_socket_enforces_transport_bounds(self):
        module = runpy.run_path(str(HERE / "herdr_review.py"))
        with patch.dict(module["socket_request"].__globals__,
                        {"_config": {"socket_path": "/test/herdr.sock"}}):
            with self.assertRaisesRegex(RuntimeError, "2 MiB"):
                module["socket_request"]({"id": "large", "method": "agent.prompt",
                                          "params": {"target": "w6:p1", "text": "x" * 2_100_000}})
            connection = unittest.mock.Mock()
            connection.recv.return_value = b"x" * 65536
            with self.assertRaisesRegex(RuntimeError, "4 MB"):
                module["socket_request"]({"id": "large-response", "method": "agent.list", "params": {}},
                                          socket_factory=lambda *_: connection)
            connection.close.assert_called_once_with()

    def test_interactive_reviewer_keeps_visible_cli_and_validates_result(self):
        module = runpy.run_path(str(HERE / "herdr_review.py"))
        launch, schema = module["interactive_command"]("cc_k3", [
            "-p", "--output-format", "json", "--json-schema",
            '{"type":"object","properties":{"findings":{"type":"array"}},"required":["findings"]}',
            "--no-session-persistence", "--tools", "Read", "--safe-mode", "--setting-sources", "",
            "--strict-mcp-config", "--permission-mode", "plan", "--disable-slash-commands"])
        self.assertEqual(launch, "cc_k3")
        self.assertIn('"findings"', schema)
        marker = "PR_REVIEW_RESULT_0123456789abcdef01234567"
        screen = ("❯ Review prompt\n  " + marker + "\n  old prompt marker\n"
                  "● " + marker + '\n  {"findings":[]}\n  ' + marker + "\n✻ done")
        self.assertEqual(module["terminal_result"](screen, marker, schema), {"findings": []})
        with self.assertRaises(RuntimeError):
            module["terminal_result"]("❯ " + marker + "\n" + marker, marker, schema)
        with self.assertRaises(RuntimeError):
            module["terminal_result"](screen.replace('"findings"', '"other"'), marker, schema)

    def test_interactive_transcript_reads_exact_final_marked_message(self):
        module = runpy.run_path(str(HERE / "herdr_review.py"))
        marker = "PR_REVIEW_RESULT_0123456789abcdef01234567"
        session_id = "12345678-1234-1234-1234-123456789abc"
        long_summary = "long punctuation: " + ("alpha beta gamma; " * 100)
        finding = {"path": "probe.txt", "line": 1, "severity": "P2",
                   "summary": long_summary, "suggestion": "Keep it intact."}
        schema = json.dumps({"type": "object", "properties": {"findings": {"type": "array"}}, "required": ["findings"]})
        with tempfile.TemporaryDirectory(prefix="review-transcript-test-") as root:
            worktree = Path(root) / "worktree.with.dots"
            worktree.mkdir()
            config = Path(root) / "claude"
            config.mkdir()
            path = module["transcript_path"](worktree, session_id, config)
            path.parent.mkdir(parents=True)
            expected_project = "-" + str(worktree).lstrip("/").replace("/", "-").replace(".", "-").replace("_", "-")
            self.assertEqual(path.parent.name, expected_project)
            records = [
                {"type": "assistant", "sessionId": session_id,
                 "message": {"stop_reason": "tool_use", "content": [{"type": "text", "text": "Reading surrounding source"}]}},
                {"type": "assistant", "sessionId": session_id,
                 "message": {"stop_reason": "end_turn", "content": [{"type": "text", "text":
                     marker + "\n" + json.dumps({"findings": [finding]}) + "\n" + marker}]}},
            ]
            path.write_text("\n".join(json.dumps(record) for record in records) + "\n")
            self.assertEqual(module["transcript_result"](path, marker, schema), {"findings": [finding]})
            records[-1]["message"]["content"][0]["text"] = "Explanation before the marker\n" + records[-1]["message"]["content"][0]["text"]
            path.write_text("\n".join(json.dumps(record) for record in records) + "\n")
            with self.assertRaisesRegex(RuntimeError, "only its marked JSON"):
                module["transcript_result"](path, marker, schema)
            with self.assertRaisesRegex(RuntimeError, "invalid"):
                module["transcript_path"](worktree, "../other-session", config)

    def test_interactive_transcript_rejects_stale_or_unfinished_results(self):
        module = runpy.run_path(str(HERE / "herdr_review.py"))
        marker = "PR_REVIEW_RESULT_0123456789abcdef01234567"
        session_id = "12345678-1234-1234-1234-123456789abc"
        schema = '{"type":"object","properties":{"findings":{"type":"array"}},"required":["findings"]}'
        with tempfile.TemporaryDirectory(prefix="review-transcript-test-") as root:
            path = Path(root) / (session_id + ".jsonl")
            record = {"type": "assistant", "sessionId": session_id,
                      "message": {"stop_reason": "end_turn", "content": [{"type": "text", "text":
                          marker + '\n{"findings":[]}\n' + marker}]}}
            path.write_text("")
            with self.assertRaisesRegex(RuntimeError, "no final result yet"):
                module["transcript_result"](path, marker, schema)
            with self.assertRaisesRegex(RuntimeError, "no final result yet"):
                module["transcript_result"](path, marker, schema)
            path.write_text(json.dumps(record) + "\n" + json.dumps(record) + "\n")
            with self.assertRaisesRegex(RuntimeError, "not unique"):
                module["transcript_result"](path, marker, schema)
            record["message"]["stop_reason"] = "tool_use"
            path.write_text(json.dumps(record) + "\n")
            with self.assertRaisesRegex(RuntimeError, "not final"):
                module["transcript_result"](path, marker, schema)

    def test_interactive_transcript_waits_while_the_agent_is_working(self):
        module = runpy.run_path(str(HERE / "herdr_review.py"))
        marker = "PR_REVIEW_RESULT_0123456789abcdef01234567"
        session_id = "12345678-1234-1234-1234-123456789abc"
        schema = '{"type":"object","properties":{"findings":{"type":"array"}},"required":["findings"]}'
        with tempfile.TemporaryDirectory(prefix="review-transcript-wait-test-") as root:
            path = Path(root) / (session_id + ".jsonl")
            path.write_text("")
            calls = []
            with patch.dict(module["wait_transcript"].__globals__, {
                    "command": lambda *args, **kwargs: calls.append(args) or {
                        "agents": [{"pane_id": "w3:p1", "agent_status": "working"}]},
                    "scope_running": unittest.mock.Mock(return_value=True),
                    "time": unittest.mock.Mock(**{"monotonic.side_effect": [0, 0, 0.1, 0.2]})}):
                with self.assertRaisesRegex(TimeoutError, "complete marked result"):
                    module["wait_transcript"](path, marker, schema, "w3:p1",
                                             "pr-review-r09-123-01234567.scope", 0.1)
            self.assertTrue(calls)

    def test_interactive_transcript_rejects_final_malformed_results_without_polling(self):
        module = runpy.run_path(str(HERE / "herdr_review.py"))
        marker = "PR_REVIEW_RESULT_0123456789abcdef01234567"
        session_id = "12345678-1234-1234-1234-123456789abc"
        schema = '{"type":"object","properties":{"findings":{"type":"array"}},"required":["findings"]}'
        cases = [
            ("Explanation before the marker\n" + marker + '\n{"findings":[]}\n' + marker,
             "only its marked JSON"),
            (marker + '\n{"findings":[]}\n' + marker + "\n" + marker, "markers are not unique"),
            (marker + '\n{"other":[]}\n' + marker, "does not match"),
            (marker + '\nnot JSON\n' + marker, "invalid marked JSON"),
        ]
        with tempfile.TemporaryDirectory(prefix="review-transcript-final-test-") as root:
            path = Path(root) / (session_id + ".jsonl")
            for text, error in cases:
                with self.subTest(error):
                    record = {"type": "assistant", "sessionId": session_id,
                              "message": {"stop_reason": "end_turn", "content": [{"type": "text", "text": text}]}}
                    path.write_text(json.dumps(record) + "\n")
                    command = unittest.mock.Mock()
                    with patch.dict(module["wait_transcript"].__globals__, {
                            "command": command, "scope_running": unittest.mock.Mock(return_value=True)}), \
                            patch.object(module["time"], "sleep") as sleep:
                        with self.assertRaisesRegex(RuntimeError, error):
                            module["wait_transcript"](path, marker, schema, "w3:p1",
                                                     "pr-review-r00-123-01234567.scope", 7200)
                    command.assert_not_called()
                    sleep.assert_not_called()

            duplicate = {"type": "assistant", "sessionId": session_id,
                         "message": {"stop_reason": "end_turn", "content": [{"type": "text", "text":
                             marker + '\n{"findings":[]}\n' + marker}]}}
            path.write_text(json.dumps(duplicate) + "\n" + json.dumps(duplicate) + "\n")
            with patch.dict(module["wait_transcript"].__globals__, {"command": unittest.mock.Mock()}), \
                    patch.object(module["time"], "sleep") as sleep:
                with self.assertRaisesRegex(RuntimeError, "result is not unique"):
                    module["wait_transcript"](path, marker, schema, "w3:p1",
                                             "pr-review-r00-123-01234567.scope", 7200)
            sleep.assert_not_called()

    def test_interactive_transcript_waits_for_unfinished_tool_use_message(self):
        module = runpy.run_path(str(HERE / "herdr_review.py"))
        marker = "PR_REVIEW_RESULT_0123456789abcdef01234567"
        session_id = "12345678-1234-1234-1234-123456789abc"
        schema = '{"type":"object","properties":{"findings":{"type":"array"}},"required":["findings"]}'
        with tempfile.TemporaryDirectory(prefix="review-transcript-progress-test-") as root:
            path = Path(root) / (session_id + ".jsonl")
            partial = {"type": "assistant", "sessionId": session_id,
                       "message": {"stop_reason": "tool_use", "content": [{"type": "text", "text": marker}]}}
            path.write_text(json.dumps(partial) + "\n")
            command = unittest.mock.Mock(return_value={"agents": [{"pane_id": "w3:p1", "agent_status": "working"}]})
            with patch.dict(module["wait_transcript"].__globals__, {
                    "command": command, "scope_running": unittest.mock.Mock(return_value=True)}), \
                    patch.object(module["time"], "sleep", return_value=None), \
                    patch.object(module["time"], "monotonic", side_effect=[0, 0, 1, 1.1]):
                with self.assertRaisesRegex(TimeoutError, "complete marked result"):
                    module["wait_transcript"](path, marker, schema, "w3:p1",
                                             "pr-review-r00-123-01234567.scope", 1)
            command.assert_called()

    def test_interactive_slot_waits_for_release_and_refills(self):
        module = runpy.run_path(str(HERE / "herdr_review.py"))
        import fcntl
        import threading
        with tempfile.TemporaryDirectory(prefix="review-slot-wait-test-") as root:
            path = Path(root) / "slot-00.lock"
            held = path.open("a+")
            fcntl.flock(held, fcntl.LOCK_EX)
            calls = []

            def release():
                if held.closed:
                    return
                time.sleep(0.05)
                fcntl.flock(held, fcntl.LOCK_UN)
                held.close()

            thread = threading.Thread(target=release)
            thread.start()
            self.addCleanup(release)
            self.addCleanup(thread.join, timeout=2)
            with patch.dict(module["acquire_slot"].__globals__, {"run": lambda slot, flags: calls.append(slot)}):
                module["acquire_slot"](Path(root), 1, [])
            thread.join(timeout=2)
            self.assertFalse(thread.is_alive())
            self.assertEqual(calls, [0])

    def test_interactive_slot_uses_another_free_slot_before_waiting(self):
        module = runpy.run_path(str(HERE / "herdr_review.py"))
        import fcntl
        with tempfile.TemporaryDirectory(prefix="review-slot-choice-test-") as root:
            held = (Path(root) / "slot-00.lock").open("a+")
            fcntl.flock(held, fcntl.LOCK_EX)
            self.addCleanup(held.close)
            with patch.dict(module["acquire_slot"].__globals__, {"run": lambda slot, flags: slot}):
                self.assertEqual(module["acquire_slot"](Path(root), 2, []), 1)

    def test_interactive_session_id_uses_the_padded_slot_path(self):
        module = runpy.run_path(str(HERE / "herdr_review.py"))
        with tempfile.TemporaryDirectory(prefix="review-session-test-") as root:
            path = Path(root) / "slot-00-session"
            session_id = "12345678-1234-1234-1234-123456789abc"
            with patch.dict(module["wait_session_id"].__globals__, {
                    "scope_running": unittest.mock.Mock(return_value=True)}):
                path.write_text(session_id + "\n")
                self.assertEqual(module["wait_session_id"](path, "w3:p1", "pr-review-r00.scope"), session_id)
            path.unlink()
            with patch.dict(module["wait_session_id"].__globals__, {
                    "scope_running": unittest.mock.Mock(return_value=False),
                    "terminal_text": unittest.mock.Mock(return_value="cc_k3: invalid PR review session path")}):
                with self.assertRaisesRegex(RuntimeError, "invalid PR review session path"):
                    module["wait_session_id"](path, "w3:p1", "pr-review-r00.scope", timeout=0.01)

    def test_interactive_scope_waits_for_registration_before_exit(self):
        module = runpy.run_path(str(HERE / "herdr_review.py"))
        with patch.dict(module["wait_scope_start"].__globals__, {
                "scope_state": unittest.mock.Mock(
                    side_effect=[("not-found", "inactive"), ("loaded", "active")])}), \
                patch.object(module["time"], "sleep", return_value=None):
            module["wait_scope_start"]("pr-review-r00.scope", module["time"].monotonic() + 5)

    def test_interactive_scope_rejects_an_early_exit(self):
        module = runpy.run_path(str(HERE / "herdr_review.py"))
        with patch.dict(module["wait_scope_start"].__globals__, {
                "scope_state": unittest.mock.Mock(return_value=("loaded", "inactive"))}):
            with self.assertRaisesRegex(RuntimeError, "before startup"):
                module["wait_scope_start"]("pr-review-r00-123-01234567.scope", module["time"].monotonic() + 5)

    def test_interactive_workspace_trust_uses_down_before_prompt(self):
        module = runpy.run_path(str(HERE / "herdr_review.py"))
        responses = iter([
            {"agents": [{"pane_id": "w3:p1", "agent": "claude", "agent_status": "idle"}]},
            {"agents": [{"pane_id": "w3:p1", "agent": "claude", "agent_status": "idle"}]},
        ])
        calls = []
        def fake_command(*args, **kwargs):
            calls.append(args)
            if args[:2] == ("agent", "list"):
                return next(responses)
            return {}
        globals_ = module["wait_agent"].__globals__
        screens = ["Quick safety check\n❯ No, exit\n  Yes, I trust this folder", ""]
        with patch.dict(globals_, {
                "_config": {},
                "scope_running": unittest.mock.Mock(return_value=True),
                "terminal_text": unittest.mock.Mock(side_effect=screens),
                "command": fake_command}):
            module["wait_agent"]("w3:p1", "pr-review-r00-123-01234567.scope", timeout=5)
        self.assertIn(("pane", "send-keys", "w3:p1", "down", "enter"), calls)

    def test_interactive_danger_confirmation_uses_down(self):
        module = runpy.run_path(str(HERE / "herdr_review.py"))
        responses = iter([
            {"agents": [{"pane_id": "w3:p1", "agent": "claude", "agent_status": "blocked"}]},
            {"agents": [{"pane_id": "w3:p1", "agent": "claude", "agent_status": "idle"}]},
        ])
        calls = []
        def fake_command(*args, **kwargs):
            calls.append(args)
            if args[:2] == ("agent", "list"):
                return next(responses)
            return {}
        with patch.dict(module["wait_agent"].__globals__, {
                "_config": {"startup_warning": "WARNING: Claude Code running in Bypass Permissions",
                            "startup_acceptance": "Yes, I accept"},
                "scope_running": unittest.mock.Mock(return_value=True),
                "terminal_text": unittest.mock.Mock(side_effect=[
                    "WARNING: Claude Code running in Bypass Permissions\n❯ No, exit\n  Yes, I accept", ""]),
                "command": fake_command}):
            module["wait_agent"]("w3:p1", "pr-review-r00.scope", timeout=5)
        self.assertIn(("pane", "send-keys", "w3:p1", "down", "enter"), calls)

    def test_reviewer_scope_wraps_visible_shell(self):
        module = runpy.run_path(str(HERE / "herdr_review.py"))
        argv = module["reviewer_command"](7, "pr-review-r07-123-01234567.scope", {
            "CPUQuota": "400%", "CPUWeight": 20, "MemoryHigh": "5G",
            "MemoryMax": "6G", "TasksMax": 512}, "cc_k3")
        self.assertEqual(argv, [
            "/usr/bin/systemd-run", "--user", "--scope", "--unit=pr-review-r07-123-01234567", "--collect",
            "-p", "CPUQuota=400%", "-p", "CPUWeight=20", "-p", "MemoryHigh=5G",
            "-p", "MemoryMax=6G", "-p", "TasksMax=512", "zsh", "-lic", "cc_k3"])
        with self.assertRaises(RuntimeError):
            module["scoped_reviewer"](7, "refract-worker-222.scope", {})
        with self.assertRaises(RuntimeError):
            module["scoped_reviewer"](7, "pr-review-r07.scope", {"ExecStartPost": "true"})
        with self.assertRaises(RuntimeError):
            module["scoped_reviewer"](7, "pr-review-r07.scope", {"MemoryMax": 6442450944})

    def test_interactive_scope_waits_for_agent_completion_after_markers(self):
        module = runpy.run_path(str(HERE / "herdr_review.py"))
        marker = "PR_REVIEW_RESULT_0123456789abcdef01234567"
        screen = "● " + marker + '\n  {"findings":[]}\n  ' + marker
        with patch.dict(module["wait_result"].__globals__, {
                "terminal_text": unittest.mock.Mock(return_value=screen),
                "command": unittest.mock.Mock(return_value={
                    "agents": [{"pane_id": "w3:p1", "agent_status": "done"}]}),
                "scope_running": unittest.mock.Mock(return_value=True)}):
            self.assertEqual(module["wait_result"]("w3:p1", "pr-review-r00-123-01234567.scope", marker, 5), screen)

    def test_scope_state_handles_collected_units(self):
        module = runpy.run_path(str(HERE / "herdr_review.py"))
        result = subprocess.CompletedProcess(["systemctl"], 0, "LoadState=not-found\nActiveState=inactive\n", "")
        with patch.object(module["subprocess"], "run", return_value=result) as run:
            self.assertEqual(module["scope_state"]("pr-review-r00-123-01234567.scope"),
                             ("not-found", "inactive"))
        self.assertIn("pr-review-r00-123-01234567.scope", run.call_args.args[0])

    def test_reviewer_closes_its_tab_once(self):
        module = runpy.run_path(str(HERE / "herdr_review.py"))
        close = unittest.mock.Mock()
        with patch.dict(module["cleanup_tab"].__globals__, {"command": close}):
            module["cleanup_tab"].__globals__["_cleanup_tab"] = "w3:t9"
            module["cleanup_tab"]()
            module["cleanup_tab"]()
        close.assert_called_once_with("tab", "close", "w3:t9", timeout=10)

    def test_resource_lock_survives_exec_and_limits_process(self):
        with tempfile.TemporaryDirectory(prefix="pr-review-resource-test-") as root:
            config = Path(root) / "resources.json"
            config.write_text(json.dumps({"slots": 2, "cpus_per_slot": 1, "first_cpu": 0,
                                          "lock_directory": str(Path(root) / "locks"),
                                          "address_space_bytes": 1024**3, "cpu_seconds": 60}))
            children = [subprocess.Popen(
                [sys.executable, "-I", str(HERE / "pr_watch_resources.py"), str(config),
                 "/usr/bin/python3", "-I", "-c",
                 "import os,resource,time; print(os.sched_getaffinity(0),resource.getrlimit(resource.RLIMIT_AS)); time.sleep(2)"],
                stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True) for _ in range(2)]
            try:
                outputs = [child.communicate(timeout=10)[0].split() for child in children]
                self.assertTrue(all(child.returncode == 0 for child in children))
                self.assertEqual(sorted(output[0] for output in outputs), ["{0}", "{1}"])
                self.assertIn("(1073741824,", outputs[0])
            finally:
                for child in children:
                    if child.poll() is None:
                        child.kill()
                        child.wait(timeout=5)

    def test_repair_mounts_rewrite_only_configured_arguments(self):
        with tempfile.TemporaryDirectory(prefix="pr-review-mount-test-") as root:
            source = Path(root) / "adapter.py"
            source.write_text("pass\n")
            with patch.dict(os.environ, {"PR_WATCH_REPAIR_MOUNTS": str(source) + ":/tools/adapter.py"}), \
                    patch.object(pr_watch_repair.shutil, "which", return_value="/usr/bin/bwrap"):
                argv = pr_watch_repair.repair_worker_argv(
                    ["/usr/bin/taskset", "--cpu-list", "0-3", "/usr/bin/python3", "-I", str(source)],
                    root, home=Path(root) / "home")
            index = argv.index("/tools/adapter.py", argv.index("--") + 1)
            self.assertEqual(argv[index - 5:index + 1],
                             ["/tools/repair-bwrap", "--cpu-list", "0-3", "/usr/bin/python3", "-I", "/tools/adapter.py"])
            mount = argv.index("/tools/adapter.py", 0, argv.index("--"))
            self.assertEqual(argv[mount - 2:mount + 1], ["--ro-bind", str(source), "/tools/adapter.py"])

    def test_process_wrapper_is_opt_in_and_never_a_host_default(self):
        with patch.dict(os.environ, {}, clear=True):
            self.assertEqual(pr_watch_common._bounded_command(["git", "status"]), ["git", "status"])
        with patch.dict(os.environ, {"PR_WATCH_PROCESS_WRAPPER": "/usr/bin/python3 -I /private/wrapper.py"}):
            self.assertEqual(pr_watch_common._bounded_command(["git", "status"]),
                             ["/usr/bin/python3", "-I", "/private/wrapper.py", "git", "status"])


if __name__ == "__main__":
    unittest.main()
