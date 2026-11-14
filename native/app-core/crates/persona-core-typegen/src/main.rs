//! Generates the clients' codecs for the core's wire types (`persona_core_runtime::wire`), bincode encoded.
//!
//!     cargo run -p persona-core-typegen            # writes the generated sources
//!     cargo run -p persona-core-typegen -- --check # fails when they are out of date
//!
//! Kotlin goes to android/core/rustcore. The generated files are committed, so a change to a wire type and its codecs
//! land together.

mod arkts;

use std::path::{Path, PathBuf};

use persona_core_runtime::wire::{
    Action, ChatEvent, ChatPatch, ChatSnapshot, Config, Effect, ListOp, Payload, Request, WireBatch,
};
use serde_generate::{CodeGeneratorConfig, Encoding, SourceInstaller, kotlin, swift};
use serde_reflection::{Registry, Tracer, TracerConfig};

const KOTLIN_PACKAGE: &str = "persona.core.rustcore.wire";

fn registry() -> Registry {
    let mut tracer = Tracer::new(TracerConfig::default());
    tracer.trace_simple_type::<Request>().expect("Request");
    tracer.trace_simple_type::<ListOp>().expect("ListOp");
    tracer.trace_simple_type::<ChatEvent>().expect("ChatEvent");
    tracer.trace_simple_type::<WireBatch>().expect("WireBatch");
    tracer.trace_simple_type::<ChatSnapshot>().expect("ChatSnapshot");
    tracer.trace_simple_type::<Config>().expect("Config");
    tracer.trace_simple_type::<Action>().expect("Action");
    tracer.trace_simple_type::<Payload>().expect("Payload");
    tracer.trace_simple_type::<Effect>().expect("Effect");
    tracer.trace_simple_type::<ChatPatch>().expect("ChatPatch");
    tracer.registry().expect("every wire type is traced")
}

fn kotlin_string_golden() -> String {
    // An independent Rust/bincode fixture shared by the Kotlin codec tests, not Kotlin-generated expectations.
    let snapshot = ChatSnapshot {
        items_json: "[\"ASCII\", \"中文\", \"emoji: 🦀\", \"\"]".into(),
        history: Some("a".repeat(257)),
        covered: 1,
        has_more: None,
        loaded_once: true,
    };
    let bytes = persona_core_runtime::wire::encode(&snapshot);
    let mut json = format!(
        "{{\n  \"itemsJson\": {:?},\n  \"history\": {:?},\n  \"nulText\": \"nul: \u{0}\",\n  \"encodedBase64\": \"",
        snapshot.items_json,
        snapshot.history.as_deref().expect("history fixture"),
    );
    const BASE64: &[u8; 64] = b"ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/";
    for chunk in bytes.chunks(3) {
        let value = (u32::from(chunk[0]) << 16)
            | (u32::from(*chunk.get(1).unwrap_or(&0)) << 8)
            | u32::from(*chunk.get(2).unwrap_or(&0));
        for index in 0..4 {
            json.push(if index > chunk.len() {
                '='
            } else {
                BASE64[((value >> (18 - index * 6)) & 63) as usize] as char
            });
        }
    }
    json.push_str("\"\n}\n");
    json.replace('\u{0}', "\\u0000")
}

fn write_kotlin(root: &Path, registry: &Registry) {
    let config = CodeGeneratorConfig::new(KOTLIN_PACKAGE.to_owned()).with_encodings([Encoding::Bincode]);
    let installer = kotlin::Installer::new(root.to_path_buf());
    installer.install_module(&config, registry).expect("Kotlin codecs");
    installer.install_serde_runtime().expect("Kotlin serde runtime");
    installer.install_bincode_runtime().expect("Kotlin bincode runtime");
    let path = root.join("com/novi/serde/BinaryDeserializer.kt");
    let mut source = std::fs::read_to_string(&path).expect("generated Kotlin string decoder");
    let old = r#"        val content = readBytes(len.toInt())
        return try {
            content.decodeToString(throwOnInvalidSequence = true)
"#;
    let new = r#"        // Decode the already-checked range without a temporary array; retain strict UTF-8 validation.
        val start = position
        requireAvailable(len.toInt())
        position = start + len.toInt()
        return try {
            input.decodeToString(start, position, throwOnInvalidSequence = true)
"#;
    assert_eq!(
        source.matches(old).count(),
        1,
        "upstream Kotlin string decoder changed; update its range-copy optimization"
    );
    source = source.replace(old, new);
    std::fs::write(path, source).expect("write allocation-reduced Kotlin string decoder");
}

fn write_swift(root: &Path, registry: &Registry) {
    let config = CodeGeneratorConfig::new("PersonaCoreWire".to_owned()).with_encodings([Encoding::Bincode]);
    let installer = swift::Installer::new(root.to_path_buf());
    installer.install_module(&config, registry).expect("Swift codecs");
    installer.install_serde_runtime().expect("Swift serde runtime");
    let path = root.join("Sources/PersonaCoreWire/PersonaCoreWire.swift");
    let mut source = std::fs::read_to_string(&path).expect("generated Swift codecs");
    // `PersonaCoreWire` is the public wire module; its value types live in the generator's one-file namespace.
    source = source
        .replace("import Serde", "import PersonaCoreSerde")
        .replace("PersonaCoreSerde.", "PersonaCoreWire.");
    // The upstream generator predates strict concurrency. Every wire type contains only value types, so each can
    // promise Sendable without wrapping the C handle or weakening the compiler's isolation checks.
    for name in registry.keys() {
        source = source.replace(
            &format!("struct {name}: Hashable {{"),
            &format!("struct {name}: Hashable, Sendable {{"),
        );
        source = source.replace(
            &format!("enum {name}: Hashable {{"),
            &format!("enum {name}: Hashable, Sendable {{"),
        );
    }
    while source.ends_with("\n\n") {
        source.pop();
    }
    std::fs::write(path, source).expect("mark generated codecs Sendable");
    for (file, conformance) in [
        ("Int128.swift", "\nextension Int128: Sendable {}\n"),
        ("UInt128.swift", "\nextension UInt128: Sendable {}\n"),
        ("Unit.swift", "\nextension Unit: Sendable {}\n"),
        (
            "Indirect.swift",
            "\nextension Indirect: Sendable where T: Sendable {}\n",
        ),
        (
            "Tuple2.swift",
            "\nextension Tuple2: Sendable where T0: Sendable, T1: Sendable {}\n",
        ),
        (
            "Tuple3.swift",
            "\nextension Tuple3: Sendable where T0: Sendable, T1: Sendable, T2: Sendable {}\n",
        ),
        (
            "Tuple4.swift",
            "\nextension Tuple4: Sendable where T0: Sendable, T1: Sendable, T2: Sendable, T3: Sendable {}\n",
        ),
        (
            "Tuple5.swift",
            "\nextension Tuple5: Sendable where T0: Sendable, T1: Sendable, T2: Sendable, T3: Sendable, T4: Sendable {}\n",
        ),
        (
            "Tuple6.swift",
            "\nextension Tuple6: Sendable where T0: Sendable, T1: Sendable, T2: Sendable, T3: Sendable, T4: Sendable, T5: Sendable {}\n",
        ),
    ] {
        let path = root.join("Sources/Serde").join(file);
        let mut source = std::fs::read_to_string(&path).expect("generated serde runtime");
        source.push_str(conformance);
        std::fs::write(path, source).expect("mark serde value Sendable");
    }
}

fn files(root: &Path) -> Vec<(PathBuf, Vec<u8>)> {
    let mut out = Vec::new();
    let mut stack = vec![root.to_path_buf()];
    while let Some(dir) = stack.pop() {
        let Ok(entries) = std::fs::read_dir(&dir) else { continue };
        for entry in entries {
            let path = entry.expect("directory entry").path();
            if path.is_dir() {
                stack.push(path);
            } else {
                let relative = path.strip_prefix(root).expect("under root").to_path_buf();
                out.push((relative, std::fs::read(&path).expect("read")));
            }
        }
    }
    out.sort();
    out
}

/// The generated trees under `root`: the wire package and the codec runtime.
fn generated(root: &Path) -> Vec<(PathBuf, Vec<u8>)> {
    ["persona/core/rustcore/wire", "com/novi"]
        .iter()
        .flat_map(|dir| {
            files(&root.join(dir))
                .into_iter()
                .map(move |(path, bytes)| (Path::new(dir).join(path), bytes))
        })
        .collect()
}

fn generated_swift(root: &Path) -> Vec<(PathBuf, Vec<u8>)> {
    ["PersonaCoreWire", "Serde"]
        .iter()
        .flat_map(|dir| files(&root.join("Sources").join(dir)))
        .collect()
}

fn main() {
    let repository = Path::new(env!("CARGO_MANIFEST_DIR")).join("../../../..");
    let kotlin_root = repository.join("android/core/rustcore/src/main/kotlin");
    let arkts_root = repository.join("harmony/entry/src/main/ets/core/rustcore");
    let swift_root = repository.join("apple/Packages/PersonaCore");
    let registry = registry();
    let golden = repository.join("android/core/rustcore/src/test/resources/wire-golden.json");
    if std::env::args().any(|a| a == "--check") {
        if std::fs::read_to_string(&golden).ok().as_deref() != Some(kotlin_string_golden().as_str()) {
            eprintln!("persona-core-typegen: Rust wire golden is out of date");
            std::process::exit(1);
        }
        let scratch = std::env::temp_dir().join(format!("persona-core-typegen-{}", std::process::id()));
        let _ = std::fs::remove_dir_all(&scratch);
        write_kotlin(&scratch.join("kotlin"), &registry);
        write_swift(&scratch.join("swift"), &registry);
        arkts::write(&scratch.join("arkts"), &registry).expect("ArkTS codecs");
        let fresh_kotlin = generated(&scratch.join("kotlin"));
        let fresh_swift = generated_swift(&scratch.join("swift"));
        let fresh_arkts = files(&scratch.join("arkts"));
        let _ = std::fs::remove_dir_all(&scratch);
        if fresh_kotlin != generated(&kotlin_root)
            || fresh_swift != generated_swift(&swift_root)
            || fresh_arkts != files(&arkts_root)
        {
            eprintln!("persona-core-typegen: codecs are out of date; run `cargo run -p persona-core-typegen`");
            std::process::exit(1);
        }
        println!("persona-core-typegen: Kotlin, Swift and ArkTS codecs up to date");
        return;
    }
    for dir in ["persona/core/rustcore/wire", "com/novi"] {
        let _ = std::fs::remove_dir_all(kotlin_root.join(dir));
    }
    for dir in ["Sources/PersonaCoreWire", "Sources/Serde"] {
        let _ = std::fs::remove_dir_all(swift_root.join(dir));
    }
    write_kotlin(&kotlin_root, &registry);
    write_swift(&swift_root, &registry);
    arkts::write(&arkts_root, &registry).expect("ArkTS codecs");
    std::fs::create_dir_all(golden.parent().expect("golden parent")).expect("golden directory");
    std::fs::write(golden, kotlin_string_golden()).expect("write independent Rust wire golden");
    println!(
        "persona-core-typegen: wrote {}, {} and {}",
        kotlin_root.display(),
        swift_root.display(),
        arkts_root.display()
    );
}
