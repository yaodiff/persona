# 屏幕夹具

`pane read --source visible` 的真实屏幕，`<agent>-<版本>-<场景>-<宽度>.{ansi,txt}`：`.ansi` 带颜色（`format: ansi, strip_ansi: false`），
`.txt` 是同一屏的纯文本。Claude Code 改界面时加一份新版本的，老版本的留着。

- `claude-2.1.312-*`：本仓库在一个临时目录里起的 claude 上录的（路径换成了 `/private/tmp/project`）；`-suggestion-171` 是一轮结束后输入框里的灰色建议（`-suggestion-typed-171` 是在它上面打了字）。
- `claude-2.1.309-auto-mode-notice-120.txt`：auto mode 计费变更的提示框（「Enter to continue · Esc to cancel」，#2553）。当时的窗格已经被按掉，没能录下；照 2.1.309 二进制里的原文和 Claude 对话框的版式拼的，网关链接换成了 example.com。录到真屏后替换。
- `claude-2.1.318-help-*-120`：本仓库在一个临时目录（`/tmp/project`）里起的 claude 2.1.318 上录的：一条会折行的消息（或带输出的 `/effort medium`）之后打开 `/help`（#4159）。
- `claude-2.1.318-theme-picker-120.txt`, `claude-2.1.318-security-notes-120.txt`: the first-run screens of a fresh Claude Code 2.1.318 (an empty `CLAUDE_CONFIG_DIR`), captured 2026-11-07 in a 120×40 pty rather than herdr and rendered from its escape stream: the style picker (choices without numbers, the cursor on the default) and, after Enter, the security notes (assembled from the same stream; it redraws in place). No paths or names on them.
- `claude-2.1.322-bypass-confirm-160.txt`: Claude Code 2.1.322's Bypass Permissions confirmation (`--permission-mode bypassPermissions` on a profile that never accepted it), captured 2026-11-11 with `tmux capture-pane` from a 160×50 pane started with an empty `CLAUDE_CONFIG_DIR` (onboarding and the folder's trust marked done). The dialog is at the top with blank rows down to the pane's bottom, as a fresh tab shows it. No paths or names on it.
- `claude-rewind-2.1.304-*`：取自 Ovis（github.com/devswha/ovis，`server/fixtures/`，MIT License，Copyright (c) 2026 devswha）。
- `codex-0.153.4-*` / `codex-0.163.0-*`：本仓库在一个独立的 herdr 会话里起的 Codex（ChatGPT 登录）上录的，内容是编的（横幅颜色、example.com），
  路径换成了 `/private/tmp/project`；`@` 文件选择那屏会列出这台机器装的插件和 skill，没收进来。宽度是无头 herdr 窗格的 120 列。

录制：herdr 里开一个 tab 跑 claude，用 socket 的 `pane.read` 各读一次 ansi 和纯文本。入库前确认没有路径、主机名、账号。
