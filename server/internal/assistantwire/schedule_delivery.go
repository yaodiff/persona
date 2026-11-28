package assistantwire

// TaskDelivery names an optional channel thread where a scheduled task's finished result is posted
// instead of the owner's private DM mirror, so replies in that thread continue the task's execution conversation.
type TaskDelivery struct {
	Channel  string         `json:"channel"`
	ChatID   FeishuChatID   `json:"chat_id"`
	ThreadID FeishuThreadID `json:"thread_id"`
}
