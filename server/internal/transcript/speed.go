package transcript

import (
	"bytes"
	"encoding/json"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/screen"
)

// SpeedObservation is the speed a runtime recorded. Applied marks a settings acknowledgment, rather than a turn's
// last observed usage. A Claude turn already in progress can keep using its previous speed.
type SpeedObservation struct {
	Tier    string
	Model   string // Codex identifies the model alongside its speed
	At      time.Time
	Applied bool
	usage   bool // Claude reply usage
}

// CodexServiceTier distinguishes a missing value (unknown) from native null (Standard).
func CodexServiceTier(raw json.RawMessage) string {
	var tier string
	if len(raw) == 0 || json.Unmarshal(raw, &tier) != nil {
		return ""
	}
	switch tier {
	case "", "standard":
		return "default"
	case "fast":
		return "priority"
	default:
		return tier
	}
}

// SessionSpeed reads only runtime records. In particular, assistant text quoting a confirmation is not evidence.
func SessionSpeed(line []byte) SpeedObservation {
	if bytes.Contains(line, []byte(`"service_tier"`)) {
		var e struct {
			Type      string    `json:"type"`
			Timestamp time.Time `json:"timestamp"`
			Payload   struct {
				Type           string          `json:"type"`
				Model          string          `json:"model"`
				ServiceTier    json.RawMessage `json:"service_tier"`
				ThreadSettings struct {
					Model       string          `json:"model"`
					ServiceTier json.RawMessage `json:"service_tier"`
				} `json:"thread_settings"`
			} `json:"payload"`
		}
		if unmarshalLine(line, &e) == nil {
			switch {
			case e.Type == "event_msg" && e.Payload.Type == "thread_settings_applied":
				return SpeedObservation{Tier: CodexServiceTier(e.Payload.ThreadSettings.ServiceTier), Model: e.Payload.ThreadSettings.Model, At: e.Timestamp, Applied: true}
			case e.Type == "turn_context":
				return SpeedObservation{Tier: CodexServiceTier(e.Payload.ServiceTier), Model: e.Payload.Model, At: e.Timestamp}
			}
		}
	}
	if !bytes.Contains(line, []byte("Fast mode ")) && !bytes.Contains(line, []byte(`"speed"`)) {
		return SpeedObservation{}
	}
	var e struct {
		Type        string          `json:"type"`
		Subtype     string          `json:"subtype"`
		Timestamp   time.Time       `json:"timestamp"`
		IsSidechain bool            `json:"isSidechain"`
		APIError    bool            `json:"isApiErrorMessage"`
		Content     json.RawMessage `json:"content"`
		Message     struct {
			Content json.RawMessage `json:"content"`
			Usage   struct {
				Speed string `json:"speed"`
			} `json:"usage"`
		} `json:"message"`
	}
	if unmarshalLine(line, &e) != nil || e.IsSidechain {
		return SpeedObservation{}
	}
	switch {
	case e.Type == "user" || e.Type == "system" && e.Subtype == "local_command":
		raw := e.Content
		if e.Type == "user" {
			raw = e.Message.Content
		}
		out, ok := strings.CutPrefix(rawString(raw), "<local-command-stdout>")
		if !ok {
			break
		}
		out = strings.TrimSpace(screen.Plain(strings.TrimSuffix(out, "</local-command-stdout>")))
		out = strings.TrimSpace(strings.TrimPrefix(out, "↯"))
		switch outcome, _, _ := strings.Cut(out, " ·"); outcome {
		case "Fast mode ON":
			return SpeedObservation{Tier: "priority", At: e.Timestamp, Applied: true}
		case "Fast mode OFF":
			return SpeedObservation{Tier: "default", At: e.Timestamp, Applied: true}
		}
	case e.Type == "assistant" && !e.APIError:
		switch e.Message.Usage.Speed {
		case "fast":
			return SpeedObservation{Tier: "priority", At: e.Timestamp, usage: true}
		case "standard":
			return SpeedObservation{Tier: "default", At: e.Timestamp, usage: true}
		}
	}
	return SpeedObservation{}
}

// ObserveSpeed retains confirmed Claude settings when a still-running turn reports older usage.
func (m *Meta) ObserveSpeed(line []byte) {
	if speed := SessionSpeed(line); speed.Supersedes(m.Speed) {
		m.Speed = speed
	}
}

// Supersedes keeps the latest native setting, using Claude tool usage as a fallback until a command acknowledges it.
func (s SpeedObservation) Supersedes(previous SpeedObservation) bool {
	return s.Tier != "" && !s.At.IsZero() && (!s.At.Before(previous.At) || s.Applied && previous.usage) &&
		(!s.usage || !previous.Applied)
}
