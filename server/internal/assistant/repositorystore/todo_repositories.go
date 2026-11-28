// Package repositorystore keeps to-do repository associations in the assistant manager's database.
package repositorystore

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"slices"

	"github.com/jackc/pgx/v5"
	"github.com/nwverse/persona/server/internal/assistant/externalsourcestore"
	"github.com/nwverse/persona/server/internal/assistant/repository"
	"github.com/nwverse/persona/server/internal/db"
)

// Store adds repository persistence to the manager's ordinary database store.
// Embedding preserves every unrelated assistant store capability on the same connection pool.
type Store struct {
	*db.DB
	*externalsourcestore.Store
}

// New wraps the assistant manager's database without opening another pool.
func New(database *db.DB) *Store {
	return &Store{DB: database, Store: externalsourcestore.New(database)}
}

// The database carries link provenance alongside the existing wire fields.
type storedTodoRepository struct {
	repository.TaskRepository
	Links []storedTodoRepositoryLink `json:"links"`
}

type storedTodoRepositoryLink struct {
	repository.TaskLink
	Provenance repository.LinkProvenance `json:"provenance,omitempty"`
}

func decodeTodoRepositories(id string, revision int64, raw []byte) (repository.TaskSet, error) {
	out := repository.TaskSet{SchemaVersion: 1, TodoID: id, Revision: revision, Repositories: []repository.TaskRepository{}}
	if len(raw) == 0 {
		return out, nil
	}
	var rows []storedTodoRepository
	if err := json.Unmarshal(raw, &rows); err != nil {
		return out, err
	}
	if rows == nil {
		return out, repository.Problem("store_unavailable")
	}
	out.Repositories = make([]repository.TaskRepository, len(rows))
	for i, row := range rows {
		out.Repositories[i] = row.TaskRepository
		if row.Links != nil {
			out.Repositories[i].Links = make([]repository.TaskLink, len(row.Links))
		}
		for j, record := range row.Links {
			link := record.TaskLink
			switch record.Provenance {
			case repository.LinkProvenanceUnbound, repository.LinkProvenanceExplicit:
				link.Provenance = record.Provenance
			}
			out.Repositories[i].Links[j] = link
		}
	}
	return out, nil
}

func (d *Store) TodoRepositories(ctx context.Context, id string) (repository.TaskSet, error) {
	var revision int64
	var raw []byte
	err := d.QueryRow(ctx, `SELECT COALESCE(r.revision,0), r.repositories FROM todos t LEFT JOIN todo_repository_sets r ON r.todo_id=t.id WHERE t.id=$1`, id).Scan(&revision, &raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return repository.TaskSet{}, db.ErrTodoNotFound
	}
	if err != nil {
		return repository.TaskSet{}, err
	}
	return decodeTodoRepositories(id, revision, raw)
}

// ListTodoRepositories loads saved repository associations for the named to-dos in one query.
func (d *Store) ListTodoRepositories(ctx context.Context, ids []string) (map[string][]repository.TaskRepository, error) {
	out := make(map[string][]repository.TaskRepository, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	err := d.Transaction(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT todo_id, repositories FROM todo_repository_sets WHERE todo_id = ANY($1)`, ids)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var todoID string
			var raw []byte
			if err := rows.Scan(&todoID, &raw); err != nil {
				return err
			}
			set, err := decodeTodoRepositories(todoID, 0, raw)
			if err != nil {
				return err
			}
			if len(set.Repositories) > 0 {
				out[todoID] = set.Repositories
			}
		}
		return rows.Err()
	})
	return out, err
}

// ReadTodoRepositoriesTx locks the Todo, including an empty association set. Preparation
// consumers can read this value and persist their own selected snapshot in the same
// transaction. It never seals business associations or changes legacy execution paths.
// Transactions needing both locks take the Todo before the catalog; ordinary EditTodo
// takes only the Todo lock, and catalog CAS updates never lock a Todo.
func ReadTodoRepositoriesTx(ctx context.Context, tx pgx.Tx, id string, expected *int64) (repository.TaskSet, error) {
	var todo string
	if err := tx.QueryRow(ctx, `SELECT id FROM todos WHERE id=$1 FOR UPDATE`, id).Scan(&todo); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return repository.TaskSet{}, db.ErrTodoNotFound
		}
		return repository.TaskSet{}, err
	}
	var revision int64
	var raw []byte
	err := tx.QueryRow(ctx, `SELECT revision,repositories FROM todo_repository_sets WHERE todo_id=$1`, id).Scan(&revision, &raw)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return repository.TaskSet{}, err
	}
	if expected != nil && revision != *expected {
		return repository.TaskSet{}, repository.Problem("revision_conflict")
	}
	return decodeTodoRepositories(id, revision, raw)
}

// ReadRepositoryCatalogTx holds a shared lock through association validation and commit.
// Existing catalog CAS updates take the conflicting row lock, so identity cannot change
// between validation and save. No network call occurs while holding these locks.
func ReadRepositoryCatalogTx(ctx context.Context, tx pgx.Tx) (repository.Catalog, error) {
	var raw []byte
	var catalog repository.Catalog
	err := tx.QueryRow(ctx, `SELECT value FROM settings WHERE key=$1 FOR SHARE`, repository.CatalogKey).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return catalog, nil
	}
	if err != nil {
		return catalog, err
	}
	err = json.Unmarshal(raw, &catalog)
	return catalog, err
}

func writeTodoRepositoriesTx(ctx context.Context, tx pgx.Tx, set repository.TaskSet) error {
	rows := make([]storedTodoRepository, len(set.Repositories))
	for i, row := range set.Repositories {
		rows[i].TaskRepository = row
		if row.Links != nil {
			rows[i].Links = make([]storedTodoRepositoryLink, len(row.Links))
		}
		for j, link := range row.Links {
			rows[i].Links[j] = storedTodoRepositoryLink{TaskLink: link, Provenance: link.Provenance}
		}
	}
	raw, err := json.Marshal(rows)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO todo_repository_sets(todo_id,revision,repositories) VALUES($1,$2,$3) ON CONFLICT(todo_id) DO UPDATE SET revision=$2,repositories=$3`, set.TodoID, set.Revision, raw)
	return err
}

func (d *Store) CreateTodoWithRepositories(ctx context.Context, t db.TodoRow, in repository.CreateBindings) error {
	if !repository.ValidRevision(in.CatalogRevision) || in.Repositories == nil {
		return repository.Problem("invalid")
	}
	return d.Transaction(ctx, func(tx pgx.Tx) error {
		if err := db.CreateTodoTx(ctx, tx, t); err != nil {
			return err
		}
		catalog, err := ReadRepositoryCatalogTx(ctx, tx)
		if err != nil {
			return err
		}
		if catalog.Revision != *in.CatalogRevision {
			return repository.Problem("catalog_conflict")
		}
		rows, err := repository.Bind(nil, in.Repositories, catalog)
		if err != nil {
			return err
		}
		return writeTodoRepositoriesTx(ctx, tx, repository.TaskSet{TodoID: t.ID, Revision: 1, Repositories: rows})
	})
}

func (d *Store) SetTodoRepositories(ctx context.Context, id string, in repository.SetInput) (repository.TaskSet, error) {
	var out repository.TaskSet
	if !repository.ValidRevision(in.ExpectedRevision) || !repository.ValidRevision(in.ExpectedCatalogRevision) || in.Repositories == nil {
		return out, repository.Problem("invalid")
	}
	err := d.Transaction(ctx, func(tx pgx.Tx) error {
		cur, err := ReadTodoRepositoriesTx(ctx, tx, id, in.ExpectedRevision)
		if err != nil {
			return err
		}
		catalog, err := ReadRepositoryCatalogTx(ctx, tx)
		if err != nil {
			return err
		}
		if catalog.Revision != *in.ExpectedCatalogRevision {
			return repository.Problem("catalog_conflict")
		}
		if err := rememberTodoRepositoryLinksTx(ctx, tx, id, repositoryURLs(cur.Repositories), false); err != nil {
			return err
		}
		rows, err := repository.Bind(cur.Repositories, in.Repositories, catalog)
		if err != nil {
			return err
		}
		cur.Repositories = rows
		cur.Revision++
		out = cur
		return writeTodoRepositoriesTx(ctx, tx, cur)
	})
	return out, err
}

func (d *Store) SetTodoRepositoryLinks(ctx context.Context, id, repoID string, in repository.LinksInput) (repository.TaskSet, error) {
	var out repository.TaskSet
	if !repository.ValidRevision(in.ExpectedRevision) || in.Links == nil {
		return out, repository.Problem("invalid")
	}
	err := d.Transaction(ctx, func(tx pgx.Tx) error {
		cur, err := ReadTodoRepositoriesTx(ctx, tx, id, in.ExpectedRevision)
		if err != nil {
			return err
		}
		if err := rememberTodoRepositoryLinksTx(ctx, tx, id, repositoryURLs(cur.Repositories), false); err != nil {
			return err
		}
		for index, row := range cur.Repositories {
			if row.RepoID == repoID {
				links, err := repository.NormalizeLinks(row.SourceSnapshot.Identity, in.Links)
				if err != nil {
					return err
				}
				repository.ReconcileLinkEdits(row.Links, links, in.Links)
				cur.Repositories[index].Links = links
				cur.Revision++
				out = cur
				return writeTodoRepositoriesTx(ctx, tx, cur)
			}
		}
		return repository.Problem("not_found")
	})
	return out, err
}

// SetTodoRepositoryLinkRole rewrites the role of a single link matched by canonical URL inside one transaction.
func (d *Store) SetTodoRepositoryLinkRole(ctx context.Context, id, canonicalURL, role string) (repository.TaskSet, error) {
	normalized, perr := repository.NormalizeLinkRole(role)
	if perr != "" {
		return repository.TaskSet{}, perr
	}
	// NormalizeLink stores the default delivery role as an empty string.
	storedRole, provenance := "", repository.LinkProvenanceExplicit
	if normalized == repository.LinkRoleReference {
		storedRole, provenance = normalized, repository.LinkProvenanceUnbound
	}
	return d.updateTodoRepositoryLink(ctx, id, canonicalURL, func(link *repository.TaskLink) {
		link.Role, link.Provenance = storedRole, provenance
	})
}

// SetTodoPRLinkTitle updates only an existing PR link. A poll must never recreate a removed link.
func (d *Store) SetTodoPRLinkTitle(ctx context.Context, id, canonicalURL, title string) error {
	_, err := d.updateTodoRepositoryLink(ctx, id, canonicalURL, func(link *repository.TaskLink) {
		if link.Kind == "pull_request" && title != "" {
			link.Title = title
		}
	})
	return err
}

func (d *Store) updateTodoRepositoryLink(ctx context.Context, id, canonicalURL string, update func(*repository.TaskLink)) (repository.TaskSet, error) {
	var out repository.TaskSet
	err := d.Transaction(ctx, func(tx pgx.Tx) error {
		cur, err := ReadTodoRepositoriesTx(ctx, tx, id, nil)
		if err != nil {
			return err
		}
		changed := false
		for _, row := range cur.Repositories {
			for i, link := range row.Links {
				if link.CanonicalURL == canonicalURL {
					update(&row.Links[i])
					changed = changed || row.Links[i] != link
				}
			}
		}
		if !changed {
			out = cur
			return nil
		}
		if cur.Revision >= repository.MaxRevision {
			return repository.Problem("limit")
		}
		cur.Revision++
		out = cur
		return writeTodoRepositoriesTx(ctx, tx, cur)
	})
	return out, err
}

func repositoryURLs(rows []repository.TaskRepository) []string {
	urls := []string{}
	for _, row := range rows {
		for _, link := range row.Links {
			urls = append(urls, link.CanonicalURL)
		}
	}
	return urls
}
func rememberTodoRepositoryLinksTx(ctx context.Context, tx pgx.Tx, id string, urls []string, legacy bool) error {
	if urls == nil {
		urls = []string{}
	}
	_, err := tx.Exec(ctx, `INSERT INTO todo_repository_link_receipts(todo_id,seen_urls,legacy_imported) VALUES($1,$2,$3)
 ON CONFLICT(todo_id) DO UPDATE SET seen_urls=ARRAY(SELECT DISTINCT value FROM unnest(todo_repository_link_receipts.seen_urls || EXCLUDED.seen_urls) AS value), legacy_imported=todo_repository_link_receipts.legacy_imported OR EXCLUDED.legacy_imported`, id, urls, legacy)
	return err
}

// ImportTodoRepositoryLinks is additive and serializes with interactive edits on the Todo.
// Historical imports run once; repeated reports cannot resurrect removed references.
// An explicit agent/user request can deliberately add a previously removed reference.
func (d *Store) ImportTodoRepositoryLinks(ctx context.Context, id string, urls []string, source string) (repository.TaskSet, bool, error) {
	input := make([]repository.LinkInput, 0, len(urls))
	for _, raw := range urls {
		input = append(input, repository.LinkInput{URL: raw})
	}
	return d.ImportTodoRepositoryLinkDetails(ctx, id, input, source)
}

// ImportTodoRepositoryLinkDetails uses the same transaction for references and titles.
func (d *Store) ImportTodoRepositoryLinkDetails(ctx context.Context, id string, input []repository.LinkInput, source string) (repository.TaskSet, bool, error) {
	var out repository.TaskSet
	var changed bool
	err := d.Transaction(ctx, func(tx pgx.Tx) error {
		var err error
		out, changed, err = ImportTodoRepositoryLinkDetailsTx(ctx, tx, id, input, source)
		return err
	})
	return out, changed && err == nil, err
}

// ImportTodoRepositoryLinkDetailsTx imports saved links and their receipts in a caller's transaction.
func ImportTodoRepositoryLinkDetailsTx(ctx context.Context, tx pgx.Tx, id string, input []repository.LinkInput, source string) (repository.TaskSet, bool, error) {
	var out repository.TaskSet
	var changed bool
	if source != "legacy" && source != "report" && source != "explicit" {
		return out, false, repository.Problem("invalid")
	}
	if len(input) > repository.MaxTaskRepositories*repository.MaxRepositoryLinks {
		return out, false, repository.Problem("limit")
	}
	if source == "report" {
		// Serialize automatic claims across to-dos (and manager instances), not
		// just writes to one row. Explicit user associations remain deliberate.
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('persona.todo_repository_reports'))`); err != nil {
			return out, false, err
		}
	}
	cur, err := ReadTodoRepositoriesTx(ctx, tx, id, nil)
	if err != nil {
		return out, false, err
	}
	out = cur
	if source == "report" {
		var open bool
		if err := tx.QueryRow(ctx, `SELECT done_at IS NULL AND done_by <> $2 FROM todos WHERE id=$1`,
			id, db.DoneByCancelled).Scan(&open); err != nil {
			return out, false, err
		}
		if !open {
			return out, false, nil
		}
	}
	var seen []string
	var legacy bool
	err = tx.QueryRow(ctx, `SELECT seen_urls,legacy_imported FROM todo_repository_link_receipts WHERE todo_id=$1`, id).Scan(&seen, &legacy)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return out, false, err
	}
	legacyReplay := source == "legacy" && legacy
	saved := repositoryURLs(cur.Repositories)
	pending := []repository.LinkInput{}
	canonical := []string{}
	for _, in := range input {
		if legacyReplay && in.Role != repository.LinkRoleDelivery {
			continue
		}
		_, link, err := repository.ParseTaskLinkInput(in)
		if err != nil {
			return out, false, err
		}
		if source == "report" && link.Kind == "pull_request" {
			match, err := json.Marshal([]any{map[string]any{"links": []any{map[string]string{"canonical_url": link.CanonicalURL}}}})
			if err != nil {
				return out, false, err
			}
			var claimed bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS (
				SELECT 1 FROM todo_repository_sets r JOIN todos t ON t.id=r.todo_id
				WHERE r.todo_id<>$1 AND t.done_at IS NULL AND t.done_by<>$3 AND r.repositories @> $2::jsonb
			)`, id, match, db.DoneByCancelled).Scan(&claimed); err != nil {
				return out, false, err
			}
			if claimed {
				// A historical duplicate in cur is not an accepted claim.
				// Fail closed without deleting deliberate associations.
				for _, row := range cur.Repositories {
					for _, existing := range row.Links {
						if existing.CanonicalURL == link.CanonicalURL {
							return out, false, errors.New("reported PR is already linked to multiple open to-dos")
						}
					}
				}
				continue
			}
		}
		canonical = append(canonical, link.CanonicalURL)
		// A delivery role can promote a saved mirror even when its URL has already been seen.
		// Keep the receipt's protection for deleted links.
		bindSaved := in.Role == repository.LinkRoleDelivery && slices.Contains(saved, link.CanonicalURL)
		if source == "explicit" || bindSaved || !legacyReplay && !slices.Contains(seen, link.CanonicalURL) {
			pending = append(pending, repository.LinkInput{URL: link.CanonicalURL, Title: link.Title, Role: in.Role, Resolved: in.Resolved})
		}
	}
	if len(pending) == 0 {
		if source == "legacy" && !legacy {
			return out, false, rememberTodoRepositoryLinksTx(ctx, tx, id, canonical, true)
		}
		return out, false, nil
	}
	catalog, err := ReadRepositoryCatalogTx(ctx, tx)
	if err != nil {
		return out, false, err
	}
	provenance := repository.LinkProvenanceUnbound
	if source == "explicit" {
		provenance = repository.LinkProvenanceExplicit
	}
	rows, err := repository.MergeTaskLinkDetails(cur.Repositories, pending, catalog, provenance)
	if err != nil {
		return out, false, err
	}
	if !reflect.DeepEqual(rows, cur.Repositories) {
		if cur.Revision >= repository.MaxRevision {
			return out, false, repository.Problem("limit")
		}
		cur.Repositories = rows
		cur.Revision++
		changed = true
		if err := writeTodoRepositoriesTx(ctx, tx, cur); err != nil {
			return out, false, err
		}
	}
	out = cur
	err = rememberTodoRepositoryLinksTx(ctx, tx, id, canonical, source == "legacy")
	return out, changed && err == nil, err
}
