//! Runs persona-core on one worker thread (docs/plans/plan-rust-core.md, "A deterministic core").
//!
//! - `dispatch` only queues an action's bytes, so a client may call it on its main thread.
//! - The worker applies actions, sends requests to the client and builds each chat's patch.
//! - `changed` wakes the client once per batch of changes; the client then pulls the patch with `drain` on its next
//!   frame, so a burst of pushes reaches the screen as one patch.

pub mod wire;

pub use persona_core::transport;

#[cfg(any(target_os = "android", feature = "test-support"))]
mod diagnostics;
#[cfg(any(target_os = "android", feature = "test-support"))]
use diagnostics::{Diagnostics, scheduler_switches};

use std::collections::{HashMap, HashSet};
use std::panic::{AssertUnwindSafe, catch_unwind};
#[cfg(any(target_os = "android", feature = "test-support"))]
use std::sync::atomic::Ordering;
use std::sync::mpsc::{Receiver, Sender, channel};
use std::sync::{Arc, Condvar, Mutex, MutexGuard, PoisonError};
use std::thread::JoinHandle;
use std::time::{Duration, Instant};

use persona_core::Item;
use persona_core::continuity::{Batch, Session, Step};
use persona_core::transport::{DecodeError, Frame, LatestPage};
use wire::{Action, ChatEvent, ChatPatch, ChatSnapshot, Effect, ListOp, Payload, WireBatch};

/// The client's side: called on the core's worker thread, so implementations must be thread-safe and quick.
pub trait Host: Send + Sync + 'static {
    /// Something changed at `version`: pull patches on the next frame.
    fn changed(&self, _version: u64) {}
    /// The chat whose patch changed. Existing hosts keep the version-only callback by default; adapters that need a
    /// targeted wake override this without changing the bincode or continuity state machine.
    fn changed_chat(&self, _chat: &str, version: u64) {
        self.changed(version)
    }
    /// An encoded `wire::Effect` for the client to run.
    fn run(&self, effect: &[u8]);
}

enum Message {
    Action(Vec<u8>),
    Stop,
}

#[derive(Default)]
struct Queue {
    pending: u64,
    stopping: bool,
}

#[derive(Default)]
struct Shared {
    patches: Mutex<Patches>,
    /// Pending accounting, the stop flag and action sends share one lock. An action accepted before shutdown is
    /// processed before Stop; no later action can be accepted into an abandoned queue.
    queue: Mutex<Queue>,
    idle: Condvar,
    /// Once-lock means ordinary dispatch/drain only read a null pointer; all instrumentation is opt-in.
    #[cfg(any(target_os = "android", feature = "test-support"))]
    diagnostics: std::sync::OnceLock<Arc<Diagnostics>>,
    #[cfg(any(target_os = "android", feature = "test-support"))]
    worker_thread: std::sync::atomic::AtomicU64,
}

#[derive(Default)]
struct Patches {
    version: u64,
    chats: HashMap<String, ChatPatch>,
    /// Chats whose latest changes have not been drained. One chat's drain cannot consume another's wake-up.
    woken: HashSet<String>,
}

fn lock<T>(mutex: &Mutex<T>) -> MutexGuard<'_, T> {
    mutex.lock().unwrap_or_else(PoisonError::into_inner)
}

pub struct Runtime {
    sender: Sender<Message>,
    shared: Arc<Shared>,
    worker: Mutex<Option<JoinHandle<()>>>,
    /// Registry-backed clients own shutdown explicitly; ordinary leased operations never take its JoinHandle.
    join_on_drop: bool,
}

impl Runtime {
    pub fn new(host: Arc<dyn Host>) -> Runtime {
        let (sender, receiver) = channel();
        let shared = Arc::new(Shared::default());
        let worker_shared = shared.clone();
        let worker = std::thread::Builder::new()
            .name("persona-core".into())
            .spawn(move || Worker::new(host, worker_shared).run(receiver))
            .expect("start the core's worker thread");
        Runtime {
            sender,
            shared,
            worker: Mutex::new(Some(worker)),
            join_on_drop: true,
        }
    }

    /// Queues an encoded `wire::Action`. Never blocks on the worker.
    pub fn dispatch(&self, action: &[u8]) -> bool {
        #[cfg(any(target_os = "android", feature = "test-support"))]
        let sent = if self.shared.diagnostics.get().is_some() {
            let mut sent = Err(std::sync::mpsc::SendError(Message::Stop));
            diagnostics::measure_queue(&self.shared, || {
                let mut queue = diagnostics::timed_lock(&self.shared, &self.shared.queue, false);
                if queue.stopping {
                    return;
                }
                queue.pending += 1;
                diagnostics::measure_send(&self.shared, || {
                    sent = self.sender.send(Message::Action(action.to_vec()));
                });
            });
            sent
        } else {
            let mut queue = lock(&self.shared.queue);
            if queue.stopping {
                return false;
            }
            queue.pending += 1;
            self.sender.send(Message::Action(action.to_vec()))
        };
        #[cfg(not(any(target_os = "android", feature = "test-support")))]
        let sent = {
            let mut queue = lock(&self.shared.queue);
            if queue.stopping {
                return false;
            }
            queue.pending += 1;
            self.sender.send(Message::Action(action.to_vec()))
        };
        if sent.is_ok() {
            return true;
        }
        self.done(1);
        false
    }

    /// The chat's encoded `wire::ChatPatch` since the last successful drain. Encoding and removal are committed in
    /// that order: a failed encode leaves the pending operations and wake available for the next drain.
    pub fn drain(&self, chat: &str) -> Vec<u8> {
        #[cfg(any(target_os = "android", feature = "test-support"))]
        let mut patches = diagnostics::timed_lock(&self.shared, &self.shared.patches, true);
        #[cfg(not(any(target_os = "android", feature = "test-support")))]
        let mut patches = lock(&self.shared.patches);
        #[cfg(any(target_os = "android", feature = "test-support"))]
        {
            diagnostics::measure_patch_held(&self.shared, || {
                let started = Instant::now();
                let patch = patches.chats.get(chat).cloned().unwrap_or_default();
                if let Some(diagnostics) = self.shared.diagnostics.get() {
                    diagnostics::add(&diagnostics.counters.patch_clone, started);
                }
                let started = Instant::now();
                let encoded = wire::encode(&patch);
                if let Some(diagnostics) = self.shared.diagnostics.get() {
                    diagnostics::add(&diagnostics.counters.patch_encode, started);
                }
                if let Some(patch) = patches.chats.get_mut(chat) {
                    patch.ops.clear();
                }
                patches.woken.remove(chat);
                encoded
            })
        }
        #[cfg(not(any(target_os = "android", feature = "test-support")))]
        {
            let patch = patches.chats.get(chat).cloned().unwrap_or_default();
            let encoded = wire::encode(&patch);
            if let Some(patch) = patches.chats.get_mut(chat) {
                patch.ops.clear();
            }
            patches.woken.remove(chat);
            encoded
        }
    }

    /// Waits until every queued action is applied, up to `timeout`. Zero checks once without waiting. For tests and
    /// shutdown; never on a main thread.
    pub fn flush(&self, timeout: Duration) -> bool {
        if timeout.is_zero() {
            return self
                .shared
                .queue
                .try_lock()
                .map(|queue| queue.pending == 0)
                .unwrap_or(false);
        }
        let deadline = Instant::now() + timeout;
        let mut queue = lock(&self.shared.queue);
        while queue.pending > 0 {
            let left = deadline.saturating_duration_since(Instant::now());
            if left.is_zero() {
                return false;
            }
            queue = self
                .shared
                .idle
                .wait_timeout(queue, left)
                .unwrap_or_else(PoisonError::into_inner)
                .0;
        }
        true
    }

    /// Same wait as [flush], with opt-in caller timing outside the original benchmark spans.
    #[cfg(any(target_os = "android", feature = "test-support"))]
    pub fn flush_diagnostic(&self, timeout: Duration) -> bool {
        self.flush(timeout)
    }
    /// Cumulative opt-in timings, off the ordinary dispatch/drain path. Nanosecond values are inclusive spans, so
    /// nested queue and lock diagnostics overlap. Scheduler counters are zero when /proc cannot be read.
    #[cfg(any(target_os = "android", feature = "test-support"))]
    pub fn diagnostics(&self) -> Vec<i64> {
        let counters = self
            .shared
            .diagnostics
            .get_or_init(|| Arc::new(Diagnostics::default()))
            .counters
            .clone();
        let worker = self.shared.worker_thread.load(Ordering::Relaxed);
        let (worker_voluntary, worker_involuntary) = scheduler_switches(worker);
        let (caller_voluntary, caller_involuntary) = scheduler_switches(0);
        #[allow(unused_mut)]
        let mut values = vec![
            counters.pending_lock.load(Ordering::Relaxed) as i64,
            counters.pending_switches.load(Ordering::Relaxed) as i64,
            counters.pending_lock_calls.load(Ordering::Relaxed) as i64,
            counters.pending_contended.load(Ordering::Relaxed) as i64,
            counters.queue.load(Ordering::Relaxed) as i64,
            counters.queue_send.load(Ordering::Relaxed) as i64,
            counters.patch_lock.load(Ordering::Relaxed) as i64,
            counters.patch_switches.load(Ordering::Relaxed) as i64,
            counters.patch_lock_calls.load(Ordering::Relaxed) as i64,
            counters.patch_contended.load(Ordering::Relaxed) as i64,
            counters.patch_lock_without_proc.load(Ordering::Relaxed) as i64,
            counters.patch_held_work.load(Ordering::Relaxed) as i64,
            counters.patch_clone.load(Ordering::Relaxed) as i64,
            counters.patch_encode.load(Ordering::Relaxed) as i64,
            counters.worker_decode_apply.load(Ordering::Relaxed) as i64,
            counters.worker_effect_encode.load(Ordering::Relaxed) as i64,
            counters.worker_host_run.load(Ordering::Relaxed) as i64,
            counters.worker_host_changed.load(Ordering::Relaxed) as i64,
            counters.worker_actions.load(Ordering::Relaxed) as i64,
            worker as i64,
            worker_voluntary as i64,
            worker_involuntary as i64,
            caller_voluntary as i64,
            caller_involuntary as i64,
        ];
        #[cfg(feature = "test-support")]
        values.push(counters.pending_lock_without_proc.load(Ordering::Relaxed) as i64);
        values
    }

    #[cfg(any(target_os = "android", feature = "test-support"))]
    pub fn enable_diagnostics(&self) {
        self.shared.diagnostics.get_or_init(|| Arc::new(Diagnostics::default()));
    }

    /// Requests worker shutdown and joins it outside any handle-registry lock. An ordinary operation must never call
    /// this on its final lease: only the client's close/shutdown operation owns worker termination.
    pub fn shutdown(&self) {
        self.stop();
        self.join_worker();
    }

    /// Mark that a client registry has already requested shutdown. Ordinary borrowed calls may finish their operation,
    /// but only the close path may wait for the worker and release its host.
    pub fn shutdown_requested(&self) -> bool {
        lock(&self.shared.queue).stopping
    }

    fn join_worker(&self) {
        let worker = self.worker.lock().unwrap_or_else(PoisonError::into_inner).take();
        // Drop the mutex guard before joining: the host's release callback may reenter shutdown through a weak lease.
        if let Some(worker) = worker
            && worker.thread().id() != std::thread::current().id()
        {
            let _ = worker.join();
        }
    }

    /// Registry handles own worker shutdown, independently of any ordinary lease's destruction.
    pub fn set_explicit_shutdown(&mut self) {
        self.join_on_drop = false;
    }

    fn stop(&self) {
        let mut queue = lock(&self.shared.queue);
        if !queue.stopping {
            queue.stopping = true;
            let _ = self.sender.send(Message::Stop);
        }
    }

    fn done(&self, count: u64) {
        let mut queue = lock(&self.shared.queue);
        queue.pending = queue.pending.saturating_sub(count);
        if queue.pending == 0 {
            self.shared.idle.notify_all();
        }
    }
}

impl Drop for Runtime {
    fn drop(&mut self) {
        // A direct Runtime requests stop on drop. A registry close owns stopping explicitly, so an ordinary
        // borrowed lease only releases its Arc and never changes queue state or joins the worker.
        if self.join_on_drop {
            self.stop();
            if let Some(worker) = self.worker.get_mut().unwrap_or_else(PoisonError::into_inner).take()
                && worker.thread().id() != std::thread::current().id()
            {
                let _ = worker.join();
            }
        }
    }
}

struct Chat {
    session: Session,
    /// The list as the client last received it, to diff against.
    published: Vec<Arc<Item>>,
    /// The highest acknowledged input sequence number, for `Action::Ack`.
    acked: Option<u64>,
}

struct Worker {
    host: Arc<dyn Host>,
    shared: Arc<Shared>,
    chats: HashMap<String, Chat>,
}

impl Worker {
    fn new(host: Arc<dyn Host>, shared: Arc<Shared>) -> Worker {
        Worker {
            host,
            shared,
            chats: HashMap::new(),
        }
    }

    fn run(mut self, receiver: Receiver<Message>) {
        #[cfg(any(target_os = "android", feature = "test-support"))]
        self.shared
            .worker_thread
            .store(diagnostics::thread_id(), Ordering::Relaxed);
        loop {
            match receiver.recv() {
                Ok(Message::Stop) | Err(_) => return,
                Ok(Message::Action(bytes)) => {
                    self.guarded(|worker| worker.action(&bytes));
                    let mut queue = lock(&self.shared.queue);
                    queue.pending = queue.pending.saturating_sub(1);
                    if queue.pending == 0 {
                        self.shared.idle.notify_all();
                    }
                }
            }
        }
    }

    /// A panic in one action is reported and the worker goes on with the next.
    fn guarded(&mut self, work: impl FnOnce(&mut Worker)) {
        if catch_unwind(AssertUnwindSafe(|| work(self))).is_err() {
            self.reject("the core failed on an action".into());
        }
    }

    fn reject(&self, reason: String) {
        #[cfg(any(target_os = "android", feature = "test-support"))]
        if let Some(diagnostics) = self.shared.diagnostics.get() {
            let started = Instant::now();
            let encoded = wire::encode(&Effect::Rejected { reason });
            diagnostics::add(&diagnostics.counters.worker_effect_encode, started);
            let started = Instant::now();
            self.host.run(&encoded);
            diagnostics::add(&diagnostics.counters.worker_host_run, started);
            return;
        }
        self.host.run(&wire::encode(&Effect::Rejected { reason }));
    }

    fn action(&mut self, bytes: &[u8]) {
        #[cfg(any(target_os = "android", feature = "test-support"))]
        let started = Instant::now();
        #[cfg(any(target_os = "android", feature = "test-support"))]
        if let Some(diagnostics) = self.shared.diagnostics.get() {
            diagnostics::increment(&diagnostics.counters.worker_actions);
        }
        let action: Action = match wire::decode(bytes) {
            Ok(action) => action,
            Err(error) => return self.reject(format!("undecodable action: {error}")),
        };
        match action {
            Action::OpenChat { chat, snapshot } => {
                let ChatSnapshot {
                    items_json,
                    history,
                    covered,
                    has_more,
                    loaded_once,
                } = snapshot;
                let items = match items_from(&items_json) {
                    Ok(items) => items,
                    Err(error) => return self.reject(error),
                };
                let mut session = Session::new();
                session.restore(items, history, covered, has_more, loaded_once);
                self.chats.insert(
                    chat.clone(),
                    Chat {
                        session,
                        published: Vec::new(),
                        acked: None,
                    },
                );
                self.publish(&chat);
            }
            Action::CloseChat { chat } => {
                self.chats.remove(&chat);
                let mut patches = lock(&self.shared.patches);
                patches.chats.remove(&chat);
                patches.woken.remove(&chat);
            }
            Action::Chat { chat, event } => {
                let Some(state) = self.chats.get_mut(&chat) else {
                    return self.reject("event for a chat that is not open".into());
                };
                let step = match apply(&mut state.session, event) {
                    Ok(step) => step,
                    Err(error) => return self.reject(error),
                };
                self.after_step(&chat, step);
            }
            Action::WirePayload { chat, payload } => {
                let Some(state) = self.chats.get_mut(&chat) else {
                    return self.reject("payload for a chat that is not open".into());
                };
                let step = match payload_step(&mut state.session, &chat, payload) {
                    Ok(Some(step)) => step,
                    Ok(None) => return,
                    Err(error) => return self.reject(error),
                };
                self.after_step(&chat, step);
            }
            Action::ContinueFill { chat } => {
                let Some(state) = self.chats.get_mut(&chat) else {
                    return self.reject("fill for a chat that is not open".into());
                };
                let step = state.session.continue_fill();
                self.after_step(&chat, step);
            }
            Action::HubChanged { chat } => {
                let Some(state) = self.chats.get_mut(&chat) else {
                    return self.reject("hub change for a chat that is not open".into());
                };
                let step = state.session.hub_changed();
                self.after_step(&chat, step);
            }
            Action::Ack { chat, seq } => {
                let Some(state) = self.chats.get_mut(&chat) else {
                    return self.reject("ack for a chat that is not open".into());
                };
                state.acked = Some(state.acked.map_or(seq, |previous| previous.max(seq)));
                // An acknowledgement must reach the client even when nothing else changed, so publish with a
                // forced wake rather than relying on the diff.
                self.publish_ack(&chat);
            }
        }
        #[cfg(any(target_os = "android", feature = "test-support"))]
        if let Some(diagnostics) = self.shared.diagnostics.get() {
            diagnostics::add(&diagnostics.counters.worker_decode_apply, started);
        }
    }

    fn after_step(&mut self, chat: &str, step: Step) {
        for request in step.requests {
            #[cfg(any(target_os = "android", feature = "test-support"))]
            if let Some(diagnostics) = self.shared.diagnostics.get() {
                let started = Instant::now();
                let encoded = wire::encode(&Effect::Fetch {
                    chat: chat.to_owned(),
                    request,
                });
                diagnostics::add(&diagnostics.counters.worker_effect_encode, started);
                let started = Instant::now();
                self.host.run(&encoded);
                diagnostics::add(&diagnostics.counters.worker_host_run, started);
                continue;
            }
            self.host.run(&wire::encode(&Effect::Fetch {
                chat: chat.to_owned(),
                request,
            }));
        }
        self.publish(chat);
    }

    /// Diffs the chat against what the client has and adds the changes to its pending patch.
    fn publish(&mut self, chat: &str) {
        let Some(state) = self.chats.get_mut(chat) else { return };
        let reducer = state.session.reducer();
        let current = reducer.items();
        let ops = diff(&state.published, current);
        let items_changed = !ops.is_empty();
        if items_changed {
            state.published = current.to_vec();
        }
        let wake = {
            let mut patches = lock(&self.shared.patches);
            let patch = patches.chats.entry(chat.to_owned()).or_default();
            let state_changed = patch.covered != reducer.covered()
                || patch.history.as_deref() != reducer.history()
                || patch.has_more != reducer.has_more()
                || patch.loaded_once != reducer.loaded_once()
                || patch.verified != state.session.verified()
                || patch.acked != state.acked;
            if !items_changed && !state_changed && patch.version != 0 {
                None
            } else {
                patch.ops.extend(ops);
                patch.covered = reducer.covered();
                patch.history = reducer.history().map(str::to_owned);
                patch.has_more = reducer.has_more();
                patch.loaded_once = reducer.loaded_once();
                patch.verified = state.session.verified();
                patch.acked = state.acked;
                patches.version += 1;
                let version = patches.version;
                patches.chats.get_mut(chat).expect("patch inserted above").version = version;
                // Every real change wakes this chat, even if a previous frame's patch is still pending. The client
                // coalesces at its frame boundary; another chat's drain cannot consume this chat's notification.
                Some(version)
            }
        };
        if let Some(version) = wake {
            #[cfg(any(target_os = "android", feature = "test-support"))]
            if let Some(diagnostics) = self.shared.diagnostics.get() {
                let started = Instant::now();
                self.host.changed_chat(chat, version);
                diagnostics::add(&diagnostics.counters.worker_host_changed, started);
                return;
            }
            self.host.changed_chat(chat, version);
        }
    }
}

impl Worker {
    /// Publishes an acknowledgement, waking the client even though the chat's items did not change.
    fn publish_ack(&mut self, chat: &str) {
        let Some(state) = self.chats.get_mut(chat) else { return };
        let mut patches = lock(&self.shared.patches);
        let patch = patches.chats.entry(chat.to_owned()).or_default();
        patch.covered = state.session.reducer().covered();
        patch.history = state.session.reducer().history().map(str::to_owned);
        patch.has_more = state.session.reducer().has_more();
        patch.loaded_once = state.session.reducer().loaded_once();
        patch.verified = state.session.verified();
        patch.acked = state.acked;
        patches.version += 1;
        let version = patches.version;
        patches.chats.get_mut(chat).expect("patch inserted above").version = version;
        drop(patches);
        self.host.changed(version);
    }
}

/// A refused payload, reported without any of its content: a reason code, and key names or positions only
/// (`transport::DecodeError`). The chat id is not content either, but it is not part of the decoder's errors.
fn refused(error: DecodeError) -> String {
    format!("refused payload: {error}")
}

/// Applies a transport payload to its chat. `Ok(None)` is a frame that is not an `items` frame or one iOS would
/// drop, so it is not an error and changes nothing.
fn payload_step(session: &mut Session, chat: &str, payload: Payload) -> Result<Option<Step>, String> {
    let step = match payload {
        Payload::Latest { body } => {
            session.received(transport::decode_latest(body.as_bytes()).map_err(refused)?.into_batch())
        }
        Payload::Newer { body } => {
            session.after(transport::decode_newer(body.as_bytes()).map_err(refused)?.into_batch())
        }
        Payload::Reloaded { body } => {
            let LatestPage {
                items,
                has_more,
                history,
                to_seq,
            } = transport::decode_latest(body.as_bytes()).map_err(refused)?;
            session.reload(items, history, to_seq, has_more)
        }
        Payload::Older { body } => {
            let page = transport::decode_latest(body.as_bytes()).map_err(refused)?;
            session.older(page.items, page.has_more)
        }
        Payload::Range { fold_id, body } => {
            let (items, more) = transport::decode_range(body.as_bytes()).map_err(refused)?.into_parts();
            // A transport range page answers the opening it was requested for; with no fold id on the wire payload,
            // it answers the single opening, matching the older callers.
            session.range(fold_id.as_deref().unwrap_or(""), items, more)
        }
        Payload::Frame { body } => match transport::decode_frame(body.as_bytes()) {
            Ok(Frame::Other) => return Ok(None),
            // iOS's `ServerEvent.decode` drops a frame it cannot decode; that is not a failure of the chat.
            Err(_) => return Ok(None),
            Ok(Frame::Items(frame)) => {
                // The frame names its own machine and session but no hub group, so the client's chat id may be
                // `machine_id/session_id`, iOS's `group|machine_id/session_id` for a non-default group, or either
                // bare id. Anything else routed the frame wrongly: report it rather than apply another chat's items.
                let full = format!("{}/{}", frame.machine_id, frame.session_id);
                let routed = chat == full
                    || chat
                        .strip_suffix(full.as_str())
                        .is_some_and(|prefix| prefix.ends_with('|'))
                    || frame.machine_id == chat
                    || frame.session_id == chat;
                if !routed {
                    return Err("payload frame is for another chat".into());
                }
                session.received(frame.into_batch())
            }
        },
    };
    Ok(Some(step))
}

fn items_from(json: &str) -> Result<Vec<Item>, String> {
    if json.is_empty() {
        return Ok(Vec::new());
    }
    Item::list_from_json(json).map_err(|e| e.to_string())
}

fn batch(wire: WireBatch, backlog: bool) -> Result<Batch, String> {
    Ok(Batch {
        items: items_from(&wire.items_json)?,
        history: wire.history,
        from_seq: wire.from_seq,
        to_seq: wire.to_seq,
        backlog,
        has_more: wire.has_more,
        more_after: wire.more_after,
    })
}

fn apply(session: &mut Session, event: ChatEvent) -> Result<Step, String> {
    Ok(match event {
        ChatEvent::Latest { batch: b } => session.received(batch(b, true)?),
        ChatEvent::Pushed { batch: b } => {
            let backlog = b.backlog;
            session.received(batch(b, backlog)?)
        }
        ChatEvent::After { batch: b } => session.after(batch(b, false)?),
        ChatEvent::Reloaded {
            items_json,
            history,
            to_seq,
            has_more,
        } => session.reload(items_from(&items_json)?, history, to_seq, has_more),
        ChatEvent::Older { items_json, has_more } => session.older(items_from(&items_json)?, has_more),
        ChatEvent::Range {
            fold_id,
            items_json,
            more,
        } => {
            // A page without a fold id answers the only opening, for older callers; with one, it answers that fold.
            let id = fold_id.unwrap_or_default();
            session.range(&id, items_from(&items_json)?, more)
        }
        ChatEvent::Reconnected => session.reconnect(),
        ChatEvent::LoadOlder => session.load_older(),
        ChatEvent::OpenFold { fold_id } => session.open_fold(&fold_id),
        ChatEvent::FillFailed => {
            session.finish_fill();
            Step::default()
        }
        ChatEvent::OlderFailed => {
            session.finish_older();
            Step::default()
        }
        ChatEvent::RangeFailed { fold_id } => {
            session.cancel_fold(fold_id.as_deref());
            Step::default()
        }
    })
}

/// The list operations that turn `old` into `new`. Identical items retain their Arc. Removals go from the end;
/// a changed item with the same position and id is an Update, preserving its native list identity.
fn diff(old: &[Arc<Item>], new: &[Arc<Item>]) -> Vec<ListOp> {
    let common = old.iter().zip(new).take_while(|(a, b)| Arc::ptr_eq(a, b)).count();
    let overlap = old.len().min(new.len());
    let mut ops = Vec::new();
    let mut paired = vec![false; old.len()];
    for index in common..overlap {
        if old[index].id == new[index].id && old[index].seq == new[index].seq && old[index].part() == new[index].part()
        {
            paired[index] = true;
            if old[index].json() != new[index].json() {
                ops.push(update(index, &new[index]));
            }
        }
    }
    let kept: HashSet<*const Item> = new
        .iter()
        .enumerate()
        .filter(|(index, _)| !paired.get(*index).copied().unwrap_or(false))
        .map(|(_, item)| Arc::as_ptr(item))
        .collect();
    let mut survivors = Vec::new();
    for (index, item) in old.iter().enumerate().rev() {
        if index >= common && !paired[index] && kept.contains(&Arc::as_ptr(item)) {
            survivors.push(item);
        } else if index >= common && !paired[index] {
            ops.push(ListOp::Remove { index: index as u32 });
        }
    }
    survivors.reverse();
    let mut next = 0;
    for (index, item) in new.iter().enumerate().skip(common) {
        if paired.get(index).copied().unwrap_or(false) {
            continue;
        }
        if next < survivors.len() && Arc::ptr_eq(survivors[next], item) {
            next += 1;
        } else {
            ops.push(insert(index, item));
        }
    }
    if next == survivors.len() {
        return ops;
    }
    // Never expected: items that stayed changed order. Start the list over.
    let mut ops: Vec<ListOp> = (0..old.len())
        .rev()
        .map(|index| ListOp::Remove { index: index as u32 })
        .collect();
    ops.extend(new.iter().enumerate().map(|(i, item)| insert(i, item)));
    ops
}

fn insert(index: usize, item: &Item) -> ListOp {
    ListOp::Insert {
        index: index as u32,
        id: item.id.clone(),
        json: item.json().to_owned(),
    }
}

fn update(index: usize, item: &Item) -> ListOp {
    ListOp::Update {
        index: index as u32,
        id: item.id.clone(),
        json: item.json().to_owned(),
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn items(json: &str) -> Vec<Arc<Item>> {
        Item::list_from_json(json).unwrap().into_iter().map(Arc::new).collect()
    }

    fn replay(old: &[Arc<Item>], ops: &[ListOp]) -> Vec<String> {
        let mut list: Vec<String> = old.iter().map(|i| i.id.clone()).collect();
        for op in ops {
            match op {
                ListOp::Remove { index } => {
                    list.remove(*index as usize);
                }
                ListOp::Insert { index, id, .. } => list.insert(*index as usize, id.clone()),
                ListOp::Update { index, id, .. } => list[*index as usize] = id.clone(),
            }
        }
        list
    }

    #[test]
    fn diff_appends_removes_and_inserts() {
        let old = items(r#"[{"id":"1#0","seq":1},{"id":"2#0","seq":2},{"id":"3#0","seq":3}]"#);
        let extra = items(r#"[{"id":"4#0","seq":4},{"id":"0#0","seq":0}]"#);
        let appended = vec![old[0].clone(), old[1].clone(), old[2].clone(), extra[0].clone()];
        assert_eq!(diff(&old, &appended), vec![insert(3, &extra[0])]);
        let mixed = vec![extra[1].clone(), old[0].clone(), old[2].clone(), extra[0].clone()];
        let ops = diff(&old, &mixed);
        assert_eq!(replay(&old, &ops), vec!["0#0", "1#0", "3#0", "4#0"]);
        assert!(diff(&old, &old).is_empty());
    }

    #[test]
    fn a_changed_item_is_only_updated_not_removed_and_reinserted() {
        let old = items(r#"[{"id":"1#0","seq":1,"text":"old"},{"id":"2#0","seq":2}]"#);
        let new = items(r#"[{"id":"1#0","seq":1,"text":"new"},{"id":"2#0","seq":2}]"#);
        assert_eq!(diff(&old, &new), vec![update(0, &new[0])]);
        assert_eq!(replay(&old, &diff(&old, &new)), vec!["1#0", "2#0"]);
    }

    #[test]
    fn zero_timeout_is_a_nonblocking_probe() {
        struct NoopHost;
        impl Host for NoopHost {
            fn changed(&self, _version: u64) {}
            fn run(&self, _effect: &[u8]) {}
        }
        let runtime = Arc::new(Runtime::new(Arc::new(NoopHost)));
        assert!(runtime.flush(Duration::ZERO));
        let action = wire::encode(&Action::OpenChat {
            chat: "probe".into(),
            snapshot: ChatSnapshot::default(),
        });
        for _ in 0..128 {
            assert!(runtime.dispatch(&action));
        }
        assert!(!runtime.flush(Duration::ZERO));
        assert!(runtime.flush(Duration::from_secs(5)));
        // A contended pending lock must not turn a zero-duration probe into an unbounded wait.
        let guard = lock(&runtime.shared.queue);
        assert!(!runtime.flush(Duration::ZERO));
        drop(guard);
        assert!(runtime.flush(Duration::ZERO));
        runtime.shutdown();
    }

    #[test]
    fn worker_host_release_runs_on_the_worker_without_joining_itself() {
        struct ReleaseHost {
            released: std::sync::mpsc::Sender<()>,
        }
        impl Host for ReleaseHost {
            fn changed(&self, _version: u64) {}
            fn run(&self, _effect: &[u8]) {}
        }
        impl Drop for ReleaseHost {
            fn drop(&mut self) {
                let _ = self.released.send(());
            }
        }
        let (released, released_rx) = std::sync::mpsc::channel();
        let runtime = Runtime::new(Arc::new(ReleaseHost { released }));
        assert!(runtime.dispatch(&wire::encode(&Action::OpenChat {
            chat: "release".into(),
            snapshot: ChatSnapshot::default(),
        })));
        assert!(runtime.flush(Duration::from_secs(5)));
        runtime.shutdown();
        released_rx
            .recv_timeout(Duration::from_secs(5))
            .expect("worker host release must complete without joining itself");
    }
}
