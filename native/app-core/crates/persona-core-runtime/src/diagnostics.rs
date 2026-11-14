//! Optional Android-only timing and Linux scheduler evidence. Ordinary calls only check a OnceLock;
//! no clock or /proc read occurs until the instrumentation test opts in.

use std::sync::atomic::{AtomicU64, Ordering};
use std::sync::{Arc, Mutex, MutexGuard};
use std::time::Instant;

use super::{Shared, lock};

#[derive(Default)]
pub(crate) struct Diagnostics {
    pub counters: Arc<Counters>,
}

#[derive(Default)]
pub(crate) struct Counters {
    pub pending_lock: AtomicU64,
    pub pending_switches: AtomicU64,
    pub pending_lock_calls: AtomicU64,
    pub pending_contended: AtomicU64,
    pub queue: AtomicU64,
    pub queue_send: AtomicU64,
    pub patch_lock: AtomicU64,
    pub patch_switches: AtomicU64,
    pub patch_lock_calls: AtomicU64,
    pub patch_contended: AtomicU64,
    pub patch_lock_without_proc: AtomicU64,
    pub patch_held_work: AtomicU64,
    pub patch_clone: AtomicU64,
    pub patch_encode: AtomicU64,
    pub worker_decode_apply: AtomicU64,
    pub worker_effect_encode: AtomicU64,
    pub worker_host_run: AtomicU64,
    pub worker_host_changed: AtomicU64,
    pub worker_actions: AtomicU64,
    pub pending_lock_without_proc: AtomicU64,
}

/// Time the real lock acquisition once, including the scheduler counter reads' own overhead.
pub(crate) fn timed_lock<'a, T>(shared: &Shared, mutex: &'a Mutex<T>, patch: bool) -> MutexGuard<'a, T> {
    let Some(diagnostics) = shared.diagnostics.get() else {
        return lock(mutex);
    };
    let counters = diagnostics.counters.clone();
    let started = Instant::now();
    let before = scheduler_switches(0);
    let low_overhead_started = Instant::now();
    let contended = matches!(mutex.try_lock(), Err(std::sync::TryLockError::WouldBlock));
    let guard = lock(mutex);
    let low_overhead_ns = low_overhead_started.elapsed().as_nanos() as u64;
    let after = scheduler_switches(0);
    let (total, switches, calls) = if patch {
        (
            &counters.patch_lock,
            &counters.patch_switches,
            &counters.patch_lock_calls,
        )
    } else {
        (
            &counters.pending_lock,
            &counters.pending_switches,
            &counters.pending_lock_calls,
        )
    };
    add(total, started);
    switches.fetch_add(after.0 - before.0 + after.1 - before.1, Ordering::Relaxed);
    increment(calls);
    if patch {
        if contended {
            increment(&counters.patch_contended);
        }
        counters
            .patch_lock_without_proc
            .fetch_add(low_overhead_ns, Ordering::Relaxed);
    } else {
        counters
            .pending_lock_without_proc
            .fetch_add(low_overhead_ns, Ordering::Relaxed);
        if contended {
            increment(&counters.pending_contended);
        }
    }
    guard
}

pub(crate) fn measure_queue(shared: &Shared, work: impl FnOnce()) {
    let Some(diagnostics) = shared.diagnostics.get() else {
        return work();
    };
    let counters = diagnostics.counters.clone();
    let started = Instant::now();
    work();
    add(&counters.queue, started);
}

pub(crate) fn measure_send(shared: &Shared, work: impl FnOnce()) {
    let Some(diagnostics) = shared.diagnostics.get() else {
        return work();
    };
    let counters = diagnostics.counters.clone();
    let started = Instant::now();
    work();
    add(&counters.queue_send, started);
}

pub(crate) fn measure_patch_held<T>(shared: &Shared, work: impl FnOnce() -> T) -> T {
    let Some(diagnostics) = shared.diagnostics.get() else {
        return work();
    };
    let counters = diagnostics.counters.clone();
    let started = Instant::now();
    let value = work();
    add(&counters.patch_held_work, started);
    value
}

unsafe extern "C" {
    fn gettid() -> i32;
}

pub(crate) fn thread_id() -> u64 {
    // SAFETY: Android's gettid has no arguments and returns the calling Linux task's kernel thread id.
    unsafe { gettid() as u64 }
}

pub(crate) fn scheduler_switches(tid: u64) -> (u64, u64) {
    let path = if tid == 0 {
        "/proc/self/status".to_owned()
    } else {
        format!("/proc/{tid}/status")
    };
    let Ok(status) = std::fs::read_to_string(path) else {
        return (0, 0);
    };
    let mut switches = (0, 0);
    for line in status.lines() {
        if let Some(value) = line.strip_prefix("voluntary_ctxt_switches:") {
            switches.0 = value.trim().parse().unwrap_or(0);
        } else if let Some(value) = line.strip_prefix("nonvoluntary_ctxt_switches:") {
            switches.1 = value.trim().parse().unwrap_or(0);
        }
    }
    switches
}

pub(crate) fn add(counter: &AtomicU64, started: Instant) {
    counter.fetch_add(started.elapsed().as_nanos() as u64, Ordering::Relaxed);
}

pub(crate) fn increment(counter: &AtomicU64) {
    counter.fetch_add(1, Ordering::Relaxed);
}
