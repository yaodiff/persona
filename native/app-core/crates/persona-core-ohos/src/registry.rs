//! A close-safe runtime registry, testable without loading an ArkTS NAPI environment.

use std::collections::HashMap;
use std::sync::atomic::{AtomicU64, Ordering};
use std::sync::{Arc, Mutex, MutexGuard, PoisonError};
use std::time::Duration;

use persona_core_runtime::wire::{self, Config};
use persona_core_runtime::{Host, Runtime};

#[cfg(feature = "ohos")]
use napi_ohos::bindgen_prelude::{Buffer, Error, FnArgs, Result, Status};
#[cfg(feature = "ohos")]
use napi_ohos::threadsafe_function::{ThreadsafeFunction, ThreadsafeFunctionCallMode};

#[cfg(not(feature = "ohos"))]
#[derive(Debug)]
pub struct Error(pub String);
#[cfg(not(feature = "ohos"))]
pub type Result<T> = std::result::Result<T, Error>;

#[cfg(feature = "ohos")]
fn error(message: impl Into<String>) -> Error {
    Error::new(Status::GenericFailure, message.into())
}

#[cfg(not(feature = "ohos"))]
fn error(message: impl Into<String>) -> Error {
    Error(message.into())
}

static NEXT_HANDLE: AtomicU64 = AtomicU64::new(1);
static CORES: Mutex<Option<HashMap<i64, Arc<Runtime>>>> = Mutex::new(None);

fn cores() -> &'static Mutex<Option<HashMap<i64, Arc<Runtime>>>> {
    &CORES
}

fn lock() -> MutexGuard<'static, Option<HashMap<i64, Arc<Runtime>>>> {
    // A failed action must not poison the registry or deadlock later close/reconnect operations.
    cores().lock().unwrap_or_else(PoisonError::into_inner)
}

pub fn create(config: &[u8], host: Arc<dyn Host>) -> Result<i64> {
    let _: Config = wire::decode(config).map_err(error)?;
    let handle = NEXT_HANDLE.fetch_add(1, Ordering::Relaxed);
    let handle = i64::try_from(handle).map_err(|_| error("core handle space exhausted"))?;
    let mut map = lock();
    let mut runtime = Runtime::new(host);
    runtime.set_explicit_shutdown();
    map.get_or_insert_with(HashMap::new).insert(handle, Arc::new(runtime));
    Ok(handle)
}

fn with_core<T>(handle: i64, f: impl FnOnce(&Runtime) -> T) -> Result<T> {
    let core = {
        let map = lock();
        map.as_ref()
            .and_then(|map| map.get(&handle).cloned())
            .ok_or_else(|| error("core is closed"))?
    };
    Ok(f(&core))
}

pub fn dispatch(handle: i64, action: &[u8]) -> Result<bool> {
    with_core(handle, |runtime| runtime.dispatch(action))
}

pub fn drain(handle: i64, chat: &str) -> Result<Vec<u8>> {
    with_core(handle, |runtime| runtime.drain(chat))
}

pub fn flush(handle: i64, timeout_ms: u32) -> Result<bool> {
    with_core(handle, |runtime| {
        runtime.flush(Duration::from_millis(timeout_ms.into()))
    })
}

pub fn close(handle: i64) -> Result<()> {
    let removed = {
        let mut map = lock();
        map.as_mut().and_then(|map| map.remove(&handle))
    };
    // Explicit close owns worker termination; a normal lease drop never joins the worker.
    if let Some(runtime) = removed {
        runtime.shutdown();
    }
    Ok(())
}

pub fn config() -> Vec<u8> {
    wire::encode(&Config::default())
}

#[cfg(feature = "ohos")]
// The false const argument selects callee-unhandled callbacks: JavaScript receives no leading error argument.
type ChangedCallback = ThreadsafeFunction<i64, (), i64, Status, false>;
#[cfg(feature = "ohos")]
type ChangedChatCallback = ThreadsafeFunction<FnArgs<(String, i64)>, (), FnArgs<(String, i64)>, Status, false>;
#[cfg(feature = "ohos")]
type EffectsCallback = ThreadsafeFunction<Buffer, (), Buffer, Status, false>;

#[cfg(feature = "ohos")]
pub struct CoreHost {
    changed: Arc<ChangedCallback>,
    changed_chat: Option<Arc<ChangedChatCallback>>,
    effects: Arc<EffectsCallback>,
}

#[cfg(feature = "ohos")]
impl CoreHost {
    pub fn new(changed: ChangedCallback, effects: EffectsCallback) -> Arc<Self> {
        Arc::new(CoreHost {
            changed: Arc::new(changed),
            changed_chat: None,
            effects: Arc::new(effects),
        })
    }

    pub fn with_changed_chat(
        changed: ChangedCallback,
        changed_chat: ChangedChatCallback,
        effects: EffectsCallback,
    ) -> Arc<Self> {
        Arc::new(CoreHost {
            changed: Arc::new(changed),
            changed_chat: Some(Arc::new(changed_chat)),
            effects: Arc::new(effects),
        })
    }
}

#[cfg(feature = "ohos")]
impl Host for CoreHost {
    fn changed(&self, version: u64) {
        // This is a wake-up only. The patch's u64 version stays in bincode; do not silently wrap a NAPI number.
        let Ok(version) = i64::try_from(version) else { return };
        let _ = self.changed.call(version, ThreadsafeFunctionCallMode::NonBlocking);
    }

    fn changed_chat(&self, chat: &str, version: u64) {
        let Some(callback) = &self.changed_chat else {
            self.changed(version);
            return;
        };
        let Ok(version) = i64::try_from(version) else { return };
        let _ = callback.call(
            FnArgs::from((chat.to_owned(), version)),
            ThreadsafeFunctionCallMode::NonBlocking,
        );
    }

    fn run(&self, effect: &[u8]) {
        let _ = self
            .effects
            .call(Buffer::from(effect.to_vec()), ThreadsafeFunctionCallMode::NonBlocking);
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use persona_core_runtime::wire::{Action, ChatEvent, ChatPatch, ChatSnapshot, ListOp, WireBatch};
    use std::sync::mpsc::{Receiver, Sender, channel};

    struct TestHost {
        changes: Sender<u64>,
        effects: Sender<Vec<u8>>,
    }

    impl Host for TestHost {
        fn changed(&self, version: u64) {
            let _ = self.changes.send(version);
        }

        fn run(&self, effect: &[u8]) {
            let _ = self.effects.send(effect.to_vec());
        }
    }

    fn host() -> (Arc<dyn Host>, Receiver<u64>, Receiver<Vec<u8>>) {
        let (changes, changed) = channel();
        let (effects, effect) = channel();
        (Arc::new(TestHost { changes, effects }), changed, effect)
    }

    #[test]
    fn registry_lifecycle_and_real_runtime_patches() {
        let (host, changed, effects) = host();
        let handle = create(&config(), host).unwrap();
        let open = Action::OpenChat {
            chat: "chat".into(),
            snapshot: ChatSnapshot::default(),
        };
        assert!(dispatch(handle, &wire::encode(&open)).unwrap());
        assert!(drain(handle, "chat").is_ok());
        for (index, (id, text)) in [("1#0", "old"), ("1#0", "new")].into_iter().enumerate() {
            let batch = WireBatch {
                items_json: format!(r#"[{{"id":"{id}","seq":1,"role":"assistant","kind":"text","text":"{text}"}}]"#),
                to_seq: (index == 0).then_some(1),
                ..WireBatch::default()
            };
            let event = if index == 0 {
                ChatEvent::Latest { batch }
            } else {
                ChatEvent::Pushed { batch }
            };
            let action = Action::Chat {
                chat: "chat".into(),
                event,
            };
            assert!(dispatch(handle, &wire::encode(&action)).unwrap());
        }
        assert!(flush(handle, 5000).unwrap());
        let patch: ChatPatch = wire::decode(&drain(handle, "chat").unwrap()).unwrap();
        assert!(
            matches!(&patch.ops[..], [ListOp::Insert { json, .. }, ListOp::Update { json: updated, .. }] if json.contains("old") && updated.contains("new")),
            "unexpected patch: {patch:?}"
        );
        assert!(changed.try_iter().count() >= 2);
        assert!(effects.try_iter().next().is_none());
        close(handle).unwrap();

        let handle = create(&config(), self::host().0).unwrap();
        for _ in 0..128 {
            let action = Action::OpenChat {
                chat: "chat".into(),
                snapshot: ChatSnapshot::default(),
            };
            assert!(dispatch(handle, &wire::encode(&action)).unwrap());
        }
        let runtime = {
            let map = lock();
            map.as_ref().and_then(|map| map.get(&handle).cloned()).unwrap()
        };
        let flusher = std::thread::spawn(move || runtime.flush(Duration::from_secs(5)));
        close(handle).unwrap();
        assert!(flusher.join().unwrap());
        assert!(dispatch(handle, &config()).is_err());
        close(handle).unwrap();
    }

    #[test]
    fn invalid_config_is_rejected_before_creating_a_runtime() {
        assert!(create(&[255], host().0).is_err());
    }
}
