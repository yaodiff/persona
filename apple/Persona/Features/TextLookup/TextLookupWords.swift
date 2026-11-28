import Foundation

/// Which words a tap looks up (#10977): a Latin word of 2 to 40 letters, with an apostrophe or a hyphen only between
/// letters ("don't", "well-known"). Pure and nonisolated, so the tests and the views share one rule. Anything that
/// reads as code or an address is no word: a token joined to its neighbours by `_ / \ @ : .` or digits ("snake_case",
/// "example.com", "v2"), or one with a capital inside ("ChatView"). Sentence punctuation around a word is fine.
nonisolated enum TextLookupWords {
    static let minLength = 2
    static let maxLength = 40
    /// The context sent with a lookup: the sentence around the word, at most this many characters.
    static let contextLimit = 240

    /// A word found in a text: the word as written and where it is (UTF-16 offsets, as `NSString` and TextKit count).
    struct Hit: Equatable, Sendable {
        var word: String
        var range: NSRange
    }

    /// The word at UTF-16 offset `offset` of `text`, or nil when the character there is not part of a word to look up.
    static func word(in text: String, at offset: Int) -> Hit? {
        let s = text as NSString
        guard offset >= 0, offset < s.length else { return nil }
        guard isLetter(s.character(at: offset)) || isJoiner(s.character(at: offset)) else { return nil }
        // The token around it: letters, digits and everything that glues words into code or addresses.
        var lo = offset, hi = offset + 1
        while lo > 0, isToken(s.character(at: lo - 1)) { lo -= 1 }
        while hi < s.length, isToken(s.character(at: hi)) { hi += 1 }
        // Trailing and leading punctuation of a sentence is not part of it ("word." "(word" "word,").
        while lo < hi, isEdgePunctuation(s.character(at: lo)) { lo += 1 }
        while hi > lo, isEdgePunctuation(s.character(at: hi - 1)) { hi -= 1 }
        guard lo <= offset, offset < hi else { return nil }
        let range = NSRange(location: lo, length: hi - lo)
        guard let word = plainWord(s.substring(with: range)) else { return nil }
        return Hit(word: word, range: range)
    }

    /// `token` when it is one plain word to look up, nil otherwise.
    static func plainWord(_ token: String) -> String? {
        let units = Array(token.utf16)
        guard units.count >= minLength, units.count <= maxLength else { return nil }
        guard let first = units.first, let last = units.last, isLetter(first), isLetter(last) else { return nil }
        var letters = 0
        var sawLower = false
        for (i, u) in units.enumerated() {
            if isLetter(u) {
                letters += 1
                if isUpper(u), sawLower { return nil } // camelCase: an identifier, not a word
                if isLower(u) { sawLower = true }
            } else if isJoiner(u) {
                // Only between two letters, and never two in a row ("a--b").
                guard i > 0, i < units.count - 1, isLetter(units[i - 1]), isLetter(units[i + 1]) else { return nil }
            } else {
                return nil
            }
        }
        guard letters >= minLength else { return nil }
        // A typographic apostrophe is looked up as the plain one.
        return token.replacingOccurrences(of: "\u{2019}", with: "'")
    }

    /// The sentence around `range` in `text`, trimmed to `contextLimit` characters around the word.
    static func context(in text: String, around range: NSRange) -> String {
        let s = text as NSString
        guard range.location != NSNotFound, NSMaxRange(range) <= s.length else { return "" }
        var lo = range.location, hi = NSMaxRange(range)
        while lo > 0, !isSentenceEnd(s.character(at: lo - 1)) { lo -= 1 }
        while hi < s.length, !isSentenceEnd(s.character(at: hi)) { hi += 1 }
        if hi < s.length { hi += 1 } // keep the full stop
        let half = contextLimit / 2
        if range.location - lo > half { lo = range.location - half }
        if hi - NSMaxRange(range) > half { hi = NSMaxRange(range) + half }
        // Never cut a surrogate pair in half.
        let sub = s.rangeOfComposedCharacterSequences(for: NSRange(location: lo, length: hi - lo))
        return s.substring(with: sub).trimmingCharacters(in: .whitespacesAndNewlines)
            .replacingOccurrences(of: "\n", with: " ")
    }

    /// Every place a saved word (lowercased lemma or form) occurs in `text` as a whole word, for underlining.
    static func occurrences(of saved: Set<String>, in text: String) -> [NSRange] {
        guard !saved.isEmpty else { return [] }
        let s = text as NSString
        var out: [NSRange] = []
        var i = 0
        while i < s.length {
            guard isLetter(s.character(at: i)) else { i += 1; continue }
            var j = i + 1
            while j < s.length, isLetter(s.character(at: j)) || (isJoiner(s.character(at: j)) && j + 1 < s.length && isLetter(s.character(at: j + 1))) { j += 1 }
            let r = NSRange(location: i, length: j - i)
            let joinedBefore = i > 0 && joins(s, i - 1, toward: -1)
            let joinedAfter = j < s.length && joins(s, j, toward: 1)
            if !joinedBefore, !joinedAfter, let w = plainWord(s.substring(with: r)), saved.contains(w.lowercased()) { out.append(r) }
            i = j
        }
        return out
    }

    /// Whether the character at `k`, next to a word, glues it into code or an address: a token character, except
    /// sentence punctuation with no letter or digit past it ("word." ends a sentence; "word.swift" is a file).
    private static func joins(_ s: NSString, _ k: Int, toward step: Int) -> Bool {
        let u = s.character(at: k)
        guard isToken(u) else { return false }
        guard isEdgePunctuation(u) else { return true }
        let next = k + step
        guard next >= 0, next < s.length else { return false }
        let n = s.character(at: next)
        return isLetter(n) || (0x30...0x39).contains(n)
    }

    // MARK: Characters (UTF-16 units: everything this rule accepts is in the Basic Multilingual Plane)

    /// A Latin letter: ASCII, Latin-1 and Latin Extended-A/B (é, ß, ł…), not × or ÷.
    static func isLetter(_ u: unichar) -> Bool {
        switch u {
        case 0x41...0x5A, 0x61...0x7A: true
        case 0xC0...0x24F: u != 0xD7 && u != 0xF7
        default: false
        }
    }

    private static func isUpper(_ u: unichar) -> Bool { (0x41...0x5A).contains(u) || ((0xC0...0xDE).contains(u) && u != 0xD7) }
    private static func isLower(_ u: unichar) -> Bool { (0x61...0x7A).contains(u) || ((0xDF...0xFF).contains(u) && u != 0xF7) }
    /// Inside a word: an apostrophe (straight or typographic) or a hyphen.
    static func isJoiner(_ u: unichar) -> Bool { u == 0x27 || u == 0x2019 || u == 0x2D }
    /// Part of a token: letters, digits, joiners and the characters that make code or an address of words.
    private static func isToken(_ u: unichar) -> Bool {
        isLetter(u) || isJoiner(u) || (0x30...0x39).contains(u) || [0x5F, 0x2F, 0x5C, 0x40, 0x3A, 0x2E, 0x23].contains(u)
    }
    /// What may end a sentence-level token without making it code: a full stop, a colon, an apostrophe or hyphen left over.
    private static func isEdgePunctuation(_ u: unichar) -> Bool { u == 0x2E || u == 0x3A || isJoiner(u) }
    private static func isSentenceEnd(_ u: unichar) -> Bool { u == 0x2E || u == 0x21 || u == 0x3F || u == 0x0A || u == 0x3002 || u == 0xFF01 || u == 0xFF1F }
}
