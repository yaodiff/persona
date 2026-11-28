package assistant

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/nwverse/persona/server/internal/connectorwire"
)

// nextActorExternal is the evidence.next_actor of a wait on a third party, a system or a date: no owner action.
const nextActorExternal = "external"

// ParkEvidence records the supervisor's source-backed account of an external wait. It is not a provider receipt
// fetched by the hub: the supervisor must inspect the linked source before recording it. nil means a legacy park
// without recorded evidence, not a verified contact. RecordedBy is set by task_review, never trusted from its input.
type ParkEvidence struct {
	Kind   string `json:"kind"`
	Target string `json:"target"`
	URL    string `json:"url,omitempty"`
	Wake   string `json:"wake"`
	// NextActor is required on admission. Old reviews can have typed evidence without it.
	NextActor string `json:"next_actor,omitempty"`
	// InteractionID is an external_event's existing pending owner request, verified against this task's worker.
	InteractionID string                        `json:"interaction_id,omitempty"`
	PREvent       string                        `json:"pr_event,omitempty"`
	Contact       *ParkContact                  `json:"contact,omitempty"`
	Ready         bool                          `json:"ready,omitempty"`
	RecordedBy    string                        `json:"recorded_by,omitempty"`
	Reply         *connectorwire.LarkReplyWatch `json:"reply,omitempty"`
}

// ParkContact names an already sent request, not a draft, a proposed reminder or a send whose outcome is unknown.
type ParkContact struct {
	Channel string    `json:"channel"`
	URL     string    `json:"url"`
	Sender  string    `json:"sender"`
	SentAt  time.Time `json:"sent_at"`
	Request string    `json:"request"`
	Status  string    `json:"status"`
}

func (e *ParkEvidence) clone() *ParkEvidence {
	if e == nil {
		return nil
	}
	out := *e
	if e.Contact != nil {
		c := *e.Contact
		out.Contact = &c
	}
	if e.Reply != nil {
		w := *e.Reply
		out.Reply = &w
	}
	return &out
}

// readParkEvidence checks structure and sent-outcome consistency, without network reads or any state changes.
func readParkEvidence(input *ParkEvidence, waitingOn, until string, now time.Time) (*ParkEvidence, []string) {
	if input == nil {
		return nil, []string{"evidence (person_reply, merge_review, external_event or date, with its source and wake condition)"}
	}
	e := input.clone()
	e.Kind, e.Target, e.URL, e.Wake = strings.TrimSpace(e.Kind), strings.TrimSpace(e.Target), strings.TrimSpace(e.URL), strings.TrimSpace(e.Wake)
	e.InteractionID = strings.TrimSpace(e.InteractionID)
	e.RecordedBy = ""
	var missing []string
	text := func(name, value string, limit int) {
		if value == "" || utf8.RuneCountInString(value) > limit {
			missing = append(missing, name+" (nonblank, at most "+strconv.Itoa(limit)+" characters)")
		}
	}
	text("evidence.target (the named person/reviewer or external object)", e.Target, 200)
	text("evidence.wake (the observable change that ends this wait)", e.Wake, 600)
	e.NextActor = strings.TrimSpace(e.NextActor)
	switch e.NextActor {
	case nextActorExternal:
	case "owner":
		missing = append(missing, "evidence.next_actor: only the owner can take the next step: use needs_user with why_owner, reason, recommend, options and cost_of_waiting")
	default:
		missing = append(missing, "evidence.next_actor (external for a third party, a system or a date that needs no owner action; use needs_user for owner action)")
	}
	want := ""
	switch e.Kind {
	case "person_reply", "merge_review":
		want = parkSomeoneElse
		if c := e.Contact; c == nil {
			missing = append(missing, "evidence.contact (the request already sent: channel, url, sender, sent_at, request and status sent)")
		} else {
			c.Channel, c.URL, c.Sender = strings.TrimSpace(c.Channel), strings.TrimSpace(c.URL), strings.TrimSpace(c.Sender)
			c.Request, c.Status = strings.TrimSpace(c.Request), strings.TrimSpace(c.Status)
			text("evidence.contact.channel", c.Channel, 100)
			text("evidence.contact.sender", c.Sender, 200)
			text("evidence.contact.request", c.Request, 600)
			if !parkSourceURL(c.URL) {
				missing = append(missing, "evidence.contact.url (an http(s) sent-message/receipt link, not a channel homepage)")
			}
			if c.Status != "sent" {
				missing = append(missing, "evidence.contact.status (must be sent, not draft, failed or unknown)")
			}
			if c.SentAt.IsZero() || c.SentAt.After(now) {
				missing = append(missing, "evidence.contact.sent_at (the actual sent time, not in the future)")
			}
			c.SentAt = c.SentAt.UTC()
		}
		if e.Kind == "merge_review" {
			if !e.Ready {
				missing = append(missing, "evidence.ready (the PR/MR is ready for the named reviewer)")
			}
			if !parkReviewURL(e.URL) {
				missing = append(missing, "evidence.url (the ready PR/MR link)")
			}
		} else if e.URL != "" || e.Ready {
			missing = append(missing, "evidence.url and ready apply to merge_review or an external-event object, not person_reply")
		}
	case "external_event":
		want = parkExternalEvent
		if !parkSourceURL(e.URL) {
			missing = append(missing, "evidence.url (the observable external object)")
		}
	case "date":
		want = parkDate
		if _, err := time.Parse(time.RFC3339, strings.TrimSpace(until)); err != nil {
			missing = append(missing, "until (date waits require an explicit RFC 3339 date, not a duration or default)")
		}
	default:
		missing = append(missing, "evidence.kind (person_reply, merge_review, external_event or date; owner/internal work cannot be parked)")
	}
	if want != "" && waitingOn != want {
		missing = append(missing, "waiting_on must be "+want+" for evidence.kind "+e.Kind)
	}
	if e.Kind != "person_reply" && e.Kind != "merge_review" && (e.Contact != nil || e.Ready) {
		missing = append(missing, "evidence.contact and ready apply to person_reply/merge_review only")
	}
	if e.InteractionID != "" && e.Kind != "external_event" {
		missing = append(missing, "evidence.interaction_id (an existing pending owner request on an external_event only)")
	}
	if e.Reply != nil && (e.Kind != "person_reply" || !parkReplyBinding(e)) {
		missing = append(missing, "evidence.reply (person_reply only: larkcli source, brand, app_id, account_open_id, person_open_id, chat_id, thread_id and request_message_id matching the sent topic link)")
	}
	if e.PREvent != "" && (e.Kind != "external_event" || !externalPREvent(e.PREvent) || githubParkPRURL(e.URL) == "") {
		missing = append(missing, "evidence.pr_event (external_event with a GitHub PR URL: terminal or terminal_or_review)")
	}
	return e, missing
}

// verifyParkInteraction binds a park only to a live request of its own worker. Releasing the worker does not resolve
// its request; dismissing the link or a failed start does make it ineligible. Projection checks liveness again on every read.
func (s *Service) verifyParkInteraction(t Todo, e *ParkEvidence) error {
	if e == nil || e.InteractionID == "" {
		return nil
	}
	ix, ok := s.pendingIx(e.InteractionID)
	if !ok {
		return fmt.Errorf("evidence.interaction_id %q is not a pending owner request", e.InteractionID)
	}
	for _, a := range t.Agents {
		if a.DismissedAt == nil && a.StartError == "" && a.MachineID == ix.MachineID && a.SessionID == ix.SessionID {
			return nil
		}
	}
	return fmt.Errorf("evidence.interaction_id %q is not a pending owner request of task %s's worker", e.InteractionID, t.ID)
}

func parkSourceURL(raw string) bool {
	if len(raw) > 2000 || strings.IndexFunc(raw, unicode.IsSpace) >= 0 {
		return false
	}
	u, err := url.Parse(raw)
	return err == nil && (u.Scheme == "https" || u.Scheme == "http") && u.Hostname() != "" && u.User == nil &&
		u.Path != "" && u.Path != "/"
}

// parkSelfTodoURL rejects evidence URLs that point back to the task's own page (e.g. /todos/<todoID> or
// /api/v1/todos/<todoID>), because an internal task cannot act as its own external wait event.
func parkSelfTodoURL(todoID, raw string) bool {
	todoID = strings.TrimSpace(todoID)
	raw = strings.TrimSpace(raw)
	if todoID == "" || raw == "" {
		return false
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return false
	}
	segs := strings.Split(strings.Trim(u.Path, "/"), "/")
	for i := 0; i+1 < len(segs); i++ {
		if segs[i] == "todos" && segs[i+1] == todoID {
			return true
		}
	}
	return false
}

func verifyParkTarget(todoID string, e *ParkEvidence) error {
	if e != nil && e.Kind == "external_event" && parkSelfTodoURL(todoID, e.URL) {
		return fmt.Errorf("parked is refused: evidence.url points to task %s's own page, not an observable external event; owner/internal waits cannot be parked. %s", todoID, parkElse)
	}
	return nil
}

func parkReviewURL(raw string) bool {
	if !parkSourceURL(raw) {
		return false
	}
	u, _ := url.Parse(raw)
	p := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(p) < 4 {
		return false
	}
	n, err := strconv.Atoi(p[len(p)-1])
	return err == nil && n > 0 && (p[len(p)-2] == "pull" ||
		len(p) >= 5 && p[len(p)-3] == "-" && p[len(p)-2] == "merge_requests")
}
