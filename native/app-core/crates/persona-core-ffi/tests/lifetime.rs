//! The C boundary owns a core until its one free call; concurrent runtime calls may finish or observe closure.

use std::ffi::c_void;
use std::sync::mpsc::{Receiver, Sender, channel};
use std::sync::{Arc, Barrier};
use std::time::{Duration, Instant};

use persona_core::{
    PersonaCore, PersonaCoreChatCallback, PersonaCoreHost, notify_drain_started_for_test, persona_core_bytes_free,
    persona_core_dispatch, persona_core_drain, persona_core_flush, persona_core_free, persona_core_new,
    persona_core_set_changed_chat,
};
use persona_core_runtime::wire::{self, Action, ChatEvent, ChatPatch, ChatSnapshot, Config, WireBatch};

#[derive(Clone, Copy)]
struct SendPointer(*mut PersonaCore);
unsafe impl Send for SendPointer {}
unsafe impl Sync for SendPointer {}

struct Context {
    ready: Sender<bool>,
    release: Sender<()>,
    reenter: Option<SendPointer>,
    hold: Option<Receiver<()>>,
}

unsafe extern "C" fn changed(context: *mut c_void, _version: u64) {
    let context = unsafe { &*(context as *const Context) };
    let _ = context.ready.send(true);
    if let Some(hold) = &context.hold {
        let _ = hold.recv_timeout(Duration::from_secs(5));
    }
}

unsafe extern "C" fn release(context: *mut c_void) {
    let context = unsafe { &*(context as *const Context) };
    if let Some(core) = context.reenter {
        // The public dispatch result distinguishes an orderly reentrant close from a deadlock behind its write lock.
        let result = unsafe { persona_core_dispatch(core.0, std::ptr::null(), 0) };
        let _ = context.ready.send(result);
    }
    let _ = context.release.send(());
}

fn host() -> (PersonaCoreHost, Receiver<bool>, Receiver<()>) {
    let (ready_tx, ready_rx) = channel();
    let (release_tx, release_rx) = channel();
    let context = Box::into_raw(Box::new(Context {
        ready: ready_tx,
        release: release_tx,
        reenter: None,
        hold: None,
    })) as *mut c_void;
    (
        PersonaCoreHost {
            context,
            changed: Some(changed),
            run: None,
            release: Some(release),
        },
        ready_rx,
        release_rx,
    )
}

fn new_core() -> (*mut PersonaCore, *mut Context, Receiver<bool>, Receiver<()>) {
    let (host, ready, release) = host();
    let context = host.context as *mut Context;
    let config = wire::encode(&Config::default());
    let core = unsafe { persona_core_new(host, config.as_ptr(), config.len()) };
    assert!(!core.is_null());
    // Only the reentrant-close test installs the callback pointer after creating the core.
    (core, context, ready, release)
}

fn open() -> Vec<u8> {
    wire::encode(&Action::OpenChat {
        chat: "chat".into(),
        snapshot: ChatSnapshot::default(),
    })
}

fn reconnect() -> Vec<u8> {
    wire::encode(&Action::Chat {
        chat: "chat".into(),
        event: ChatEvent::Latest {
            batch: WireBatch {
                items_json: r#"[{"id":"1#0","seq":1,"kind":"text","text":"item"}]"#.into(),
                to_seq: Some(1),
                ..WireBatch::default()
            },
        },
    })
}

fn recv_ready(receiver: &Receiver<bool>) {
    receiver
        .recv_timeout(Duration::from_secs(5))
        .expect("worker callback within five seconds");
}

fn free(core: *mut PersonaCore) {
    unsafe { persona_core_free(core) }
}

fn free_shared(core: SendPointer) {
    free(core.0);
}

fn drain_shared(core: SendPointer) -> bool {
    let bytes = unsafe { persona_core_drain(core.0, c"chat".as_ptr() as *const u8, 4) };
    let present = !bytes.data.is_null();
    unsafe { persona_core_bytes_free(bytes) };
    present
}

fn exercise_until(core: SendPointer, barrier: Arc<Barrier>, action: Vec<u8>, deadline: Instant) {
    barrier.wait();
    let core = core.0;
    while Instant::now() < deadline {
        let _ = unsafe { persona_core_dispatch(core, action.as_ptr(), action.len()) };
        let bytes = unsafe { persona_core_drain(core, c"chat".as_ptr() as *const u8, 4) };
        if !bytes.data.is_null() {
            unsafe { persona_core_bytes_free(bytes) };
        }
        let _ = unsafe { persona_core_flush(core, 1) };
    }
}

#[test]
fn release_releases_the_host_once_and_rejects_later_calls() {
    let (core, _context, ready, release) = new_core();
    let action = reconnect();
    assert!(unsafe { persona_core_dispatch(core, action.as_ptr(), action.len()) });
    assert!(unsafe { persona_core_flush(core, 5_000) });
    // The successful flush returned while the handle was live. Close must not require a fresh flush on a closed ID.
    free(core);
    release
        .recv_timeout(Duration::from_secs(5))
        .expect("the host is released exactly once");
    assert!(release.try_recv().is_err());
    assert!(!unsafe { persona_core_flush(core, 0) });
    assert!(!unsafe { persona_core_dispatch(core, std::ptr::null(), 0) });
    let empty = unsafe { persona_core_drain(core, std::ptr::null(), 0) };
    assert!(empty.data.is_null());
    unsafe { persona_core_bytes_free(empty) };
    let _ = ready;
}

#[test]
fn live_empty_patch_stays_distinct_from_a_closed_drain() {
    let (core, _context, ready, release) = new_core();
    let action = open();
    assert!(unsafe { persona_core_dispatch(core, action.as_ptr(), action.len()) });
    assert!(unsafe { persona_core_flush(core, 5_000) });
    recv_ready(&ready);
    let empty = unsafe { persona_core_drain(core, c"chat".as_ptr() as *const u8, 4) };
    assert!(!empty.data.is_null());
    let patch: ChatPatch = wire::decode(unsafe { std::slice::from_raw_parts(empty.data, empty.length) }).unwrap();
    assert!(patch.ops.is_empty());
    assert!(patch.version > 0);
    unsafe { persona_core_bytes_free(empty) };
    free(core);
    release.recv_timeout(Duration::from_secs(5)).expect("host released");
}

#[test]
fn last_lease_drain_does_not_join_a_callback_that_is_still_finishing() {
    let (core, context, ready, release) = new_core();
    let (hold_tx, hold_rx) = channel();
    unsafe { (*context).hold = Some(hold_rx) };
    let action = open();
    assert!(unsafe { persona_core_dispatch(core, action.as_ptr(), action.len()) });
    recv_ready(&ready); // The worker is now in its callback, not merely scheduled.
    let core = SendPointer(core);
    let (drain_started, drain_started_rx) = channel();
    notify_drain_started_for_test(core.0, drain_started);
    let drained = std::thread::spawn(move || drain_shared(core));
    drain_started_rx
        .recv_timeout(Duration::from_secs(5))
        .expect("drain acquired its lease before close");
    // The callback is held, but drain has already leased the runtime. Close can only join the worker on its own path.
    let (close_finished_tx, close_finished_rx) = channel();
    let closer = std::thread::spawn(move || {
        free_shared(core);
        let _ = close_finished_tx.send(());
    });
    assert!(drained.join().expect("drain thread panicked"));
    assert!(
        close_finished_rx.recv_timeout(Duration::from_millis(100)).is_err(),
        "close must not return while its worker callback is held"
    );
    let _ = hold_tx.send(());
    closer.join().expect("close thread panicked");
    release
        .recv_timeout(Duration::from_secs(5))
        .expect("host released after its callback completes");
}

unsafe extern "C" fn changed_chat(context: *mut c_void, chat: *const u8, length: usize, _version: u64) {
    let context = unsafe { &*(context as *const Context) };
    let chat = unsafe { std::slice::from_raw_parts(chat, length) };
    let _ = context.ready.send(chat == b"chat");
}

#[test]
fn close_rejects_late_targeted_registration() {
    let (core, _context, _ready, release) = new_core();
    assert!(unsafe { persona_core_set_changed_chat(core, PersonaCoreChatCallback(changed_chat)) });
    free(core);
    assert!(!unsafe { persona_core_set_changed_chat(core, PersonaCoreChatCallback(changed_chat)) });
    release
        .recv_timeout(Duration::from_secs(5))
        .expect("host released once");
}

#[test]
fn release_callback_can_call_back_into_the_closed_core() {
    let (core, _context, ready, release) = new_core();
    unsafe { (*_context).reenter = Some(SendPointer(core)) };
    free(core);
    assert!(
        !ready
            .recv_timeout(Duration::from_secs(5))
            .expect("reentrant dispatch observes closed rather than deadlocking")
    );
    release
        .recv_timeout(Duration::from_secs(5))
        .expect("release completes without self-deadlock");
    assert!(release.try_recv().is_err());
}

#[test]
fn concurrent_calls_complete_or_observe_closed_without_touching_freed_runtime() {
    for _ in 0..32 {
        let (core, _context, ready, release) = new_core();
        let action = open();
        assert!(unsafe { persona_core_dispatch(core, action.as_ptr(), action.len()) });
        assert!(unsafe { persona_core_flush(core, 5_000) });
        recv_ready(&ready);
        let core = SendPointer(core);
        let barrier = Arc::new(Barrier::new(4));
        let mut threads = Vec::new();
        for _ in 0..3 {
            let barrier = barrier.clone();
            let action = reconnect();
            threads.push(std::thread::spawn(move || {
                let deadline = Instant::now() + Duration::from_millis(20);
                exercise_until(core, barrier, action, deadline);
            }));
        }
        barrier.wait();
        free(core.0);
        for thread in threads {
            thread.join().unwrap();
        }
        release
            .recv_timeout(Duration::from_secs(5))
            .expect("the racing calls finish before host release");
        assert!(release.try_recv().is_err());
        assert!(!unsafe { persona_core_flush(core.0, 0) });
    }
}
