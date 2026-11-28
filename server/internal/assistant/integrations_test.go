package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/channel"
)

func TestIntegrationProjectionSettingsAccess(t *testing.T) {
	s := New(Options{})
	for _, tt := range []struct {
		name, method, path, role string
		want                     int
	}{
		{"catalog", "GET", "/api/v1/assistant/integrations", roleFull, http.StatusOK},
		{"unknown detail", "GET", "/api/v1/assistant/integrations/unknown/example", roleFull, http.StatusNotFound},
		{"answer role", "GET", "/api/v1/assistant/integrations", "answer", http.StatusForbidden},
		{"verification role", "GET", "/api/v1/assistant/integrations/extension/example", "readonly", http.StatusForbidden},
		{"extension role", "GET", "/api/v1/assistant/integrations", roleExtension, http.StatusForbidden},
		{"write", "PUT", "/api/v1/assistant/integrations/service/mail", roleFull, http.StatusMethodNotAllowed},
	} {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			r := httptest.NewRequest(tt.method, tt.path, nil)
			r.Header.Set(RoleHeader, tt.role)
			s.ServeHTTP(w, r)
			if w.Code != tt.want {
				t.Fatalf("HTTP %d, want %d: %s", w.Code, tt.want, w.Body)
			}
		})
	}
}

type integrationFixture struct {
	input                integrationInput
	configurationCalls   []string
	configurationFailure error
}

func (f *integrationFixture) IntegrationExtensions(context.Context) ([]assistantwire.IntegrationExtensionRead, error) {
	return f.input.extensions, f.input.extensionsErr
}

func (f *integrationFixture) IntegrationChannels(context.Context) ([]channel.View, error) {
	return f.input.channels, f.input.channelsErr
}

func (f *integrationFixture) IntegrationConnectors(context.Context) ([]assistantwire.IntegrationConnectorRead, error) {
	return f.input.connectors, f.input.connectorsErr
}

func (f *integrationFixture) IntegrationSubscriptions(context.Context) ([]assistantwire.IntegrationSubscriptionRead, error) {
	return f.input.subscriptions, f.input.subscriptionsErr
}

func (f *integrationFixture) IntegrationServices(context.Context) []assistantwire.IntegrationServiceRead {
	return f.input.services
}

func (f *integrationFixture) IntegrationConfiguration(_ context.Context, extension string) ([]string, error) {
	f.configurationCalls = append(f.configurationCalls, extension)
	return []string{"inbox", "connection", "channel", "advanced", "", "future"}, f.configurationFailure
}

func integrationGET(t *testing.T, target *Service, suffix string, status int, out any) string {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, assistantwire.IntegrationsCatalogPath+suffix, nil)
	// Manager identity comes from the service, even if the transport gets a contradictory header.
	r.Header.Set(assistantwire.ManagerHeader, "other-manager")
	r.Header.Set(RoleHeader, roleFull)
	w := httptest.NewRecorder()
	target.ServeHTTP(w, r)
	if w.Code != status {
		t.Fatalf("%s: HTTP %d, want %d: %s", suffix, w.Code, status, w.Body)
	}
	if w.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatalf("%s can be cached", suffix)
	}
	if out != nil {
		if err := json.Unmarshal(w.Body.Bytes(), out); err != nil {
			t.Fatalf("%s: %v", suffix, err)
		}
	}
	return w.Body.String()
}

func TestIntegrationProjectionCatalogAndOwnedDetail(t *testing.T) {
	a, b := New(Options{}), New(Options{ID: "m-example"})
	fixture := &integrationFixture{input: integrationInput{
		extensions: []assistantwire.IntegrationExtensionRead{
			{ID: "bridge", Name: "Bridge for personal use", Origin: assistantwire.IntegrationOriginInstalledExtension, Availability: integrationAvailable(), Contributions: map[string][]string{
				"channel": {"messages"}, "connectors": {"records"}, "tools": {"lookup"},
			}},
			{ID: "voice", Name: "Voice", Origin: assistantwire.IntegrationOriginBuiltinExtension, Availability: integrationAvailable(), Contributions: map[string][]string{"voice": {"voice"}}},
		},
		channels: []channel.View{
			{ID: "messages", Kind: channel.KindIM, Source: channel.SourcePlugin, Plugin: "bridge", State: channel.StateConnected, Account: &channel.Account{Name: "private-account-field"}},
			{ID: "voice", Kind: channel.KindVoice, Source: channel.SourcePlugin, Plugin: "voice", State: channel.StateConnected, ReadOnly: true},
			{ID: "uninstalled", Source: channel.SourceMarket},
		},
		connectors: []assistantwire.IntegrationConnectorRead{{ID: "records", OwnerExtension: "bridge", Availability: integrationAvailable(), StoresData: true}},
		subscriptions: []assistantwire.IntegrationSubscriptionRead{
			{ID: "sub-records", Connector: "records", Availability: integrationUnavailable(assistantwire.IntegrationReasonRateLimited)},
			{ID: "records", Name: "From Shortcuts", Connector: "records", Extension: "bridge", Push: true, Availability: integrationAvailable()},
		},
		services: []assistantwire.IntegrationServiceRead{
			{ID: "mail", Name: "Mail", Connector: "mail", ModuleFeature: "module.mail", Capability: assistantwire.IntegrationFilterCapabilityData,
				Availability: assistantwire.IntegrationAvailability{State: assistantwire.IntegrationStateNeedsSetup}},
		},
	}}
	a.integrationReads = fixture
	b.integrationReads = &integrationFixture{input: integrationInput{extensions: []assistantwire.IntegrationExtensionRead{
		{ID: "bridge", Name: "Bridge for team use", Origin: assistantwire.IntegrationOriginInstalledExtension, Availability: assistantwire.IntegrationAvailability{State: assistantwire.IntegrationStateDisabled}},
	}}}

	var list assistantwire.IntegrationCatalog
	body := integrationGET(t, a, "", 200, &list)
	if len(fixture.configurationCalls) != 0 || strings.Contains(body, `"sections"`) || strings.Contains(body, "private-account-field") {
		t.Fatalf("catalog performed a detail read or exposed settings values: %s", body)
	}
	if want := (assistantwire.IntegrationCounts{All: 4, Channels: 2, Data: 3, Tools: 1}); list.Counts != want {
		t.Fatalf("counts %+v, want %+v", list.Counts, want)
	}
	origins := []assistantwire.IntegrationOrigin{
		assistantwire.IntegrationOriginInstalledExtension, assistantwire.IntegrationOriginBuiltinExtension,
		assistantwire.IntegrationOriginStandalonePushSource, assistantwire.IntegrationOriginBuiltinService,
	}
	for i, item := range list.Items {
		if item.Ref.Manager != assistantwire.AssistantID || item.Origin != origins[i] {
			t.Fatalf("catalog ref or origin %v", item)
		}
	}
	bridge := list.Items[0]
	if !slices.Equal(bridge.Capabilities, assistantwire.IntegrationFilterCapabilities) || len(bridge.CapabilityAvailability) != 1 ||
		bridge.CapabilityAvailability[0] != (assistantwire.IntegrationCapabilityAvailability{
			Capability: assistantwire.IntegrationFilterCapabilityData, Availability: integrationUnavailable(assistantwire.IntegrationReasonRateLimited),
		}) {
		t.Fatalf("merged capabilities and status %+v", bridge)
	}
	var detail assistantwire.IntegrationDetail
	body = integrationGET(t, a, "/extension/bridge", 200, &detail)
	if !slices.Equal(fixture.configurationCalls, []string{"bridge"}) || detail.Item.Ref != bridge.Ref || strings.Contains(body, "private-account-field") {
		t.Fatalf("extension detail: %s", body)
	}
	sectionIDs := make(map[assistantwire.IntegrationSectionKind][]assistantwire.IntegrationResourceID)
	for _, section := range detail.Sections {
		if section.Ref.Manager != bridge.Ref.Manager || section.Ref.Kind == assistantwire.IntegrationKindPushSource {
			t.Fatalf("section outside its owner or resource: %+v", section)
		}
		sectionIDs[section.Kind] = append(sectionIDs[section.Kind], section.Ref.ID)
		if section.Kind == assistantwire.IntegrationSectionKindExtensionConfiguration &&
			!slices.Equal(section.ConfigurationGroups, []assistantwire.IntegrationConfigurationGroup{"advanced", "general"}) {
			t.Fatalf("settings groups: %+v", section)
		}
	}
	if !slices.Equal(sectionIDs[assistantwire.IntegrationSectionKindConversationAndPush], []assistantwire.IntegrationResourceID{"messages", "bridge"}) ||
		!slices.Equal(sectionIDs[assistantwire.IntegrationSectionKindInboxAndSync], []assistantwire.IntegrationResourceID{"records", "bridge", "sub-records"}) ||
		!slices.Equal(sectionIDs[assistantwire.IntegrationSectionKindInformationAccess], []assistantwire.IntegrationResourceID{"bridge"}) {
		t.Fatalf("section owners: %v", sectionIDs)
	}
	var push assistantwire.IntegrationDetail
	integrationGET(t, a, "/push_source/records", 200, &push)
	if len(push.Sections) != 1 || push.Sections[0].Ref != push.Item.Ref {
		t.Fatalf("standalone push settings %+v", push)
	}
	var other assistantwire.IntegrationDetail
	integrationGET(t, b, "/extension/bridge", 200, &other)
	if other.Item.Ref.Manager != "m-example" || other.Item.Name != "Bridge for team use" || other.Item.Availability.State != assistantwire.IntegrationStateDisabled {
		t.Fatalf("same id from another assistant: %+v", other)
	}
	integrationGET(t, b, "/connector/records", 404, nil)
}

func TestIntegrationProjectionOwnershipPrecedence(t *testing.T) {
	book := newIntegrationBook("assistant", integrationInput{
		extensions: []assistantwire.IntegrationExtensionRead{
			{ID: "registered", Availability: integrationAvailable()},
			{ID: "declared", Availability: integrationAvailable(), Contributions: map[string][]string{"channels": {"same"}, "connectors": {"shared", "explicit", "live", "other"}}},
			{ID: "same", Availability: integrationAvailable(), Contributions: map[string][]string{"connectors": {"other"}}},
			{ID: "second", Availability: integrationAvailable(), Contributions: map[string][]string{"connector": {"shared", "live"}}},
			{ID: "legacy", Availability: integrationAvailable()},
			{ID: "kind-only", Availability: integrationAvailable(), Contributions: map[string][]string{"channel": {"other"}}},
			{ID: "missing", Availability: integrationAvailable()},
		},
		channels: []channel.View{{ID: "same", Source: channel.SourcePlugin}, {ID: "kind-only", Source: channel.SourcePlugin}},
		connectors: []assistantwire.IntegrationConnectorRead{
			{ID: "shared"}, {ID: "explicit"}, {ID: "live", OwnerExtension: "registered"}, {ID: "legacy"},
			{ID: "same"}, {ID: "kind-only"}, {ID: "missing", OwnerExtension: "absent"},
			{ID: "other", Builtin: true},
		},
		subscriptions: []assistantwire.IntegrationSubscriptionRead{
			{ID: "saved", Connector: "explicit", Extension: "registered"},
			{ID: "unscoped", Connector: "shared"},
		},
	})
	for _, tt := range []struct {
		kind    assistantwire.IntegrationKind
		id      string
		owner   string
		problem assistantwire.IntegrationRelationshipIssue
	}{
		{"channel", "same", "declared", ""},
		{"channel", "kind-only", "", ""},
		{"connector", "shared", "", "ambiguous_owner"},
		{"connector", "explicit", "declared", ""},
		{"connector", "live", "registered", ""},
		{"connector", "legacy", "legacy", ""},
		{"connector", "other", "", ""},
		{"connector", "same", "", ""},
		{"connector", "kind-only", "kind-only", ""},
		{"connector", "missing", "", "owner_missing"},
		{"subscription", "saved", "", "owner_conflict"},
		{"subscription", "unscoped", "shared", ""},
	} {
		key := integrationResource(tt.kind, assistantwire.IntegrationResourceID(tt.id))
		if string(book.owners[key].id) != tt.owner || book.nodes[key].item.RelationshipIssue != tt.problem {
			t.Errorf("%s/%s owner %+v, issue %s", tt.kind, tt.id, book.owners[key], book.nodes[key].item.RelationshipIssue)
		}
	}
}

func TestIntegrationProjectionIsolatesFailedReads(t *testing.T) {
	s := New(Options{})
	readError := errors.New("private diagnostic from the provider")
	fixture := &integrationFixture{
		input: integrationInput{
			extensions: []assistantwire.IntegrationExtensionRead{
				{ID: "example", Availability: integrationAvailable(), Contributions: map[string][]string{"connector": {"one"}, "channel": {"chat"}}},
			},
			channels:      []channel.View{{ID: "chat", Source: channel.SourcePlugin, Plugin: "example", State: channel.StateConnected}},
			connectorsErr: readError,
			subscriptions: []assistantwire.IntegrationSubscriptionRead{{ID: "shortcuts", Push: true, Availability: integrationAvailable()}},
		},
		configurationFailure: readError,
	}
	s.integrationReads = fixture
	var catalog assistantwire.IntegrationCatalog
	body := integrationGET(t, s, "", 200, &catalog)
	if strings.Contains(body, readError.Error()) || catalog.Counts.All != 2 || catalog.Counts.Channels != 1 ||
		catalog.Availability[2].Source != assistantwire.IntegrationSourceConnectors ||
		catalog.Availability[2].Availability.Reason != assistantwire.IntegrationReasonReadFailed ||
		len(catalog.Items[0].CapabilityAvailability) != 1 ||
		catalog.Items[0].CapabilityAvailability[0].Capability != assistantwire.IntegrationFilterCapabilityData {
		t.Fatalf("mixed read: %s", body)
	}
	var detail assistantwire.IntegrationDetail
	body = integrationGET(t, s, "/extension/example", 200, &detail)
	if strings.Contains(body, readError.Error()) {
		t.Fatalf("provider diagnostics escaped: %s", body)
	}
	if !slices.ContainsFunc(detail.Sections, func(section assistantwire.IntegrationSection) bool {
		return section.Ref.Kind == assistantwire.IntegrationKindChannel && section.Edit == assistantwire.IntegrationSectionEditSettings
	}) || !slices.ContainsFunc(detail.Sections, func(section assistantwire.IntegrationSection) bool {
		return section.Kind == assistantwire.IntegrationSectionKindExtensionConfiguration && section.Availability.Reason == assistantwire.IntegrationReasonReadFailed
	}) {
		t.Fatalf("detail sections: %+v", detail.Sections)
	}
	integrationGET(t, s, "/connector/one", 503, nil)
	integrationGET(t, s, "/push_source/shortcuts", 200, nil)
	integrationGET(t, s, "/extension/missing", 404, nil)
}

func TestIntegrationProjectionModuleOffPreservesLegacyMailLink(t *testing.T) {
	s := New(Options{})
	fixture := &integrationFixture{input: integrationInput{
		services: []assistantwire.IntegrationServiceRead{
			{ID: "mail", Connector: "mail", ModuleFeature: "module.mail", Capability: assistantwire.IntegrationFilterCapabilityData,
				Availability: assistantwire.IntegrationAvailability{State: assistantwire.IntegrationStateModuleOff}},
			{ID: "feishu-bot", Channel: channel.Feishu, Connector: "larkcli", ModuleFeature: "module.lark", Capability: assistantwire.IntegrationFilterCapabilityChannels,
				Availability: assistantwire.IntegrationAvailability{State: assistantwire.IntegrationStateModuleOff}},
		},
		extensions: []assistantwire.IntegrationExtensionRead{
			{ID: "gateway", Availability: integrationAvailable(), Contributions: map[string][]string{"channel": {"gateway-chat"}, "connector": {"lark"}}},
		},
		channels: []channel.View{{ID: "gateway-chat", Source: channel.SourcePlugin, Plugin: "gateway", State: channel.StateConnected}},
		connectors: []assistantwire.IntegrationConnectorRead{
			{ID: "larkcli", Builtin: true, StoresData: true}, {ID: "lark", OwnerExtension: "gateway", StoresData: true, Availability: integrationAvailable()},
		},
		subscriptions: []assistantwire.IntegrationSubscriptionRead{{ID: "saved-mail", Connector: "mail", Availability: assistantwire.IntegrationAvailability{State: assistantwire.IntegrationStateDisabled}}},
	}}
	s.integrationReads = fixture
	var catalog assistantwire.IntegrationCatalog
	integrationGET(t, s, "", 200, &catalog)
	if want := (assistantwire.IntegrationCounts{All: 1, Channels: 1, Data: 1}); catalog.Counts != want ||
		catalog.Items[0].Ref.ID != "gateway" {
		t.Fatalf("module-off catalog produced %+v", catalog)
	}
	for _, entry := range catalog.Availability[4:] {
		if entry.Ref == nil || entry.Availability.State != assistantwire.IntegrationStateModuleOff {
			t.Fatalf("module state %+v", entry)
		}
	}
	var mail assistantwire.IntegrationDetail
	integrationGET(t, s, "/service/mail", 200, &mail)
	if mail.Item.Availability.State != assistantwire.IntegrationStateModuleOff || len(mail.Sections) != 1 ||
		mail.Sections[0].Ref.Kind != assistantwire.IntegrationKindSubscription || mail.Sections[0].Ref.ID != "saved-mail" {
		t.Fatalf("dormant mail settings %+v", mail)
	}
	var feishu assistantwire.IntegrationDetail
	integrationGET(t, s, "/service/feishu-bot", 200, &feishu)
	if feishu.Item.Availability.State != assistantwire.IntegrationStateModuleOff || len(feishu.Sections) != 0 {
		t.Fatalf("dormant Lark settings %+v", feishu)
	}
}

func TestIntegrationProjectionCapabilityInHello(t *testing.T) {
	b := NewBusHub(BusOptions{Hub: "http://127.0.0.1:18660", Token: "test-token"})
	params := b.client.HelloParams().(map[string]any)
	if !slices.Contains(params["capabilities"].([]string), assistantwire.CapabilityIntegrationsProjection) {
		t.Fatal("manager.hello does not advertise the Integrations projection")
	}
}
