package iphone

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// fakeBridgeScript speaks the bridge's command line (its wire contract, #3451) well enough for these tests.
// serve answers every request, except op "drop", which loses the link without an answer.
const fakeBridgeScript = `#!/bin/sh
cmd=$1; shift
case "$cmd" in
version) echo "0.0.0-test" ;;
usb-list) echo '[{"udid":"00000000-TESTPHONE0001","name":"Test Phone","product_version":"26.1"}]' ;;
pair)
  udid=$2; out=$4
  # FAKE_PAIR_GATE=<file>: pairing waits until the file is there, so a test decides when a pair job may end.
  if [ -n "$FAKE_PAIR_GATE" ]; then
    while [ ! -e "$FAKE_PAIR_GATE" ]; do sleep 0.01; done
  fi
  if [ -n "$FAKE_PAIR_FAIL" ]; then
    echo "diagnostics" >&2
    echo '{"v":1,"ok":false,"error":{"code":"trust_pending","message":"tap Trust on the phone"}}' >&2
    exit 1
  fi
  # FAKE_PAIR_WAITS=<file>: each line in the file is one answer to give before pairing (trust_pending, locked, …).
  if [ -n "$FAKE_PAIR_WAITS" ] && [ -s "$FAKE_PAIR_WAITS" ]; then
    code=$(head -1 "$FAKE_PAIR_WAITS"); tail -n +2 "$FAKE_PAIR_WAITS" > "$FAKE_PAIR_WAITS.next"; mv "$FAKE_PAIR_WAITS.next" "$FAKE_PAIR_WAITS"
    echo '{"v":1,"ok":false,"error":{"code":"'"$code"'","message":"not yet"}}' >&2
    exit 1
  fi
  printf 'opaque' > "$out"
  if [ -n "$FAKE_SIMULATOR" ]; then
    echo '{"udid":"'"$udid"'","name":"Test Simulator","model":"iPhone99,1","product_version":"26.1","transport":"simulator"}'
    exit 0
  fi
  if [ -n "$FAKE_DEVMODE_OFF" ]; then
    echo '{"udid":"'"$udid"'","name":"Test Phone","model":"iPhone99,1","product_version":"27.0","developer_mode":false}'
    exit 0
  fi
  echo '{"udid":"'"$udid"'","name":"Test Phone","model":"iPhone99,1","product_version":"26.1"}'
  ;;
reveal-developer-mode)
  # reveal-developer-mode --udid <udid>: noted in FAKE_REVEAL_LOG
  if [ -n "$FAKE_REVEAL_LOG" ]; then echo "$2" >> "$FAKE_REVEAL_LOG"; fi
  if [ -n "$FAKE_REVEAL_FAIL" ]; then
    echo '{"v":1,"ok":false,"error":{"code":"failed","message":"no AMFI service"}}' >&2
    exit 1
  fi
  echo '{"udid":"'"$2"'","revealed":true}'
  ;;
match)
  # match --record <path> --identifier <id> --auth-tag <tag>: the record "knows" the tag TAG-1
  if [ "$6" = "TAG-1" ]; then echo '{"match":true}'; else echo '{"match":false}'; fi
  ;;
serve)
  if [ -n "$FAKE_SERVE_FAIL" ]; then
    echo '{"v":1,"ok":false,"error":{"code":"developer_mode_off","message":"turn on Developer Mode"}}' >&2
    exit 1
  fi
  echo "$@" > "$FAKE_SERVE_ARGS"
  # FAKE_DDI_UNMOUNTED: the phone comes up without the developer disk image, and device_info says so until mount_ddi.
  mounted=true
  if [ -n "$FAKE_DDI_UNMOUNTED" ]; then
    mounted=false
    echo '{"event":"ready","device":{"name":"Test Phone","developer_mode":true,"ddi_mounted":false}}'
  else
    echo '{"event":"ready","device":{"name":"Test Phone"}}'
  fi
  while IFS= read -r line; do
    id=$(printf '%s' "$line" | sed 's/.*"id":"\([^"]*\)".*/\1/')
    op=$(printf '%s' "$line" | sed 's/.*"op":"\([^"]*\)".*/\1/')
    echo "$op" >> "$FAKE_SERVE_LOG"
    # FAKE_SERVE_PIDLOG: each request as "<serve pid> <op>", to tell one link's requests from the next one's.
    if [ -n "$FAKE_SERVE_PIDLOG" ]; then echo "$$ $op" >> "$FAKE_SERVE_PIDLOG"; fi
    if [ -n "$FAKE_SERVE_REQUESTS" ]; then echo "$line" >> "$FAKE_SERVE_REQUESTS"; fi
    if [ "$op" = "drop" ]; then
      echo '{"event":"disconnected","reason":"wifi lost"}'
      exit 1
    fi
    if [ "$op" = "mount_ddi" ]; then
      # FAKE_MOUNT_SILENT=<file>: while the file is there, mount_ddi gets no answer at all (a mount that hangs).
      if [ -n "$FAKE_MOUNT_SILENT" ] && [ -e "$FAKE_MOUNT_SILENT" ]; then continue; fi
      # FAKE_MOUNT_FAIL=<file>: while the file is there, mount_ddi fails with the code it holds (locked, ddi_missing, …).
      # Read once: a file removed between a check and the read would give an answer without a code.
      code=""
      if [ -n "$FAKE_MOUNT_FAIL" ]; then code=$(cat "$FAKE_MOUNT_FAIL" 2>/dev/null); fi
      if [ -n "$code" ]; then
        echo '{"v":1,"id":"'"$id"'","ok":false,"op":"mount_ddi","error":{"code":"'"$code"'","message":"not now"}}'
        continue
      fi
      mounted=true
      echo '{"v":1,"id":"'"$id"'","ok":true,"op":"mount_ddi","result":{"mounted":true,"already":false,"image":"x.dmg","ticket":"apple","services_refreshed":true}}'
      continue
    fi
    if [ "$op" = "device_info" ] && [ -n "$FAKE_DDI_UNMOUNTED" ]; then
      echo '{"v":1,"id":"'"$id"'","ok":true,"op":"device_info","result":{"name":"Test Phone","developer_mode":true,"ddi_mounted":'"$mounted"'}}'
      continue
    fi
    echo '{"v":1,"id":"'"$id"'","ok":true,"op":"'"$op"'","result":{"op":"'"$op"'"}}'
  done
  exit 0
  ;;
*) echo '{"v":1,"ok":false,"error":{"code":"bad_request","message":"unknown command"}}' >&2; exit 1 ;;
esac
`

// fakeBridge writes the script and points FAKE_SERVE_LOG / FAKE_SERVE_ARGS at files; it returns the script's path
// and the log's.
func fakeBridge(t *testing.T) (string, string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake bridge is a shell script")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, BridgeName)
	if err := os.WriteFile(path, []byte(fakeBridgeScript), 0o755); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(dir, "serve.log")
	t.Setenv("FAKE_SERVE_LOG", log)
	t.Setenv("FAKE_SERVE_ARGS", filepath.Join(dir, "serve.args"))
	return path, log
}

func TestBridgeOneShots(t *testing.T) {
	path, _ := fakeBridge(t)
	b := Bridge{Path: path}
	ctx := context.Background()
	if v, err := b.Version(ctx); err != nil || v != "0.0.0-test" {
		t.Fatalf("Version = %q, %v", v, err)
	}
	list, err := b.USBList(ctx)
	if err != nil || len(list) != 1 || list[0].UDID != testUDID || list[0].ProductVersion != "26.1" {
		t.Fatalf("USBList = %+v, %v", list, err)
	}
	out := filepath.Join(t.TempDir(), "rec")
	info, err := b.Pair(ctx, testUDID, out)
	if err != nil || info.ProductType != "iPhone99,1" || info.Name != "Test Phone" {
		t.Fatalf("Pair = %+v, %v", info, err)
	}
	for tag, want := range map[string]bool{"TAG-1": true, "TAG-2": false} {
		svc := Service{TXT: map[string]string{"identifier": "ID", "authtag": tag}}
		if ok, err := b.Match(ctx, out, svc); err != nil || ok != want {
			t.Fatalf("Match(%s) = %v, %v", tag, ok, err)
		}
	}
	t.Setenv("FAKE_PAIR_FAIL", "1")
	if _, err := b.Pair(ctx, testUDID, out); Code(err) != "trust_pending" {
		t.Fatalf("want the bridge's code from stderr's last line, got %v", err)
	}
}

func TestFindBridge(t *testing.T) {
	path, _ := fakeBridge(t)
	t.Setenv(BridgeEnv, path)
	if got, err := FindBridge(); err != nil || got != path {
		t.Fatalf("FindBridge = %q, %v", got, err)
	}
	// Not in PATH: <PERSONA_HOME>/bin.
	home := t.TempDir()
	t.Setenv("PERSONA_HOME", home)
	t.Setenv("PATH", t.TempDir())
	t.Setenv(BridgeEnv, "")
	_ = os.MkdirAll(filepath.Join(home, "bin"), 0o700)
	data, _ := os.ReadFile(path)
	inHome := filepath.Join(home, "bin", BridgeName)
	_ = os.WriteFile(inHome, data, 0o755)
	if got, err := FindBridge(); err != nil || got != inHome {
		t.Fatalf("FindBridge = %q, %v", got, err)
	}
	t.Setenv(BridgeEnv, filepath.Join(t.TempDir(), "missing"))
	if _, err := FindBridge(); Code(err) != CodeBridgeMissing {
		t.Fatalf("want bridge_missing, got %v", err)
	}
}

// bridgeManager is a Manager over the fake bridge script, with a paired phone at a manual address.
func bridgeManager(t *testing.T) (*Manager, string) {
	t.Helper()
	fastTiming(t)
	path, log := fakeBridge(t)
	m := New(t.TempDir())
	t.Cleanup(m.Close)
	m.findBridge = func() (string, error) { return path, nil }
	m.browse = func(context.Context, time.Duration, func([]Service) bool) ([]Service, error) { return nil, nil }
	m.logf = t.Logf
	return m, log
}

func TestSessionOverBridgeProcess(t *testing.T) {
	m, log := bridgeManager(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := m.Pair(ctx, testUDID, nil); err != nil {
		t.Fatal(err)
	}
	if err := m.SetAddress(testUDID, "192.0.2.7:50000"); err != nil {
		t.Fatal(err)
	}
	res, err := m.Call(ctx, testUDID, "tap", json.RawMessage(`{"x":1,"y":2}`))
	if err != nil || string(res) != `{"op":"tap"}` {
		t.Fatalf("Call = %s, %v", res, err)
	}
	args, _ := os.ReadFile(os.Getenv("FAKE_SERVE_ARGS"))
	rec, _ := m.store.RecordPath(testUDID)
	if want := "--udid " + testUDID + " --record " + rec + " --host 192.0.2.7 --port 50000"; strings.TrimSpace(string(args)) != want {
		t.Fatalf("serve args %q, want %q", args, want)
	}
	if _, err := m.Call(ctx, testUDID, "drop", nil); Code(err) != CodeUncertain {
		t.Fatalf("want uncertain, got %v", err)
	}
	if res, err := m.Call(ctx, testUDID, "screenshot", nil); err != nil || string(res) != `{"op":"screenshot"}` {
		t.Fatalf("after reconnect: %s, %v", res, err)
	}
	ops, _ := os.ReadFile(log)
	if got := strings.Fields(string(ops)); strings.Join(got, ",") != "tap,drop,screenshot" {
		t.Fatalf("bridge got %v: drop must reach it once", got)
	}
	d := m.Devices(ctx)
	if len(d) != 1 || !d[0].Paired || !d[0].USB || d[0].State != StateConnected || d[0].ManualAddress != "192.0.2.7:50000" {
		t.Fatalf("Devices = %+v", d)
	}
}

func TestServeFailureBeforeReady(t *testing.T) {
	m, _ := bridgeManager(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := m.Pair(ctx, testUDID, nil); err != nil {
		t.Fatal(err)
	}
	_ = m.SetAddress(testUDID, "192.0.2.7")
	t.Setenv("FAKE_SERVE_FAIL", "1")
	// Waiting won't fix this one: the call ends with the bridge's reason without waiting out its deadline.
	cctx, ccancel := context.WithTimeout(ctx, 5*time.Second)
	defer ccancel()
	started := time.Now()
	_, err := m.Call(cctx, testUDID, "tap", nil)
	if Code(err) != "developer_mode_off" || Sent(err) != "no" || time.Since(started) > 3*time.Second {
		t.Fatalf("want developer_mode_off at once, got %v after %s", err, time.Since(started))
	}
	if d := m.Devices(ctx); d[0].LastErrorCode != "developer_mode_off" {
		t.Fatalf("Devices = %+v", d[0])
	}
}

func TestPairKeepsAddressAndCleansUpOnFailure(t *testing.T) {
	m, _ := bridgeManager(t)
	ctx := context.Background()
	t.Setenv("FAKE_PAIR_FAIL", "1")
	// The phone never trusts this computer: Pair waits for it until its context ends.
	short, cancel := context.WithTimeout(ctx, 150*time.Millisecond)
	defer cancel()
	if _, err := m.Pair(short, testUDID, nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("want the wait to run out, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(m.store.Dir, testUDID)); !os.IsNotExist(err) {
		t.Fatalf("a failed first pairing left its directory: %v", err)
	}
	t.Setenv("FAKE_PAIR_FAIL", "")
	if _, err := m.Pair(ctx, testUDID, nil); err != nil {
		t.Fatal(err)
	}
	_ = m.SetAddress(testUDID, "phone.example.com")
	rec, err := m.Pair(ctx, testUDID, nil)
	if err != nil || rec.Address != "phone.example.com" || rec.ProductType != "iPhone99,1" {
		t.Fatalf("pairing again: %+v, %v", rec, err)
	}
	path, _ := m.store.RecordPath(testUDID)
	st, err := os.Stat(path)
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("record: %v %v", st, err)
	}
	if b, _ := os.ReadFile(path); !bytes.Equal(b, []byte("opaque")) {
		t.Fatalf("record = %q", b)
	}
	entries, _ := os.ReadDir(filepath.Join(m.store.Dir, testUDID))
	if len(entries) != 2 {
		t.Fatalf("left over files: %v", entries)
	}
	// A failure pairing again keeps the pairing there was.
	t.Setenv("FAKE_PAIR_FAIL", "1")
	short2, cancel2 := context.WithTimeout(ctx, 150*time.Millisecond)
	defer cancel2()
	if _, err := m.Pair(short2, testUDID, nil); err == nil {
		t.Fatal("want failure")
	}
	if _, err := m.store.Load(testUDID); err != nil {
		t.Fatalf("the earlier pairing is gone: %v", err)
	}
}

func TestPairJob(t *testing.T) {
	m, _ := bridgeManager(t)
	// The pairing ends only once the gate is there: a second job is refused while the first surely runs, rather than
	// after reading its state, which the pairing could end behind before the second job starts (#16230).
	gate := filepath.Join(t.TempDir(), "pair-gate")
	t.Setenv("FAKE_PAIR_GATE", gate)
	if err := m.StartPair(testUDID); err != nil {
		t.Fatal(err)
	}
	if err := m.StartForget(testUDID); Code(err) != CodeBusy {
		t.Fatalf("a second job: %v", err)
	}
	if err := os.WriteFile(gate, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if j := waitJob(t, m); j.State != JobDone || j.Kind != "pair" || j.UDID != testUDID {
		t.Fatalf("job = %+v", j)
	}
	if _, err := m.store.Load(testUDID); err != nil {
		t.Fatalf("after pairing: %v", err)
	}
	// A phone that doesn't trust this computer yet: the job says so while it waits. It has no deadline to say it
	// before, however long the bridge takes to answer; it is cancelled once it has (#16230).
	t.Setenv("FAKE_PAIR_FAIL", "1")
	if err := m.StartPair(testUDID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { j := m.Job(); return j.Waiting == CodeTrustPending || j.State != JobRunning })
	if j := m.Job(); j.State != JobRunning || j.Waiting != CodeTrustPending {
		t.Fatalf("job = %+v, want it running and waiting for trust", j)
	}
	m.Cancel()
	if j := waitJob(t, m); j.State != JobCancelled || j.Waiting != "" {
		t.Fatalf("job = %+v", j)
	}
	// One that never trusts this computer: the job waits until its time runs out.
	saved := pairTimeout
	pairTimeout = 300 * time.Millisecond
	t.Cleanup(func() { pairTimeout = saved })
	if err := m.StartPair(testUDID); err != nil {
		t.Fatal(err)
	}
	if j := waitJob(t, m); j.State != JobFailed || j.Code != "timeout" || j.Waiting != "" {
		t.Fatalf("job = %+v", j)
	}
	if err := m.StartForget(testUDID); err != nil {
		t.Fatal(err)
	}
	if j := waitJob(t, m); j.State != JobDone || j.Kind != "forget" {
		t.Fatalf("job = %+v", j)
	}
	if _, err := m.store.Load(testUDID); err != ErrNotPaired {
		t.Fatalf("after forget: %v", err)
	}
}

// TestPairWaitsForThePhone: while the phone is locked, then doesn't trust this computer yet, Pair asks again and
// says what it waits for, once each, and pairs once the phone trusts it (#8199).
func TestPairWaitsForThePhone(t *testing.T) {
	m, _ := bridgeManager(t)
	waits := filepath.Join(t.TempDir(), "waits")
	if err := os.WriteFile(waits, []byte("locked\ntrust_pending\ntrust_pending\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAKE_PAIR_WAITS", waits)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var said []string
	rec, err := m.Pair(ctx, testUDID, func(step, waiting string) { said = append(said, waiting) })
	if err != nil || rec.UDID != testUDID {
		t.Fatalf("Pair = %+v, %v", rec, err)
	}
	if got := strings.Join(said, ","); got != ",locked,trust_pending," {
		t.Fatalf("progress said waiting %q", got)
	}
	// Another refusal is not waited out.
	if err := os.WriteFile(waits, []byte("pairing_rejected\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Pair(ctx, testUDID, nil); Code(err) != "pairing_rejected" {
		t.Fatalf("want pairing_rejected at once, got %v", err)
	}
}

// gatedWriter's Write finishes only when release is closed, then succeeds or fails.
type gatedWriter struct {
	release chan struct{}
	fail    bool
}

func (w *gatedWriter) Write(p []byte) (int, error) {
	<-w.release
	if w.fail {
		return 0, errors.New("file already closed")
	}
	return len(p), nil
}

func TestSendLineAtTheDeadline(t *testing.T) {
	for _, fail := range []bool{false, true} {
		w := &gatedWriter{release: make(chan struct{}), fail: fail}
		ctx, cancel := context.WithCancel(context.Background())
		cancel() // the deadline has passed while the write is under way
		closed := false
		// Closing the link is what ends a blocked write; here it lets the write finish, as it may in a real race.
		err := sendLine(ctx, w, func() { closed = true; close(w.release) }, []byte(`{"op":"tap"}`))
		if !closed {
			t.Fatal("the link wasn't closed")
		}
		if !fail && err != nil {
			t.Fatalf("the whole line was written: want nil (the caller waits for the answer), got %v", err)
		}
		if fail && err == nil {
			t.Fatal("a failed write is a line that never left")
		}
	}
}

func TestSimulatorRecordOverBridgeProcess(t *testing.T) {
	m, _ := bridgeManager(t)
	browsed := false
	m.browse = func(context.Context, time.Duration, func([]Service) bool) ([]Service, error) {
		browsed = true
		return nil, nil
	}
	t.Setenv("FAKE_SIMULATOR", "1")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	rec, err := m.Pair(ctx, testUDID, nil)
	if err != nil || !rec.Simulator() {
		t.Fatalf("Pair = %+v, %v", rec, err)
	}
	// No address anywhere, and none needed.
	if res, err := m.Call(ctx, testUDID, "tap", nil); err != nil || string(res) != `{"op":"tap"}` {
		t.Fatalf("Call = %s, %v", res, err)
	}
	args, _ := os.ReadFile(os.Getenv("FAKE_SERVE_ARGS"))
	path, _ := m.store.RecordPath(testUDID)
	if want := "--udid " + testUDID + " --record " + path; strings.TrimSpace(string(args)) != want {
		t.Fatalf("serve args %q, want %q (no --host for a Simulator device)", args, want)
	}
	if browsed {
		t.Fatal("a Simulator device was looked for on the network")
	}
	d := m.Devices(ctx)
	if len(d) != 1 || d[0].Transport != TransportSimulator || d[0].State != StateConnected || d[0].AddressSource != TransportSimulator {
		t.Fatalf("Devices = %+v", d)
	}
}

// TestDeveloperModeFollowsThePhone: a phone paired with Developer Mode off says so in Devices, and once its
// device_info says it is on (it was turned on after pairing), the record and Devices follow (#8199).
func TestDeveloperModeFollowsThePhone(t *testing.T) {
	m, _ := bridgeManager(t)
	t.Setenv("FAKE_DEVMODE_OFF", "1")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := m.Pair(ctx, testUDID, nil); err != nil {
		t.Fatal(err)
	}
	if d := m.Devices(ctx); len(d) != 1 || d[0].DeveloperMode == nil || *d[0].DeveloperMode {
		t.Fatalf("after pairing with it off: %+v", d)
	}
	m.remember(testUDID, Endpoint{}, json.RawMessage(`{"name":"Test Phone","developer_mode":true}`))
	if rec, err := m.store.Load(testUDID); err != nil || rec.DeveloperMode == nil || !*rec.DeveloperMode {
		t.Fatalf("record = %+v, %v", rec, err)
	}
	if d := m.Devices(ctx); d[0].DeveloperMode == nil || !*d[0].DeveloperMode {
		t.Fatalf("Devices = %+v", d[0])
	}
	// A device_info that doesn't say leaves it as it was.
	m.remember(testUDID, Endpoint{}, json.RawMessage(`{"name":"Test Phone"}`))
	if rec, _ := m.store.Load(testUDID); rec.DeveloperMode == nil || !*rec.DeveloperMode {
		t.Fatalf("record = %+v", rec)
	}
}

// TestPrepareMountsAndThenAgainByItself: Prepare mounts the developer disk image through the session and allows this
// computer to mount it again by itself; a phone that then comes back without it (restarted) gets it mounted on
// connecting, once per link, and one whose owner never prepared it never does (#8199).
func TestPrepareMountsAndThenAgainByItself(t *testing.T) {
	m, log := bridgeManager(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := m.Pair(ctx, testUDID, nil); err != nil {
		t.Fatal(err)
	}
	_ = m.SetAddress(testUDID, "192.0.2.7")
	ops := func() string { b, _ := os.ReadFile(log); return strings.Join(strings.Fields(string(b)), ",") }

	// Never prepared: a phone without the image is left as it is.
	t.Setenv("FAKE_DDI_UNMOUNTED", "1")
	if _, err := m.Call(ctx, testUDID, "tap", nil); err != nil {
		t.Fatal(err)
	}
	if got := ops(); got != "tap" {
		t.Fatalf("mounted without the owner: %s", got)
	}

	if err := m.StartPrepare(testUDID); err != nil {
		t.Fatal(err)
	}
	if j := waitJob(t, m); j.State != JobDone || j.Kind != "prepare" || j.UDID != testUDID {
		t.Fatalf("job = %+v", j)
	}
	if rec, _ := m.store.Load(testUDID); !rec.AutoPrepare {
		t.Fatal("prepared, but not allowed to mount again")
	}
	// The phone's status says it at once: device_info is read again right after the mount, not at the next health
	// check a minute later (#12171).
	d := m.Devices(ctx)
	if !d[0].AutoPrepare || !bytes.Contains(d[0].Info, []byte(`"ddi_mounted":true`)) {
		t.Fatalf("Devices = %+v (info %s)", d[0], d[0].Info)
	}
	if got := ops(); got != "tap,mount_ddi,device_info" {
		t.Fatalf("ops %s", got)
	}

	// The phone restarted: the next link comes up without the image and mounts it by itself, once.
	m.disconnect(testUDID)
	if _, err := m.Call(ctx, testUDID, "screenshot", nil); err != nil {
		t.Fatal(err)
	}
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	for strings.Count(ops(), "mount_ddi") < 2 {
		select {
		case <-tick.C:
		case <-ctx.Done():
			t.Fatalf("not mounted again: %s", ops())
		}
	}
	if _, err := m.Call(ctx, testUDID, "tap", nil); err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(ops(), "mount_ddi"); n != 2 {
		t.Fatalf("mounted %d times on one link: %s", n, ops())
	}
	// Pairing again keeps the owner's word.
	if _, err := m.Pair(ctx, testUDID, nil); err != nil {
		t.Fatal(err)
	}
	if rec, _ := m.store.Load(testUDID); !rec.AutoPrepare {
		t.Fatal("pairing again dropped AutoPrepare")
	}
	if err := m.StartPrepare("00000000-NOTPAIRED"); err == nil {
		t.Fatal("preparing a phone that isn't paired")
	}
}

// autoPrepare is a phone paired over the fake bridge, prepared once by its owner (Record.AutoPrepare), whose links come
// up without the image: what the node mounts again by itself (#12556).
type autoPrepare struct {
	t     *testing.T
	m     *Manager
	ctx   context.Context
	log   string // every request's op, in order
	links string // every request as "<serve pid> <op>"
}

func newAutoPrepare(t *testing.T) *autoPrepare {
	t.Helper()
	m, log := bridgeManager(t)
	// Both restored by fastTiming. The fake answers slowly, and a check that times out would end the link: every new
	// link tries to mount once anyway, so the tests count per link.
	healthEvery, healthTimeout = 20*time.Millisecond, 2*time.Second
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	if _, err := m.Pair(ctx, testUDID, nil); err != nil {
		t.Fatal(err)
	}
	_ = m.SetAddress(testUDID, "192.0.2.7")
	rec, err := m.store.Load(testUDID)
	if err != nil {
		t.Fatal(err)
	}
	rec.AutoPrepare = true
	if err := m.store.Save(rec); err != nil {
		t.Fatal(err)
	}
	links := filepath.Join(t.TempDir(), "links")
	t.Setenv("FAKE_SERVE_PIDLOG", links)
	t.Setenv("FAKE_DDI_UNMOUNTED", "1")
	return &autoPrepare{t: t, m: m, ctx: ctx, log: log, links: links}
}

func (a *autoPrepare) ops() string {
	b, _ := os.ReadFile(a.log)
	return strings.Join(strings.Fields(string(b)), ",")
}

func (a *autoPrepare) mounts() int { return strings.Count(a.ops(), "mount_ddi") }

// mostOnOneLink is the most mount_ddi requests any one serve process (one link) got.
func (a *autoPrepare) mostOnOneLink() int {
	b, _ := os.ReadFile(a.links)
	per, most := map[string]int{}, 0
	for _, l := range strings.Split(string(b), "\n") {
		if pid, op, ok := strings.Cut(l, " "); ok && op == "mount_ddi" {
			per[pid]++
			most = max(most, per[pid])
		}
	}
	return most
}

func (a *autoPrepare) mounted() bool {
	d := a.m.Devices(a.ctx)
	return len(d) == 1 && bytes.Contains(d[0].Info, []byte(`"ddi_mounted":true`))
}

// afterChecks waits until n more device_info requests (health checks; after a mount, the read that follows it) have
// reached the fake: what a count that must not grow is checked after, rather than a fixed while.
func (a *autoPrepare) afterChecks(n int) {
	a.t.Helper()
	checks := func() int { return strings.Count(a.ops(), "device_info") }
	want := checks() + n
	waitFor(a.t, func() bool { return checks() >= want })
}

// connect brings a link up, through a call that needs one.
func (a *autoPrepare) connect() {
	a.t.Helper()
	if _, err := a.m.Call(a.ctx, testUDID, "screenshot", nil); err != nil {
		a.t.Fatal(err)
	}
}

// TestAutoPrepareTriesAgainWhileLocked: a link that comes up without the image while the phone is locked tries to
// mount it again at its next health checks, and stops once it is mounted; a failure that needs someone (no image on
// this computer) is tried once per link, however many checks follow (#12556).
func TestAutoPrepareTriesAgainWhileLocked(t *testing.T) {
	a := newAutoPrepare(t)
	fail := filepath.Join(t.TempDir(), "mount-fail")
	t.Setenv("FAKE_MOUNT_FAIL", fail)

	// No image on this computer: once on this link, however many checks follow.
	if err := os.WriteFile(fail, []byte("ddi_missing"), 0o600); err != nil {
		t.Fatal(err)
	}
	a.connect()
	waitFor(t, func() bool { return a.mounts() >= 1 })
	a.afterChecks(5)
	if n := a.mostOnOneLink(); n != 1 {
		t.Fatalf("ddi_missing tried %d times on one link: %s", n, a.ops())
	}

	// A new link while the phone is locked: tried again at the next checks, on that same link. Locked first: the
	// supervisor may bring the new link up at once.
	if err := os.WriteFile(fail, []byte("locked"), 0o600); err != nil {
		t.Fatal(err)
	}
	a.m.disconnect(testUDID)
	a.connect()
	waitFor(t, func() bool { return a.mostOnOneLink() >= 3 })
	if a.mounted() {
		t.Fatal("mounted while locked")
	}

	// Unlocked: the next try mounts it, and then the checks leave it alone.
	if err := os.Remove(fail); err != nil {
		t.Fatal(err)
	}
	waitFor(t, a.mounted)
	n := a.mounts()
	a.afterChecks(5)
	if got := a.mounts(); got != n {
		t.Fatalf("mounted %d more times after it was mounted: %s", got-n, a.ops())
	}
}

// TestAutoPrepareTriesAgainWhenTheMountNeverAnswers: an automatic mount that gets no answer in time comes back from
// Call as uncertain, and is tried again at the next checks on the same link rather than waiting for another link; it
// mounts once the phone answers (#12556).
func TestAutoPrepareTriesAgainWhenTheMountNeverAnswers(t *testing.T) {
	saved := prepareTimeout
	t.Cleanup(func() { prepareTimeout = saved }) // registered first, so it runs after the manager has closed
	prepareTimeout = 150 * time.Millisecond      // how long an automatic mount waits for its answer
	a := newAutoPrepare(t)
	silent := filepath.Join(t.TempDir(), "mount-silent")
	if err := os.WriteFile(silent, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAKE_MOUNT_SILENT", silent)

	a.connect()
	waitFor(t, func() bool { return a.mostOnOneLink() >= 2 })
	if a.mounted() {
		t.Fatal("mounted without an answer")
	}

	if err := os.Remove(silent); err != nil {
		t.Fatal(err)
	}
	waitFor(t, a.mounted)
}

// TestPairShowsDeveloperModeWhenItIsOff: a phone that paired with Developer Mode off gets its hidden switch shown in
// Settings while it is still on the cable; one with it on, or a simulator, doesn't; a bridge that can't show it
// doesn't fail the pairing (#8199).
func TestPairShowsDeveloperModeWhenItIsOff(t *testing.T) {
	m, _ := bridgeManager(t)
	reveals := filepath.Join(t.TempDir(), "reveals")
	t.Setenv("FAKE_REVEAL_LOG", reveals)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	asked := func() string { b, _ := os.ReadFile(reveals); return strings.TrimSpace(string(b)) }
	if _, err := m.Pair(ctx, testUDID, nil); err != nil || asked() != "" {
		t.Fatalf("Developer Mode not off: %v, asked %q", err, asked())
	}
	t.Setenv("FAKE_DEVMODE_OFF", "1")
	var steps []string
	if _, err := m.Pair(ctx, testUDID, func(step, _ string) { steps = append(steps, step) }); err != nil || asked() != testUDID {
		t.Fatalf("Developer Mode off: %v, asked %q", err, asked())
	}
	if !strings.Contains(strings.Join(steps, "|"), "showing Developer Mode") {
		t.Fatalf("steps %v", steps)
	}
	t.Setenv("FAKE_REVEAL_FAIL", "1")
	if rec, err := m.Pair(ctx, testUDID, nil); err != nil || rec.DeveloperMode == nil || *rec.DeveloperMode {
		t.Fatalf("a bridge that can't show it: %+v, %v", rec, err)
	}
}
