package recordingwire

import "time"

// MinutesItem is a decision or an open question; At is seconds from the recording's start (a segment's start, or the
// photo's time when it cites photo Photo).
type MinutesItem struct {
	Text  string  `json:"text"`
	At    float64 `json:"at"`
	Photo int     `json:"photo,omitempty"`
}

// MinutesPoint is a point discussed; Who only when the transcript or the calendar names the person.
type MinutesPoint struct {
	Text  string  `json:"text"`
	At    float64 `json:"at"`
	Photo int     `json:"photo,omitempty"`
	Who   string  `json:"who,omitempty"`
}

// MinutesAction is an action item: a suggested to-do until the user confirms it (TodoID then).
type MinutesAction struct {
	ID      string  `json:"id"`
	Who     string  `json:"who,omitempty"`
	What    string  `json:"what"`
	By      string  `json:"by,omitempty"`       // the deadline as said
	DueDate string  `json:"due_date,omitempty"` // YYYY-MM-DD, when By is one date
	At      float64 `json:"at"`
	Photo   int     `json:"photo,omitempty"`
	TodoID  string  `json:"todo_id,omitempty"`
}

// Minutes is minutes.json, and detail.minutes of GET /api/v1/kb/recordings/{id}.
type Minutes struct {
	Version     int             `json:"version"`  // counts the minutes written for this recording
	Revision    int64           `json:"revision"` // the recording revision they were made from
	GeneratedAt time.Time       `json:"generated_at"`
	Title       string          `json:"title"`
	When        string          `json:"when"` // Start–End in one fixed format, for older clients and the Markdown
	Where       string          `json:"where,omitempty"`
	Attendees   []string        `json:"attendees,omitempty"`
	Points      []MinutesPoint  `json:"points,omitempty"`
	Decisions   []MinutesItem   `json:"decisions,omitempty"`
	Actions     []MinutesAction `json:"actions,omitempty"`
	Open        []MinutesItem   `json:"open,omitempty"`
	// Overview is the same minutes as one summary card (#6563, minutes_overview.go): only when every point and
	// decision is a bullet of one of its topics. Minutes from before it, or from an older hub, have none.
	Overview *MinutesOverview `json:"overview,omitempty"`
	// Debates are the real disagreements of the meeting (#16728, minutes_debates.go): omitted when there is none.
	Debates []MinutesDebate `json:"debates,omitempty"`
	Partial bool            `json:"partial,omitempty"` // the transcript's end was not read
	// Language is what they are written in, their Markdown's headings too (language.go): an i18n code (zh-Hans, en,
	// ja); minutes written before it was kept have none, and are Chinese.
	Language string `json:"language,omitempty"`
	Markdown string `json:"markdown"`
	// Start and End are the recording's span When says, in the user's zone (#4713). Clients format them with their own
	// localized dates and show When only without them (minutes from an older hub).
	Start time.Time `json:"start,omitzero"`
	End   time.Time `json:"end,omitzero"`

	// ModelTitle is the title the model wrote for them (titleLine; "" none), which settles the recording's AITitle
	// when they are stored (#8873); never in minutes.json.
	ModelTitle string `json:"-"`
}
