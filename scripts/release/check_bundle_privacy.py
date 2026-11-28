#!/usr/bin/env python3
"""Fail when a built app bundle holds this person's private values: anyone with the TestFlight public link can install it.

    python3 scripts/release/check_bundle_privacy.py <Persona.app>     # the watch app and extensions inside are scanned too
    python3 scripts/release/check_bundle_privacy.py <Mac Persona.app> # the Mac app (Contents/…), same rules
    python3 scripts/release/check_bundle_privacy.py --whole-short <dir>  # published personad builds (scripts/release/publish.sh)

The values are scripts/privacy_check.py's (config, private-words.txt, CLAUDE.local.md, the home directory), plus private
IPv4 addresses and emails outside placeholder domains, looked for in every file's printable strings (like `strings`,
after decompressing lzfse files) and in binary plists. The bundle id and team id are in every signed build on purpose
and are removed before matching. App mode also recognizes only the exact configured public HTTPS /relay endpoint, and
--whole-short only the exact public download address (publish.site_url + /dl, the hub's update URL, docs/operations/hub-update.md);
their host in any other context, related paths and credentials remain subject to private-value checks.
Signing data is Apple's and is skipped: embedded.mobileprovision (embedded.provisionprofile on the Mac), _CodeSignature,
and each Mach-O slice's validated embedded-signature blob (certificate names; the archive's development profile lists
device ids, which export replaces anyway). A universal (fat) binary's remaining bytes are scanned across every slice;
its signature offsets are relative to its slice, and cutting at the first would skip the rest of the file.
Values match as written or lowercased, not in any case: Swift's mangled symbol names are full of short fragments
that equal a value in another case.
The repo's own Go module path (it names the GitHub owner) is in every Go binary's imports and is removed too, as
scripts/privacy_check.py does for the sources. --whole-short: a value of four characters or fewer only counts as a
whole word, not inside a longer one (a Go binary carries Unicode tables and minified JavaScript, where short values turn
up inside ordinary words); app bundles keep the strict substring match except for short alphabetic fragments inside
word tokens of the exact SHA-256-pinned, unmodified public Mermaid resource. Complete words, nonalphabetic values,
long values and generic IP/email checks remain active there; any changed byte falls back to strict app matching.
--whole-short also reads every gzip member inside a file: a Go build embeds code compressed (the runtime packages,
server/internal/runtimepkg, are gzipped tars of JavaScript; the web client's hashed assets are stored gzipped), and
its printable strings are noise until it is inflated. Each member is scanned like the rest of the file, besides the
file's own bytes.
Prints the file and the kind of hit, never the value.
For public Go builds, narrowly scoped dependency constants are classified separately from private data. This requires
a build with exactly the tags of server/build-tags.txt (the public marker and ts_omit_ feature tags), and unreplaced
dependencies whose build-info checksums match server/go.sum; registered personal values always win.
Two exact Go rodata contexts are classified too (#6314): larkcli's chat-link path and its display name, in their
observed public constant prefixes, read as a banned name; wireguard-go's handshake identifier (its author string) is
classified only with that unreplaced dependency.
The seven known swift-nio-ssh algorithm/certificate names are also classified as a complete original printable field
or a complete comma-separated list; that fallback excludes URLs, surrounding context and normalized fragments.
"""
from __future__ import annotations

import base64
import datetime
import hashlib
import ipaddress
import itertools
import os
import plistlib
import re
import struct
import subprocess
import sys
import tarfile
import threading
import zipfile
import zlib
from urllib.parse import urlsplit

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import persona_config  # noqa: E402
import privacy_check as pc  # noqa: E402

SKIP = {"embedded.mobileprovision", "embedded.provisionprofile", "CodeResources"}
PRINTABLE = re.compile(rb"[\x20-\x7e]{4,}")
PLIST_UTF16 = re.compile(rb"(?:\x00[\x20-\x7e]){4,}")

# npm mermaid@12.3.0, package/dist/mermaid.min.js; verified against the registry tarball's SHA-512 integrity.
PUBLIC_JS_SHA256 = {
    "mermaid.min.js": "336df803d87a38b70a01e736e666c8f7601d2ce42f5e47c8fafe5d721074b9a3",
}


def verified_public_js(path, data):
    """Use the same byte snapshot for provenance and scanning, not a second file read or a mutable manifest."""
    digest = PUBLIC_JS_SHA256.get(os.path.basename(path))
    return digest is not None and hashlib.sha256(data).hexdigest() == digest


# These are protocol and standards constants, not accounts or deployment addresses. Keep contexts narrow:
# SSH wire identifiers, the upstream web client's exact contact link, and complete RFC 1918 networks.
# Exact names from x/crypto/ssh, gliderssh and pkg/sftp; never exempt either domain as a whole.
GO_SSH_IDENTIFIERS = re.compile(
    r"(?<![A-Za-z0-9._%+-])(?:"
    r"(?:aes128-gcm|aes256-gcm|chacha20-poly1305|"
    r"hmac-sha2-256-etm|hmac-sha2-512-etm|"
    r"rsa-sha2-256-cert-v01|rsa-sha2-512-cert-v01|ssh-rsa-cert-v01|ssh-dss-cert-v01|ssh-ed25519-cert-v01|"
    r"ecdsa-sha2-nistp256-cert-v01|ecdsa-sha2-nistp384-cert-v01|ecdsa-sha2-nistp521-cert-v01|"
    r"sk-ssh-ed25519|sk-ssh-ed25519-cert-v01|sk-ecdsa-sha2-nistp256|sk-ecdsa-sha2-nistp256-cert-v01|"
    r"kex-strict-c-v00|kex-strict-s-v00|ping|auth-agent-req|auth-agent|"
    r"direct-streamlocal|forwarded-streamlocal|streamlocal-forward|cancel-streamlocal-forward|keepalive|"
    r"statvfs|fstatvfs|fsync|hardlink|posix-rename)@openssh[.]com|"
    r"curve25519-sha256@libssh[.]org)(?![A-Za-z0-9._%+-])"
)
# Fixed wire names from swift-nio-ssh. A whole field may be one name or a complete known name-list, never a URL.
SSH_ALGORITHM_NAMES = frozenset(name + "@" + domain for name, domain in (
    ("aes128-gcm", "openssh.com"), ("aes256-gcm", "openssh.com"), ("curve25519-sha256", "libssh.org"),
    ("ecdsa-sha2-nistp256-cert-v01", "openssh.com"), ("ecdsa-sha2-nistp384-cert-v01", "openssh.com"),
    ("ecdsa-sha2-nistp521-cert-v01", "openssh.com"), ("ssh-ed25519-cert-v01", "openssh.com"),
))
TAILSCALE_CONTACT = re.compile(r'<a href="mailto:support\+webclient@tailscale[.]com" class="link">contact us</a>')
# persona-iphone-bridge (Rust, native/iphone-bridge): aws-lc's assembly credits ("SHA256 block transform for ARMv8,
# CRYPTOGAMS by <…@openssl.org>", "AES GCM module for ARMv8, SPDX BSD-3-Clause by <…@arm.com>", the two authors below).
# Upstream author strings, not accounts; only right after "by <" and before ">".
RUST_TLS_CONSTANTS = re.compile(r"(?<=by <)(?:appro@openssl[.]org|xiaokang[.]qian@arm[.]com)(?=>)")
# Go lays string constants out back to back in rodata. larkcli's chat-link path ends where its display name begins
# (the path, then the display name), which reads as a banned name that neither constant holds. Only the exact observed
# public prefixes count: the full public chat URL, or the iMessage parser's error next to the path (#14310).
# The name is split here because this file must not spell it either.
GO_ADJACENT_CONSTANTS = re.compile(
    r"(?:(?<=applink[.]feishu[.]cn)|(?<=not a typedstream))/client/chat/" + "open" + "Lark via lark-cli")
# wireguard-go's handshake identifier (device/noise-protocol.go): an upstream author string, not an account.
WIREGUARD_IDENTIFIER = re.compile(r"(?<![A-Za-z0-9._%+-])WireGuard v1 zx2c4 Jason@zx2c4[.]com(?![.][A-Za-z])")
WIREGUARD_MODULES = frozenset({"github.com/tailscale/wireguard-go", "golang.zx2c4.com/wireguard"})
# Go rodata also joins the unrelated title_mail field identifier with the
# domain constant @gmail.com. Only that exact join is not an email address.
GO_ADJACENT_MAIL_FIELD = re.compile(r'(?<![A-Za-z0-9._%+-])title_mail@gmail[.]com(?![A-Za-z0-9._%+-])')
# The repository identity adapter's generic SSH remote, not a person's email. Match only this exact public constant,
# not the GitLab domain, another account or surrounding text.
GITLAB_SSH_IDENTITY = re.compile(r'(?<![A-Za-z0-9._%+-])git@gitlab[.]com(?![A-Za-z0-9._%+-])')
RFC1918_NETWORKS = tuple(ipaddress.ip_network((address, prefix)) for address, prefix in (
    (0x0A000000, 8), (0xAC100000, 12), (0xC0A80000, 16),
))
# RFC 3927 link-local address used by AWS, Google Cloud and Azure for instance metadata.
CLOUD_METADATA_IP = ipaddress.ip_address(0xA9FEA9FE)


def public_build_tags(tags_path=None):
    """The -tags of a published build (server/build-tags.txt): the public marker plus tailscale.com's ts_omit_ feature
    tags, each once. A file with any other tag, or none, marks no build as public."""
    try:
        with open(tags_path or os.path.join(pc.ROOT, "server", "build-tags.txt")) as f:
            tags = f.read().strip().split(",")
    except OSError:
        return None
    if "public" not in tags or len(set(tags)) != len(tags) or not all(
            tag == "public" or re.fullmatch(r"ts_omit_[a-z0-9_]+", tag) for tag in tags):
        return None
    return tags


def go_dependencies(path, sums_path=None, tags_path=None):
    """Only classify constants from unreplaced modules recorded in both this Go executable and go.sum, in a build
    with exactly the published tags."""
    sums_path = sums_path or os.path.join(pc.ROOT, "server", "go.sum")
    public_tags = public_build_tags(tags_path)
    if public_tags is None:
        return set()
    try:
        with open(sums_path) as sums:
            trusted = {tuple(line.split()) for line in sums if len(line.split()) == 3}
        result = subprocess.run(["go", "version", "-m", path], capture_output=True, text=True, timeout=10)
    except (OSError, subprocess.TimeoutExpired):
        return set()
    if result.returncode:
        return set()
    dependencies = set()
    previous = None
    public_build = False
    for line in result.stdout.splitlines():
        fields = line.split()
        if fields == ["build", "-tags=" + ",".join(public_tags)]:
            public_build = True
        if fields[:1] == ["dep"] and len(fields) == 4:
            previous = fields[1]
            if tuple(fields[1:]) in trusted:
                dependencies.add(previous)
        elif fields[:1] == ["=>"]:
            dependencies.discard(previous)
        elif fields[:1] in (["mod"], ["dep"]):
            previous = None
    return dependencies if public_build else set()


def binary_noise_email(domain):
    """Machine code read as text, not an address (#4497): a two-letter mixed-case country domain ("Sg") or a
    second-level label with no letter ("9"). Longer mixed-case tails ("comA") still count: a real name runs into them."""
    labels = domain.split(".")
    tld, sld = labels[-1], labels[-2]
    return (len(tld) == 2 and tld != tld.lower() and tld != tld.upper()) or not any(c.isalpha() for c in sld)


def generic_text(text, dependencies, original_text=None):
    """Classify public constants only for generic IP/email detection, never private-word matching."""
    if original_text is None or text == original_text:
        if text in SSH_ALGORITHM_NAMES or ("," in text and all(name in SSH_ALGORITHM_NAMES for name in text.split(","))):
            return ""
    if dependencies and "tailscale.com" in dependencies:  # a public Go build: its own adjacent constants are known
        text = GO_ADJACENT_CONSTANTS.sub("", text)
    if WIREGUARD_MODULES & set(dependencies):
        text = WIREGUARD_IDENTIFIER.sub("", text)
    text = RUST_TLS_CONSTANTS.sub("", text)
    text = pc.EMAIL.sub(lambda m: "" if binary_noise_email(m.group(1)) else m.group(0), text)
    text = GO_ADJACENT_MAIL_FIELD.sub("", text)
    text = GITLAB_SSH_IDENTITY.sub("", text)
    if "golang.org/x/crypto" in dependencies:
        text = GO_SSH_IDENTIFIERS.sub("", text)
    if "github.com/tailscale/web-client-prebuilt" in dependencies:
        text = TAILSCALE_CONTACT.sub("", text)
    if "tailscale.com" not in dependencies:
        return text

    def public_address(match):
        address = match.group(1)
        try:
            ip = ipaddress.ip_address(address)
        except ValueError:
            return match.group(0)
        suffix = text[match.end():]
        for network in RFC1918_NETWORKS:
            if ip != network.network_address:
                continue
            if re.match(r"/%d(?!\d)" % network.prefixlen, suffix):
                return ""
            # ipnlocal's private and link-local CIDRs are consecutive Go string constants.
            link_local = str(ipaddress.ip_network((0xA9FE0000, 16)))
            if network.prefixlen == 16 and suffix.startswith("/16" + link_local):
                return ""
        if ip == CLOUD_METADATA_IP:
            # Go concatenates string constants in rodata, so the HTTP literal can touch adjacent strings.
            if text[max(0, match.start() - 7):match.start()] == "http://":
                return ""
            # cloudenv's adjacent metadata/resolver constants provide a specific non-URL context.
            resolver = str(ipaddress.ip_address(int(CLOUD_METADATA_IP) - 1))
            if suffix.startswith(resolver + "Metadata-Flavor"):
                return ""
        return match.group(0)

    return pc.IPV4.sub(public_address, text)


def text_kinds(text, words, whole_short=False, dependencies=frozenset(), original_text=None, public_js=False,
               generic=True):
    kinds = set()
    for word in words:
        pinned_short = (public_js and (original_text is None or text == original_text)
                        and len(word) <= 4 and word.isascii() and word.isalpha())
        if pinned_short:
            if re.search(r"(?<![A-Za-z0-9_$])(%s|%s)(?![A-Za-z0-9_$])" %
                         (re.escape(word), re.escape(word.lower())), text):
                kinds.add("private value (%d chars)" % len(word))
        elif whole_short and len(word) <= 4:
            if re.search(r"(?<![A-Za-z])(%s|%s)(?![A-Za-z])" % (re.escape(word), re.escape(word.lower())), text):
                kinds.add("private value (%d chars)" % len(word))
        elif word in text or word.lower() in text:
            kinds.add("private value (%d chars)" % len(word))
    if generic:
        for word in pc.problems(generic_text(text, dependencies, original_text), []):
            kinds.add(pc.private_kind(word))
    return kinds


THIN_HEADERS = {b"\xce\xfa\xed\xfe": ("<", 28), b"\xfe\xed\xfa\xce": (">", 28),
                b"\xcf\xfa\xed\xfe": ("<", 32), b"\xfe\xed\xfa\xcf": (">", 32)}
FAT_HEADERS = {b"\xca\xfe\xba\xbe": (">", False), b"\xbe\xba\xfe\xca": ("<", False),
               b"\xca\xfe\xba\xbf": (">", True), b"\xbf\xba\xfe\xca": ("<", True)}


def signature_blob_length(data, start, size):
    """Accept only a bounded embedded-signature SuperBlob; its padding is still scanned."""
    if size < 12:
        return None
    magic, length, count = struct.unpack_from(">III", data, start)
    if magic != 0xFADE0CC0 or not 12 <= length <= size or not 0 < count <= (length - 12) // 8:
        return None
    table_end = 12 + count * 8
    blobs = []
    for i in range(count):
        _, offset = struct.unpack_from(">II", data, start + 12 + i * 8)
        if offset < table_end or offset + 8 > length:
            return None
        blob_magic, blob_size = struct.unpack_from(">II", data, start + offset)
        if blob_magic not in (0xFADE0C01, 0xFADE0C02, 0xFADE0B01, 0xFADE7171, 0xFADE7172):
            return None
        if blob_size < 8 or offset + blob_size > length:
            return None
        blobs.append((offset, offset + blob_size))
    blobs.sort()
    if any(a[1] > b[0] for a, b in zip(blobs, blobs[1:])):
        return None
    return length


def thin_signature_ranges(data, base, size):
    """Read slice-relative load commands. None means malformed: do not exclude any bytes."""
    spec = THIN_HEADERS.get(data[base:base + 4])
    if spec is None:
        return None
    endian, header_size = spec
    if size < header_size:
        return None
    commands, command_bytes = struct.unpack_from(endian + "II", data, base + 16)
    commands_end = base + header_size + command_bytes
    if commands > command_bytes // 8 or commands_end > base + size:
        return None
    cursor = base + header_size
    ranges = []
    for _ in range(commands):
        if cursor + 8 > commands_end:
            return None
        command, command_size = struct.unpack_from(endian + "II", data, cursor)
        if command_size < 8 or command_size % 4 or cursor + command_size > commands_end:
            return None
        if command == 0x1D:  # LC_CODE_SIGNATURE
            if command_size != 16 or ranges:
                return None
            offset, signature_size = struct.unpack_from(endian + "II", data, cursor + 8)
            start = base + offset
            if start < commands_end or signature_size > base + size - start:
                return None
            length = signature_blob_length(data, start, signature_size)
            if length is None:
                return None
            ranges.append((start, start + length))
        cursor += command_size
    return ranges if cursor == commands_end else None


def signature_ranges(data):
    """Validated signature ranges of every thin / universal 32- or 64-bit Mach-O slice."""
    if data[:4] in THIN_HEADERS:
        return thin_signature_ranges(data, 0, len(data)) or []
    spec = FAT_HEADERS.get(data[:4])
    if spec is None or len(data) < 8:
        return []
    endian, wide = spec
    count = struct.unpack_from(endian + "I", data, 4)[0]
    arch_size = 32 if wide else 20
    if not 0 < count <= (len(data) - 8) // arch_size:
        return []
    table_end = 8 + count * arch_size
    slices = []
    for i in range(count):
        entry = 8 + i * arch_size
        offset, size = struct.unpack_from(endian + ("QQ" if wide else "II"), data, entry + 8)
        if offset < table_end or size < 28 or offset + size > len(data):
            return []
        slices.append((offset, offset + size))
    slices.sort()
    if any(a[1] > b[0] for a, b in zip(slices, slices[1:])):
        return []
    ranges = []
    for start, end in slices:
        found = thin_signature_ranges(data, start, end - start)
        if found is None:
            return []
        ranges.extend(found)
    return ranges


def plist_references(data):
    """Only validated array/dictionary reference payloads are non-text (#3166).

    Check every indexed object, including unreachable ones, before trusting any range. Unknown types, overlaps,
    bad lengths/references and cycles fall back to raw scanning. Data, scalar payloads, gaps and offset/trailer bytes
    are never excluded. Decode every string object too: plist_xml's root traversal misses unreachable UTF-16 strings.
    """
    if data[:8] != b"bplist00" or len(data) < 40:
        return [], []
    offset_size, ref_size, count, root, table = struct.unpack_from(">6xBBQQQ", data, len(data) - 32)
    if (data[-32:-26] != b"\0" * 6 or offset_size not in (1, 2, 4, 8) or ref_size not in (1, 2, 4, 8)
            or not 0 < count <= (len(data) - 32 - table) // offset_size or root >= count or table < 8
            or count > 1 << (8 * ref_size)):
        return [], []
    offsets = [int.from_bytes(data[table + i * offset_size:table + (i + 1) * offset_size], "big")
               for i in range(count)]
    if len(set(offsets)) != count or any(not 8 <= offset < table for offset in offsets):
        return [], []
    spans, ranges, strings = [], [], {}
    references = [[] for _ in offsets]
    dictionaries = []
    try:
        for index, offset in enumerate(offsets):
            marker = data[offset]
            kind, length = marker >> 4, marker & 15
            start = offset + 1
            if kind in (4, 5, 6, 10, 13) and length == 15:
                if start >= table or data[start] >> 4 != 1 or data[start] & 15 > 3:
                    return [], []
                width = 1 << (data[start] & 15)
                start += 1
                if start + width > table:
                    return [], []
                length = int.from_bytes(data[start:start + width], "big")
                start += width
            if kind == 0 and marker in (0, 8, 9, 15):
                size = 0
            elif kind == 1 and length <= 4:
                size = 1 << length
            elif (kind == 2 and length in (2, 3)) or marker == 0x33:
                size = 1 << length
            elif kind in (4, 5, 6):
                size = length * (2 if kind == 6 else 1)
            elif kind == 8:
                size = length + 1
            elif kind in (10, 13):
                size = length * ref_size * (2 if kind == 13 else 1)
            else:
                return [], []
            end = start + size
            if end > table:
                return [], []
            spans.append((offset, end))
            if kind in (5, 6):
                strings[index] = data[start:end].decode("ascii" if kind == 5 else "utf-16be")
            elif kind in (10, 13):
                refs = [int.from_bytes(data[pos:pos + ref_size], "big") for pos in range(start, end, ref_size)]
                if any(ref >= count for ref in refs):
                    return [], []
                references[index] = refs
                if size:
                    ranges.append((start, end))
                if kind == 13:
                    dictionaries.append(refs[:length])
    except UnicodeDecodeError:
        return [], []
    spans.sort()
    if any(a[1] > b[0] for a, b in zip(spans, spans[1:])):
        return [], []
    for keys in dictionaries:
        if any(key not in strings for key in keys) or len({strings[key] for key in keys}) != len(keys):
            return [], []
    # Iterative traversal bounds work by the object/reference count, not Python's recursion limit.
    states = [0] * count
    for index in range(count):
        stack = [(index, False)]
        while stack:
            index, exiting = stack.pop()
            if exiting:
                states[index] = 2
            elif states[index] == 1:
                return [], []
            elif states[index] == 0:
                states[index] = 1
                stack.append((index, True))
                stack.extend((ref, False) for ref in references[index])
    return sorted(ranges), list(strings.values())


PLIST_XML_HEAD = ('<?xml version="1.0" encoding="UTF-8"?>\n<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" '
                  '"http://www.apple.com/DTDs/PropertyList-1.0.dtd">\n<plist version="1.0">\n')


def plist_xml(data):
    """The text `plutil -convert xml1` prints for a binary plist, read in text mode (a CR line end reads as LF), built
    with plistlib so the scan is the same where plutil doesn't exist (Linux, #7571). CoreFoundation's layout: keys in
    UTF-16 order, reals as %.17g, a UID as a CF$UID dictionary, data in base64 lines of 76 columns less 8 per tab (at
    most 8 tabs). Empty for bytes plistlib can't read and, as plutil prints nothing for them, a cycle, a non-string
    key or a NUL in a string."""
    lines = [PLIST_XML_HEAD]
    try:
        plist_xml_lines(plistlib.loads(data, fmt=plistlib.FMT_BINARY), 0, lines, set())
    except (ValueError, TypeError, RecursionError):  # plistlib.InvalidFileException is a ValueError
        return ""
    return "".join(lines) + "</plist>\n"


def plist_xml_text(text):
    if "\0" in text:
        raise ValueError("a NUL in a string")
    return (text.replace("\r\n", "\n").replace("\r", "\n")
            .replace("&", "&amp;").replace("<", "&lt;").replace(">", "&gt;"))


def plist_xml_lines(value, depth, lines, open_containers):
    pad = "\t" * depth
    if isinstance(value, plistlib.UID):
        value = {"CF$UID": value.data}
    if isinstance(value, (list, dict)):
        tag = "array" if isinstance(value, list) else "dict"
        if not value:
            lines.append("%s<%s/>\n" % (pad, tag))
            return
        if id(value) in open_containers:
            raise ValueError("a cycle")
        open_containers.add(id(value))
        lines.append("%s<%s>\n" % (pad, tag))
        if tag == "array":
            for item in value:
                plist_xml_lines(item, depth + 1, lines, open_containers)
        else:
            if not all(isinstance(key, str) for key in value):
                raise TypeError("a non-string key")
            for key in sorted(value, key=lambda key: key.encode("utf-16-be")):
                lines.append("%s\t<key>%s</key>\n" % (pad, plist_xml_text(key)))
                plist_xml_lines(value[key], depth + 1, lines, open_containers)
        lines.append("%s</%s>\n" % (pad, tag))
        open_containers.discard(id(value))
    elif isinstance(value, str):
        lines.append("%s<string>%s</string>\n" % (pad, plist_xml_text(value)))
    elif isinstance(value, bool):
        lines.append("%s<%s/>\n" % (pad, "true" if value else "false"))
    elif isinstance(value, int):
        lines.append("%s<integer>%d</integer>\n" % (pad, value))
    elif isinstance(value, float):
        real = ("nan" if value != value else "+infinity" if value == float("inf") else
                "-infinity" if value == float("-inf") else "0.0" if value == 0 else "%.17g" % value)
        lines.append("%s<real>%s</real>\n" % (pad, real))
    elif isinstance(value, datetime.datetime):
        lines.append("%s<date>%04d-%02d-%02dT%02d:%02d:%02dZ</date>\n" % (
            pad, value.year, value.month, value.day, value.hour, value.minute, value.second))
    elif isinstance(value, bytes):
        indent = min(depth, 8)
        encoded, width = base64.b64encode(value).decode("ascii"), 76 - 8 * indent
        lines.append("%s<data>\n" % pad)
        lines.extend("%s%s\n" % ("\t" * indent, encoded[i:i + width]) for i in range(0, len(encoded), width))
        lines.append("%s</data>\n" % pad)
    else:
        raise TypeError("an unsupported plist value")


LZFSE_MAGICS = (b"bvx1", b"bvx2", b"bvxn", b"bvx-")
LZFSE_MAGIC_RE = re.compile(rb"bvx[12n-]")
LZFSE_EOS = b"bvx$"
LZFSE_V1_HEADER_SIZE = 772
LZFSE_OUTPUT_LIMIT = 64 << 20
LZFSE_MAX_DEPTH = 4
LZFSE_MAX_STREAMS = 16
LZFSE_MAX_BLOCKS = 4096
LZFSE_TIMEOUT = 30.0
LZFSE_CHUNK = 64 << 10


class LzfseDecodeError(ValueError):
    """Raised when an LZFSE-prefixed payload cannot be decoded safely."""


def _is_lzfse_prefix(data: bytes) -> bool:
    return data[:4] in LZFSE_MAGICS or data[:4] == LZFSE_EOS


def parse_lzfse_stream(data: bytes, offset: int = 0, *, limit: int | None = None) -> tuple[int, int, bool]:
    """Validate Apple LZFSE block headers from offset to the true block-boundary bvx$ marker.

    Returns (end_offset, declared_raw_bytes, all_literal_empty). Raises LzfseDecodeError on any
    truncated/malformed header, excessive block count, or declared output exceeding limit.
    """
    if limit is None:
        limit = LZFSE_OUTPUT_LIMIT
    pos = offset
    length = len(data)
    total_raw = 0
    all_literal_empty = True
    for _ in range(LZFSE_MAX_BLOCKS):
        if pos + 4 > length:
            raise LzfseDecodeError("truncated lzfse block header")
        magic = data[pos:pos + 4]
        if magic == LZFSE_EOS:
            return pos + 4, total_raw, all_literal_empty
        if magic == b"bvx-":
            if pos + 8 > length:
                raise LzfseDecodeError("truncated lzfse literal block")
            (n_raw,) = struct.unpack_from("<I", data, pos + 4)
            nxt = pos + 8 + n_raw
            if nxt > length or total_raw + n_raw > limit:
                raise LzfseDecodeError("lzfse literal block out of bounds or over limit")
            if n_raw > 0:
                all_literal_empty = False
            total_raw += n_raw
            pos = nxt
        elif magic == b"bvxn":
            if pos + 12 > length:
                raise LzfseDecodeError("truncated lzvn block")
            n_raw, n_pay = struct.unpack_from("<II", data, pos + 4)
            nxt = pos + 12 + n_pay
            if nxt > length or total_raw + n_raw > limit:
                raise LzfseDecodeError("lzvn block out of bounds or over limit")
            all_literal_empty = False
            total_raw += n_raw
            pos = nxt
        elif magic == b"bvx2":
            if pos + 32 > length:
                raise LzfseDecodeError("truncated lzfse v2 block")
            n_raw, v0, v1, v2 = struct.unpack_from("<IQQQ", data, pos + 4)
            n_lit_pay = (v0 >> 20) & 0xFFFFF
            n_lmd_pay = (v1 >> 40) & 0xFFFFF
            hdr_size = v2 & 0xFFFFFFFF
            if hdr_size < 32:
                raise LzfseDecodeError("invalid lzfse v2 header size")
            nxt = pos + hdr_size + n_lit_pay + n_lmd_pay
            if nxt > length or total_raw + n_raw > limit:
                raise LzfseDecodeError("lzfse v2 block out of bounds or over limit")
            all_literal_empty = False
            total_raw += n_raw
            pos = nxt
        elif magic == b"bvx1":
            if pos + LZFSE_V1_HEADER_SIZE > length:
                raise LzfseDecodeError("truncated lzfse v1 block")
            n_raw, _n_pay, _n_lit, _n_mat, n_lit_pay, n_lmd_pay = struct.unpack_from("<IIIIII", data, pos + 4)
            nxt = pos + LZFSE_V1_HEADER_SIZE + n_lit_pay + n_lmd_pay
            if nxt > length or total_raw + n_raw > limit:
                raise LzfseDecodeError("lzfse v1 block out of bounds or over limit")
            all_literal_empty = False
            total_raw += n_raw
            pos = nxt
        else:
            raise LzfseDecodeError("unknown lzfse block magic")
    raise LzfseDecodeError("lzfse stream exceeds block limit")


def _run_lzfse_decode(payload: bytes, *, limit: int | None = None, timeout: float = LZFSE_TIMEOUT) -> bytes:
    if limit is None:
        limit = LZFSE_OUTPUT_LIMIT
    try:
        proc = subprocess.Popen(
            ["compression_tool", "-decode", "-a", "lzfse"],
            stdin=subprocess.PIPE,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
        )
    except OSError as error:
        raise LzfseDecodeError("compression_tool failed to start") from error

    timed_out = threading.Event()

    def _kill_on_timeout():
        timed_out.set()
        try:
            proc.kill()
        except OSError:
            pass

    def _write_stdin():
        try:
            if proc.stdin is not None:
                proc.stdin.write(payload)
        except OSError:
            pass
        finally:
            if proc.stdin is not None:
                try:
                    proc.stdin.close()
                except OSError:
                    pass

    stderr_chunks = []

    def _drain_stderr():
        if proc.stderr is None:
            return
        remaining = 4096
        try:
            while remaining > 0:
                chunk = proc.stderr.read(min(remaining, 1024))
                if not chunk:
                    break
                stderr_chunks.append(chunk)
                remaining -= len(chunk)
            while proc.stderr.read(4096):
                pass
        except OSError:
            pass

    timer = threading.Timer(timeout, _kill_on_timeout)
    timer.daemon = True
    writer = threading.Thread(target=_write_stdin, daemon=True)
    err_reader = threading.Thread(target=_drain_stderr, daemon=True)
    timer.start()
    writer.start()
    err_reader.start()
    pieces = []
    size = 0
    try:
        assert proc.stdout is not None
        while True:
            chunk = proc.stdout.read(LZFSE_CHUNK)
            if not chunk:
                break
            size += len(chunk)
            if size > limit:
                try:
                    proc.kill()
                except OSError:
                    pass
                raise LzfseDecodeError("lzfse output exceeds size limit")
            pieces.append(chunk)
        returncode = proc.wait(timeout=timeout)
    except subprocess.TimeoutExpired as error:
        try:
            proc.kill()
            proc.wait()
        except OSError:
            pass
        raise LzfseDecodeError("compression_tool timed out") from error
    finally:
        timer.cancel()
        if proc.stdout is not None:
            try:
                proc.stdout.close()
            except OSError:
                pass
        if proc.poll() is None:
            try:
                proc.kill()
                proc.wait()
            except OSError:
                pass
        writer.join(timeout=1.0)
        err_reader.join(timeout=1.0)
        if proc.stderr is not None:
            try:
                proc.stderr.close()
            except OSError:
                pass
    if timed_out.is_set():
        raise LzfseDecodeError("compression_tool timed out")
    if returncode != 0:
        detail = b"".join(stderr_chunks).decode("utf-8", errors="replace").strip()
        raise LzfseDecodeError(detail or "invalid lzfse stream")
    return b"".join(pieces)


def plist_data_blobs(data: bytes):
    """Extract deduplicated <data> byte objects from a binary plist, including unreferenced objects and plists with sets."""
    if data[:6] != b"bplist":
        return []
    blobs = []
    seen_blobs = set()

    def _add(blob: bytes):
        if blob and blob not in seen_blobs:
            seen_blobs.add(blob)
            blobs.append(blob)

    # Walk the raw bplist00 object table directly so unreferenced <data> objects and plists containing
    # types plistlib rejects (such as set 0xC_) are still extracted without losing deduplication.
    if data[:8] == b"bplist00" and len(data) >= 40:
        offset_size, _ref_size, count, _root, table = struct.unpack_from(">6xBBQQQ", data, len(data) - 32)
        if (data[-32:-26] == b"\0" * 6 and offset_size in (1, 2, 4, 8)
                and 0 < count <= (len(data) - 32 - table) // offset_size and table >= 8):
            seen_offsets = set()
            for i in range(count):
                offset = int.from_bytes(data[table + i * offset_size:table + (i + 1) * offset_size], "big")
                if offset in seen_offsets or not 8 <= offset < table:
                    continue
                seen_offsets.add(offset)
                marker = data[offset]
                if (marker >> 4) != 4:
                    continue
                length = marker & 15
                start = offset + 1
                if length == 15:
                    if start >= table or data[start] >> 4 != 1 or data[start] & 15 > 3:
                        continue
                    width = 1 << (data[start] & 15)
                    start += 1
                    if start + width > table:
                        continue
                    length = int.from_bytes(data[start:start + width], "big")
                    start += width
                end = start + length
                if end <= table:
                    _add(data[start:end])

    try:
        root = plistlib.loads(data, fmt=plistlib.FMT_BINARY)
    except (ValueError, TypeError, RecursionError):
        return blobs
    seen_nodes = set()
    stack = [root]
    while stack:
        node = stack.pop()
        if isinstance(node, bytes):
            _add(node)
        elif isinstance(node, list):
            if id(node) in seen_nodes:
                continue
            seen_nodes.add(id(node))
            stack.extend(node)
        elif isinstance(node, dict):
            if id(node) in seen_nodes:
                continue
            seen_nodes.add(id(node))
            stack.extend(node.values())
    return blobs


def decode_lzfse_streams(
    data: bytes,
    *,
    depth: int = 0,
    budget: list[int] | None = None,
    limit: int | None = None,
    byte_budget: list[int] | None = None,
):
    """Decode LZFSE streams in memory (including trailing, nested, and plist-<data>-embedded streams)."""
    if not _is_lzfse_prefix(data) and data[:6] != b"bplist":
        return
    if depth > LZFSE_MAX_DEPTH:
        raise LzfseDecodeError("lzfse nesting exceeds depth limit")
    if budget is None:
        budget = [LZFSE_MAX_STREAMS]
    if byte_budget is None:
        byte_budget = [LZFSE_OUTPUT_LIMIT if limit is None else limit]

    def _expand_decoded(decoded: bytes):
        if _is_lzfse_prefix(decoded) or decoded[:6] == b"bplist":
            yield from decode_lzfse_streams(decoded, depth=depth + 1, budget=budget, byte_budget=byte_budget)

    if data[:6] == b"bplist":
        for blob in plist_data_blobs(data):
            if _is_lzfse_prefix(blob):
                yield from decode_lzfse_streams(blob, depth=depth + 1, budget=budget, byte_budget=byte_budget)
            elif blob[:6] == b"bplist":
                yield blob
                yield from decode_lzfse_streams(blob, depth=depth + 1, budget=budget, byte_budget=byte_budget)
        return

    def _consume(start: int):
        budget[0] -= 1
        if budget[0] < 0:
            raise LzfseDecodeError("too many lzfse streams in one file")
        remaining = byte_budget[0]
        if remaining < 0:
            raise LzfseDecodeError("lzfse output exceeds cumulative size limit")
        end, _declared, all_literal_empty = parse_lzfse_stream(data, start, limit=remaining)
        decoded = b"" if all_literal_empty else _run_lzfse_decode(data[start:end], limit=remaining)
        byte_budget[0] -= len(decoded)
        if byte_budget[0] < 0:
            raise LzfseDecodeError("lzfse output exceeds cumulative size limit")
        return end, decoded

    cursor, decoded = _consume(0)
    yield decoded
    yield from _expand_decoded(decoded)

    while cursor < len(data):
        if _is_lzfse_prefix(data[cursor:cursor + 4]):
            nxt = cursor
        else:
            match = LZFSE_MAGIC_RE.search(data, cursor)
            if match is None:
                break
            nxt = match.start()
        cursor, decoded = _consume(nxt)
        yield decoded
        yield from _expand_decoded(decoded)


def _bytes_texts(data: bytes, *, private_only=False):
    references, strings = plist_references(data) if not private_only else ([], [])
    binary_plist = data[:6] == b"bplist"
    # Do not truncate at the first slice's signature: later slices and trailing bytes must remain visible.
    cursor = 0
    for start, end in sorted(signature_ranges(data) + references) + [(len(data), len(data))]:
        chunk = data[cursor:start]
        yield from (m.group(0).decode("ascii") for m in PRINTABLE.finditer(chunk))
        if binary_plist:
            # Keep ASCII IP/email text in UTF-16 data, gaps or malformed/unreachable objects visible too.
            yield from (m.group(0).decode("utf-16be") for m in PLIST_UTF16.finditer(chunk))
        cursor = end
    yield from strings
    if binary_plist and not private_only:
        yield plist_xml(data)


def texts(path: str, data=None, *, private_only=False, lzfse_payloads=None):
    if data is None:
        with open(path, "rb") as source:
            data = source.read()
    # Always scan raw bytes too so non-LZFSE bvx-prefixed files and trailing bytes after bvx$ stay visible.
    yield from _bytes_texts(data, private_only=private_only)
    if lzfse_payloads is None:
        if _is_lzfse_prefix(data) or data[:6] == b"bplist":
            for decoded in decode_lzfse_streams(data):
                yield from _bytes_texts(decoded, private_only=private_only)
    else:
        for decoded in lzfse_payloads:
            yield from _bytes_texts(decoded, private_only=private_only)


GZIP_MAGIC = b"\x1f\x8b\x08"
# A member's output bound: far above any package (runtimepkg.MaxSize, 8 MiB) or web asset, and a cap on a bomb.
GZIP_MEMBER_LIMIT = 64 << 20
GZIP_CHUNK = 1 << 20


def gzip_members(data, limit=GZIP_MEMBER_LIMIT):
    """The inflated bytes of each gzip member in data, wherever it starts. A magic that starts no valid stream yields
    nothing; a member cut short, broken off or longer than limit yields what it inflated, so its text is still scanned.
    The search goes on after a complete member, else from the next byte."""
    view = memoryview(data)
    start = data.find(GZIP_MAGIC)
    while start >= 0:
        inflater = zlib.decompressobj(16 + zlib.MAX_WBITS)
        pieces, size, pos, end = [], 0, start, None
        try:
            while pos < len(data) and size < limit and not inflater.eof:
                chunk = view[pos:pos + GZIP_CHUNK]
                pos += len(chunk)
                while chunk and size < limit and not inflater.eof:
                    piece = inflater.decompress(chunk, limit - size)
                    pieces.append(piece)
                    size += len(piece)
                    chunk = inflater.unconsumed_tail
            if inflater.eof:
                end = pos - len(inflater.unused_data)
        except zlib.error:
            pass
        if size:
            yield b"".join(pieces)
        start = data.find(GZIP_MAGIC, end if end is not None else start + 1)


def member_texts(data, *, private_only=False, depth=0, budget=None, byte_budget=None):
    """Printable strings of every gzip member in data, including bounded recursive LZFSE and plist payloads."""
    if depth > LZFSE_MAX_DEPTH:
        raise LzfseDecodeError("gzip/lzfse nesting exceeds depth limit")
    if budget is None:
        budget = [LZFSE_MAX_STREAMS]
    if byte_budget is None:
        byte_budget = [LZFSE_OUTPUT_LIMIT]
    for member in gzip_members(data):
        yield from (m.group(0).decode("ascii") for m in PRINTABLE.finditer(member))
        if _is_lzfse_prefix(member) or member[:6] == b"bplist":
            for decoded in decode_lzfse_streams(member, depth=depth + 1, budget=budget, byte_budget=byte_budget):
                yield from _bytes_texts(decoded, private_only=private_only)
                if GZIP_MAGIC in decoded:
                    yield from member_texts(
                        decoded, private_only=private_only, depth=depth + 1, budget=budget, byte_budget=byte_budget
                    )


def remove_public_relay_url(text, configured_url, path="/relay"):
    """The app intentionally ships this exact public HTTPS endpoint, not its entire host or other URLs. path: the one
    path the endpoint may have (/relay for the push relay, /dl for the hub's update URL in a published personad)."""
    if not isinstance(configured_url, str):
        return text
    url = configured_url.rstrip("/")
    try:
        parsed = urlsplit(url)
        if (parsed.scheme != "https" or not parsed.hostname or "." not in parsed.hostname
                or parsed.username or parsed.password or parsed.query or parsed.fragment
                or parsed.port is not None or parsed.path != path):
            return text
        try:
            ipaddress.ip_address(parsed.hostname)
            return text
        except ValueError:
            pass
    except ValueError:
        return text
    # Do not remove a prefix of another URL, a credential URL, or an extended path/query.
    boundary = r"[A-Za-z0-9._~:/?#@!$&()*+,;=%-]"
    return re.sub(r"(?<!" + boundary + ")" + re.escape(url) + r"(?!" + boundary + ")", "", text)


def verified_public_file(path):
    with open(path, "rb") as source:
        return verified_public_js(path, source.read())


def public_download_url(cfg):
    """The update URL every published personad carries and the one path it may have: an inherited environment override,
    then publish.update_url (a GitHub repository's releases/latest/download, say), else publish.site_url's /dl."""
    override = os.environ.get("PERSONA_UPDATE_URL")
    url = persona_config.update_url(cfg, override)
    if url and (override or persona_config.get(cfg, "publish.update_url", "") not in ("", None)):
        return url, urlsplit(url).path
    return url, "/dl"


def public_download_urls(cfg):
    """Both legitimate public feeds: the update base and the site's own /dl publication feed."""
    urls = [public_download_url(cfg)]
    site = persona_config.get(cfg, "publish.site_url", "")
    if isinstance(site, str) and site.strip():
        urls.append((site.rstrip("/") + "/dl", "/dl"))
    return urls


def public_archive_dependencies(path, whole_short):
    """Only a named public CLI member gets its own verified Go dependency context, never a whole archive."""
    if not whole_short:
        return set()
    name = os.path.basename(path)
    if name in {"personad", "persona-hub", "persona-manager", "persona-node", "persona-link"}:
        return go_dependencies(path)
    match = re.fullmatch(r"(personad|persona-hub|persona-manager|persona-node|persona-link)(?:-windows-(?:amd64|arm64))?[.]exe", name)
    return go_dependencies(path) if match else set()


def archive_members(path):
    """Yield safe, bounded executable members only, keeping archive traversal out of the release scanner."""
    if path.endswith(".zip"):
        with zipfile.ZipFile(path) as archive:
            seen = set()
            for entry in archive.infolist():
                name = entry.filename
                parts = name.split("/")
                if (not name or name.startswith("/") or "\\" in name or any(part in ("", ".", "..") for part in parts)
                        or len(parts) != 2 or name.casefold() in seen):
                    raise ValueError("unsafe path in release ZIP")
                seen.add(name.casefold())
                if entry.is_dir() or entry.file_size > 512 * 1024 * 1024:
                    continue
                program = parts[1]
                if program in {"personad", "persona-hub", "persona-manager", "persona-node", "persona-link"}:
                    yield name, archive.read(entry)
                elif re.fullmatch(r"(?:personad|persona-hub|persona-manager|persona-node|persona-link)(?:-windows-(?:amd64|arm64))?[.]exe", program):
                    yield name, archive.read(entry)
    elif path.endswith(".tar.gz"):
        with tarfile.open(path, "r:gz") as archive:
            seen = set()
            for entry in archive:
                name = entry.name
                parts = name.split("/")
                if (not name or name.startswith("/") or "\\" in name or any(part in ("", ".", "..") for part in parts)
                        or len(parts) != 2 or name.casefold() in seen):
                    raise ValueError("unsafe path in release archive")
                seen.add(name.casefold())
                if not entry.isfile() or entry.size > 512 * 1024 * 1024:
                    continue
                program = parts[1]
                if program in {"personad", "persona-hub", "persona-manager", "persona-node", "persona-link", "persona-iphone-bridge"}:
                    stream = archive.extractfile(entry)
                    if stream is None:
                        continue
                    with stream:
                        yield name, stream.read()


def main():
    args = sys.argv[1:]
    whole_short = "--whole-short" in args
    args = [a for a in args if a != "--whole-short"]
    if len(args) != 1 or not os.path.isdir(args[0]):
        sys.exit(__doc__)
    root = args[0]
    modules = pc.module_paths()
    cfg = persona_config.load()
    inherent = [v for v in (persona_config.get(cfg, "app.bundle_id", ""), persona_config.get(cfg, "app.team_id", "")) if v]
    words, _ = pc.terms()
    words = [w for w in words if w not in inherent]
    # Hermes stores the same pinned public resource as strings in the main executable, not as a .js file.
    # Its longer JavaScript tokens use the resource's existing short-word boundary; every other binary stays strict.
    hermes_public_js = not whole_short and os.path.isfile(os.path.join(root, "Persona")) and any(
        verified_public_file(os.path.join(dirpath, filename))
        for dirpath, _, filenames in os.walk(root) for filename in filenames)
    bad = []
    for dirpath, dirnames, files in os.walk(root):
        dirnames[:] = [d for d in dirnames if d != "_CodeSignature"]
        for f in files:
            if f in SKIP:
                continue
            path = os.path.join(dirpath, f)
            members = [(path, None)]
            archive = whole_short and path.endswith(".zip") or whole_short and path.endswith(".tar.gz") and not path.endswith("-web.tar.gz")
            if archive:
                try:
                    members = [(os.path.join(dirpath, name), data) for name, data in archive_members(path)]
                except (OSError, ValueError, zipfile.BadZipFile, tarfile.TarError) as error:
                    bad.append((os.path.relpath(path, root), "invalid release archive"))
                    continue
            for member_path, member_data in members:
                kinds = set()
                dependencies = go_dependencies(member_path) if whole_short and not archive else set()
                if member_data is not None and archive:
                    # A nested member needs a real executable file for Go's metadata reader, not an archive-name exemption.
                    import tempfile
                    with tempfile.TemporaryDirectory(prefix="persona-release-scan-") as temporary:
                        executable = os.path.join(temporary, os.path.basename(member_path))
                        with open(executable, "wb") as output:
                            output.write(member_data)
                        dependencies = go_dependencies(executable)
                data = member_data
                if data is None:
                    with open(path, "rb") as source:
                        data = source.read()
                public_js = (not whole_short and verified_public_js(member_path, data)) or (
                    hermes_public_js and f == "Persona")
                is_lzfse = _is_lzfse_prefix(data)
                file_budget = [LZFSE_MAX_STREAMS]
                file_byte_budget = [LZFSE_OUTPUT_LIMIT]
                lzfse_payloads = []
                if is_lzfse or data[:6] == b"bplist":
                    try:
                        for decoded in decode_lzfse_streams(data, budget=file_budget, byte_budget=file_byte_budget):
                            lzfse_payloads.append(decoded)
                    except LzfseDecodeError:
                        kinds.add("unreadable lzfse stream")
                has_bplist = data[:6] == b"bplist" or any(p[:6] == b"bplist" for p in lzfse_payloads)
                modes = (False, True) if has_bplist or is_lzfse else (False,)

                def scan_stream(stream, *, generic):
                    for t in stream:
                        original_text = t
                        if not whole_short:
                            t = remove_public_relay_url(t, persona_config.get(cfg, "app.push_relay", ""))
                        else:
                            for public_url, public_path in public_download_urls(cfg):
                                t = remove_public_relay_url(t, public_url, public_path)
                        for v in inherent:
                            t = t.replace(v, "")
                        if modules:
                            t = modules.sub("", t)
                        kinds.update(text_kinds(t, words, whole_short, dependencies, original_text, public_js=public_js,
                                                generic=generic))

                for private_only in modes:
                    scan_stream(
                        texts(member_path, data, private_only=private_only, lzfse_payloads=lzfse_payloads),
                        generic=not private_only,
                    )
                if whole_short:
                    # Scan gzip members in raw data AND in LZFSE-decoded payloads, with bounded gzip<->LZFSE nesting.
                    gzip_queue = [(data, 0)] + [(p, 1) for p in lzfse_payloads if GZIP_MAGIC in p]
                    while gzip_queue:
                        src, src_depth = gzip_queue.pop(0)
                        if src_depth > LZFSE_MAX_DEPTH:
                            kinds.add("unreadable lzfse stream")
                            continue
                        for member in gzip_members(src):
                            scan_stream((m.group(0).decode("ascii") for m in PRINTABLE.finditer(member)), generic=True)
                            if _is_lzfse_prefix(member) or member[:6] == b"bplist":
                                gz_decoded = []
                                try:
                                    for decoded in decode_lzfse_streams(
                                        member, depth=src_depth + 1, budget=file_budget, byte_budget=file_byte_budget
                                    ):
                                        gz_decoded.append(decoded)
                                except LzfseDecodeError:
                                    kinds.add("unreadable lzfse stream")
                                for decoded in gz_decoded:
                                    scan_stream(_bytes_texts(decoded, private_only=False), generic=True)
                                    scan_stream(_bytes_texts(decoded, private_only=True), generic=False)
                                    if GZIP_MAGIC in decoded:
                                        gzip_queue.append((decoded, src_depth + 1))
                bad += [(os.path.relpath(member_path, root), k) for k in sorted(kinds)]
    for f, k in bad:
        print("%s: %s" % (f, k))
    if bad:
        sys.exit("check_bundle_privacy: private values in %s (scripts/release/check_bundle_privacy.py)" % root)
    print("check_bundle_privacy: clean (%d values checked)" % len(words))


if __name__ == "__main__":
    main()
