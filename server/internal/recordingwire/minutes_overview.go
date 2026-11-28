package recordingwire

import "encoding/json"

// MinutesConclusion is a core conclusion: where the meeting landed, citing a line, or a photo a bullet cites.
type MinutesConclusion struct {
	Text   string   `json:"text"`
	Strong []string `json:"strong,omitempty"`
	At     float64  `json:"at"`
	Photo  int      `json:"photo,omitempty"`
}

// MinutesGroup is a heading over topics; Title is "" for the single untitled group.
type MinutesGroup struct {
	Title  string         `json:"title"`
	Topics []MinutesTopic `json:"topics"`
}

// MinutesTopic is a topic card: Icon one of minutesTopicIcons.
type MinutesTopic struct {
	Title   string          `json:"title"`
	Icon    string          `json:"icon"`
	Bullets []MinutesBullet `json:"bullets"`
}

// MinutesBullet is a point (Who as the point's) or a decision (Decision) of the minutes, with the same Text, At and
// Photo; Strong the places of Text set in bold (strongSpans).
type MinutesBullet struct {
	Text     string   `json:"text"`
	Strong   []string `json:"strong,omitempty"`
	At       float64  `json:"at"`
	Photo    int      `json:"photo,omitempty"`
	Who      string   `json:"who,omitempty"`
	Decision bool     `json:"decision,omitempty"`
}

// MinutesOverview is the minutes as one summary card: optional, present only when every point and decision is a
// bullet of one of its topics.
type MinutesOverview struct {
	Headline    string              `json:"headline,omitempty"`
	Conclusions []MinutesConclusion `json:"conclusions,omitempty"`
	// Groups: one titled "" (no heading), or 2 to 4 with distinct titles; never empty, and no group or topic is.
	Groups []MinutesGroup `json:"groups"`
}

// UnmarshalJSON reads Minutes with its overview and debates decoded on their own: one this hub cannot read (another
// hub's shape, a hand edit) or that cleans down to nothing counts as absent, as on the clients, and never makes the
// rest unreadable.
func (m *Minutes) UnmarshalJSON(data []byte) error {
	type plain Minutes
	var aux struct {
		*plain
		Overview json.RawMessage `json:"overview"` // shallower than plain's: it takes the field
		Debates  json.RawMessage `json:"debates"`
	}
	aux.plain = (*plain)(m)
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	m.Overview = nil
	if len(aux.Overview) > 0 {
		var o MinutesOverview
		if json.Unmarshal(aux.Overview, &o) == nil && o.hasBullet() {
			m.Overview = &o
		}
	}
	m.Debates = decodeMinutesDebates(aux.Debates, len(m.Decisions))
	return nil
}

func (o *MinutesOverview) hasBullet() bool {
	for _, g := range o.Groups {
		for _, t := range g.Topics {
			if len(t.Bullets) > 0 {
				return true
			}
		}
	}
	return false
}
