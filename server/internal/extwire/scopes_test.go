package extwire

import (
	"errors"
	"maps"
	"strings"
	"testing"
)

// A domain's scopes join the table only when every one of them is a route the table does not have yet, written as a
// pattern, with a scope; a refused merge leaves the table as it was, even for the entries beside the refused one.
func TestMergeRouteScopes(t *testing.T) {
	t.Parallel()
	table := map[string]string{"GET /api/v1/a": ScopeKBRead}
	if err := mergeRouteScopes(table, map[string]string{
		"PUT /api/v1/a":            ScopeKBWrite,
		"/api/v1/extensions/{id}/": ScopeCallPrefix,
	}); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"GET /api/v1/a": ScopeKBRead, "PUT /api/v1/a": ScopeKBWrite, "/api/v1/extensions/{id}/": ScopeCallPrefix}
	if !maps.Equal(table, want) {
		t.Fatalf("merged into %v, want %v", table, want)
	}
	for _, c := range []struct {
		route, scope string
		want         error
	}{
		{"GET /api/v1/a", ScopeKBWrite, errRouteScopeTaken},
		{"GET /api/v1/a", ScopeKBRead, errRouteScopeTaken}, // the same decision twice is still two places deciding it
		{"/api/v1/extensions/{id}/", ScopeCallPrefix, errRouteScopeTaken},
		{"", ScopeKBRead, errRouteScopeNoRoute},
		{"GET", ScopeKBRead, errRouteScopeNoRoute},
		{"GET api/v1/b", ScopeKBRead, errRouteScopeNoRoute},
		{" /api/v1/b", ScopeKBRead, errRouteScopeNoRoute},
		{"GET  /api/v1/b", ScopeKBRead, errRouteScopeNoRoute},
		{"GET /api/v1/b c", ScopeKBRead, errRouteScopeNoRoute},
		{"GET /api/v1/b", "", errRouteScopeNoScope},
	} {
		before := maps.Clone(table)
		err := mergeRouteScopes(table, map[string]string{c.route: c.scope, "GET /api/v1/fine": ScopeKBRead})
		if !errors.Is(err, c.want) || !strings.Contains(err.Error(), "\""+c.route+"\"") {
			t.Errorf("%q: %q: got %v, want %v naming the route", c.route, c.scope, err, c.want)
		}
		if !maps.Equal(table, before) {
			t.Errorf("%q: %q: a refused merge changed the table to %v", c.route, c.scope, table)
		}
	}
}

// addRouteScopes, the way in a domain's file uses, panics at start-up on a route RouteScopes has already, and leaves
// RouteScopes as it was.
func TestAddRouteScopesPanicsOnADuplicate(t *testing.T) {
	const route = "GET /api/v1/inbox" // from inbox_scopes.go
	before := maps.Clone(RouteScopes)
	if before[route] != ScopeInboxRead {
		t.Fatalf("RouteScopes gives %q %q, want %q (inbox_scopes.go)", route, before[route], ScopeInboxRead)
	}
	defer func() {
		err, _ := recover().(error)
		if !errors.Is(err, errRouteScopeTaken) {
			t.Errorf("adding %q again: recovered %v, want a panic with %v", route, err, errRouteScopeTaken)
		}
		if !maps.Equal(RouteScopes, before) {
			t.Error("a refused addRouteScopes changed RouteScopes")
		}
	}()
	addRouteScopes(map[string]string{route: ScopeInboxWrite, "GET /api/v1/never-added": ScopeKBRead})
}

// A scope that says it reads opens routes that only read: every route of a scope named *.read is a GET (#17851). An
// extension granted one was told on the install page that it reads, and a route added to such a scope later asks the
// user nothing again, so the name is the promise: a route that changes, sends or spends takes a scope of its own.
func TestReadScopesOnlyGet(t *testing.T) {
	t.Parallel()
	for route, scope := range RouteScopes {
		if !strings.HasSuffix(scope, ".read") {
			continue
		}
		if method, _, ok := strings.Cut(route, " "); !ok || method != "GET" {
			t.Errorf("%q is opened by %s, a read scope: only GET routes are; give it a scope that says what it does", route, scope)
		}
	}
	if RouteScopes["GET /api/v1/agents/triage"] != ScopeAgentsRead {
		t.Errorf("GET /api/v1/agents/triage needs %q, want %s (agents_scopes.go)", RouteScopes["GET /api/v1/agents/triage"], ScopeAgentsRead)
	}
	if scope, ok := RouteScopes["PUT /api/v1/agents/triage"]; ok {
		t.Errorf("PUT /api/v1/agents/triage is opened by %s: changing the triage's switches is for full credentials only", scope)
	}
}
