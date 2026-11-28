package repositorystore

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/db"
)

const claimedPR = "https://github.com/example/project/pull/17"

func TestReportedRepositoryPRHasOneOpenTodoOwner(t *testing.T) {
	for _, kind := range []string{"open", "closed", "cancelled", "explicit"} {
		t.Run(kind, func(t *testing.T) {
			d := open(t)
			now := time.Now()
			owner := db.TodoRow{ID: "owner", Title: "Original work", CreatedAt: now, UpdatedAt: now}
			if kind == "closed" || kind == "cancelled" {
				owner.DoneAt = &now
			}
			if kind == "cancelled" {
				owner.DoneBy = db.DoneByCancelled
			}
			for _, row := range []db.TodoRow{owner, {ID: "reporter", Title: "Deployment", CreatedAt: now, UpdatedAt: now}} {
				if err := d.CreateTodo(t.Context(), row); err != nil {
					t.Fatal(err)
				}
			}
			if _, _, err := d.ImportTodoRepositoryLinks(t.Context(), owner.ID, []string{claimedPR}, "explicit"); err != nil {
				t.Fatal(err)
			}
			source := "report"
			if kind == "explicit" {
				source = "explicit"
			}
			set, changed, err := d.ImportTodoRepositoryLinks(t.Context(), "reporter", []string{claimedPR}, source)
			if err != nil {
				t.Fatal(err)
			}
			allowed := kind != "open"
			if changed != allowed || (len(set.Repositories) > 0) != allowed {
				t.Fatalf("owner=%s: changed=%v, set=%+v", kind, changed, set)
			}
			if !allowed {
				done := true
				if _, err := d.UpdateTodo(t.Context(), owner.ID, nil, nil, &done, now); err != nil {
					t.Fatal(err)
				}
				set, changed, err = d.ImportTodoRepositoryLinks(t.Context(), "reporter", []string{claimedPR}, "report")
				if err != nil || !changed || len(set.Repositories) != 1 {
					t.Fatal("a skipped conflict was permanently consumed instead of retryable", set, err)
				}
			}
		})
	}
}

func TestReportedRepositoryPRConcurrentClaimsHaveOneWinner(t *testing.T) {
	d := open(t)
	const reporters = 8
	start := make(chan struct{})
	results := make(chan error, reporters)
	var wg sync.WaitGroup
	for i := range reporters {
		id := fmt.Sprint("reporter-", i)
		now := time.Now()
		if err := d.CreateTodo(t.Context(), db.TodoRow{ID: id, Title: "Work", CreatedAt: now, UpdatedAt: now}); err != nil {
			t.Fatal(err)
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			// Independent Store values still share the database's claim lock.
			_, _, err := New(d.DB).ImportTodoRepositoryLinks(t.Context(), id, []string{claimedPR}, "report")
			results <- err
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatal(err)
		}
	}
	var owners []string
	for i := range reporters {
		id := fmt.Sprint("reporter-", i)
		set, err := d.TodoRepositories(t.Context(), id)
		if err != nil {
			t.Fatal(err)
		}
		if len(set.Repositories) > 0 {
			owners = append(owners, id)
		}
	}
	if len(owners) != 1 {
		t.Fatalf("one PR acquired %d open owners: %v", len(owners), owners)
	}
}
