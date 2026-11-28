package repository

import "github.com/nwverse/persona/server/internal/repositorywire"

// Preserve the delivered import path while contracts live outside Manager code.
const CatalogKey = repositorywire.CatalogKey
const MaxTaskRepositories = repositorywire.MaxTaskRepositories
const MaxRepositoryLinks = repositorywire.MaxRepositoryLinks
const MaxRevision = repositorywire.MaxRevision
const LinkRoleDelivery = repositorywire.LinkRoleDelivery
const LinkRoleReference = repositorywire.LinkRoleReference
const LinkProvenanceUnbound = repositorywire.LinkProvenanceUnbound
const LinkProvenanceExplicit = repositorywire.LinkProvenanceExplicit
const LayoutRoot = repositorywire.LayoutRoot
const LayoutContainer = repositorywire.LayoutContainer
const TaskWorkflowIsolated = repositorywire.TaskWorkflowIsolated
const TaskWorkflowSharedBranch = repositorywire.TaskWorkflowSharedBranch

type Problem = repositorywire.Problem
type RequestedRef = repositorywire.RequestedRef
type Layout = repositorywire.Layout
type CatalogEntry = repositorywire.CatalogEntry
type Catalog = repositorywire.Catalog
type SourceSnapshot = repositorywire.SourceSnapshot
type LinkInput = repositorywire.LinkInput
type ResolvedLink = repositorywire.ResolvedLink
type TaskLink = repositorywire.TaskLink
type LinkProvenance = repositorywire.LinkProvenance
type TaskInput = repositorywire.TaskInput
type TaskRepository = repositorywire.TaskRepository
type TaskSet = repositorywire.TaskSet
type CreateBindings = repositorywire.CreateBindings
type SetInput = repositorywire.SetInput
type LinksInput = repositorywire.LinksInput
type PreparationRepository = repositorywire.PreparationRepository
type PreparationSnapshot = repositorywire.PreparationSnapshot

func ValidateAlias(alias string) error { return repositorywire.ValidateAlias(alias) }
func RepositoryPath(layout Layout, alias string, count int) (string, error) {
	return repositorywire.RepositoryPath(layout, alias, count)
}
func NormalizeRef(ref RequestedRef) (RequestedRef, error) { return repositorywire.NormalizeRef(ref) }
func CloneTransport(raw string, identity Identity) (string, error) {
	return repositorywire.CloneTransport(raw, identity)
}
func NormalizeLink(identity Identity, in LinkInput) (TaskLink, error) {
	return repositorywire.NormalizeLink(identity, in)
}
func NormalizeLinks(identity Identity, input []LinkInput) ([]TaskLink, error) {
	return repositorywire.NormalizeLinks(identity, input)
}
func Bind(current []TaskRepository, input []TaskInput, catalog Catalog) ([]TaskRepository, error) {
	return repositorywire.Bind(current, input, catalog)
}
func CatalogState(row TaskRepository, catalog Catalog) string {
	return repositorywire.CatalogState(row, catalog)
}
func Snapshot(manager, todo string, revision int64, rows []TaskRepository, selected []string) (PreparationSnapshot, error) {
	return repositorywire.Snapshot(manager, todo, revision, rows, selected)
}
func ValidRevision(revision *int64) bool              { return repositorywire.ValidRevision(revision) }
func NormalizeLinkRole(role string) (string, Problem) { return repositorywire.NormalizeLinkRole(role) }
func NormalizeTaskWorkflow(workflow string) (string, Problem) {
	return repositorywire.NormalizeTaskWorkflow(workflow)
}
func LinkKey(identity Identity, link TaskLink) string { return repositorywire.LinkKey(identity, link) }
func DefaultAlias(identity Identity) string           { return repositorywire.DefaultAlias(identity) }

func ReconcileLinkEdits(current, next []TaskLink, input []LinkInput) {
	repositorywire.ReconcileLinkEdits(current, next, input)
}
func ParseTaskLink(raw string) (Identity, TaskLink, error) { return repositorywire.ParseTaskLink(raw) }
func ParseTaskLinkInput(in LinkInput) (Identity, TaskLink, error) {
	return repositorywire.ParseTaskLinkInput(in)
}
func MergeTaskLinks(current []TaskRepository, urls []string, catalog Catalog) ([]TaskRepository, error) {
	return repositorywire.MergeTaskLinks(current, urls, catalog)
}
func MergeTaskLinkDetails(current []TaskRepository, input []LinkInput, catalog Catalog, provenance LinkProvenance) ([]TaskRepository, error) {
	return repositorywire.MergeTaskLinkDetails(current, input, catalog, provenance)
}

func IsWindowsReservedName(name string) bool { return repositorywire.IsWindowsReservedName(name) }
