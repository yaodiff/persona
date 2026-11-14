"""The mock hub's GET /api/v1/assistant/search answers as the manager does (#6282): the shared rule of
spec/fixtures/chat-search ("index") for what a message counts as, the feature flag, titles and messages."""
import json
import os
from pathlib import Path
import runpy
import socket
import subprocess
import tempfile
import time
import unittest
from urllib.error import URLError
from urllib.request import Request, urlopen

HERE = Path(__file__).resolve().parent
FIXTURE = HERE.parent.parent / "spec" / "fixtures" / "chat-search" / "cases.json"


class AssistantSearchRuleTests(unittest.TestCase):
    def test_index_text_follows_the_shared_rule(self):
        mock = runpy.run_path(str(HERE / "mock_hub.py"))
        cases = json.loads(FIXTURE.read_text())["cases"]
        self.assertGreater(len(cases), 10)
        for case in cases:
            with self.subTest(case["name"]):
                self.assertEqual(mock["search_index_text"](case["item"]), case["index"])

    def test_a_reply_is_indexed_without_markdown(self):
        # the manager's searchPlain cases (server/internal/assistant/search_text_test.go, #7072), the same answers
        mock = runpy.run_path(str(HERE / "mock_hub.py"))
        for md, want in [
            ("2. **照片同步脚本**（Mac Studio）", "照片同步脚本（Mac Studio）"),
            ("- [ ] 待办：~~旧的~~ 新的\n  * 嵌套 _斜体_ 项", "待办：旧的 新的\n嵌套 斜体 项"),
            ("见 [文档](https://example.com/a_b) 和 ![图](x.png) 和 [引用][1]", "见 文档 和 图 和 引用"),
            ("用 `**args` 和 `` a ` b `` 调用", "用 **args 和 a ` b 调用"),
            ("```go\nx := 1 // **keep**\n```\n后面 **粗**", "x := 1 // **keep**\n后面 粗"),
            ("| a | b |\n|---|---|\n| 1 | 2 |\n\n***", "a b\n1 2"),
            ("2 * 3 * 4，snake_case_name，\\*星\\*，未闭合的 ` 反引号", "2 * 3 * 4，snake_case_name，*星*，未闭合的 ` 反引号"),
        ]:
            with self.subTest(md):
                self.assertEqual(mock["search_plain"](md), want)
        self.assertEqual(mock["search_index_text"]({"role": "user", "kind": "text", "text": "**不是** Markdown"}), "**不是** Markdown")

    def test_a_reply_keeps_no_strong_markers(self):
        # the manager's TestSearchPlainLeavesNoStrongMarkers (#7195), the same answers
        mock = runpy.run_path(str(HERE / "mock_hub.py"))
        for md, want in [
            ("计划写好了。\n**步骤一 - `make test`**", "计划写好了。\n步骤一 - make test"),
            ("结论：**先跑 `go vet` 再提交**，然后合并", "结论：先跑 go vet 再提交，然后合并"),
            ("上一段\n\n**1. 准备 `config.json`**\n内容", "上一段\n\n1. 准备 config.json\n内容"),
            ("**`--dry-run`** 只打印", "--dry-run 只打印"),
            ("**第一行\n第二行**", "第一行\n第二行"),
            ("只有开头的 **没有结尾", "只有开头的 没有结尾"),
            ("**a **b** c**，***粗斜***，***没收", "a b c，粗斜，没收"),
            ("见 [`config.json`](https://example.com/c) 和 *用 `x` 时*", "见 config.json 和 用 x 时"),
            ("2 ** 3 = 8，a**b\nx**2 和 **注意**", "2 ** 3 = 8，a**b\nx**2 和 注意"),
            ("用 `**kwargs` 和 `a\\*b`", "用 **kwargs 和 a*b"),
            ("`\U000F0000` 在代码里", "\U000F0000 在代码里"),
        ]:
            with self.subTest(md):
                self.assertEqual(mock["search_plain"](md), want)

    def test_a_cut_snippet_keeps_its_ranges(self):
        # NFKC makes the leading "…" three dots: the ranges still point at the match in the snippet as sent (#7045)
        mock = runpy.run_path(str(HERE / "mock_hub.py"))
        body = "早" * 60 + "照片同步脚本（Ｍac）😀 Ａfter"
        snippet, ranges = mock["search_snippet"](body, ["照片同步脚本", "after"])
        self.assertTrue(snippet.startswith("…"))
        utf16 = snippet.encode("utf-16-le")
        self.assertEqual([utf16[a * 2:b * 2].decode("utf-16-le") for a, b in ranges], ["照片同步脚本", "Ａfter"])


class AssistantSearchMockTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        with socket.socket() as sock:
            sock.bind(("127.0.0.1", 0))
            port = sock.getsockname()[1]
        cls.base = f"http://127.0.0.1:{port}"
        cls.log = tempfile.TemporaryFile()
        cls.process = subprocess.Popen(["python3", "-I", str(HERE / "mock_hub.py")],
                                       env=dict(os.environ, PORT=str(port), MOCK_CHAT_SEARCH="1"), stdout=cls.log, stderr=cls.log)
        cls.addClassCleanup(cls.stop)
        for _ in range(100):
            if cls.process.poll() is not None:
                raise RuntimeError("the mock hub did not start")
            try:
                with urlopen(cls.base + "/api/v1/health", timeout=1):
                    return
            except (URLError, TimeoutError):
                time.sleep(0.1)
        raise RuntimeError("the mock hub took too long to start")

    @classmethod
    def stop(cls):
        cls.process.terminate()
        try:
            cls.process.wait(timeout=5)
        except subprocess.TimeoutExpired:
            cls.process.kill()
            cls.process.wait(timeout=5)
        cls.log.close()

    def get(self, path):
        req = Request(self.base + path, headers={"Authorization": "Bearer mock"})
        with urlopen(req, timeout=5) as response:
            return json.loads(response.read())

    def search(self, q):
        from urllib.parse import quote
        return self.get("/api/v1/assistant/search?q=" + quote(q))

    def test_feature_listed(self):
        self.assertIn("assistant_search", self.get("/api/v1/health").get("features", []))

    def test_finds_the_users_words_and_replies_never_hidden_text(self):
        trip = "b2e4d6f8-0000-4000-8000-000000000004"
        for q in ("BlueLane", "青石巷", "WisteriaTea", "GlacierPost", "EarlyReply", "LastReplyWord"):
            with self.subTest(q):
                results = self.search(q)["results"]
                self.assertEqual([r["session_id"] for r in results], [trip])
                hit = results[0]["hits"][0]
                start, end = hit["ranges"][0]
                self.assertEqual(hit["snippet"][start:end].lower(), q.lower())
        for q in ("ThinkWord", "CopperKey", "SilverKey", "HiddenCtx", "ActionSecret", "persona-action", "InjectedRaw", "示例任务验收"):
            with self.subTest(q):
                self.assertEqual(self.search(q)["results"], [])

    def test_title_first(self):
        results = self.search("AmberTitle")["results"]
        self.assertTrue(results and results[0]["title_match"] and results[0]["hits"] == [])

    def test_blank_query(self):
        answer = self.search("  ")
        self.assertEqual(answer["results"], [])
        self.assertFalse(answer["partial"])


if __name__ == "__main__":
    unittest.main()
