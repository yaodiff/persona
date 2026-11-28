package prompts

// TopicFollowup is a human follow-up in a Lark topic linked to a task (#15407): the topic's link, the message, its
// sender and time, and how it was delivered.
type TopicFollowup struct {
	URL, Message, Sender, At, Delivery string
}

// RenderTopicFollowup is the note that wakes the linked task with it.
func RenderTopicFollowup(d TopicFollowup) string { return render("assistant/topic_followup", d) }
