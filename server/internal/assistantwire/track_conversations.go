package assistantwire

import "time"

// A space's open conversations (#15991, docs/assistant/health.md "Tracks and kinds"): the apps and an extension granted
// the spaces scope list a space track's conversations without the main list's rule that an extension sees none of a
// space's (GET /api/v1/assistant?space=). An extension is shown only the rows its information policy lets it read.

// trackConversationRoutes list a space track's open conversations and available launchers for starting a space chat.
var trackConversationRoutes = []string{
	"GET /api/v1/assistant/tracks/{slug}/conversations",
	"GET /api/v1/assistant/tracks/launchers",
}

func init() { Routes = append(Routes, trackConversationRoutes...) }

// TrackConversationsLimit is how many rows GET /api/v1/assistant/tracks/{slug}/conversations answers by default;
// TrackConversationsMaxLimit the most its limit may ask for.
const (
	TrackConversationsLimit    = 20
	TrackConversationsMaxLimit = 50
)

// TrackConversationTextMax is the most runes a row's LastText carries.
const TrackConversationTextMax = 200

// The ids a space's conversation is known by: named at this wire boundary only, their JSON bytes are plain strings.
type (
	SpaceMachineID string
	SpaceSessionID string
)

// TrackConversations is GET /api/v1/assistant/tracks/{slug}/conversations's answer.
type TrackConversations struct {
	// MachineID is the assistant's machine, where the space's conversations run.
	MachineID SpaceMachineID `json:"machine_id"`
	// Total counts the space's open conversations the caller is shown, before the limit.
	Total int `json:"total"`
	// Conversations are the newest of them, by UpdatedAt.
	Conversations []TrackConversation `json:"conversations"`
}

// TrackConversation is one open conversation of a space.
type TrackConversation struct {
	SessionID SpaceSessionID `json:"session_id"`
	MachineID SpaceMachineID `json:"machine_id"`
	Title     string         `json:"title"`
	// LastText is the beginning of its latest message, at most TrackConversationTextMax runes.
	LastText  string    `json:"last_text"`
	UpdatedAt time.Time `json:"updated_at"`
	Space     string    `json:"space"`
}

// TrackLaunchers is GET /api/v1/assistant/tracks/launchers's answer: the launchers an extension with the spaces scope
// may offer when starting a space conversation, plus the assistant's current global default.
type TrackLaunchers struct {
	Default   TrackLauncherDefault `json:"default"`
	Launchers []TrackLauncher      `json:"launchers"`
}

// TrackLauncherDefault is the launcher, model and effort used when a space start leaves them empty.
type TrackLauncherDefault struct {
	Launcher string `json:"launcher,omitempty"`
	Model    string `json:"model,omitempty"`
	Effort   string `json:"effort,omitempty"`
}

// TrackLauncher is one healthy assistant launcher available to start a space conversation.
type TrackLauncher struct {
	Name         string   `json:"name"`
	DisplayName  string   `json:"display_name,omitempty"`
	Agent        string   `json:"agent,omitempty"`
	DefaultModel string   `json:"default_model,omitempty"`
	Models       []string `json:"models,omitempty"`
}
