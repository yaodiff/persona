"""Two of one computer's spaces with one name (#13522).

MOCK_SPACE_FOLDERS=1 (with MOCK_SESSIONS_LIST=1 and MOCK_SESSIONS_CATALOG=1) adds to the MacBook two herdr spaces named
`site`, one in ~/Desktop/site and one in ~/code/site, each with an Agent. The catalog says each space's folder (`path`,
docs/protocol/protocol.md "Projects and spaces"), so the web's By project tells them apart by their parent folder:
`site · ~/Desktop` and `site · ~/code`. Unset: nothing changes.
"""
import os

MODE = os.environ.get("MOCK_SPACE_FOLDERS", "")


def register(d):
    d.help("MOCK_SPACE_FOLDERS=1  two MacBook spaces named site, in ~/Desktop and in ~/code (#13522)")
    if not MODE:
        return
    hub = d.hub
    hub.SESSIONS.extend([
        hub.sess("macbook", "5f01de55-0000-4000-8000-000000000001", "改首页的轮播图", "/Users/me/Desktop/site", "idle", "unread",
                 "site", 41, "hero", 1, 25, "轮播图换成了三张新的，移动端的高度也调好了。"),
        hub.sess("macbook", "5f01de55-0000-4000-8000-000000000002", "部署脚本改成增量上传", "/Users/me/code/site", "working", "working",
                 "site", 42, "deploy", 1, 2, "正在比对上次上传的文件清单。"),
    ])
