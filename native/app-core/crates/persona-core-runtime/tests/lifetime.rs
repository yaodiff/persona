use std::sync::Arc;
use std::sync::mpsc::{Sender, channel};
use std::time::Duration;

use persona_core_runtime::wire::{self, Action, ChatSnapshot};
use persona_core_runtime::{Host, Runtime};

struct ReleaseHost {
    released: Sender<()>,
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

#[test]
fn direct_runtime_host_release_finishes() {
    let (released, released_rx) = channel();
    let host = Arc::new(ReleaseHost { released });
    let runtime = Runtime::new(host);
    assert!(runtime.dispatch(&wire::encode(&Action::OpenChat {
        chat: "release".into(),
        snapshot: ChatSnapshot::default(),
    })));
    assert!(runtime.flush(Duration::from_secs(5)));
    drop(runtime);
    released_rx
        .recv_timeout(Duration::from_secs(5))
        .expect("direct runtime host release must finish without worker self-join");
}
