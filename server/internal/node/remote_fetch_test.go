package node

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"testing/synctest"
	"time"
)

func TestRemoteFetchKey(t *testing.T) {
	for _, raw := range []string{
		"HTTPS://EXAMPLE.COM/Team/repo.git/",
		"https://example:example@example.com:443/Team/repo/",
		"https://example.com/Team/repo?example=opaque#fragment",
	} {
		if got := remoteFetchKey(raw); got != "https://example.com/Team/repo" {
			t.Errorf("key for %q = %q", raw, got)
		}
	}
	for raw, want := range map[string]string{
		"git@EXAMPLE.COM:Team/repo.git":          "ssh://example.com/Team/repo",
		"ssh://example@example.com:22/Team/repo": "ssh://example.com/Team/repo",
		"https://example.com:8443/Team/repo":     "https://example.com:8443/Team/repo",
		"https://example.com/team/repo":          "https://example.com/team/repo",
		"file:///example/repo.git":               "",
		"FILE:///example/repo.git":               "",
		filepath.Join(t.TempDir(), "repo.git"):   "",
		"./local:repo.git":                       "",
		"../repo.git":                            "",
	} {
		if got := remoteFetchKey(raw); got != want {
			t.Errorf("key for %q = %q, want %q", raw, got, want)
		}
	}
}

func TestRemoteFetchCoordinatorLimitsDistinctUpstreams(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var coordinator remoteFetchCoordinator
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		started := make(chan string, 4)
		release := make(chan struct{})
		done := make(chan error, 4)
		for _, cloneURL := range []string{
			"https://example.com/a", "https://example.com/b",
			"https://example.com/c", "https://example.com/d",
		} {
			go func() {
				_, err := coordinator.run(ctx, cloneURL, func(ctx context.Context) (string, error) {
					started <- cloneURL
					select {
					case <-release:
						return "", nil
					case <-ctx.Done():
						return "", ctx.Err()
					}
				})
				done <- err
			}()
		}
		synctest.Wait()
		if got := len(started); got != 2 {
			t.Fatalf("started %d distinct fetches with no releases, want 2", got)
		}
		release <- struct{}{}
		synctest.Wait()
		if got := len(started); got != 3 {
			t.Fatalf("started %d fetches after releasing a slot, want 3", got)
		}
		close(release)
		for range 4 {
			if err := <-done; err != nil {
				t.Fatal(err)
			}
		}
		if got := len(started); got != 4 {
			t.Fatalf("started %d fetches after all slots were released, want 4", got)
		}
		assertRemoteFetchIdle(t, &coordinator)
	})
}

func TestRemoteFetchCoordinatorSerializesURLBeforeTakingSlot(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var coordinator remoteFetchCoordinator
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		started := make(chan string, 3)
		releaseFirst := make(chan struct{})
		done := make(chan error, 3)
		start := func(label, cloneURL string, release <-chan struct{}) {
			go func() {
				_, err := coordinator.run(ctx, cloneURL, func(ctx context.Context) (string, error) {
					started <- label
					select {
					case <-release:
						return "", nil
					case <-ctx.Done():
						return "", ctx.Err()
					}
				})
				done <- err
			}()
		}
		start("first", "https://example.com/team/repo", releaseFirst)
		if got := <-started; got != "first" {
			t.Fatal(got)
		}
		start("same", "HTTPS://example:example@EXAMPLE.COM/team/repo.git/", releaseFirst)
		synctest.Wait()
		if len(started) != 0 {
			t.Fatal("equivalent URL started before the first fetch finished")
		}

		otherRelease := make(chan struct{})
		start("other", "https://example.com/team/other", otherRelease)
		synctest.Wait()
		if len(started) != 1 || <-started != "other" {
			t.Fatal("an equivalent URL waiting for its turn blocked another repository")
		}
		close(releaseFirst)
		synctest.Wait()
		if len(started) != 1 || <-started != "same" {
			t.Fatal("equivalent URL did not get its turn")
		}
		close(otherRelease)
		for range 3 {
			if err := <-done; err != nil {
				t.Fatal(err)
			}
		}
		assertRemoteFetchIdle(t, &coordinator)
	})
}

func TestRemoteFetchCoordinatorCancelsBothQueues(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		coordinator := remoteFetchCoordinator{slots: make(chan struct{}, 1)}
		const first, other = "https://example.com/active", "https://example.com/queued"
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		active := make(chan error, 1)
		go func() {
			_, err := coordinator.run(ctx, first, func(ctx context.Context) (string, error) {
				<-ctx.Done()
				return "", ctx.Err()
			})
			active <- err
		}()
		synctest.Wait()

		startWaiter := func(ctx context.Context, url string) <-chan error {
			done := make(chan error, 1)
			go func() {
				_, err := coordinator.run(ctx, url, func(context.Context) (string, error) {
					return "", errors.New("queued command ran")
				})
				done <- err
			}()
			return done
		}
		sameCtx, cancelSame := context.WithCancel(ctx)
		defer cancelSame()
		slotCtx, cancelSlot := context.WithTimeout(ctx, time.Minute)
		defer cancelSlot()
		same, slot := startWaiter(sameCtx, first), startWaiter(slotCtx, other)
		synctest.Wait()
		if len(same) != 0 || len(slot) != 0 {
			t.Fatal("waiter returned before cancellation")
		}
		cancelSame()
		if err := <-same; !errors.Is(err, context.Canceled) {
			t.Fatalf("waiting for URL turn returned %v", err)
		}
		if err := <-slot; !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("waiting for global slot returned %v", err)
		}
		turn := coordinator.turns[first]
		if len(coordinator.turns) != 1 || turn == nil || turn.users != 1 || len(coordinator.slots) != 1 {
			t.Fatal("cancellation did not leave exactly the active fetch")
		}
		cancel()
		if err := <-active; !errors.Is(err, context.Canceled) {
			t.Fatalf("active command returned %v", err)
		}
		assertRemoteFetchIdle(t, &coordinator)
		for _, url := range []string{first, other} {
			if _, err := coordinator.run(t.Context(), url, func(context.Context) (string, error) { return "", nil }); err != nil {
				t.Fatal(err)
			}
		}
		assertRemoteFetchIdle(t, &coordinator)
	})
}

func assertRemoteFetchIdle(t *testing.T, coordinator *remoteFetchCoordinator) {
	t.Helper()
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	if len(coordinator.turns) != 0 || len(coordinator.slots) != 0 {
		t.Fatalf("coordinator leaked %d URL turns and %d slots", len(coordinator.turns), len(coordinator.slots))
	}
}
