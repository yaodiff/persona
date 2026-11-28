package assistant

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/managerconf"
	"github.com/nwverse/persona/server/internal/secretkey"
)

// rotRig is a built-in manager after the 3a copy: its secret.key the same bytes as the hub's, its own database with
// secrets sealed with it in a column, a row of two columns and a settings value.
type rotRig struct {
	t         *testing.T
	url, home string
	key       string // the manager's secret.key
	hubKey    []byte
	d         *db.DB
	mu        sync.Mutex
	logs      []string
}

var rotPlain = map[string]string{
	`SELECT sealed FROM connector_secrets WHERE connector = 'telegram'`:            "tg-session",
	`SELECT password_enc FROM mail_accounts WHERE id = 'a'`:                        "mail-pw",
	`SELECT oauth_enc FROM mail_accounts WHERE id = 'a'`:                           "mail-oauth",
	`SELECT value->0->>'key_sealed' FROM settings WHERE key = 'launchers.managed'`: "sk-provider",
}

func newRotRig(t *testing.T) *rotRig {
	t.Helper()
	return newRotRigSets(t, db.ManagerSet)
}

func newRotRigSets(t *testing.T, sets db.Sets) *rotRig {
	t.Helper()
	r := &rotRig{t: t, url: newMigratedDatabase(t, sets), home: t.TempDir()}
	ctx := context.Background()
	was := oldKeyGrace
	oldKeyGrace = 0 // the old key goes as soon as nothing else holds it (TestTheOldKeyStaysForItsGrace has one)
	t.Cleanup(func() { oldKeyGrace = was })
	d, err := db.Open(ctx, r.url, db.ManagerSet)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.Close)
	r.d = d
	if r.hubKey, err = secretkey.Fresh(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(r.home, managerconf.SecretKey), r.hubKey, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := managerconf.MoveFiles(r.home); err != nil { // the 3a copy: the same bytes in its own directory
		t.Fatal(err)
	}
	r.key = managerconf.OwnFile(r.home, managerconf.SecretKey)
	if r.key != filepath.Join(managerconf.DefaultDir(r.home), managerconf.SecretKey) {
		t.Fatalf("own key %s", r.key)
	}
	seal := func(p string) string { return r.seal(r.hubKey, p) }
	r.exec(`INSERT INTO connector_secrets (connector, sealed) VALUES ('telegram', $1)`, seal("tg-session"))
	r.exec(`INSERT INTO mail_accounts (id, email, data, password_enc, oauth_enc) VALUES ('a', 'me@example.com', '{}', $1, $2)`,
		seal("mail-pw"), seal("mail-oauth"))
	r.exec(`INSERT INTO settings (key, value) VALUES ('launchers.managed', jsonb_build_array(jsonb_build_object('name', 'glm', 'key_sealed', $1::text)))`,
		seal("sk-provider"))
	return r
}

func (r *rotRig) seal(key []byte, plain string) string {
	r.t.Helper()
	s, err := secretkey.SealWith(key, plain)
	if err != nil {
		r.t.Fatal(err)
	}
	return s
}

func (r *rotRig) exec(q string, args ...any) {
	r.t.Helper()
	c, err := pgx.Connect(context.Background(), r.url+"&application_name=persona-test")
	if err != nil {
		r.t.Fatal(err)
	}
	defer c.Close(context.Background())
	if _, err := c.Exec(context.Background(), q, args...); err != nil {
		r.t.Fatalf("%s: %v", q, err)
	}
}

func (r *rotRig) value(q string) string {
	r.t.Helper()
	c, err := pgx.Connect(context.Background(), r.url+"&application_name=persona-test")
	if err != nil {
		r.t.Fatal(err)
	}
	defer c.Close(context.Background())
	var v string
	if err := c.QueryRow(context.Background(), q).Scan(&v); err != nil {
		r.t.Fatalf("%s: %v", q, err)
	}
	return v
}

func (r *rotRig) logf(f string, a ...any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.logs = append(r.logs, fmt.Sprintf(f, a...))
}

func (r *rotRig) logged(part string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, l := range r.logs {
		if strings.Contains(l, part) {
			return true
		}
	}
	return false
}

func (r *rotRig) rotate() error {
	_, err := rotateSecretKey(context.Background(), r.d, r.home, r.key, r.logf)
	return err
}

func (r *rotRig) current() []byte {
	r.t.Helper()
	k, err := secretkey.ReadKey(r.key)
	if err != nil {
		r.t.Fatal(err)
	}
	return k
}

// rotated: secret.key is a key of its own, every secret opens with it and none with the hub's, the database records
// it, nothing is left in secret.key.next, and the hub's key is as it was.
func (r *rotRig) rotated() []byte {
	r.t.Helper()
	key := r.current()
	if bytes.Equal(key, r.hubKey) {
		r.t.Fatal("secret.key is still the hub's")
	}
	for q, want := range rotPlain {
		v := r.value(q)
		if p, err := secretkey.OpenWith(key, v); err != nil || p != want {
			r.t.Fatalf("%s: %q %v with secret.key", q, p, err)
		}
		if _, err := secretkey.OpenWith(r.hubKey, v); err == nil {
			r.t.Fatalf("%s: still opens with the hub's key", q)
		}
	}
	rec, err := r.d.SealingKey(context.Background())
	if err != nil || rec == nil || rec.Fingerprint != secretkey.Fingerprint(key) || rec.Previous != secretkey.Fingerprint(r.hubKey) {
		r.t.Fatalf("record %+v %v", rec, err)
	}
	if left, _ := filepath.Glob(secretkey.NextGlob(r.key)); len(left) != 0 {
		r.t.Fatalf("new keys' files left: %v", left)
	}
	if hub, err := os.ReadFile(filepath.Join(r.home, managerconf.SecretKey)); err != nil || !bytes.Equal(hub, r.hubKey) {
		r.t.Fatalf("the hub's secret.key changed: %v", err)
	}
	return key
}

// waitAlone waits for the connections of a pool just closed (another manager process) to leave the server.
func (r *rotRig) waitAlone() {
	r.t.Helper()
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
		if n, err := r.d.OtherManagers(context.Background()); err == nil && n == 0 {
			return
		}
	}
	r.t.Fatal("the other process's connections did not leave")
}

// waitGone waits for the old key's file to be deleted.
func (r *rotRig) waitGone() {
	r.t.Helper()
	for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		if !r.prevKept() {
			return
		}
	}
	r.t.Fatalf("the old key still kept: %v", r.logs)
}

func (r *rotRig) prevKept() bool {
	_, err := os.Lstat(secretkey.PrevPath(r.key))
	return err == nil
}

// The first start on its own database rotates: a fresh key, every secret sealed again, the old key deleted (no
// other manager uses the database); a second start changes nothing.
func TestTheBuiltinManagerRotatesItsKey(t *testing.T) {
	r := newRotRig(t)
	if err := r.rotate(); err != nil {
		t.Fatal(err)
	}
	key := r.rotated()
	if r.prevKept() {
		t.Fatalf("the old key was kept: %v", r.logs)
	}
	rec, _ := r.d.SealingKey(context.Background())
	if rec.PreviousDropped.IsZero() {
		t.Fatal("the drop is not recorded")
	}
	before := map[string]string{}
	for q := range rotPlain {
		before[q] = r.value(q)
	}
	if err := r.rotate(); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(r.current(), key) {
		t.Fatal("a second start rotated again")
	}
	for q, v := range before {
		if r.value(q) != v {
			t.Fatalf("%s: sealed again by a second start", q)
		}
	}
	// what the services make now opens with the new key, also through a Box with the fallback
	s, err := secretkey.NewWithFallback(r.key).Seal("new")
	if err != nil {
		t.Fatal(err)
	}
	if p, err := secretkey.OpenWith(key, s); err != nil || p != "new" {
		t.Fatalf("sealed with %q %v", p, err)
	}
}

// While another manager process uses the database (one started before may still seal with the old key), the old key
// stays, only for reading; a later start seals again what that process sealed meanwhile and deletes it.
func TestTheOldKeyStaysWhileAnotherManagerRuns(t *testing.T) {
	r := newRotRig(t)
	other, err := db.Attach(context.Background(), r.url, db.ManagerSet)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if _, err := other.SealingKey(context.Background()); err != nil { // a connection of its pool stays open
		t.Fatal(err)
	}
	if err := r.rotate(); err != nil {
		t.Fatal(err)
	}
	key := r.rotated()
	if !r.prevKept() || !r.logged("while another manager process uses its database") {
		t.Fatalf("the old key was not kept: %v", r.logs)
	}
	// the older process seals with the old key meanwhile; the services read it through the fallback
	late := r.seal(r.hubKey, "tg-session")
	r.exec(`UPDATE connector_secrets SET sealed = $1`, late)
	if p, err := secretkey.NewWithFallback(r.key).Open(late); err != nil || p != "tg-session" {
		t.Fatalf("fallback %q %v", p, err)
	}
	if err := r.rotate(); err != nil { // still there: kept again, but what it sealed is sealed again
		t.Fatal(err)
	}
	if !r.prevKept() {
		t.Fatal("dropped while the other process runs")
	}
	if v := r.value(`SELECT sealed FROM connector_secrets`); v == late {
		t.Fatal("not sealed again")
	}
	other.Close()
	r.waitAlone()
	if err := r.rotate(); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(r.rotated(), key) {
		t.Fatal("rotated again")
	}
	if r.prevKept() {
		t.Fatalf("the old key stays after the other process stopped: %v", r.logs)
	}
	if _, err := secretkey.NewWithFallback(r.key).Open(late); err == nil {
		t.Fatal("the old key still opens through the fallback")
	}
}

// A start stopped after any step (a crash there) leaves a state the next start goes on from: in the end every secret
// opens with secret.key, and the old key is gone.
func TestTheRotationGoesOnAfterAStop(t *testing.T) {
	for _, step := range []string{"kept", "staged", "resealed", "swapped", "drop"} {
		t.Run(step, func(t *testing.T) {
			r := newRotRig(t)
			keyRotationStep = func(s string) error {
				if s == step {
					return errStopped
				}
				return nil
			}
			err := r.rotate()
			keyRotationStep = func(string) error { return nil }
			if err != nil {
				t.Fatal(err)
			}
			// what the services would read if it went on now opens: with the key file and the kept keys. Not after
			// the commit and before the rename: nothing reads then, the next start finishes the rename first.
			if step != "resealed" {
				box := secretkey.NewWithFallback(r.key)
				for q, want := range rotPlain {
					if p, err := box.Open(r.value(q)); err != nil || p != want {
						t.Fatalf("after stopping at %s, %s: %q %v", step, q, p, err)
					}
				}
			}
			if err := r.rotate(); err != nil {
				t.Fatal(err)
			}
			r.rotated()
			if r.prevKept() {
				t.Fatalf("the old key stays: %v", r.logs)
			}
		})
	}
}

// Two starts at once: one rotates, the other waits for its lock and finds it done.
func TestTwoRotationsAtOnce(t *testing.T) {
	r := newRotRig(t)
	second, err := db.Open(context.Background(), r.url, db.ManagerSet)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i, d := range []*db.DB{r.d, second} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = rotateSecretKey(context.Background(), d, r.home, r.key, r.logf)
		}()
	}
	wg.Wait()
	if errs[0] != nil || errs[1] != nil {
		t.Fatal(errs)
	}
	key := r.rotated()
	second.Close()
	r.waitAlone()
	if err := r.rotate(); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(r.rotated(), key) || r.prevKept() {
		t.Fatalf("after both: rotated again or the old key kept: %v", r.logs)
	}
	n := 0
	for _, l := range r.logs {
		if strings.Contains(l, "rotated to a key of its own") {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("rotated %d times: %v", n, r.logs)
	}
}

// It touches only the manager's own key in its own database: not the data directory's (the hub's) when that is the one
// it runs with, nor a database that has the hub's tables.
func TestTheRotationNeverTouchesTheHubs(t *testing.T) {
	t.Run("the hub's key file", func(t *testing.T) {
		r := newRotRig(t)
		hubPath := filepath.Join(r.home, managerconf.SecretKey)
		if err := os.Remove(r.key); err != nil {
			t.Fatal(err)
		}
		if got := managerconf.OwnFile(r.home, managerconf.SecretKey); got != hubPath {
			t.Fatalf("OwnFile %s", got)
		}
		if _, err := rotateSecretKey(context.Background(), r.d, r.home, hubPath, r.logf); err != nil {
			t.Fatal(err)
		}
		if b, err := os.ReadFile(hubPath); err != nil || !bytes.Equal(b, r.hubKey) {
			t.Fatalf("the hub's key changed: %v", err)
		}
		if _, err := os.Lstat(secretkey.PrevPath(hubPath)); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("secret.key.prev made beside the hub's key")
		}
		if left, _ := filepath.Glob(secretkey.NextGlob(hubPath)); len(left) != 0 {
			t.Fatalf("new keys beside the hub's key: %v", left)
		}
		if rec, err := r.d.SealingKey(context.Background()); err != nil || rec != nil {
			t.Fatalf("recorded %+v %v", rec, err)
		}
	})
	t.Run("the hub's database", func(t *testing.T) {
		r := newRotRigSets(t, db.AllSets)
		shared, err := db.Open(context.Background(), r.url, db.AllSets) // the hub's set too: a database it shares
		if err != nil {
			t.Fatal(err)
		}
		defer shared.Close()
		before := r.value(`SELECT sealed FROM connector_secrets`)
		if _, err := rotateSecretKey(context.Background(), shared, r.home, r.key, r.logf); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(r.current(), r.hubKey) || r.value(`SELECT sealed FROM connector_secrets`) != before || r.prevKept() {
			t.Fatal("rotated in a database the hub shares")
		}
	})
}

// A legacy mail file not imported yet holds passwords sealed with the old key: the old key stays until it is.
func TestTheOldKeyStaysForALegacyMailFile(t *testing.T) {
	r := newRotRig(t)
	legacy := filepath.Join(r.home, legacyMailFile)
	if err := os.WriteFile(legacy, []byte(`{"accounts":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := r.rotate(); err != nil {
		t.Fatal(err)
	}
	r.rotated()
	if !r.prevKept() {
		t.Fatal("dropped with a legacy mail file waiting")
	}
	if err := os.Rename(legacy, legacy+".migrated"); err != nil { // what the import does
		t.Fatal(err)
	}
	if err := r.rotate(); err != nil {
		t.Fatal(err)
	}
	if r.prevKept() {
		t.Fatal("kept after the import")
	}
}

// A key file that is not the one the database names (restored from an old backup, the record says another) is left
// alone: nothing is sealed again with it and nothing deleted.
func TestAKeyTheDatabaseDoesNotName(t *testing.T) {
	r := newRotRig(t)
	if err := r.rotate(); err != nil {
		t.Fatal(err)
	}
	key := r.rotated()
	other, _ := secretkey.Fresh()
	if err := secretkey.WriteKeys(r.key, other); err != nil {
		t.Fatal(err)
	}
	before := r.value(`SELECT sealed FROM connector_secrets`)
	if err := r.rotate(); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(r.current(), other) || r.value(`SELECT sealed FROM connector_secrets`) != before || !r.logged("sealed with another key") {
		t.Fatalf("acted on a key the database does not name: %v", r.logs)
	}
	// putting the right one back makes it whole again
	if err := secretkey.WriteKeys(r.key, key); err != nil {
		t.Fatal(err)
	}
	if err := r.rotate(); err != nil {
		t.Fatal(err)
	}
	r.rotated()
}

// Recovery from a database backup made before the rotation (its secrets sealed with the hub's key, which the hub
// still has): the hub's key put in secret.key.prev is used to seal them again, then deleted.
func TestRestoringADatabaseFromBeforeTheRotation(t *testing.T) {
	r := newRotRig(t)
	if err := r.rotate(); err != nil {
		t.Fatal(err)
	}
	r.rotated()
	// the backup: the old values and no record
	r.exec(`DELETE FROM sealing_key`)
	r.exec(`UPDATE connector_secrets SET sealed = $1`, r.seal(r.hubKey, "tg-session"))
	if err := secretkey.WriteKeys(secretkey.PrevPath(r.key), r.hubKey); err != nil {
		t.Fatal(err)
	}
	if err := r.rotate(); err != nil {
		t.Fatal(err)
	}
	key := r.current()
	if p, err := secretkey.OpenWith(key, r.value(`SELECT sealed FROM connector_secrets`)); err != nil || p != "tg-session" {
		t.Fatalf("not recovered: %q %v (%v)", p, err, r.logs)
	}
	if r.prevKept() {
		t.Fatal("the old keys stay")
	}
}

// A running manager that kept the old key deletes it once the other process stops, without a restart; the watcher
// never rotates itself.
func TestTheOldKeyGoesWhileItRuns(t *testing.T) {
	r := newRotRig(t)
	other, err := db.Attach(context.Background(), r.url, db.ManagerSet)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if _, err := other.SealingKey(context.Background()); err != nil {
		t.Fatal(err)
	}
	pending, err := rotateSecretKey(context.Background(), r.d, r.home, r.key, r.logf)
	if err != nil || !pending || !r.prevKept() {
		t.Fatalf("pending %v %v kept %v", pending, err, r.prevKept())
	}
	key := r.rotated()
	defer func(was time.Duration) { oldKeyCheckEvery = was }(oldKeyCheckEvery)
	oldKeyCheckEvery = 50 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		keepDroppingOldKey(ctx, r.d, r.home, r.key, r.logf)
	}()
	late := r.seal(r.hubKey, "tg-session") // what the other process sealed meanwhile
	r.exec(`UPDATE connector_secrets SET sealed = $1`, late)
	time.Sleep(200 * time.Millisecond)
	if !r.prevKept() {
		t.Fatal("dropped while the other process runs")
	}
	// a check that fails meanwhile (the record cannot be read) is tried again, not given up
	r.exec(`ALTER TABLE sealing_key RENAME TO sealing_key_away`)
	time.Sleep(300 * time.Millisecond)
	r.exec(`ALTER TABLE sealing_key_away RENAME TO sealing_key`)
	select {
	case <-done:
		t.Fatal("the watcher gave up after a failed check")
	default:
	}
	other.Close()
	r.waitGone()
	if !bytes.Equal(r.rotated(), key) {
		t.Fatal("rotated again")
	}
	// a value a service read before and writes back afterwards, sealed with the old key: sealed again within a tick
	r.exec(`UPDATE connector_secrets SET sealed = $1`, r.seal(r.hubKey, "tg-session"))
	resealed := func() bool {
		_, err := secretkey.OpenWith(key, r.value(`SELECT sealed FROM connector_secrets`))
		return err == nil
	}
	for deadline := time.Now().Add(10 * time.Second); !resealed(); time.Sleep(50 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("a value written back with the old key was not sealed again: %v", r.logs)
		}
	}
	// one written back just before the manager stops is sealed again too (by a tick or by its last pass)
	r.exec(`UPDATE connector_secrets SET sealed = $1`, r.seal(r.hubKey, "tg-session"))
	cancel()
	<-done
	if !resealed() {
		t.Fatalf("not sealed again when it stopped: %v", r.logs)
	}

	// the watcher on a database not rotated yet does nothing
	fresh := newRotRig(t)
	ctx2, cancel2 := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel2()
	keepDroppingOldKey(ctx2, fresh.d, fresh.home, fresh.key, fresh.logf)
	if !bytes.Equal(fresh.current(), fresh.hubKey) {
		t.Fatal("the watcher rotated")
	}
}

// The old key stays at least oldKeyGrace after the rotation (an older process may have no connection open when
// counted); the watcher deletes it once that is over.
func TestTheOldKeyStaysForItsGrace(t *testing.T) {
	r := newRotRig(t)
	oldKeyGrace = time.Hour
	pending, err := rotateSecretKey(context.Background(), r.d, r.home, r.key, r.logf)
	if err != nil || !pending || !r.prevKept() || !r.logged("after the rotation") {
		t.Fatalf("pending %v %v kept %v: %v", pending, err, r.prevKept(), r.logs)
	}
	r.rotated()
	if err := r.rotate(); err != nil || !r.prevKept() { // a start within the grace keeps it too
		t.Fatalf("dropped within the grace: %v", err)
	}
	oldKeyGrace = 0
	defer func(was time.Duration) { oldKeyCheckEvery = was }(oldKeyCheckEvery)
	oldKeyCheckEvery = 50 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		keepDroppingOldKey(ctx, r.d, r.home, r.key, r.logf)
	}()
	r.waitGone()
	cancel()
	<-done
}

// A start that cannot read which key its secrets are sealed with does not start: a rotation stopped before its rename
// would stay unfinished, and the services would seal with a key the database does not name.
func TestAStartThatCannotReadTheRecord(t *testing.T) {
	r := newRotRig(t)
	keyRotationStep = func(s string) error {
		if s == "resealed" {
			return errStopped
		}
		return nil
	}
	err := r.rotate()
	keyRotationStep = func(string) error { return nil }
	if err != nil {
		t.Fatal(err)
	}
	r.exec(`ALTER TABLE sealing_key RENAME TO sealing_key_away`)
	if err := r.rotate(); err == nil {
		t.Fatal("started without knowing which key the secrets are sealed with")
	}
	r.exec(`ALTER TABLE sealing_key_away RENAME TO sealing_key`)
	if err := r.rotate(); err != nil {
		t.Fatal(err)
	}
	r.rotated()
}

// The answer of a committed re-seal lost (the connection cut after COMMIT): it reads the record again, sees the commit,
// and finishes; the new key is never deleted.
func TestACommitWhoseAnswerWasLost(t *testing.T) {
	r := newRotRig(t)
	keyRotationReseal = func(ctx context.Context, d *db.DB, fn func(string) (string, db.ResealOutcome, error), expect string, rec *db.SealingKey) (db.ResealReport, error) {
		rep, err := d.ResealSecrets(ctx, fn, expect, rec)
		if err == nil {
			err = fmt.Errorf("%w: unexpected EOF", db.ErrCommitUnknown)
		}
		return rep, err
	}
	defer restoreReseal()
	if err := r.rotate(); err != nil {
		t.Fatal(err)
	}
	r.rotated()
	if !r.logged("but it was committed") {
		t.Fatalf("logs %v", r.logs)
	}
}

// A commit that did not answer and did not go through (the record still none once no re-seal is open): the new key's
// file goes, nothing changed, and the next start rotates.
func TestACommitThatDidNotGoThrough(t *testing.T) {
	r := newRotRig(t)
	keyRotationReseal = func(context.Context, *db.DB, func(string) (string, db.ResealOutcome, error), string, *db.SealingKey) (db.ResealReport, error) {
		return db.ResealReport{}, fmt.Errorf("%w: unexpected EOF", db.ErrCommitUnknown)
	}
	if err := r.rotate(); err != nil {
		t.Fatal(err)
	}
	restoreReseal()
	if !bytes.Equal(r.current(), r.hubKey) || !r.logged("not rotated") {
		t.Fatalf("rotated: %v", r.logs)
	}
	// a connection that broke: its file stays, until the record names another key
	if left, _ := filepath.Glob(secretkey.NextGlob(r.key)); len(left) != 1 {
		t.Fatalf("new keys' files: %v", left)
	}
	if err := r.rotate(); err != nil {
		t.Fatal(err)
	}
	r.rotated() // none left
}

func restoreReseal() {
	keyRotationReseal = func(ctx context.Context, d *db.DB, fn func(string) (string, db.ResealOutcome, error), expect string, rec *db.SealingKey) (db.ResealReport, error) {
		return d.ResealSecrets(ctx, fn, expect, rec)
	}
}

// A re-seal that fails without committing leaves everything as it was: the key, the values, no record, no new key's
// file; the next start rotates.
func TestAReSealThatFails(t *testing.T) {
	r := newRotRig(t)
	r.exec(`INSERT INTO settings (key, value) VALUES ('bots.feishu', '"not an object"')`)
	before := r.value(`SELECT sealed FROM connector_secrets`)
	if err := r.rotate(); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(r.current(), r.hubKey) || r.value(`SELECT sealed FROM connector_secrets`) != before || !r.logged("not rotated") {
		t.Fatalf("changed by a failed re-seal: %v", r.logs)
	}
	if left, _ := filepath.Glob(secretkey.NextGlob(r.key)); len(left) != 0 {
		t.Fatalf("new keys' files left: %v", left)
	}
	if rec, err := r.d.SealingKey(context.Background()); err != nil || rec != nil {
		t.Fatalf("recorded %+v %v", rec, err)
	}
	r.exec(`DELETE FROM settings WHERE key = 'bots.feishu'`)
	if err := r.rotate(); err != nil {
		t.Fatal(err)
	}
	r.rotated()
}

// Two processes rotating with no lock between them (its session lost): whichever commits first wins, the other goes on
// from what it did, and every secret opens with secret.key in the end.
func TestRotationsWhoseLockWasLost(t *testing.T) {
	for i := range 3 {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			r := newRotRig(t)
			second, err := db.Open(context.Background(), r.url, db.ManagerSet)
			if err != nil {
				t.Fatal(err)
			}
			defer second.Close()
			var wg sync.WaitGroup
			errs := make([]error, 2)
			for j, d := range []*db.DB{r.d, second} {
				wg.Add(1)
				go func() {
					defer wg.Done()
					k, ok := newKeyRotation(context.Background(), d, r.home, r.key, r.logf)
					if !ok {
						errs[j] = errors.New("refused")
						return
					}
					_, errs[j] = k.run(context.Background()) // no lock
				}()
			}
			wg.Wait()
			if errs[0] != nil || errs[1] != nil {
				t.Fatal(errs)
			}
			second.Close()
			r.waitAlone()
			if err := r.rotate(); err != nil {
				t.Fatal(err)
			}
			r.rotated()
			if r.prevKept() {
				t.Fatalf("the old key stays: %v", r.logs)
			}
		})
	}
}

// The watcher's pass after the old key's file went: what opens only with an old key it read is sealed again; nothing
// else is touched, and nothing is locked when there is nothing to do.
func TestResealRemembered(t *testing.T) {
	r := newRotRig(t)
	if err := r.rotate(); err != nil {
		t.Fatal(err)
	}
	key := r.rotated()
	if r.prevKept() {
		t.Fatal("kept")
	}
	k, ok := newKeyRotation(context.Background(), r.d, r.home, r.key, r.logf)
	if !ok {
		t.Fatal("refused")
	}
	before := map[string]string{}
	for q := range rotPlain {
		before[q] = r.value(q)
	}
	k.resealRemembered(context.Background()) // no old keys: nothing
	k.oldKeys = [][]byte{r.hubKey}
	k.resealRemembered(context.Background()) // nothing sealed with them: nothing
	for q, v := range before {
		if r.value(q) != v {
			t.Fatalf("%s changed with nothing to do", q)
		}
	}
	r.exec(`UPDATE mail_accounts SET password_enc = $1`, r.seal(r.hubKey, "mail-pw"))
	k.resealRemembered(context.Background())
	if p, err := secretkey.OpenWith(key, r.value(`SELECT password_enc FROM mail_accounts`)); err != nil || p != "mail-pw" {
		t.Fatalf("not sealed again: %q %v", p, err)
	}
	if r.value(`SELECT oauth_enc FROM mail_accounts`) != before[`SELECT oauth_enc FROM mail_accounts WHERE id = 'a'`] {
		t.Fatal("touched a value that opened with secret.key")
	}
}
