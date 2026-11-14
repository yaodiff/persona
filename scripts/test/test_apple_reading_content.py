"""The interface stops at xxxLarge; only reading content grows to AX5 (`docs/design/DESIGN_SYSTEM.md`, "Dynamic Type").

`.readingContent()` is the one way under `apple/Persona` to lift the root's cap. It may appear only in the files below,
each of which draws reading content (message bodies, Inbox and file bodies, minutes, what is being typed). A new
reading container is added here with the reason; an interface view never is.
"""
from pathlib import Path
import re
import unittest

ROOT = Path(__file__).resolve().parents[2]
APP = ROOT / "apple" / "Persona"

# file (under apple/Persona) -> what it draws as reading content
READING_FILES = {
    "DesignSystem/ReadingSize.swift": "the modifier itself",
    "DesignSystem/WindowTextSize.swift": "the window cap, which names the modifier in its comment",
    "DesignSystem/QuoteViews.swift": "the user's message bubble and its quotes",
    "DesignSystem/ShellComposerBar.swift": "what is being typed",
    "Views/Chat/ComposerParts.swift": "what is being typed",
    "Views/Chat/MessageViews.swift": "assistant replies, streamed and finished",
    "Views/Chat/AnswerPartView.swift": "a long reply's parts",
    "Views/Chat/SourceMessage.swift": "a message's source text",
    "Views/Chat/FileViewer.swift": "a Markdown file's body",
    "Views/Chat/NotebookFileView.swift": "a notebook's Markdown cells",
    "Views/Inbox/ConnectorThread/GitHubThreadView.swift": "a thread comment's body",
    "Views/Session/GitPRViews.swift": "a pull request review comment's body (#7014)",
    "Views/Inbox/Lark/LarkMessageContent.swift": "a Lark message's text",
    "Views/Interactions/InteractionViews.swift": "a plan awaiting approval",
    "Views/Interactions/CardFold.swift": "a plan's first lines in its approval card (CardPlan, #6605)",
    "Views/Mail/MailViews.swift": "an email's body",
    "Views/Knowledge/KnowledgeView.swift": "a knowledge page's body",
    "Views/Knowledge/KBCorrections.swift": "an entry's confirmed facts, written by the user and read like its profile",
    "Views/Knowledge/KBTopicViews.swift": "a topic's knowledge points, read like an entry's profile",
    "Views/Knowledge/KnowledgeMapView.swift": "a node's text in the knowledge map's detail column",
    "Views/Todos/TodoDetailView.swift": "a to-do's notes, read and edited",
    "Recording/MeetingThreadView.swift": "a transcript's words in the meeting's thread",
    "Recording/RecordingDayView.swift": "a day's transcript lines",
    "Recording/RecordingMinutesSummary.swift": "minutes' points and transcript lines",
    "Recording/RecordingMinutesView.swift": "the minutes' points",
    "Recording/RecordingsSection.swift": "a recording's transcript sentences",
    "Views/RootView.swift": "the root, which keeps the chosen size for them (a mention in a comment)",
}

USE = re.compile(r"\.readingContent\(\)")


class ReadingContentTest(unittest.TestCase):
    def test_only_listed_files_lift_the_cap(self):
        found = {}
        for path in APP.rglob("*.swift"):
            if USE.search(path.read_text(encoding="utf-8")):
                found[str(path.relative_to(APP))] = True
        extra = sorted(set(found) - set(READING_FILES))
        self.assertEqual(extra, [], "`.readingContent()` outside the reading-content list: add the file to "
                                    "READING_FILES with its reason, or keep the view capped at the interface's size")

    def test_listed_files_exist(self):
        missing = [f for f in READING_FILES if not (APP / f).exists()]
        self.assertEqual(missing, [])


if __name__ == "__main__":
    unittest.main()
