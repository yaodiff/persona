package modapi

import (
	"context"
	"github.com/nwverse/persona/sdk/go/wire"
	"github.com/nwverse/persona/server/internal/wireids"
)

// RoutineContribution is the host-attributed, read-only content of one installed
// extension's routine provider, retained alongside a run's other fixed facts.
type RoutineContribution struct {
	ExtensionID    wireids.ExtensionID        `json:"extension_id"`
	ContributionID wire.RoutineContributionID `json:"contribution_id"`
	Title          string                     `json:"title"`
	Facts          []string                   `json:"facts,omitempty"`
	Sections       []RoutineSection           `json:"sections,omitempty"`
}

// PlainLines is the reading order used by plain-text clients and delivery channels.
func (c RoutineContribution) PlainLines() []string {
	lines := []string{c.Title}
	lines = append(lines, c.Facts...)
	for _, section := range c.Sections {
		lines = append(lines, section.Title, section.Body)
	}
	return lines
}

// RoutineSection is a report heading and its plain-text body.
type RoutineSection struct {
	Title string `json:"title"`
	Body  string `json:"body"`
}

// RoutineContributionSource collects provider content for a local calendar day.
// The caller supplies only the day and language. The manager checks grants and
// bounds the collection before handing any data back to the host.
type RoutineContributionSource interface {
	RoutineContributions(ctx context.Context, day, language string) []RoutineContribution
}
