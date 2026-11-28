package kbwire

import (
	"encoding/json"
	"errors"
	"slices"
	"time"
)

// DayMessage is one message of a day page.
type DayMessage struct {
	SentBy       string     `json:"sent_by,omitempty"`
	SentLedgerID int64      `json:"sent_ledger_id,omitempty"`
	RecalledAt   *time.Time `json:"recalled_at,omitempty"`
	N            int        `json:"n"`
	Text         string     `json:"text"`
}

// Detail is everything about one entity: its metadata, compressed context, a project's timeline, a chat's latest text.
type Detail struct {
	Kind        string            `json:"kind"`
	ID          string            `json:"id"`
	Path        string            `json:"path"` // absolute, for reading more with file tools
	Meta        json.RawMessage   `json:"meta"`
	Summary     string            `json:"summary,omitempty"`
	Log         []string          `json:"log,omitempty"`
	Recent      string            `json:"recent,omitempty"`
	ProjectMode string            `json:"project_mode,omitempty"`
	Freshness   *ProjectFreshness `json:"freshness,omitempty"`
	Sources     []ProjectSource   `json:"sources,omitempty"`
	// Links are the linked projects, chats and people, named and ranked.
	Links []Entry `json:"links,omitempty"`
	// Removed: the links the user removed by hand (meta's excluded), named as Links are; automatic organizing leaves
	// them out until the user links them again or allows them again (allow_people, allow_chats, allow_projects).
	Removed []Entry `json:"removed,omitempty"`
	// Moments: a project's Moments posts (LinkMoments), newest first.
	Moments []MomentRef `json:"moments,omitempty"`
	// Segments: a recording's transcript (the app shows it; Recent has it as text for the assistant).
	Segments []RecordingSegment `json:"segments,omitempty"`
	// Minutes: a recording's meeting minutes (minutes.json, docs/assistant/meeting-minutes.md), once written.
	Minutes json.RawMessage `json:"minutes,omitempty"`
	// Recap: an all-day recording's recap (recap.json, docs/assistant/day-recap.md), once written.
	Recap json.RawMessage `json:"recap,omitempty"`
	// Points: a topic's knowledge points (points.json, docs/assistant/knowledge.md "Topics"), grouped by Path.
	Points []TopicPoint `json:"points,omitempty"`
	// Profiles: a person's profiles by writer ("user", "ext:<id>"), as written (Summary shows them too); none when no
	// one wrote one.
	Profiles map[string]ProfileRecord `json:"profiles,omitempty"`
	// Paused: a recording whose stored state is in flight (an upload not complete, minutes or a day recap pending, a
	// hub transcription not final) while nothing can settle it; "module_off": the recording module is off. Derived
	// when read, never stored (docs/assistant/knowledge.md "Recordings without the recording module"). Absent otherwise.
	Paused string `json:"paused,omitempty"`
	// Speakers: a hub-transcribed meeting's speaker list, state, quality and timeline spans
	// (docs/assistant/meeting-minutes.md "Speakers (#17674)"), derived on read (Core, with or without the recording
	// module). Absent when the recording has no speaker data.
	Speakers *DetailSpeakers `json:"speakers,omitempty"`
}

// ProfileRecord is one writer's profile of a person and when it was written.
type ProfileRecord struct {
	Profile PersonProfile `json:"profile"`
	At      time.Time     `json:"at"`
}

// TopicPoint is one knowledge point of a topic: a concept, decision, practice, pitfall, fact or open question, under
// up to two levels of subtopics (Path), with the entries it came from.
type TopicPoint struct {
	ID      string        `json:"id"`
	Path    []string      `json:"path,omitempty"`
	Kind    string        `json:"kind"` // concept | decision | practice | pitfall | fact | question
	Text    string        `json:"text"`
	Why     string        `json:"why,omitempty"` // a decision's reason, a pitfall's cause
	Sources []TopicSource `json:"sources,omitempty"`
	When    string        `json:"when,omitempty"` // YYYY-MM-DD: last confirmed
}

// TopicSource is an entry a knowledge point came from: a project's timeline or a chat's summary, and the day.
type TopicSource struct {
	Kind string `json:"kind"` // projects | chats
	ID   string `json:"id"`
	Day  string `json:"day,omitempty"`
}

// MomentRef is a Moments post as a project shows it.
type MomentRef struct {
	ID       string    `json:"id"`
	Author   string    `json:"author"`
	Text     string    `json:"text"` // clipped
	At       time.Time `json:"at"`
	Comments int       `json:"comments"`
	Note     string    `json:"note,omitempty"` // how it bears on the project
}

// Update is a change by hand (the app) or by the assistant. Set fields only; tagging set here is locked.
type Update struct {
	Pinned         *string  `json:"pinned,omitempty"`
	Lock           *bool    `json:"lock,omitempty"`
	RemovePeople   []string `json:"remove_people,omitempty"`
	RemoveChats    []string `json:"remove_chats,omitempty"`
	RemoveAliases  []string `json:"remove_aliases,omitempty"`
	RemoveProjects []string `json:"remove_projects,omitempty"`
	Name           string   `json:"name,omitempty"`
	Kind           string   `json:"project_kind,omitempty"` // projects: short | long
	Status         string   `json:"status,omitempty"`
	Goal           string   `json:"goal,omitempty"`
	MyRole         string   `json:"my_role,omitempty"`
	Note           string   `json:"note,omitempty"` // projects / people / items / topics
	Importance     string   `json:"importance,omitempty"`
	Relevance      *int     `json:"relevance,omitempty"`
	Tags           []string `json:"tags,omitempty"`    // nil preserves; non-nil replaces, including an empty slice
	Summary        string   `json:"summary,omitempty"` // replaces summary.md
	Log            string   `json:"log,omitempty"`     // projects: appended to the timeline
	Source         string   `json:"source,omitempty"`  // who writes the log line (default Persona)
	Aliases        []string `json:"aliases,omitempty"`
	Chats          []string `json:"chats,omitempty"`    // projects: chat ids to link
	People         []string `json:"people,omitempty"`   // projects: person ids to link
	Dirs           []string `json:"dirs,omitempty"`     // projects: "<machine>:<path>" code directories
	Projects       []string `json:"projects,omitempty"` // chats / people / items: project slugs to link
	// MergeInto (projects): fold this project into that one (MergeProjects); nothing else in the update applies.
	MergeInto string `json:"merge_into,omitempty"`
	// Profile (people): the writer's own profile of the person, replacing the one it wrote before; one with no items
	// removes it. Each writer (an extension, the user) keeps its own; nobody else's is touched (docs/assistant/knowledge.md
	// "Person profiles").
	Profile *PersonProfile `json:"profile,omitempty"`
}

// PersonProfile is one writer's profile of a person, its four parts kept apart: what is known as fact, what the user
// said of them in their own words, what is inferred (only a hint), and what each owes the other. Every item says where
// it comes from (Sources).
type PersonProfile struct {
	Facts       []ProfileItem       `json:"facts,omitempty"`       // their role, what they are in charge of, how they relate to the user
	Views       []ProfileItem       `json:"views,omitempty"`       // the user's own words about them, quoted
	Inferences  []ProfileItem       `json:"inferences,omitempty"`  // inferred, never stated: each with its Basis
	Commitments []ProfileCommitment `json:"commitments,omitempty"` // promises and to-dos between them, either way
}

// Empty: the profile has no items (writing it removes the writer's profile).
func (p *PersonProfile) Empty() bool {
	return p == nil || len(p.Facts)+len(p.Views)+len(p.Inferences)+len(p.Commitments) == 0
}

// ProfileItem is one line of a profile: its text, for an inference what it rests on, and where it comes from (a
// knowledge-base path such as "chats/<id>/messages/2026-11-01.md", "recordings/<id>", a URL, or "user" for the user's
// own word).
type ProfileItem struct {
	Text    string   `json:"text"`
	Basis   string   `json:"basis,omitempty"`
	Sources []string `json:"sources"`
}

// ProfileCommitment is a promise or a to-do between the user and the person: Owes says who owes it ("user": the user
// to them; "them": they to the user), Due its day (YYYY-MM-DD) when there is one, Done once kept.
type ProfileCommitment struct {
	Text    string   `json:"text"`
	Owes    string   `json:"owes"`
	Due     string   `json:"due,omitempty"`
	Done    bool     `json:"done,omitempty"`
	Sources []string `json:"sources"`
}

// IgnoredField is a field an update carried that its kind does not use: not applied, and why.
type IgnoredField struct {
	Field  string `json:"field"`
	Reason string `json:"reason"`
}

// RecordingSegment is one transcribed stretch: seconds from the recording's start.
type RecordingSegment struct {
	Start float64 `json:"start"`
	End   float64 `json:"end"`
	Text  string  `json:"text"`
	// N is an all-day recording's line number: its upload position plus one, what the recap cites
	// (docs/assistant/day-recap.md); 0 (absent) for every other recording.
	N int `json:"n,omitempty"`
	// Source is whose text a segment of the transcript of record is in hub transcription (docs/assistant/meeting-minutes.md
	// "The transcript of record"): "hub" or "device"; absent means device. Only the hub sets it, never an upload.
	Source string `json:"source,omitempty"`
	// Echo: the line is Persona's own answer as the meeting's microphone heard it (#5905,
	// docs/assistant/meeting-minutes.md "Persona's echo"), marked by the phone or by the hub's rule (modules/recording/writers/echo.go). Kept,
	// never fed to the minutes, the summary or the follow-up; absent (false) for every other line.
	Echo bool `json:"echo,omitempty"`
	// Speaker is the canonical speaker id ("s1", "s2", ...) assigned on read when a speaker timeline exists
	// (docs/assistant/meeting-minutes.md "Speakers (#17674)"). Only the hub sets it on read; never accepted from uploads.
	Speaker SpeakerID `json:"speaker,omitempty"`
	// SpeakerUnsure is true when the speaker label is uncertain or the meeting's separation quality is low.
	SpeakerUnsure bool `json:"speaker_unsure,omitempty"`
}

// Hit is one search result.
type Hit struct {
	Path string `json:"path"` // relative to the data dir; absolute for a file in one of the ExtraDirs
	Line string `json:"line"`
}

// Entry is a row of a ranked list.
type Entry struct {
	Kind        string            `json:"kind"`
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	OneLine     string            `json:"one_line,omitempty"`
	Importance  string            `json:"importance,omitempty"`
	Relevance   int               `json:"relevance"`
	Tags        []string          `json:"tags,omitempty"`
	Updated     time.Time         `json:"updated"`
	Extra       string            `json:"extra,omitempty"` // projects: short / long · status; chats: dm / group…
	ProjectMode string            `json:"project_mode,omitempty"`
	Freshness   *ProjectFreshness `json:"freshness,omitempty"`
	Sources     []ProjectSource   `json:"sources,omitempty"`
	// Source is where it came from: a chat's source (lark, mail, wechat, github…); a document's, meeting's or event's
	// connector, so an app can show which app a meeting was imported from (#8872).
	Source string `json:"source,omitempty"`
	// RecordingKind is a recording's kind: meeting, note or day (#8731), so a list can mark notes and all-day
	// recordings among meetings. Absent on other kinds, and from a hub before it.
	RecordingKind string `json:"recording_kind,omitempty"`
	// MeetingID is a recording's imported meeting (#8872): the id of the meetings item it overlaps, as the meetings list
	// has it, so a Meetings list shows the two as one row. Absent on other kinds, before a match, and from a hub before it.
	MeetingID string `json:"meeting_id,omitempty"`
	// Paused: as Detail.Paused, on a recording's list entry.
	Paused string `json:"paused,omitempty"`
}

// ErrNotFound: the knowledge base has no such entry. ErrReadOnly: it was opened read-only. The knowledge base returns
// these values (kb.ErrNotFound and kb.ErrReadOnly are the same errors), so a module that writes through a port can
// return and test them without importing the knowledge base.
var (
	ErrNotFound = errors.New("not found")
	ErrReadOnly = errors.New("the knowledge base is read-only")
)

// TaggingFieldLocked: the user set field of t by hand, so no automatic writer changes it.
func TaggingFieldLocked(t *Tagging, field string) bool {
	if slices.Contains(t.LockedFields, field) {
		return true
	}
	return t.Locked && (field == "importance" || field == "relevance" || field == "tags")
}
