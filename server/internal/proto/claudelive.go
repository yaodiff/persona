package proto

// Claude Code sessions the node finds running where Persona has no terminal pane: their source_kind
// (docs/integrations/claude-integration.md, "Claude outside Persona's terminals").
const (
	// SourceClaudeBg: a background session Claude's own daemon runs (/fork, /background).
	SourceClaudeBg = "claude_bg"
	// SourceClaudeDesktop: a session open in Claude's desktop app (entrypoint claude-desktop…).
	SourceClaudeDesktop = "claude_desktop"
	// SourceClaudeTerminal: claude in a terminal herdr, Orca and botmux do not run (entrypoint cli).
	SourceClaudeTerminal = "claude_cli"
)

// What the node says about such a session, in Chinese as it says everything else; the hub rewrites these in the
// app's language (assistant/fleet_text.go claudeLiveText), so the node and the hub share the wording here.
const (
	// ReadOnlyClaudeBg: a background session with no peer inbox the node can use.
	ReadOnlyClaudeBg = "Claude Code 的后台会话：手机上只能看，回复请在电脑上用 claude agents 打开它"
	// ReadOnlyClaudeNoInbox: the claude has no peer inbox (an older Claude Code, or cross-session messages are off).
	ReadOnlyClaudeNoInbox = "这个 Claude 没有开放会话之间的消息通道（版本较旧或功能关着）：手机上只能看，回复请在电脑上"
	// ReadOnlyClaudeInbox: it has one, but not one the node can use (unknown protocol, no key, another platform).
	ReadOnlyClaudeInbox = "这个 Claude 的消息通道 Persona 用不了（版本不认识或缺少钥匙）：手机上只能看，回复请在电脑上"
	// AttentionClaudeComputerBefore5965 is the former computer-only sentence, retained so a new hub localizes old nodes.
	AttentionClaudeComputerBefore5965 = "它在等电脑上的一个问题或权限请求：手机上答不了，请到电脑上处理"
	// AttentionClaudeComputer: it waits on a question or a permission prompt, which only the computer can answer.
	AttentionClaudeComputer = "它在等电脑上的一个问题或权限请求：手机上答不了，请到电脑上处理。下次在 tmux 里或从 Persona 启动 Claude，就能在手机上回答"
	// AttentionClaudeDesktopComputer: a Claude Desktop session with Persona's permission plugin is still waiting on a
	// prompt that bypassed hooks (for example an Auto-mode safety review that Claude Code restricts to the computer).
	AttentionClaudeDesktopComputer = "它在等 Claude 桌面端的安全复核或仅限电脑确认的请求：Claude Code 仅允许在电脑端批准此类操作，请到电脑上处理"
	// AttentionClaudePluginPassedOver: Claude Code's managed-settings policy skipped the plugin's classic hooks, so its
	// permission bridge cannot receive this prompt. The person approves on the computer or starts a terminal engine.
	AttentionClaudePluginPassedOver = "Claude Code 的管理设置策略让 Persona 插件的权限钩子被跳过了，所以此类请求只能在电脑上批准。请在电脑上处理，或从 Persona 用终端引擎启动会话后在手机上批准"
	// AttentionClaudeAnswer: it waits on a question it asked, and a message through its inbox answers it: the question's
	// card (node/claudeoutside_ask.go) sends the picks as that message. The hub words it in the app's language; this text
	// is what the node and the hub match on, so it stays as it is.
	AttentionClaudeAnswer = "它在问你一个问题：直接回复就是回答"
)
