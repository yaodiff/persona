package prompts

import "encoding/json"

// InboxItem is an inbox item as the models read it (inbox/item): what kind of thing it is, then the conversation.
type InboxItem struct {
	Language   string // Native language name; empty preserves the legacy Chinese output.
	Kind       string // dm | mention | group | bot | doc | moment | mail; a connector's own (inbox/kinds/<source>)
	MailDirect bool   // mail: sent to me, not cc / a list
	// Source is a connector's id ("github") for its items, "" for Feishu's and mail's.
	Source string
	// Described is what the item's kind is, as a plugin's connector says (its kind_descriptions); "" otherwise.
	Described string
	Context   string
	// Latest is the newest Lark DM's preview, separate from the older conversation; "" for other sources.
	Latest string
}

func RenderInboxItem(it InboxItem) string { return render("inbox/item", it) }

// LarkCLIInboxContext separates the incoming batch from older conversation context.
type LarkCLIInboxContext struct {
	Current, History string
}

func RenderLarkCLIInboxContext(c LarkCLIInboxContext) string {
	return render("inbox/larkcli_context", c)
}

// RenderOwnerPromise asks whether the owner's observed reply implies future work.
func RenderOwnerPromise(language string) string {
	return render("inbox/owner_promise", struct{ Language string }{language})
}

// InboxTriage is the system prompt that sorts an item into "Needs You" / "FYI" / "Noise" (inbox/triage).
type InboxTriage struct {
	Owner string // "" is the Chinese "you"
	Rules string // the user's own rules, trimmed: the item's source's (InboxThing.Rules), else inbox.rules
	// Language is the summary's language by its own name ("English", "Japanese" written in Japanese); "" is Chinese, as before i18n.
	Language string
	// Context is what extensions wrote for the Inbox (contributes.prompts inbox_context; inbox/extension_context).
	Context []ExtensionContext
}

func RenderInboxTriage(t InboxTriage) string { return render("inbox/triage", t) }

// RenderInboxReplyPolicyReview is the legacy schema's independent second review. The shared legacy/compiled reply-policy
// declarations are below with the upstream localized prompt declarations.
func RenderInboxReplyPolicyReview() string {
	return render("inbox/reply_policy_review", nil)
}

// InboxGroup is the system prompt that relates a conversation's new messages to its events (inbox/group, #1680).
type InboxGroup struct {
	Owner string // "" is the Chinese "you"
	// MetaDescription is what the connector says its events' meta is (its manifest's meta_description), trimmed.
	MetaDescription string
	Rules           string // the user's own rules, trimmed: the batch's source's (InboxThing.Rules), else inbox.rules
	MaxCalls        int    // tool calls the batch may make
	NewMax          int    // new events one batch may open
	OpenMax         int    // open events the events tool may list
	DoneMax         int    // handled events the events tool may list
	DoneDays        int    // days within which handled events are listed
	// Language is the titles' and summaries' language by its own name ("English", "Japanese" in Japanese); "" leaves the title's
	// unsaid and the summary in Chinese, as before i18n.
	Language string
}

func RenderInboxGroup(g InboxGroup) string { return render("inbox/group", g) }

// InboxSettled is the system prompt that says whether an open "Needs you" event is over, given what came after it in
// its conversation (inbox/settled; assistant/inbox_settle.go).
type InboxSettled struct {
	Language string // Native language name; empty preserves the legacy Chinese output.
	Owner    string // "" is the Chinese "you"
}

func RenderInboxSettled(s InboxSettled) string { return render("inbox/settled", s) }

// InboxSettledInput is the event and the messages after it (inbox/settled_input), oldest first.
type InboxSettledInput struct {
	Language                string // Native language name; empty preserves the legacy Chinese output.
	Title, Summary, Context string
	Later                   []InboxSettledLine
}

// InboxSettledLine is one message after the event: At as "01-02 15:04" ("" unknown), Mine the owner's own; ReplyTo the
// message it quotes and Thread the topic it is in (ids as the event's context names its messages), "" none.
type InboxSettledLine struct {
	At, Sender, Text, ReplyTo, Thread string
	Mine                              bool
}

func RenderInboxSettledInput(in InboxSettledInput) string { return render("inbox/settled_input", in) }

// InboxGroupInput is one batch as the model reads it, with the tool calls made so far (inbox/group_input).
type InboxGroupInput struct {
	Language string // Native language name; empty preserves the legacy Chinese output.
	Thread   string
	// Kind is who the other side of a direct chat is, in the connector's words (its kind_descriptions: a person, a bot
	// or an app); "" for a group, whose messages carry their own mention markers.
	Kind    string
	New     []string // the new messages' ids
	Context string   // their text, as the connector wrote it
	Events  string   // the events tool's answer without a query (JSON)
	Calls   []InboxGroupCall
	Left    int    // tool calls still allowed
	Problem string // the previous answer's validation or JSON error, shown once for correction
}

// InboxGroupCall is one tool call and its result (JSON as the tool answered, cut).
type InboxGroupCall struct {
	Tool, Args, Result string
}

func RenderInboxGroupInput(in InboxGroupInput) string { return render("inbox/group_input", in) }

// InboxDraft is the system prompt for drafting a reply in the user's voice (inbox/draft).
type InboxDraft struct {
	Owner string // "" is the Chinese "you"
	Notes string // what the assistant knows about the user, trimmed
	// Language is the user's language by its own name ("English", "Japanese" in Japanese), for a WeChat reply when the other side's
	// is unclear; "" keeps WeChat replies in Chinese, as before i18n.
	Language string
	// Context is what extensions wrote for the Inbox (inbox/extension_context); Rewrite asks again after a draft
	// repeated a sentence an extension marked never to quote.
	Context []ExtensionContext
	Rewrite bool
}

func RenderInboxDraft(d InboxDraft) string { return render("inbox/draft", d) }

// InboxDraftInput is the item and how the user wants to answer (inbox/draft_input).
type InboxDraftInput struct {
	Language    string // Native language name; empty preserves the legacy Chinese output.
	Item        InboxItem
	Instruction string
}

func RenderInboxDraftInput(in InboxDraftInput) string { return render("inbox/draft_input", in) }

// InboxJudge is the system prompt that links an item to one of the user's tasks (inbox/judge): since phase 1 every
// Needs you item goes to the manager, so only its task_id is used.
type InboxJudge struct {
	Language string // Native language name; empty preserves the legacy Chinese output.
	Owner    string
	Rules    string // manager.json inbox.agent.rules, trimmed
}

func RenderInboxJudge(j InboxJudge) string { return render("inbox/judge", j) }

// InboxJudgeInput is the item with its triage summary (inbox/judge_input).
type InboxJudgeInput struct {
	Language string // Native language name; empty preserves the legacy Chinese output.
	Summary  string
	Item     InboxItem
	Tasks    []InboxTask // the user's tasks going on (docs/assistant/unified-entry.md), for the judge to link the item to one
}

// InboxTask is a task the user started from the assistant's conversation: a todo and where its agents are.
type InboxTask struct {
	ID, Title, State string
	Last             string // what its latest agent said last, in a line
	Notes            string // the task's own description, clipped: the judge reads its subject there and quotes it as evidence
}

func RenderInboxJudgeInput(in InboxJudgeInput) string { return render("inbox/judge_input", in) }

// InboxAgent is the first message of the AI twin's conversation for an item (inbox/agent). At most one of Lark, Doc,
// Moment and Mail is set: where the item came from.
type InboxAgent struct {
	Language string // Native language name; empty preserves the legacy Chinese output.
	Owner    string
	EventID  string // the inbox item's id: the conversation is bound to it
	Title    string
	Item     InboxItem
	Task     string // what to do: the triage's summary ("" the default)
	Reason   string // the triage's reason
	// ReplyPolicy adds the drafting restrictions only when this item is in an enabled policy's scope. False leaves the
	// legacy prompt byte-identical when the feature is absent or disabled.
	ReplyPolicy bool
	// Resumed: the conversation that handled the event is gone; Recap and Chain are where it got to.
	Resumed bool
	Recap   string
	Chain   []string // the event's chain so far, one line an entry, oldest first
	Notes   []string // what came since (the user's answers…), each rendered (inbox/handling/*)
	// Linked: the user's task the item is about (the judge's task_id), its progress for the reply.
	Linked *InboxTask
	// HandoffTargets are the eligible existing conversations on this manager (main, leaders, linked task)
	// that kind "handoff" may propose handing the item to (#13562).
	HandoffTargets []InboxHandoffTarget
	Lark           *InboxAgentLark
	Doc            *InboxAgentDoc
	Moment         *InboxAgentMoment
	Mail           *InboxAgentMail
	// Connector: an item of a connector's (GitHub…).
	Connector *InboxAgentConnector
	KB        *KBContext // what the knowledge base knows about the item's chat
	// KnowledgeDir: the knowledge base directory the KB part names; "" is the default ~/.persona/data.
	KnowledgeDir string
}

// InboxHandoffTarget is an eligible existing conversation on this manager listed in inbox/agent (#13562).
type InboxHandoffTarget struct {
	Machine string
	Session string
	Role    string // main | leader | task
	Title   string
}

// InboxAgentLark is the item's Feishu chat; Account is the gateway account it is in when that is not lark.account.
type InboxAgentLark struct {
	FeedID  string
	Account int
}

// InboxAgentConnector is the thread of a connector's item: Source is the connector's id (a subscription's id for a
// subscription's item). ReplyTo says where a reply goes (with CanReply false, how the user replies themself:
// iMessage's "reply in Messages"); none, no line. A subscription's item names the subscription (Name), the connector it
// reads (Via) and that source's name (ViaSource, #6222).
type InboxAgentConnector struct {
	Source    string
	URL       string
	ReplyTo   string
	Name      string
	Via       string
	ViaSource string
}

type InboxAgentDoc struct {
	URL  string
	Rows bool // rows the agent may move the status of
}

type InboxAgentMoment struct{ PostID string }

type InboxAgentMail struct {
	Account, Mailbox string
	UID              uint32 // the newest message
}

func RenderInboxAgent(a InboxAgent) string { return render("inbox/agent", a) }

// InboxHandlingNote is what the program tells an event's processing conversation (inbox/handling/<name>): new
// messages (Context), the user's confirmation, rejection or words (Text, Edited), the Agent it started (Machine,
// Session), a due follow-up check (Who, Next and Checks), or that the event is closed (Text says how: self_replied,
// replied, dismissed, read, user).
type InboxHandlingNote struct {
	Language         string // Native language name; empty preserves the legacy Chinese output.
	Owner            string
	Text             string
	Edited           bool
	Failed           bool
	Context          string
	Who              string // who or what a due check waits on; "" means the other side's reply
	Next             string // the next step to consider on a due check
	Checks           int    // the consecutive due check's number, starting at 1
	Machine, Session string
	Handoff          bool // the note goes to a conversation the event was handed to (wrapped by inbox/handoff_note)
}

// The notes (their templates).
const (
	NoteNewMessages  = "inbox/handling/new_messages"
	NoteSent         = "inbox/handling/sent"
	NoteCheck        = "inbox/handling/check"
	NoteAgentStarted = "inbox/handling/agent_started"
	NoteAgentStopped = "inbox/handling/agent_stopped"
	NoteHandback     = "inbox/handling/handback"
	NoteRejected     = "inbox/handling/rejected"
	NoteInstructed   = "inbox/handling/instructed"
	NoteClosed       = "inbox/handling/closed"
	NoteSelfReplied  = "inbox/handling/self_replied"
	NoteSelfRead     = "inbox/handling/self_read"
)

func RenderInboxHandlingNote(name string, n InboxHandlingNote) string {
	return render(name, n)
}

// InboxHandoff is the message that hands an event to Persona's main conversation (inbox/handoff): the user's Text
// after a context block naming the event, so that conversation can write back with inbox_suggest.
type InboxHandoff struct {
	Language  string // Native language name; empty preserves the legacy Chinese output.
	ID, Title string
	Text      string
}

func RenderInboxHandoff(h InboxHandoff) string { return render("inbox/handoff", h) }

// InboxHandoffNote wraps a handling note (Note) for a conversation the event was handed to (inbox/handoff_note).
type InboxHandoffNote struct {
	Language  string // Native language name; empty preserves the legacy Chinese output.
	ID, Title string
	Note      string
}

func RenderInboxHandoffNote(n InboxHandoffNote) string {
	if n.Language == "" {
		return render("inbox/handoff_note", n)
	}
	marker, err := json.Marshal(inboxMetadata{Title: n.Title})
	if err != nil {
		panic(err)
	}
	return render("inbox/handoff_note", struct {
		InboxHandoffNote
		Marker string
	}{n, "<persona-inbox>" + string(marker) + "</persona-inbox>"})
}

// InboxFolders is the system prompt that files items into folder paths (inbox/folders, #2463).
type InboxFolders struct {
	Owner       string // "" is the Chinese "you"
	FolderRules string // the user's folder rules, trimmed
	Rules       string // the user's triage rules (inbox.rules), trimmed: what they care about
	// Language is the new folders' language by its own name; "" leaves it to Chinese or the folders already there.
	Language string
	MaxDepth int // levels a new folder may have
}

func RenderInboxFolders(f InboxFolders) string { return render("inbox/folders", f) }

// InboxFoldersInput is one batch of items to file, with the folders in use and the user's own moves (inbox/folders_input).
type InboxFoldersInput struct {
	Language string // Native language name; empty preserves the legacy Chinese output.
	Folders  []InboxFolderUse
	Examples []InboxFolderExample
	Items    []InboxFolderItem
}

// InboxFolderUse is a folder in use and how many items are in it.
type InboxFolderUse struct {
	Path  string
	Count int
}

// InboxFolderExample is an item the user filed by hand.
type InboxFolderExample struct {
	Title, Sender, Folder string
}

// InboxFolderItem is an item to file, as the model reads it (one line each, the preview cut).
type InboxFolderItem struct {
	ID, Kind, Source, Title, Sender, Summary, Preview string
}

func RenderInboxFoldersInput(in InboxFoldersInput) string { return render("inbox/folders_input", in) }

// InboxMergeTitle is the system prompt that writes one title for rows the user merged (inbox/merge_title, #7229).
type InboxMergeTitle struct {
	Owner string // "" is the Chinese "you"
	// Language is the title's language by its own name; "" leaves it to the language of the titles.
	Language string
}

func RenderInboxMergeTitle(m InboxMergeTitle) string { return render("inbox/merge_title", m) }

// InboxMergeTitleInput is the rows that were merged, newest first (inbox/merge_title_input).
type InboxMergeTitleInput struct {
	Language string // Native language name; empty preserves the legacy Chinese output.
	Items    []InboxFolderItem
}

func RenderInboxMergeTitleInput(in InboxMergeTitleInput) string {
	return render("inbox/merge_title_input", in)
}

// SubscriptionKind is a source subscription's kind of Inbox item (#2039, inbox/kinds/subscription): what its items'
// type line says (InboxItem.Described, through the round's kinds).
type SubscriptionKind struct {
	Language string // Native language name; empty preserves the legacy Chinese output.
	Kind     string // sub_mail … sub_digest, sub_failing
	Label    string // the subscription's label: whose items they are
}

func RenderSubscriptionKind(k SubscriptionKind) string { return render("inbox/kinds/subscription", k) }

// InboxReplyPolicy is the system prompt for classifying an Inbox item or reply text into a configured category
// (inbox/reply_policy). The server maps the returned category to a route; the model never chooses a route.
type InboxReplyPolicy struct {
	Language   string   // Native language name; empty preserves the legacy Chinese output.
	Owner      string   // "" is the Chinese "you"
	Categories []string // sorted allowed category names from server policy
	Rules      string   // optional extra classification guidance, trimmed
}

func RenderInboxReplyPolicy(p ...InboxReplyPolicy) string {
	if len(p) == 0 {
		return render("inbox/reply_policy_legacy", nil)
	}
	return render("inbox/reply_policy", p[0])
}

// InboxReplyPolicyInput combines the legacy owner-edit fields with the upstream localized item fields. Each renderer
// selects its own template: legacy Text/Original/Edit never flow into the compiled schema's item presentation.
type InboxReplyPolicyInput struct {
	// Legacy Task 2 input.
	Text     string
	Original string
	Edit     bool
	// Upstream compiled-policy input.
	Language  string // Native language name; empty preserves the legacy Chinese output.
	Title     string
	Sender    string
	Source    string
	Kind      string
	Context   string
	ReplyText string // non-empty when checking a proposed suggestion or final text before send
}

func RenderInboxReplyPolicyInput(in InboxReplyPolicyInput) string {
	legacy := in.Text != "" || in.Original != "" || in.Edit ||
		(in.Language == "" && in.Title == "" && in.Sender == "" && in.Source == "" && in.Kind == "" && in.Context == "" && in.ReplyText == "")
	if legacy {
		return render("inbox/reply_policy_legacy_input", in)
	}
	return render("inbox/reply_policy_input", in)
}

// RenderExternalInboxTriage gives the small-task model its routing response schema.
func RenderExternalInboxTriage(language string) string {
	return render("inbox/external_routing", struct{ Language string }{language})
}

// ExternalInboxUpdate is one source update routed to its existing owner.
type ExternalInboxUpdate struct {
	Language string
	Event    string
	Summary  string
	URL      string
}

func RenderExternalInboxUpdate(in ExternalInboxUpdate) string {
	return render("inbox/external_update", in)
}
