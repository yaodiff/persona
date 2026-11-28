package externalsourcestore

import (
	"context"
	"errors"
	"slices"

	"github.com/jackc/pgx/v5"
	"github.com/nwverse/persona/server/internal/assistant/externalbinding/externalbindingadapt"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/repositorywire"
)

// LookupExternalBindings returns all matches, starting with the most specific node of a canonical path.
// Existing repository and Lark topic links follow explicit bindings for the same node.
func (s *Store) LookupExternalBindings(ctx context.Context, path []assistantwire.ExternalRef) (assistantwire.ExternalBindingLookup, error) {
	out := assistantwire.ExternalBindingLookup{Path: path}
	if len(path) == 0 {
		return out, errors.New("external reference path is empty")
	}
	err := db.ReadSnapshot(ctx, s.database, func(tx pgx.Tx) error {
		matches, err := lookupMatches(ctx, tx, path)
		out.Matches = matches
		return err
	})
	return out, err
}

func lookupMatches(ctx context.Context, tx pgx.Tx, path []assistantwire.ExternalRef) ([]assistantwire.ExternalBindingMatch, error) {
	matches, err := explicitMatches(ctx, tx, `
		SELECT b.external_ref, b.target_kind, b.target_id, b.id
		FROM jsonb_array_elements($1::jsonb) WITH ORDINALITY AS path(ref, rank)
		JOIN assistant_external_bindings b ON b.external_ref = path.ref
		ORDER BY path.rank, b.target_kind, b.target_id`, path)
	if err != nil {
		return nil, err
	}
	item := path[0]
	if item.Item == (assistantwire.ExternalRefNode{}) {
		return matches, nil
	}
	var projected []assistantwire.ExternalBindingMatch
	switch {
	case item.Provider == assistantwire.ExternalProviderGitHub:
		repo, link, err := externalbindingadapt.RepositoryLinkIdentity(item)
		if err != nil {
			return nil, err
		}
		projected, err = projectedMatches(ctx, tx, &repo.InstanceURL, &repo.Path, &link)
		if err != nil {
			return nil, err
		}
	case item.Provider == "lark" && item.Channel == "":
		match, err := legacyLarkTopic(ctx, tx, item)
		if err != nil {
			return nil, err
		}
		if match != nil {
			projected = append(projected, *match)
		}
	}
	if len(projected) == 0 {
		return matches, nil
	}
	afterItem := 0
	for afterItem < len(matches) && matches[afterItem].ExternalRef == item {
		afterItem++
	}
	return slices.Insert(matches, afterItem, projected...), nil
}

// The legacy bot has one pairing and its topic link carries no account selector.
// Only the default Lark channel can use it. The native link remains authoritative.
func legacyLarkTopic(ctx context.Context, tx pgx.Tx, ref assistantwire.ExternalRef) (*assistantwire.ExternalBindingMatch, error) {
	match := assistantwire.ExternalBindingMatch{
		ExternalRef: ref, Source: assistantwire.ExternalBindingLarkTopic, TargetKind: assistantwire.ExternalTargetTodo,
	}
	err := tx.QueryRow(ctx, `
		SELECT id FROM todos WHERE thread IS NOT NULL
		AND thread->>'channel' = 'feishu' AND thread->>'chat_id' = $1 AND thread->>'thread_id' = $2`,
		ref.Container.ID, ref.Item.ID).Scan(&match.TargetID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return &match, err
}

// ExternalSourceSummary describes saved data flows, including read-only repository projections.
func (s *Store) ExternalSourceSummary(ctx context.Context, provider assistantwire.ExternalProvider, channel *assistantwire.ExternalChannelID, includeDone bool) (assistantwire.ExternalSourceSummary, error) {
	out := assistantwire.ExternalSourceSummary{Bindings: []assistantwire.ExternalBindingMatch{}}
	err := db.ReadSnapshot(ctx, s.database, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, externalSourceSummaryQuery, provider, channel)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			match := assistantwire.ExternalBindingMatch{Source: assistantwire.ExternalBindingExplicit}
			var explicitRef *assistantwire.ExternalRef
			repo := repositorywire.Identity{Provider: "github"}
			var link repositorywire.TaskLink
			if err := rows.Scan(&explicitRef, &match.TargetKind, &match.TargetID, &match.BindingID, &match.TargetTitle, &match.TargetDone,
				&repo.InstanceURL, &repo.Path, &link.Kind, &link.Number, &link.Role); err != nil {
				return err
			}
			if match.TargetDone && !includeDone {
				out.OmittedDoneCount++
				continue
			}
			if explicitRef != nil {
				match.ExternalRef = *explicitRef
			} else {
				match.Source = assistantwire.ExternalBindingRepositoryLink
				role, problem := repositorywire.NormalizeLinkRole(link.Role)
				if problem != "" {
					return problem
				}
				match.RepositoryRole = assistantwire.ExternalRepositoryRole(role)
				match.ExternalRef, err = externalbindingadapt.RepositoryLinkRef(repo, link)
				if err != nil {
					return err
				}
			}
			out.Bindings = append(out.Bindings, match)
		}
		return rows.Err()
	})
	out.TotalCount = len(out.Bindings)
	return out, err
}

// The summary orders both sources by the same to-do timestamps. The repository identity filter
// can use the GIN index before expanding individual links. An empty stored role means delivery.
// The to-do projection mirrors Todo.closedForGood. The stored cancellation marker and the legacy review state
// are also the sources for cancelled_at in formatTodo.
const externalSourceSummaryQuery = `
	WITH todo_targets AS NOT MATERIALIZED (
		SELECT td.id, td.title, td.updated_at,
			td.done_at IS NOT NULL AND (
				td.start_error = '' OR td.done_by = 'cancelled' OR
				(td.done_by = 'review' AND EXISTS (
					SELECT 1 FROM todo_supervision ts WHERE ts.todo_id = td.id AND ts.state = 'cancelled'
				))
			) AS target_done
		FROM todos td
	), github_filter AS (
		SELECT jsonb_strip_nulls(jsonb_build_object('provider', 'github',
			'instance_url', CASE WHEN $2::text = '' THEN 'https://github.com' ELSE $2 END)) AS identity
		WHERE $1 IN ('', 'github')
	), repository_matches AS (
		SELECT DISTINCT ON (t.todo_id, instance, path, kind, number)
			t.todo_id, repo #>> '{source_snapshot,identity,instance_url}' AS instance,
			repo #>> '{source_snapshot,identity,path}' AS path, link->>'kind' AS kind, link->>'number' AS number,
			COALESCE(link->>'role', '') AS role, td.title, td.target_done, td.updated_at
		FROM github_filter f
		JOIN todo_repository_sets t ON t.repositories @> jsonb_build_array(
			jsonb_build_object('source_snapshot', jsonb_build_object('identity', f.identity)))
		JOIN todo_targets td ON td.id = t.todo_id
		CROSS JOIN LATERAL jsonb_array_elements(t.repositories) AS repos(repo)
		CROSS JOIN LATERAL jsonb_array_elements(CASE WHEN jsonb_typeof(repo->'links') = 'array' THEN repo->'links' ELSE '[]'::jsonb END) AS links(link)
		WHERE repo #> '{source_snapshot,identity}' @> f.identity
		  AND link->>'kind' IN ('issue', 'pull_request')
		ORDER BY t.todo_id, instance, path, kind, number, (COALESCE(link->>'role', '') IN ('', 'delivery')) DESC, role
	), matches AS (
		SELECT b.external_ref, b.target_kind, b.target_id, b.id AS binding_id,
			COALESCE(td.title, '') AS target_title, COALESCE(td.target_done, false) AS target_done,
			COALESCE(td.updated_at, to_timestamp(0)) AS updated_at,
			''::text AS instance, ''::text AS path, ''::text AS kind, ''::text AS number, ''::text AS role
		FROM assistant_external_bindings b
		LEFT JOIN todo_targets td ON b.target_kind = 'todo' AND td.id = b.target_id
		WHERE ($1 = '' OR b.external_ref->>'provider' = $1)
		  AND ($2::text IS NULL OR COALESCE(b.external_ref->>'channel', '') = $2)
		UNION ALL
		SELECT NULL, 'todo', todo_id, '', title, target_done, updated_at,
			COALESCE(instance, ''), COALESCE(path, ''), kind, COALESCE(number, ''), role
		FROM repository_matches
	)
	SELECT external_ref, target_kind, target_id, binding_id, target_title, target_done, instance, path, kind, number, role
	FROM matches
	ORDER BY (target_kind = 'direction') DESC, target_done, updated_at DESC, external_ref NULLS LAST, target_kind,
		CASE WHEN external_ref IS NOT NULL THEN target_id END, target_id DESC, instance, path, kind, number
`

func explicitMatches(ctx context.Context, tx pgx.Tx, query string, args ...any) ([]assistantwire.ExternalBindingMatch, error) {
	rows, err := tx.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	list := []assistantwire.ExternalBindingMatch{}
	for rows.Next() {
		match := assistantwire.ExternalBindingMatch{Source: assistantwire.ExternalBindingExplicit}
		if err := rows.Scan(&match.ExternalRef, &match.TargetKind, &match.TargetID, &match.BindingID); err != nil {
			return nil, err
		}
		list = append(list, match)
	}
	return list, rows.Err()
}

func projectedMatches(ctx context.Context, tx pgx.Tx, instance, path *string, link *repositorywire.TaskLink) ([]assistantwire.ExternalBindingMatch, error) {
	var kind, number *string
	if link != nil {
		kind, number = &link.Kind, &link.Number
	}
	// The set predicate uses the GIN index with repository and link in the same JSON object.
	// The lateral scan repeats both predicates on individual rows to read the matching role.
	rows, err := tx.Query(ctx, `
		WITH filters AS (
			SELECT jsonb_strip_nulls(jsonb_build_object('provider', 'github', 'instance_url', $1::text, 'path', $2::text)) AS identity,
				CASE WHEN $3::text IS NOT NULL THEN jsonb_build_object('kind', $3::text, 'number', $4::text) END AS link_filter
		)
		SELECT DISTINCT t.todo_id, repo #>> '{source_snapshot,identity,instance_url}' AS instance,
			repo #>> '{source_snapshot,identity,path}' AS path, link->>'kind' AS kind, link->>'number' AS number,
			COALESCE(link->>'role', '') AS role
		FROM filters f
		CROSS JOIN todo_repository_sets t
		CROSS JOIN LATERAL jsonb_array_elements(t.repositories) AS repos(repo)
		CROSS JOIN LATERAL jsonb_array_elements(CASE WHEN jsonb_typeof(repo->'links') = 'array' THEN repo->'links' ELSE '[]'::jsonb END) AS links(link)
		WHERE t.repositories @> jsonb_build_array(jsonb_strip_nulls(jsonb_build_object(
			'source_snapshot', jsonb_build_object('identity', f.identity),
			'links', CASE WHEN f.link_filter IS NOT NULL THEN jsonb_build_array(f.link_filter) END)))
		  AND repo #> '{source_snapshot,identity}' @> f.identity
		  AND (f.link_filter IS NULL OR link @> f.link_filter)
		  AND link->>'kind' IN ('issue', 'pull_request')
		ORDER BY instance, path, kind, number, t.todo_id, role`, instance, path, kind, number)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	list := []assistantwire.ExternalBindingMatch{}
	for rows.Next() {
		match := assistantwire.ExternalBindingMatch{Source: assistantwire.ExternalBindingRepositoryLink, TargetKind: assistantwire.ExternalTargetTodo}
		repo := repositorywire.Identity{Provider: "github"}
		var link repositorywire.TaskLink
		if err := rows.Scan(&match.TargetID, &repo.InstanceURL, &repo.Path, &link.Kind, &link.Number, &link.Role); err != nil {
			return nil, err
		}
		role, problem := repositorywire.NormalizeLinkRole(link.Role)
		if problem != "" {
			return nil, problem
		}
		match.RepositoryRole = assistantwire.ExternalRepositoryRole(role)
		match.ExternalRef, err = externalbindingadapt.RepositoryLinkRef(repo, link)
		if err != nil {
			return nil, err
		}
		list = append(list, match)
	}
	return slices.Compact(list), rows.Err()
}
