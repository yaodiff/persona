//! HarmonyOS's NAPI binding: the same bincode actions, effects and patches as the C/JNI bindings.
//! The portable registry owns each runtime. Only the `ohos` feature links NAPI, supplied by the device runtime.
//! Dispatch/drain borrow a runtime for the whole call, so close cannot free it mid-call.

pub mod registry;

#[cfg(feature = "ohos")]
mod binding {
    use napi_derive_ohos::napi;
    use napi_ohos::bindgen_prelude::{Buffer, FnArgs, Result, Status};
    use napi_ohos::threadsafe_function::ThreadsafeFunction;

    use crate::registry::{self, CoreHost};

    #[napi(catch_unwind)]
    pub fn core_create(
        config: Buffer,
        changed: ThreadsafeFunction<i64, (), i64, Status, false>,
        effects: ThreadsafeFunction<Buffer, (), Buffer, Status, false>,
    ) -> Result<i64> {
        registry::create(config.as_ref(), CoreHost::new(changed, effects))
    }

    #[napi(catch_unwind)]
    #[allow(clippy::type_complexity)] // Type aliases are not expanded by napi-derive's TypeScript generator.
    pub fn core_create_with_chat(
        config: Buffer,
        changed: ThreadsafeFunction<i64, (), i64, Status, false>,
        changed_chat: ThreadsafeFunction<FnArgs<(String, i64)>, (), FnArgs<(String, i64)>, Status, false>,
        effects: ThreadsafeFunction<Buffer, (), Buffer, Status, false>,
    ) -> Result<i64> {
        registry::create(
            config.as_ref(),
            CoreHost::with_changed_chat(changed, changed_chat, effects),
        )
    }

    #[napi(catch_unwind)]
    pub fn core_dispatch(handle: i64, action: Buffer) -> Result<bool> {
        registry::dispatch(handle, action.as_ref())
    }

    #[napi(catch_unwind)]
    pub fn core_drain(handle: i64, chat: String) -> Result<Buffer> {
        registry::drain(handle, &chat).map(Into::into)
    }

    #[napi(catch_unwind)]
    pub fn core_flush(handle: i64, timeout_millis: u32) -> Result<bool> {
        registry::flush(handle, timeout_millis)
    }

    #[napi(catch_unwind)]
    pub fn core_close(handle: i64) -> Result<()> {
        registry::close(handle)
    }

    #[napi(catch_unwind)]
    pub fn core_config() -> Buffer {
        registry::config().into()
    }

    #[napi(catch_unwind)]
    pub fn core_project_sort(input: String) -> Result<String> {
        let input: persona_core::sort::Input =
            serde_json::from_str(&input).map_err(|e| napi_ohos::Error::from_reason(e.to_string()))?;
        let projection = persona_core::sort::project(&input);
        serde_json::to_string(&projection).map_err(|e| napi_ohos::Error::from_reason(e.to_string()))
    }
}
