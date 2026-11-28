package repositorystore

import (
	"context"
	"errors"
	"os"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/nwverse/persona/server/internal/assistant/repository"
	"github.com/nwverse/persona/server/internal/db"
)

func TestTodoRepositoriesAtomicRestartAndLegacyEdits(t *testing.T) {
	d := open(t)
	ctx := context.Background()
	zero := int64(0)
	one := int64(1)
	identity, _ := repository.Parse("https://github.com/example/web", "")
	catalog := repository.Catalog{Revision: 1, Repositories: []repository.CatalogEntry{{Identity: identity, ID: "web"}}}
	if err := d.PutSetting(ctx, repository.CatalogKey, catalog); err != nil {
		t.Fatal(err)
	}
	row := db.TodoRow{ID: "linked", Title: "Work", CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	in := repository.CreateBindings{CatalogRevision: &one, Repositories: []repository.TaskInput{{RepoID: "web", Links: []repository.LinkInput{{Kind: "pull_request", Number: "7"}, {Kind: "pull_request", Number: "8"}}}}}
	bad := in
	bad.CatalogRevision = &zero
	if err := d.CreateTodoWithRepositories(ctx, row, bad); err == nil {
		t.Fatal("stale catalog accepted")
	}
	if _, err := d.GetTodo(ctx, row.ID); !errors.Is(err, db.ErrTodoNotFound) {
		t.Fatal("partial Todo saved", err)
	}
	if err := d.CreateTodoWithRepositories(ctx, row, in); err != nil {
		t.Fatal(err)
	}
	saved, err := d.TodoRepositories(ctx, row.ID)
	if err != nil || len(saved.Repositories) != 1 || len(saved.Repositories[0].Links) != 2 {
		t.Fatal(saved, err)
	}
	for _, link := range saved.Repositories[0].Links {
		if link.Provenance != repository.LinkProvenanceExplicit {
			t.Fatalf("creation did not bind delivery link %s", link.CanonicalURL)
		}
	}
	title := "Renamed"
	if _, err := d.EditTodo(ctx, row.ID, db.TodoEdit{Title: &title}, time.Now()); err != nil {
		t.Fatal(err)
	}
	restarted := openExisting(t, os.Getenv("PERSONA_TEST_DATABASE_URL"))
	got, err := restarted.TodoRepositories(ctx, row.ID)
	if err != nil || !reflect.DeepEqual(saved, got) {
		t.Fatal("restart/legacy edit lost links", got, err)
	}
	// Capture an operation's selected inputs in a transaction; then freely edit business links.
	var snapshot repository.PreparationSnapshot
	err = d.Transaction(ctx, func(tx pgx.Tx) error {
		set, err := ReadTodoRepositoriesTx(ctx, tx, row.ID, &one)
		if err != nil {
			return err
		}
		snapshot, err = repository.Snapshot("assistant", row.ID, set.Revision, set.Repositories, []string{"web"})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = d.SetTodoRepositories(ctx, row.ID, repository.SetInput{ExpectedRevision: &one, ExpectedCatalogRevision: &one, Repositories: []repository.TaskInput{}}); err != nil {
		t.Fatal("business associations locked after snapshot", err)
	}
	if len(snapshot.Repositories) != 1 || snapshot.Repositories[0].RepoID != "web" {
		t.Fatal("snapshot mutated", snapshot)
	}
	if err = d.DeleteTodo(ctx, row.ID); err != nil {
		t.Fatal(err)
	}
	var count int
	if err = d.QueryRow(ctx, `SELECT count(*) FROM todo_repository_sets WHERE todo_id=$1`, row.ID).Scan(&count); err != nil || count != 0 {
		t.Fatal("association orphan", count, err)
	}
}

func TestTodoRepositoriesConcurrentRevisionsAndSnapshotRollback(t *testing.T) {
	d := open(t)
	ctx := context.Background()
	zero := int64(0)
	row := db.TodoRow{ID: "empty", Title: "Work", CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if err := d.CreateTodo(ctx, row); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := d.SetTodoRepositories(ctx, row.ID, repository.SetInput{ExpectedRevision: &zero, ExpectedCatalogRevision: &zero, Repositories: []repository.TaskInput{}})
			results <- err
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	wins, conflicts := 0, 0
	for err := range results {
		if err == nil {
			wins++
		} else if errors.Is(err, repository.Problem("revision_conflict")) {
			conflicts++
		} else {
			t.Fatal(err)
		}
	}
	if wins != 1 || conflicts != 1 {
		t.Fatal(wins, conflicts)
	}
	sentinel := errors.New("rollback consumer")
	if err := d.Transaction(ctx, func(tx pgx.Tx) error {
		_, err := ReadTodoRepositoriesTx(ctx, tx, row.ID, nil)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO settings(key,value) VALUES('preparation.fixture','{}')`)
		if err != nil {
			return err
		}
		return sentinel
	}); !errors.Is(err, sentinel) {
		t.Fatal(err)
	}
	var value any
	if found, err := d.Setting(ctx, "preparation.fixture", &value); err != nil || found {
		t.Fatal("partial preparation consumer", found, err)
	}
}

func TestTodoRepositoryImportsAndRemovalStayUnified(t *testing.T) {
	d := open(t)
	ctx := t.Context()
	row := db.TodoRow{ID: "reported", Title: "Work", CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if err := d.CreateTodo(ctx, row); err != nil {
		t.Fatal(err)
	}
	urls := []string{"https://github.com/example/web/pull/7", "https://github.com/example/web/pull/8", "https://github.com/example/api/pull/7"}
	set, _, err := d.ImportTodoRepositoryLinks(ctx, row.ID, urls, "legacy")
	if err != nil || len(set.Repositories) != 2 || len(set.Repositories[0].Links) != 2 {
		t.Fatal(set, err)
	}
	revision := set.Revision
	again, _, err := d.ImportTodoRepositoryLinks(ctx, row.ID, urls, "report")
	if err != nil || again.Revision != revision {
		t.Fatal("replayed report changed the set", again, err)
	}
	zero := int64(0)
	cleared, err := d.SetTodoRepositories(ctx, row.ID, repository.SetInput{ExpectedRevision: &revision, ExpectedCatalogRevision: &zero, Repositories: []repository.TaskInput{}})
	if err != nil {
		t.Fatal(err)
	}
	for _, source := range []string{"legacy", "report"} {
		got, _, err := d.ImportTodoRepositoryLinks(ctx, row.ID, urls, source)
		if err != nil || len(got.Repositories) != 0 || got.Revision != cleared.Revision {
			t.Fatal("removed links returned", source, got, err)
		}
	}
	restored, _, err := d.ImportTodoRepositoryLinks(ctx, row.ID, urls[:1], "explicit")
	if err != nil || len(restored.Repositories) != 1 {
		t.Fatal("explicit agent edit could not restore", restored, err)
	}
	if _, _, err = d.ImportTodoRepositoryLinks(ctx, row.ID, []string{"https://github.com/example/web/pull/9", "https://github.com/example/web/pull/not-a-number"}, "explicit"); err == nil {
		t.Fatal("partial invalid batch saved")
	}
	unchanged, _ := d.TodoRepositories(ctx, row.ID)
	if !reflect.DeepEqual(restored, unchanged) {
		t.Fatal("invalid batch partially saved")
	}
	if err := d.DeleteTodo(ctx, row.ID); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := d.QueryRow(ctx, `SELECT count(*) FROM todo_repository_link_receipts WHERE todo_id=$1`, row.ID).Scan(&count); err != nil || count != 0 {
		t.Fatal("receipt orphan", count, err)
	}
}

func TestTodoRepositoryTitleUpdatesAreAtomicAndRevisionChecked(t *testing.T) {
	d := open(t)
	ctx := t.Context()
	row := db.TodoRow{ID: "titled", Title: "Work", CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if err := d.CreateTodo(ctx, row); err != nil {
		t.Fatal(err)
	}
	url := "https://gitlab.example.com/team/api/-/merge_requests/7"
	set, changed, err := d.ImportTodoRepositoryLinkDetails(ctx, row.ID, []repository.LinkInput{{URL: url, Title: "Use a stable checkout path"}}, "explicit")
	if err != nil || !changed || set.Repositories[0].Links[0].Title != "Use a stable checkout path" {
		t.Fatal(set, changed, err)
	}
	oldRevision := set.Revision
	updated, changed, err := d.ImportTodoRepositoryLinkDetails(ctx, row.ID, []repository.LinkInput{{URL: url, Title: "Keep checkout context across restarts"}}, "explicit")
	if err != nil || !changed || updated.Revision != oldRevision+1 {
		t.Fatal(updated, changed, err)
	}
	repoID := updated.Repositories[0].RepoID
	_, err = d.SetTodoRepositoryLinks(ctx, row.ID, repoID, repository.LinksInput{ExpectedRevision: &oldRevision, Links: []repository.LinkInput{}})
	if err != repository.Problem("revision_conflict") {
		t.Fatal("stale editor overwrote title update", err)
	}
	kept, err := d.SetTodoRepositoryLinks(ctx, row.ID, repoID, repository.LinksInput{ExpectedRevision: &updated.Revision, Links: []repository.LinkInput{{URL: url}}})
	if err != nil || kept.Repositories[0].Links[0].Title != "Keep checkout context across restarts" {
		t.Fatal("older client erased title", kept, err)
	}
	_, _, err = d.ImportTodoRepositoryLinkDetails(ctx, row.ID, []repository.LinkInput{{URL: url, Title: "Do not partially save"}, {URL: url, Title: "bad\x00title"}}, "explicit")
	if err == nil {
		t.Fatal("accepted invalid title batch")
	}
	stored, err := d.TodoRepositories(ctx, row.ID)
	if err != nil || !reflect.DeepEqual(stored, kept) {
		t.Fatal("invalid batch partially saved", stored, err)
	}
}

func TestTodoRepositoryImportCanonicalReceiptAfterRestart(t *testing.T) {
	d := open(t)
	ctx := t.Context()
	row := db.TodoRow{ID: "canonical-receipt", Title: "Work", CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if err := d.CreateTodo(ctx, row); err != nil {
		t.Fatal(err)
	}
	raw := "https://gitlab.com/Team/Api/merge_requests/7"
	set, _, err := d.ImportTodoRepositoryLinks(ctx, row.ID, []string{raw}, "explicit")
	if err != nil {
		t.Fatal(err)
	}
	// Remove the inferred row, then replay its canonical form after reopening the database.
	canonical := set.Repositories[0].Links[0].CanonicalURL
	zero := int64(0)
	cleared, err := d.SetTodoRepositories(ctx, row.ID, repository.SetInput{ExpectedRevision: &set.Revision, ExpectedCatalogRevision: &zero, Repositories: []repository.TaskInput{}})
	if err != nil {
		t.Fatal(err)
	}
	next := openExisting(t, os.Getenv("PERSONA_TEST_DATABASE_URL"))
	got, _, err := next.ImportTodoRepositoryLinks(ctx, row.ID, []string{canonical}, "report")
	if err != nil || got.Revision != cleared.Revision || len(got.Repositories) != 0 {
		t.Fatal("canonical replay resurrected removed link", got, err)
	}
}

func TestTodoRepositoryConcurrentReportsAndInteractiveEdit(t *testing.T) {
	d := open(t)
	ctx := t.Context()
	row := db.TodoRow{ID: "concurrent-reports", Title: "Work", CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if err := d.CreateTodo(ctx, row); err != nil {
		t.Fatal(err)
	}
	base, _, err := d.ImportTodoRepositoryLinks(ctx, row.ID, []string{"https://github.com/example/web/pull/1"}, "explicit")
	if err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	results := make(chan error, 3)
	for _, raw := range []string{"https://github.com/example/web/pull/2", "https://github.com/example/api/pull/2"} {
		go func() {
			<-start
			_, _, err := d.ImportTodoRepositoryLinks(ctx, row.ID, []string{raw}, "report")
			results <- err
		}()
	}
	zero := int64(0)
	go func() {
		<-start
		_, err := d.SetTodoRepositories(ctx, row.ID, repository.SetInput{ExpectedRevision: &base.Revision, ExpectedCatalogRevision: &zero, Repositories: []repository.TaskInput{}})
		results <- err
	}()
	close(start)
	for range 3 {
		if err := <-results; err != nil && !errors.Is(err, repository.Problem("revision_conflict")) {
			t.Fatal(err)
		}
	}
	got, err := d.TodoRepositories(ctx, row.ID)
	if err != nil {
		t.Fatal(err)
	}
	urls := repositoryURLs(got.Repositories)
	for _, want := range []string{"https://github.com/example/web/pull/2", "https://github.com/example/api/pull/2"} {
		found := false
		for _, url := range urls {
			if url == want {
				found = true
			}
		}
		if !found {
			t.Fatal("concurrent report lost", want, got)
		}
	}
}
