//! Reading a known key without failing on its type, so the decoder can say which key was wrong and never has to quote
//! a serde message (those repeat the offending value, which may be message text).
//!
//! The acceptance rules are the ones Swift's `JSONDecoder` applies to the same declared type:
//!
//! - `Bool` takes only `true` / `false`, `String` only a string;
//! - `Int` takes a JSON integer literal inside the 64-bit range (`8`) and nothing else (`8.5`, `1e30`, `"8"`).
//!   Foundation also takes a whole number written with a fraction or an exponent (`8.0`, `8e0`); the core refuses
//!   that spelling, which the hub never writes, rather than read an integer through a double (`transport`'s "Known
//!   differences");
//! - an optional key takes `null` or absence as "no value";
//! - a key that appears more than once keeps its first value, as Foundation's keyed container does
//!   (swift-foundation, `JSONDecoder.swift`, `KeyedContainer.stringify`: `result[key]._setIfNil(to: value)`).

use std::borrow::Cow;
use std::fmt;
use std::marker::PhantomData;

use serde::Deserialize;
use serde::de::{Deserializer, IgnoredAny, MapAccess, SeqAccess, Visitor};

/// What a known key held.
#[derive(Debug, Default)]
pub(crate) enum Seen<T> {
    #[default]
    Missing,
    Null,
    Value(T),
    /// Present with a type the key's declaration does not take.
    Wrong,
}

/// A declared type: which JSON values it takes.
pub(crate) trait Shape<'de>: Sized {
    fn boolean(_: bool) -> Option<Self> {
        None
    }
    /// An integer literal inside the 64-bit range.
    fn int(_: i64) -> Option<Self> {
        None
    }
    /// A string that borrows from the input.
    fn borrowed(_: &'de str) -> Option<Self> {
        None
    }
    /// A string the parser had to unescape.
    fn unescaped(_: &str) -> Option<Self> {
        None
    }
    fn seq<A: SeqAccess<'de>>(mut seq: A) -> Result<Option<Self>, A::Error> {
        while seq.next_element::<IgnoredAny>()?.is_some() {}
        Ok(None)
    }
    fn map<A: MapAccess<'de>>(mut map: A) -> Result<Option<Self>, A::Error> {
        while map.next_entry::<IgnoredAny, IgnoredAny>()?.is_some() {}
        Ok(None)
    }
}

impl<'de> Shape<'de> for bool {
    fn boolean(value: bool) -> Option<Self> {
        Some(value)
    }
}

impl<'de> Shape<'de> for i64 {
    fn int(value: i64) -> Option<Self> {
        Some(value)
    }
}

impl<'de: 'a, 'a> Shape<'de> for Cow<'a, str> {
    fn borrowed(value: &'de str) -> Option<Self> {
        Some(Cow::Borrowed(value))
    }
    fn unescaped(value: &str) -> Option<Self> {
        Some(Cow::Owned(value.to_owned()))
    }
}

/// A string whose content the core does not read.
#[derive(Debug)]
pub(crate) struct AnyStr;

impl<'de> Shape<'de> for AnyStr {
    fn borrowed(_: &'de str) -> Option<Self> {
        Some(AnyStr)
    }
    fn unescaped(_: &str) -> Option<Self> {
        Some(AnyStr)
    }
}

struct SeenVisitor<T>(PhantomData<T>);

impl<'de, T: Shape<'de>> Visitor<'de> for SeenVisitor<T> {
    type Value = Seen<T>;

    fn expecting(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.write_str("any JSON value")
    }
    fn visit_unit<E>(self) -> Result<Self::Value, E> {
        Ok(Seen::Null)
    }
    fn visit_bool<E>(self, value: bool) -> Result<Self::Value, E> {
        Ok(T::boolean(value).map_or(Seen::Wrong, Seen::Value))
    }
    fn visit_i64<E>(self, value: i64) -> Result<Self::Value, E> {
        Ok(T::int(value).map_or(Seen::Wrong, Seen::Value))
    }
    fn visit_u64<E>(self, value: u64) -> Result<Self::Value, E> {
        Ok(i64::try_from(value)
            .ok()
            .and_then(T::int)
            .map_or(Seen::Wrong, Seen::Value))
    }
    /// A number written with a fraction or an exponent, or an integer literal outside the 64-bit range: no
    /// declared type takes it.
    fn visit_f64<E>(self, _: f64) -> Result<Self::Value, E> {
        Ok(Seen::Wrong)
    }
    fn visit_borrowed_str<E>(self, value: &'de str) -> Result<Self::Value, E> {
        Ok(T::borrowed(value).map_or(Seen::Wrong, Seen::Value))
    }
    fn visit_str<E>(self, value: &str) -> Result<Self::Value, E> {
        Ok(T::unescaped(value).map_or(Seen::Wrong, Seen::Value))
    }
    fn visit_seq<A: SeqAccess<'de>>(self, seq: A) -> Result<Self::Value, A::Error> {
        Ok(T::seq(seq)?.map_or(Seen::Wrong, Seen::Value))
    }
    fn visit_map<A: MapAccess<'de>>(self, map: A) -> Result<Self::Value, A::Error> {
        Ok(T::map(map)?.map_or(Seen::Wrong, Seen::Value))
    }
}

impl<'de, T: Shape<'de>> Deserialize<'de> for Seen<T> {
    fn deserialize<D: Deserializer<'de>>(deserializer: D) -> Result<Self, D::Error> {
        deserializer.deserialize_any(SeenVisitor(PhantomData))
    }
}

/// A key's fault, by the key's own name: never anything from the payload.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum KeyFault {
    /// A required key is absent or `null`.
    Missing(&'static str),
    /// A known key holds a type its declaration does not take, a number its declaration does not take included.
    WrongType(&'static str),
}

impl<T> Seen<T> {
    /// A required key.
    pub(crate) fn required(self, key: &'static str) -> Result<T, KeyFault> {
        match self {
            Seen::Value(value) => Ok(value),
            Seen::Missing | Seen::Null => Err(KeyFault::Missing(key)),
            Seen::Wrong => Err(KeyFault::WrongType(key)),
        }
    }

    /// An optional key: absent and `null` are both "no value".
    pub(crate) fn optional(self, key: &'static str) -> Result<Option<T>, KeyFault> {
        match self {
            Seen::Value(value) => Ok(Some(value)),
            Seen::Missing | Seen::Null => Ok(None),
            Seen::Wrong => Err(KeyFault::WrongType(key)),
        }
    }
}

/// The first byte that is not JSON whitespace opens an object.
pub(crate) fn opens_object(json: &str) -> bool {
    json.bytes().find(|b| !matches!(b, b' ' | b'\t' | b'\n' | b'\r')) == Some(b'{')
}

/// A slot of a [`keyed!`] struct: whether a value was already read into it.
pub(crate) trait Slot: Default {
    fn is_unset(&self) -> bool;
}

impl<T> Slot for Seen<T> {
    fn is_unset(&self) -> bool {
        matches!(self, Seen::Missing)
    }
}

/// An object's key, borrowed from the input unless the parser had to unescape it.
pub(crate) struct Key<'de>(pub(crate) Cow<'de, str>);

impl<'de> Deserialize<'de> for Key<'de> {
    fn deserialize<D: Deserializer<'de>>(deserializer: D) -> Result<Self, D::Error> {
        struct KeyVisitor;
        impl<'de> Visitor<'de> for KeyVisitor {
            type Value = Key<'de>;

            fn expecting(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
                f.write_str("a key")
            }
            fn visit_borrowed_str<E>(self, value: &'de str) -> Result<Self::Value, E> {
                Ok(Key(Cow::Borrowed(value)))
            }
            fn visit_str<E>(self, value: &str) -> Result<Self::Value, E> {
                Ok(Key(Cow::Owned(value.to_owned())))
            }
        }
        deserializer.deserialize_str(KeyVisitor)
    }
}

/// Declares a struct read from a JSON object by key: `field = "key": slot type`. Unknown keys are skipped, and a key
/// that appears again is skipped too, so its first value stays (a derived reader would refuse the object). The
/// caller checks beforehand that the value opens an object. `KEYS` lists the keys in declaration order.
macro_rules! keyed {
    ($(#[$meta:meta])* struct $name:ident<$lt:lifetime> {
        $($(#[$note:meta])* $field:ident = $key:literal: $slot:ty,)+
    }) => {
        $(#[$meta])*
        struct $name<$lt> {
            $($(#[$note])* $field: $slot,)+
            borrows: std::marker::PhantomData<&$lt ()>,
        }

        impl<$lt> $name<$lt> {
            #[cfg(test)]
            #[allow(dead_code)]
            const KEYS: &'static [&'static str] = &[$($key),+];
        }

        impl<'de: $lt, $lt> serde::Deserialize<'de> for $name<$lt> {
            fn deserialize<D: serde::Deserializer<'de>>(deserializer: D) -> Result<Self, D::Error> {
                struct Fields<$lt>(std::marker::PhantomData<&$lt ()>);

                impl<'de: $lt, $lt> serde::de::Visitor<'de> for Fields<$lt> {
                    type Value = $name<$lt>;

                    fn expecting(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
                        f.write_str("a JSON object")
                    }
                    fn visit_map<A: serde::de::MapAccess<'de>>(self, mut map: A) -> Result<Self::Value, A::Error> {
                        let mut read = $name {
                            $($field: Default::default(),)+
                            borrows: std::marker::PhantomData,
                        };
                        while let Some(key) = map.next_key::<$crate::transport::seen::Key<'de>>()? {
                            match &*key.0 {
                                $($key if $crate::transport::seen::Slot::is_unset(&read.$field) => {
                                    read.$field = map.next_value()?;
                                })+
                                _ => {
                                    map.next_value::<serde::de::IgnoredAny>()?;
                                }
                            }
                        }
                        Ok(read)
                    }
                }
                deserializer.deserialize_map(Fields(std::marker::PhantomData))
            }
        }
    };
}
pub(crate) use keyed;
