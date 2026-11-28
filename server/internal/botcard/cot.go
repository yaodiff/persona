package botcard

import (
	"encoding/json"
	"time"
)

// An agent message (the open platform's message_cot): Feishu draws it as its own collapsible process bubble — one
// line ("Task completed >" once it ends) that opens onto the process, each paragraph followed by its folded tool calls. It
// is created empty, then fed AG-UI events (PUT, at most 50 a call), and ends with RUN_FINISHED (or Complete when it
// went wrong). Clients older than PC 7.70 / mobile 7.74 get a plain post Feishu writes itself.

// CotEvent is one AG-UI event of an agent message.
type CotEvent struct {
	Type      string `json:"event_type"`
	Content   string `json:"content"` // the event's JSON
	Timestamp int64  `json:"timestamp"`
}

func cotEvent(typ string, content any) CotEvent {
	b, _ := json.Marshal(content)
	return CotEvent{Type: typ, Content: string(b), Timestamp: time.Now().UnixMilli()}
}

// CotStart opens the run and its process (threadID: the conversation, runID: the turn, first: the first paragraph's id).
func CotStart(threadID, runID, first string) []CotEvent {
	return []CotEvent{
		cotEvent("RUN_STARTED", map[string]string{"threadId": threadID, "runId": runID}),
		cotEvent("REASONING_START", map[string]string{"messageId": first}),
	}
}

// CotParagraph is one paragraph of the process (thinking, or what the agent said between its tool calls).
func CotParagraph(id, text string) []CotEvent {
	return []CotEvent{
		cotEvent("REASONING_MESSAGE_START", map[string]string{"messageId": id, "role": "reasoning"}),
		cotEvent("REASONING_MESSAGE_CONTENT", map[string]string{"messageId": id, "delta": text}),
		cotEvent("REASONING_MESSAGE_END", map[string]string{"messageId": id}),
	}
}

// CotToolCall is a tool call under paragraph parent. Feishu draws only its title (not its arguments), so what it did
// goes there; icon is one of bash, write, read, search, task, default.
func CotToolCall(id, parent, name, title, icon string) []CotEvent {
	start := map[string]string{"toolCallId": id, "toolCallName": name, "title": title, "icon": icon}
	if parent != "" {
		start["parentMessageId"] = parent
	}
	return []CotEvent{cotEvent("TOOL_CALL_START", start), cotEvent("TOOL_CALL_END", map[string]string{"toolCallId": id})}
}

// CotToolResult settles a tool call: its output as code, or a line of text (an empty output still needs one, or the
// call keeps spinning).
func CotToolResult(id, output string, code bool) CotEvent {
	body := map[string]string{"type": "text", "text": output}
	if code {
		body = map[string]string{"type": "code", "code": output}
	}
	b, _ := json.Marshal(body)
	return cotEvent("TOOL_CALL_RESULT", map[string]string{"messageId": "tr-" + id, "toolCallId": id, "role": "tool", "content": string(b)})
}

// cotTextDelta is how many runes one TEXT_MESSAGE_CONTENT carries.
const cotTextDelta = 2000

// CotReasoningEnd closes the process (last: its last paragraph's id) and leaves the run open.
func CotReasoningEnd(last string) CotEvent {
	return cotEvent("REASONING_END", map[string]string{"messageId": last})
}

// CotText is the run's answer as a text message: START (role assistant), CONTENT in deltas of at most cotTextDelta
// runes, END. Nil for an empty text.
func CotText(id, text string) []CotEvent {
	if text == "" {
		return nil
	}
	out := []CotEvent{cotEvent("TEXT_MESSAGE_START", map[string]string{"messageId": id, "role": "assistant"})}
	runes := []rune(text)
	for len(runes) > 0 {
		n := min(cotTextDelta, len(runes))
		out = append(out, cotEvent("TEXT_MESSAGE_CONTENT", map[string]string{"messageId": id, "delta": string(runes[:n])}))
		runes = runes[n:]
	}
	return append(out, cotEvent("TEXT_MESSAGE_END", map[string]string{"messageId": id}))
}

// CotRunFinished ends the run, which Feishu then shows as done.
func CotRunFinished(threadID, runID string) CotEvent {
	return cotEvent("RUN_FINISHED", map[string]string{"threadId": threadID, "runId": runID, "status": "done"})
}

// CotFinish closes the process (last: the last paragraph's id) and the run, which Feishu then shows as done.
func CotFinish(threadID, runID, last string) []CotEvent {
	return []CotEvent{CotReasoningEnd(last), CotRunFinished(threadID, runID)}
}

// CotFinishWithAnswer closes the process, writes the answer and ends the run.
func CotFinishWithAnswer(threadID, runID, last, answerID, text string) []CotEvent {
	return append(append([]CotEvent{CotReasoningEnd(last)}, CotText(answerID, text)...), CotRunFinished(threadID, runID))
}
