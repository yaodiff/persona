import Foundation

/// The final reply body, prepared locally before either speech engine sees it (#11010).
nonisolated enum ReplySpeechText {
    static func clean(_ text: String, full: Bool, truncated: String, omitted: String) -> String {
        var markdown = text.replacingOccurrences(of: "\r\n", with: "\n").replacingOccurrences(of: "\r", with: "\n")
        markdown = replacing(privateKey, in: markdown)
        // A Markdown table can consume ordinary prose after its last row. End it at the last pipe, as spoken.go does.
        markdown = replacing(afterPipeRow, in: markdown, with: "$1\n\n")
        guard let parsed = try? AttributedString(markdown: markdown) else { return "" }

        var prose = ""
        var block: Int?
        for run in parsed.runs {
            guard let component = run.presentationIntent?.components.first else { continue }
            switch component.kind {
            case .paragraph, .header: break
            default: continue // Code blocks, table cells, horizontal rules.
            }
            if block != component.identity {
                prose += "\n"
                block = component.identity
            }
            let intent = run.inlinePresentationIntent
            if intent?.contains(.softBreak) == true || intent?.contains(.lineBreak) == true {
                prose += "\n"
            } else if run.imageURL != nil || intent?.contains(.inlineHTML) == true {
                prose += " "
            } else {
                let words = String(parsed[run.range].characters)
                prose += intent?.contains(.code) == true ? inlineCode(words) : words
            }
        }

        // Work on the visible words together: Markdown emphasis and code spans can split a credential across runs.
        for secret in [authorization, commandCredential, verificationCode] {
            prose = replacing(secret, in: prose)
        }
        prose = replacing(labeledSecret, in: prose, with: "$1")
        prose = replacing(credentialToken, in: prose)
        prose = replacing(address, in: prose)
        prose = replacing(identifier, in: prose)
        prose = replacing(fileLine, in: prose, with: "$1")
        prose = replacing(quotedPath, in: prose, with: "$2")
        prose = basenames(in: prose)
        prose = replacing(checkbox, in: prose)
        prose = replacing(horizontalSpace, in: prose, with: " ")
        prose = replacing(beforePunctuation, in: prose)
        let body = prose.split(separator: "\n").map { $0.trimmingCharacters(in: .whitespaces) }
            .filter { line in line.contains { !$0.isWhitespace && !$0.isPunctuation } }.joined(separator: "\n")
        return clip(body, full: full, notice: full ? omitted : truncated)
    }

    private static func inlineCode(_ words: String) -> String {
        let value = replacing(fileLine, in: words, with: "$1")
        // Backticks delimit a whole path even when a directory or filename contains spaces.
        let range = NSRange(value.startIndex..., in: value)
        if let match = inlinePath.firstMatch(in: value, range: range), let name = Range(match.range(at: 1), in: value) {
            return String(value[name])
        }
        if words.count <= 32 { return words }
        if path.firstMatch(in: value, range: range)?.range == range, isFilePath(value) {
            return basenames(in: value)
        }
        return " "
    }

    private static func clip(_ body: String, full: Bool, notice: String) -> String {
        let limit = full ? 3000 : 300
        guard body.count > limit else { return body }
        let separators = (full ? 1 : 0) + (notice.isEmpty ? 0 : 1)
        let room = limit - notice.count - separators
        guard room >= (full ? 2 : 1) else { return String(notice.prefix(limit)) }
        let head = full ? room * 2 / 3 : room
        var parts = [body.prefix(head).trimmingCharacters(in: .whitespacesAndNewlines)]
        if !notice.isEmpty { parts.append(notice) }
        if full { parts.append(body.suffix(room - head).trimmingCharacters(in: .whitespacesAndNewlines)) }
        return parts.joined(separator: "\n")
    }

    private static func basenames(in text: String) -> String {
        let source = text as NSString
        var result = ""
        var end = 0
        for match in path.matches(in: text, range: NSRange(location: 0, length: source.length)) {
            let value = source.substring(with: match.range)
            guard isFilePath(value), let name = value.split(whereSeparator: { $0 == "/" || $0 == "\\" }).last else { continue }
            result += source.substring(with: NSRange(location: end, length: match.range.location - end))
            result += name
            end = NSMaxRange(match.range)
        }
        return result + source.substring(from: end)
    }

    private static func isFilePath(_ value: String) -> Bool {
        if value.hasPrefix("/") || value.hasPrefix("~/") || value.hasPrefix("./") || value.hasPrefix("../") || value.contains("\\") {
            return true
        }
        let parts = value.split(separator: "/")
        guard parts.contains(where: { !$0.allSatisfy { $0.isNumber || $0 == "." } }) else { return false }
        return parts.count >= 3 || parts.last?.contains(".") == true
    }

    private static func replacing(_ regex: NSRegularExpression, in text: String, with value: String = "") -> String {
        regex.stringByReplacingMatches(in: text, range: NSRange(text.startIndex..., in: text), withTemplate: value)
    }

    private static let privateKey = try! NSRegularExpression(pattern:
        #"(?is)-----BEGIN ([A-Z0-9 ]*PRIVATE KEY(?: BLOCK)?)-----.*?(?:-----END \1-----|\z)"#)
    private static let afterPipeRow = try! NSRegularExpression(pattern: #"(?m)^((?=[^\n|]*\|)[^\n]*+)\n(?=[^|\n]+$)"#)

    private static let authorization = try! NSRegularExpression(pattern:
        #"(?i)(?:\bAuthorization\h*:\h*(?:Basic|Bearer)|\bBearer)\h+[A-Za-z0-9._~+/=-]+"#)
    private static let commandCredential = try! NSRegularExpression(pattern:
        #"(?i)(?<!\S)(?:-u|--user)[ =]+(?:"[^"\n]*:[^"\n]*"|'[^'\n]*:[^'\n]*'|[^\s:]+:\S+)"#)

    private static let codeLabel = #"(?:verification|security|login|one[- ]time|confirmation)[ -]?(?:code|password)|passcode|OTP|PIN"#
    // Keep numeric token counts and boolean settings, as the sharing sanitizer does.
    private static let countSetting = #"(?:max_(?:output_|completion_)?tokens|(?:input|output|total)_tokens|token_count|token_limit|tokens)"#
    private static let assignmentValue = #"["']?\h*[:=]\h*["']?"#
    private static let safeSetting = #"(?<![a-z0-9_.-])(?:"# + countSetting + assignmentValue + #"\d+|use_tokens?"# +
        assignmentValue + #"(?:true|false))["']?(?=[\s,;}\])]|$)"#
    // Environment and header names can qualify a secret on either side, e.g. X-Hub-Signature-256.
    private static let assignmentName = #"[a-z0-9_.-]*(?:(?:api|private|access|session)[_-]?key|secret|passw(?:or)?d|"# +
        #"token|credential|authorization|cookie|signature)[a-z0-9_.-]*+(?=["']?\h*[:=：])"# // i18n-ignore: matches credential assignments with full-width punctuation
    // An explicitly labeled value may be a multiword passphrase or start on the next line.
    private static let labeledSecret = try! NSRegularExpression(pattern:
        // The first alternative preserves ordinary settings and the phrase introducing a conclusion.
        #"(?i)("# + safeSetting + #"|\bthe\h+key\h+is\h+to(?=\h+\p{L}))|(?:(?<![a-z0-9_.-])(?:"# + assignmentName +
        #"|(?:[a-z0-9]++[_-])*(?:[a-z0-9]*(?:password|passwd|passphrase)|pwd|"# +
        #"(?:(?:api|private|secret|access)[_ -]?)?key|(?:access[_ -]?|refresh[_ -]?|auth[_ -]?)?token|"# +
        #"access[_ -]?key[_ -]?id|credentials?|authorization|cookie|(?:client[_ -]?)?secret|"# + codeLabel + #")\b)|"# +
        #"密码|密碼|口令|密钥|密鑰|令牌|验证码|驗證碼|認証コード|パスワード|トークン|暗証番号)"# + // i18n-ignore
        #"["']?\h*(?:[:=：]|\bis\b|是|为|為|は)\s*(?:"(?:\\.|[^"\\])*+(?:"|\z)|'(?:\\.|[^'\\])*+(?:'|\z)|[^\n]+)"#) // i18n-ignore
    private static let verificationLabel = #"(?:\b(?:code|"# + codeLabel + #")\b|验证码|驗證碼|校[验驗][码碼]|[动動][态態](?:[码碼]|密[码碼])|[确確][认認][码碼]|安全[码碼]|激活[码碼]|認証コード|確認コード|ワンタイム(?:コード|パスワード))"# // i18n-ignore
    private static let digits = #"\d(?:[ -]?\d){3,}"#
    private static let verificationCode = try! NSRegularExpression(pattern:
        #"(?i)(?:"# + verificationLabel + #"\h*(?:[:=：]|\bis\b|是|为|為|は)?\h*"# + digits + #"|"# + // i18n-ignore
        digits + #"\h*(?:is\h+(?:your|the)\h+|是(?:您|你)?的?|[がは])\h*"# + verificationLabel + #")"#) // i18n-ignore
    private static let credentialToken = try! NSRegularExpression(pattern:
        #"(?:sk-|gh[pousr]_|github_pat_|glpat-|xox[abprs]-|AKIA|ASIA|AIza)[A-Za-z0-9_-]{8,}|"# +
        #"(?:sk|rk|pk)_live_[A-Za-z0-9]{16,}|npm_[A-Za-z0-9]{30,}|"# +
        #"eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+|ssh-(?:ed25519|rsa)\h+[A-Za-z0-9+/=]+"#)

    private static let address = try! NSRegularExpression(pattern:
        #"(?i)(?:(?<![a-z0-9+.\-])[a-z][a-z0-9+.\-]*+://|www\.)[^\s<>"，。！？、；：]*[^\s<>"，。！？、；：.,;:!?\])']"#) // i18n-ignore
    private static let identifier = try! NSRegularExpression(pattern:
        #"(?i)(?<![a-z0-9_])(?:[a-f0-9]{8}(?:-[a-f0-9]{4}){3}-[a-f0-9]{12}|(?=[a-f]*\d)(?=\d*[a-f])[a-f0-9]{7,64})(?![a-z0-9_])"#)
    private static let fileLine = try! NSRegularExpression(pattern: #"(?<![\w@+.\-])([\w@+.\-]+\.[a-zA-Z][a-zA-Z0-9]{0,7}):\d+(?::\d+)?"#)
    // Quotes and code spans give a relative path with spaces an unambiguous start. Require a filename
    // extension on an unrooted path so everyday slash expressions stay readable.
    private static let delimitedPath = #"(?:[A-Za-z]:[\\/]|~/|\.{1,2}/|/)(?:[^"'\n]*[/\\])?|"# +
        #"(?!(?i:www)\.)(?:[\w@+.\- ]++[\\/])++(?=[^/"'\\\n]+\.[\w\-]+(?:["']|\z))"#
    private static let filename = #"([^/"'\\\n]+)"#
    private static let quotedPath = try! NSRegularExpression(pattern: #"(["'])(?:"# + delimitedPath + ")" + filename + #"\1"#)
    private static let inlinePath = try! NSRegularExpression(pattern: #"\A(?:"# + delimitedPath + ")" + filename + #"\z"#)
    // Spaces can belong to a directory. Without a root, require a filename extension before reading across a space:
    // several ordinary slash expressions ("3/4 and/or TCP/IP") on the same line are not one path.
    private static let directories = #"(?:[\w@+.\-]++(?: +[\w@+.\-]++)*+[\\/])*"#
    private static let path = try! NSRegularExpression(pattern:
        #"(?<![\w@+.\-])(?:(?:[A-Za-z]:[\\/]|~/|\.{1,2}/|[/\\]+)"# + directories +
        #"|[\w@+.\-]+[\\/](?:"# + directories + #"(?=[\w@+\-]+\.[a-zA-Z])|(?:[\w@+.\-]+[\\/])*))[\w@+.\-]*[\w@+\-]"#)
    private static let checkbox = try! NSRegularExpression(pattern: #"(?m)^\h*\[[ xX]\]\h+"#)
    private static let horizontalSpace = try! NSRegularExpression(pattern: #"\h+"#)
    private static let beforePunctuation = try! NSRegularExpression(pattern: #" +(?=[.,;:!?，。！？、；：])"#) // i18n-ignore
}
