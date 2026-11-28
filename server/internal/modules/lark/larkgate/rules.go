package larkgate

import (
	"encoding/json"
	"strings"

	"github.com/nwverse/persona/server/internal/lazyre"
)

const (
	boundaryOrdinary = 0
	boundaryOutbound = 1
	boundaryDeletion = 2
	cliMissingRisk   = "missing"
)

type actionRule struct {
	re       *lazyre.Regexp
	what     string
	boundary int
}

// larkActions holds only the module-specific lark-cli patterns.
// Generic HTTP deletion, comment, mail and conversation-message patterns, plus the OpenAPI IM route,
// stay in internal/node so generic CommandGate calls keep their confirmation boundary without the lark module.
var larkActions = []actionRule{
	// i18n-ignore: node-side gate card wording matched by original text
	{lazyre.New(`^CLI /im/messages/delete`), "撤回消息", boundaryDeletion},
	// i18n-ignore: node-side gate card wording matched by original text
	{lazyre.New(`^CLI /.*(?:/|\+|-)(?:delete|remove|trash|recall)(?:$|/|-)`), "删除", boundaryDeletion},
	// i18n-ignore: node-side gate card wording matched by original text
	{lazyre.New(`^CLI /.*(?:comments|replies)/(?:create|reply)$`), "发表评论", boundaryOutbound},
	// i18n-ignore: node-side gate card wording matched by original text
	{lazyre.New(`^CLI /mail/.*(?:send|reply|forward)$`), "发邮件", boundaryOutbound},
	// i18n-ignore: node-side gate card wording matched by original text
	{lazyre.New(`^CLI /mail/\+.*(?:send|reply|forward)`), "发邮件", boundaryOutbound},
	// i18n-ignore: node-side gate card wording matched by original text
	{lazyre.New(`^CLI /im/(?:messages/(?:create|reply|patch|update|send)|threads/forward)(?:/|$)`), "发消息", boundaryOutbound},
	// lark-cli commands: "CLI /<domain>/<command>"
	// i18n-ignore: node-side gate card wording matched by original text
	{lazyre.New(`^CLI /im/\+messages-send`), "发消息", boundaryOutbound},
	// i18n-ignore: node-side gate card wording matched by original text
	{lazyre.New(`^CLI /im/\+messages-reply`), "回复消息", boundaryOutbound},
	// i18n-ignore: node-side gate card wording matched by original text
	{lazyre.New(`^CLI /im/reactions/`), "表情回复", boundaryOutbound},
	// i18n-ignore: node-side gate card wording matched by original text
	{lazyre.New(`^CLI /im/(messages/(forward|merge_forward)|threads/forward)`), "转发消息", boundaryOutbound},
	// i18n-ignore: node-side gate card wording matched by original text
	{lazyre.New(`^CLI /im/messages/urgent`), "加急", boundaryOutbound},
	// i18n-ignore: node-side gate card wording matched by original text
	{lazyre.New(`^CLI /im/`), "改群 / 会话", boundaryOrdinary},
	// i18n-ignore: node-side gate card wording matched by original text
	{lazyre.New(`^CLI /(markdown|slides|whiteboard|mindnotes)`), "写文档", boundaryOrdinary},
	// i18n-ignore: node-side gate card wording matched by original text
	{lazyre.New(`^CLI /task`), "改待办", boundaryOrdinary},
	// i18n-ignore: node-side gate card wording matched by original text
	{lazyre.New(`^CLI /approval`), "处理审批", boundaryOrdinary},
	// i18n-ignore: node-side gate card wording matched by original text
	{lazyre.New(`^CLI /calendar`), "建 / 改日程", boundaryOrdinary},
	// i18n-ignore: node-side gate card wording matched by original text
	{lazyre.New(`^CLI /(docs|sheets|wiki|drive|base)`), "写文档", boundaryOrdinary},
	// i18n-ignore: node-side gate card wording matched by original text
	{lazyre.New(`^CLI /mail`), "发 / 改邮件", boundaryOrdinary},
}

// Only the message send/reply shortcuts are documented to use the bot (plan-lark-cli.md).
// Raw API calls, mail and comments cannot claim this exemption by passing --as bot.
var botOutboundPaths = map[string]bool{
	"/im/+messages-send":  true,
	"/im/+messages-reply": true,
}

var cliBoolFlags = map[string]bool{
	"--help": true, "-h": true, "--version": true, "-v": true, "--dry-run": true,
	"--reply-in-thread": true,
}

// CLIRead reports whether a lark-cli call only reads (its own read/missing risk level, or GET/HEAD via lark-cli api).
func (*Gate) CLIRead(method, risk string) bool {
	if method == "CLI" {
		return risk == "read" || risk == cliMissingRisk
	}
	return method == "GET" || method == "HEAD"
}

// ClassifyAction matches Feishu/Lark-specific action patterns for method and path.
func (*Gate) ClassifyAction(method, path string) (what string, boundary int, matched bool) {
	target := method + " " + path
	for _, a := range larkActions {
		if a.re.MatchString(target) {
			return a.what, a.boundary, true
		}
	}
	return "", boundaryOrdinary, false
}

// CLIBypassSafe reports whether a lark-cli subcommand (Method == "CLI") may bypass phone confirmation.
// Reads pass; deletion and outbound actions stay behind confirmation except documented --as bot message shortcuts;
// other subcommands pass only when lark-cli explicitly reports Risk: write.
func (g *Gate) CLIBypassSafe(path, risk string, args []string) bool {
	if g.CLIRead("CLI", risk) {
		return true
	}
	if _, boundary, ok := g.ClassifyAction("CLI", path); ok && boundary != boundaryOrdinary {
		return boundary == boundaryOutbound && botOutboundPaths[path] && g.CLIBotActor(args)
	}
	return risk == "write"
}

// CLIBotActor reports whether args explicitly select --as bot under pflag's last-value rules.
func (*Gate) CLIBotActor(args []string) bool {
	return flagValue(args, "--as") == "bot"
}

// ActorLabel is who a lark-cli write acts as on the phone card: the app's bot with --as bot, the user otherwise.
func (g *Gate) ActorLabel(args []string) string {
	if g.CLIBotActor(args) {
		return "以机器人身份" // i18n-ignore: node-side gate card wording matched by original text
	}
	return "以你的身份" // i18n-ignore: node-side gate card wording matched by original text
}

// CLISummary renders the phone card text for a lark-cli write.
func (g *Gate) CLISummary(method, _, risk string, args []string, what string) string {
	// i18n-ignore: node-side gate card wording matched by original text
	return g.ActorLabel(args) + "在飞书上" + what + riskNote(method, risk) + "\n" + prettyBody("lark-cli "+shellJoin(args))
}

// DefaultTitle is the default service title shown on lark-cli permission cards.
func (*Gate) DefaultTitle() string {
	return "飞书" // i18n-ignore: node-side gate card wording matched by original text
}

// riskNote marks the card when lark-cli calls a command high-risk, or could not say how risky it is.
func riskNote(method, risk string) string {
	switch {
	case method != "CLI":
		return ""
	case risk == "high-risk-write":
		return "（lark-cli 标为高风险）" // i18n-ignore: node-side gate card wording matched by original text
	case risk == "":
		return "（lark-cli 没给出风险等级）" // i18n-ignore: node-side gate card wording matched by original text
	}
	return ""
}

// shellJoin formats arguments as the user would type them on the phone card, quoting only where a shell needs it.
func shellJoin(args []string) string {
	out := make([]string, len(args))
	for i, a := range args {
		if a == "" || strings.ContainsAny(a, " \t\n'\"\\$`|&;<>()*?[]{}#~!") {
			a = "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
		}
		out[i] = a
	}
	return strings.Join(out, " ")
}

func prettyBody(body string) string {
	var v any
	if json.Unmarshal([]byte(body), &v) == nil {
		if b, err := json.MarshalIndent(v, "", "  "); err == nil {
			body = string(b)
		}
	}
	if r := []rune(body); len(r) > 2000 {
		body = string(r[:2000]) + "…"
	}
	return body
}

// flagValue follows pflag's last-value semantics, including --name=value. A value flag consumes
// its next argument even when that argument starts with a dash; -- ends flag parsing.
func flagValue(flags []string, name string) string {
	value := ""
	for i := 0; i < len(flags); i++ {
		f := flags[i]
		if f == "--" {
			break
		}
		if !strings.HasPrefix(f, "-") {
			continue
		}
		key, val, inline := strings.Cut(f, "=")
		if !inline {
			if cliBoolFlags[key] {
				continue
			}
			// An unknown flag may be boolean or consume the next flag as text. Neither
			// interpretation may retain an earlier bot identity as a bypass exemption.
			if key != name && (i+1 >= len(flags) || strings.HasPrefix(flags[i+1], "-")) {
				return ""
			}
			if i+1 >= len(flags) {
				if key == name {
					value = ""
				}
				break
			}
			i++
			val = flags[i]
		}
		if key == name {
			value = val
		}
	}
	return value
}
