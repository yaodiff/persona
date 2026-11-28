package assistantwire

import (
	"encoding/json"

	"github.com/nwverse/persona/server/internal/buswire"
)

// infoAccessRoutes are who may read the assistant's sensitive information and which categories a conversation
// carries (#13246, docs/assistant/health.md "Using information by scene"). Only a full-access client (the user in the
// app) changes either; an extension's token or an agent never does.
var infoAccessRoutes = []string{
	"GET /api/v1/assistant/extension-trust",
	"PUT /api/v1/assistant/extension-trust/{id}",
	"GET /api/v1/assistant/conversations/{id}/categories",
	"PUT /api/v1/assistant/conversations/{id}/categories",
}

func init() { Routes = append(Routes, infoAccessRoutes...) }

// MethodAssistantExtensionRead is one of assistant.conversations, assistant.messages and assistant.memory asked for
// an extension, the extension manager saying which (AssistantExtensionRead): what is shown is decided by that
// extension's trust and grants. The plain methods are read as a third party's with no grant.
const MethodAssistantExtensionRead = "assistant.extension_read"

var AssistantExtensionReadMethod = buswire.Method[AssistantExtensionRead, json.RawMessage]{Name: MethodAssistantExtensionRead}

// AssistantExtensionRead is assistant.extension_read's params. Extension is the extension manager's word, never the
// extension's own.
type AssistantExtensionRead struct {
	Extension string          `json:"extension"`
	Method    string          `json:"method"`
	Params    json.RawMessage `json:"params,omitempty"`
}

// MethodExtensionTrustForget is ExtensionTrustForget → {}: an extension was removed, and what the user let it read
// goes with it (installed again, under the same id or not, it starts as a third party's with no grant). An older
// assistant answers "unknown method".
const MethodExtensionTrustForget = "assistant.extension_trust_forget"

var ExtensionTrustForgetMethod = buswire.Method[ExtensionTrustForget, buswire.None]{Name: MethodExtensionTrustForget}

// ExtensionTrustForget is assistant.extension_trust_forget's params.
type ExtensionTrustForget struct {
	Extension string `json:"extension"`
}

// Trust an extension has (ExtensionTrust.Trust).
const (
	// ExtensionTrustOwner: the user's own extension (such as their memory extension) reads everything.
	ExtensionTrustOwner = "owner"
	// ExtensionTrustThirdParty: a third party's reads sensitive information only of the categories it was granted.
	ExtensionTrustThirdParty = "third_party"
)

// ExtensionTrust is GET /api/v1/assistant/extension-trust's item and PUT …/extension-trust/{id}'s body: whether an
// extension is the user's own, and the sensitive categories a third-party one may read (replaced as a whole).
type ExtensionTrust struct {
	Extension  string   `json:"extension,omitempty"`
	Trust      string   `json:"trust"`
	Categories []string `json:"categories"`
}

// ConversationCategories is GET …/conversations/{id}/categories' answer: the categories the conversation carries,
// those of its track (Floor, which nobody takes away) among them, and the ones the user took away.
type ConversationCategories struct {
	Categories []string `json:"categories"`
	Floor      []string `json:"floor"`
	Removed    []string `json:"removed"`
}

// ConversationCategoriesChange is PUT …/conversations/{id}/categories' body: categories the user adds back or takes
// away. A track's own categories cannot be taken away.
type ConversationCategoriesChange struct {
	Add    []string `json:"add,omitempty"`
	Remove []string `json:"remove,omitempty"`
}
