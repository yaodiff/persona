//! JNI entry points for `persona.core.rustcore.NativeBridge` (android/core/rustcore). Kotlin calls them directly, with
//! no JNA and no C layer. The host's `changed` and `run` are looked up once and called on the core's worker thread,
//! which stays attached to the JVM.

use std::panic::{AssertUnwindSafe, catch_unwind};
use std::sync::Arc;
#[cfg(any(target_os = "android", feature = "test-support"))]
use std::time::Duration;

use jni::JNIEnv;
use jni::JavaVM;
use jni::objects::{GlobalRef, JByteArray, JClass, JMethodID, JObject, JString};
use jni::signature::{Primitive, ReturnType};
#[cfg(any(target_os = "android", feature = "test-support"))]
use jni::sys::jlongArray;
use jni::sys::{JNI_FALSE, JNI_TRUE, jboolean, jbyteArray, jint, jlong, jvalue};
use persona_core_runtime::Host;

use crate::{PersonaCore, create};

struct JHost {
    vm: JavaVM,
    host: GlobalRef,
    changed: JMethodID,
    changed_chat: Option<JMethodID>,
    run: JMethodID,
}

impl JHost {
    /// Calls a `void` method on the worker thread. Arguments must match the method's signature.
    fn call(&self, env: &mut JNIEnv, method: JMethodID, arguments: &[jvalue]) {
        let _ = unsafe {
            env.call_method_unchecked(
                self.host.as_obj(),
                method,
                ReturnType::Primitive(Primitive::Void),
                arguments,
            )
        };
        if env.exception_check().unwrap_or(false) {
            let _ = env.exception_clear();
        }
    }
}

impl Host for JHost {
    fn changed(&self, version: u64) {
        let Ok(mut env) = self.vm.attach_current_thread_permanently() else {
            return;
        };
        self.call(&mut env, self.changed, &[jvalue { j: version as jlong }]);
    }
    fn changed_chat(&self, chat: &str, version: u64) {
        let Some(method) = self.changed_chat else {
            self.changed(version);
            return;
        };
        let Ok(mut env) = self.vm.attach_current_thread_permanently() else {
            return;
        };
        let Ok(chat) = env.new_string(chat) else {
            if env.exception_check().unwrap_or(false) {
                let _ = env.exception_clear();
            }
            return;
        };
        self.call(
            &mut env,
            method,
            &[jvalue { l: chat.as_raw() }, jvalue { j: version as jlong }],
        );
        let _ = env.delete_local_ref(chat);
    }
    fn run(&self, effect: &[u8]) {
        let Ok(mut env) = self.vm.attach_current_thread_permanently() else {
            return;
        };
        let Ok(array) = env.byte_array_from_slice(effect) else {
            if env.exception_check().unwrap_or(false) {
                let _ = env.exception_clear();
            }
            return;
        };
        self.call(&mut env, self.run, &[jvalue { l: array.as_raw() }]);
        // The worker never returns to Java, so its local references are freed by hand.
        let _ = env.delete_local_ref(array);
    }
}

fn core(handle: jlong) -> Option<Arc<persona_core_runtime::Runtime>> {
    if handle == 0 {
        return None;
    }
    crate::runtime(handle as usize as *const PersonaCore)
}

fn optional_changed_chat(env: &mut JNIEnv, class: &jni::objects::JClass) -> Option<JMethodID> {
    match env.get_method_id(class, "changedChat", "(Ljava/lang/String;J)V") {
        Ok(method) => Some(method),
        Err(_) => {
            if env.exception_check().unwrap_or(false) {
                let _ = env.exception_clear();
            }
            None
        }
    }
}

fn boolean(value: bool) -> jboolean {
    if value { JNI_TRUE } else { JNI_FALSE }
}

#[unsafe(no_mangle)]
pub extern "system" fn Java_persona_core_rustcore_NativeBridge_create(
    mut env: JNIEnv,
    _class: JClass,
    host: JObject,
    config: JByteArray,
) -> jlong {
    catch_unwind(AssertUnwindSafe(|| {
        let config = env.convert_byte_array(&config).ok()?;
        let class = env.get_object_class(&host).ok()?;
        let changed = env.get_method_id(&class, "changed", "(J)V").ok()?;
        let changed_chat = optional_changed_chat(&mut env, &class);
        let run = env.get_method_id(&class, "run", "([B)V").ok()?;
        let host = JHost {
            vm: env.get_java_vm().ok()?,
            host: env.new_global_ref(&host).ok()?,
            changed,
            changed_chat,
            run,
        };
        let handle = crate::next_handle();
        if handle == u64::MAX {
            return None;
        }
        create(&config, Arc::new(host), handle)
    }))
    .ok()
    .flatten()
    .map_or(0, |core| core as jlong)
}

#[unsafe(no_mangle)]
pub extern "system" fn Java_persona_core_rustcore_NativeBridge_dispatch(
    env: JNIEnv,
    _class: JClass,
    handle: jlong,
    action: JByteArray,
) -> jboolean {
    let Some(runtime) = core(handle) else { return JNI_FALSE };
    catch_unwind(AssertUnwindSafe(|| {
        let Ok(action) = env.convert_byte_array(&action) else {
            return false;
        };
        runtime.dispatch(&action)
    }))
    .map_or(JNI_FALSE, boolean)
}

#[unsafe(no_mangle)]
pub extern "system" fn Java_persona_core_rustcore_NativeBridge_drain(
    mut env: JNIEnv,
    _class: JClass,
    handle: jlong,
    chat: JString,
) -> jbyteArray {
    let Some(runtime) = core(handle) else {
        return std::ptr::null_mut();
    };
    catch_unwind(AssertUnwindSafe(|| {
        let chat: String = env.get_string(&chat).ok()?.into();
        let patch = runtime.drain(&chat);
        env.byte_array_from_slice(&patch).ok().map(JByteArray::into_raw)
    }))
    .ok()
    .flatten()
    .unwrap_or(std::ptr::null_mut())
}

#[unsafe(no_mangle)]
pub extern "system" fn Java_persona_core_rustcore_NativeBridge_flush(
    _env: JNIEnv,
    _class: JClass,
    handle: jlong,
    timeout_ms: jint,
) -> jboolean {
    if handle != 0 {
        boolean(unsafe { crate::persona_core_flush(handle as *mut PersonaCore, timeout_ms.max(0) as u32) })
    } else {
        JNI_FALSE
    }
}

#[unsafe(no_mangle)]
pub extern "system" fn Java_persona_core_rustcore_NativeBridge_destroy(_env: JNIEnv, _class: JClass, handle: jlong) {
    if handle != 0 {
        unsafe { crate::persona_core_free(handle as *mut PersonaCore) }
    }
}

/// Projects the Agents list from a JSON input string. Pure function, no runtime handle needed.
#[unsafe(no_mangle)]
pub extern "system" fn Java_persona_core_rustcore_NativeBridge_projectSort<'a>(
    mut env: JNIEnv<'a>,
    _class: JClass<'a>,
    input: JString<'a>,
) -> JString<'a> {
    catch_unwind(AssertUnwindSafe(|| {
        let input: String = env.get_string(&input).ok()?.into();
        let input: persona_core_domain::sort::Input = serde_json::from_str(&input).ok()?;
        let projection = persona_core_domain::sort::project(&input);
        let json = serde_json::to_string(&projection).ok()?;
        env.new_string(json).ok()
    }))
    .ok()
    .flatten()
    .unwrap_or_default()
}

#[cfg(any(target_os = "android", feature = "test-support"))]
#[unsafe(no_mangle)]
pub extern "system" fn Java_persona_core_rustcore_NativeDiagnostics_enableDiagnostics(
    _env: JNIEnv,
    _class: JClass,
    handle: jlong,
) {
    let _ = catch_unwind(AssertUnwindSafe(|| {
        if let Some(runtime) = core(handle) {
            runtime.enable_diagnostics();
        }
    }));
}

#[cfg(feature = "test-support")]
#[unsafe(no_mangle)]
pub extern "system" fn Java_persona_core_rustcore_NativeDiagnostics_lease(
    _env: JNIEnv,
    _class: JClass,
    handle: jlong,
) -> jboolean {
    boolean(core(handle).is_some())
}

#[cfg(any(target_os = "android", feature = "test-support"))]
#[unsafe(no_mangle)]
pub extern "system" fn Java_persona_core_rustcore_NativeDiagnostics_flushDiagnostic(
    _env: JNIEnv,
    _class: JClass,
    handle: jlong,
    timeout_ms: jint,
) -> jboolean {
    let Some(runtime) = core(handle) else { return JNI_FALSE };
    let timeout = Duration::from_millis(timeout_ms.max(0) as u64);
    catch_unwind(AssertUnwindSafe(|| runtime.flush(timeout))).map_or(JNI_FALSE, boolean)
}

#[cfg(any(target_os = "android", feature = "test-support"))]
#[unsafe(no_mangle)]
pub extern "system" fn Java_persona_core_rustcore_NativeDiagnostics_diagnostics(
    env: JNIEnv,
    _class: JClass,
    handle: jlong,
) -> jlongArray {
    let Some(runtime) = core(handle) else {
        return std::ptr::null_mut();
    };
    let counters = catch_unwind(AssertUnwindSafe(|| runtime.diagnostics())).unwrap_or_default();
    env.new_long_array(counters.len() as i32)
        .and_then(|array| {
            env.set_long_array_region(&array, 0, &counters)?;
            Ok(array.into_raw())
        })
        .unwrap_or(std::ptr::null_mut())
}
