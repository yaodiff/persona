import Testing
@testable import Persona

struct ReplySpeechTextTests {
    private func clean(_ text: String, full: Bool = false) -> String {
        ReplySpeechText.clean(text, full: full, truncated: "Rest on screen.", omitted: "Middle omitted.")
    }

    @Test func speaksOnlyProseFromMarkdown() {
        let reply = """
        # **Result**
        > - [x] The [guide](https://example.invalid/a_(b)) is *ready*.
        ~~~~swift
        doNotRead()
        ~~~
        stillCode()
        ~~~~
        | Private column | Count |
        | :--- | ---: |
        | Hidden row | 11 |
        Read `npm test`.
        """
        #expect(clean(reply) == "Result\nThe guide is ready.\nRead npm test.")
        #expect(clean("Ready\n```swift\nUnfinished snippet") == "Ready")
        #expect(clean("![private caption](asset.png)\n---\n\n") == "")
        #expect(clean("`" + String(repeating: "x", count: 33) + "`") == "")
    }

    @Test func dropsAddressesAndIdentifiersButKeepsOrdinaryWordsAndPunctuation() {
        let reply = """
        Read [the notes](https://example.invalid/secret) or <https://example.invalid/private>.
        See https://example.invalid/query?a=b。继续。
        a0781ebb8 550e8400-e29b-41d4-a716-446655440000
        1234567 deadbeef 2026/11/06 3/4 and/or TCP/IP
        """
        #expect(clean(reply) == """
        Read the notes or.
        See。继续。
        1234567 deadbeef 2026/11/06 3/4 and/or TCP/IP
        """)
    }

    @Test func speaksOnlyPathBasenamesIncludingInCodeAndLinkLabels() {
        let reply = #"See [`server/internal/voice/spoken.go:42:3`](file:///private/test), /etc/hosts and ~/drafts/notes.md."#
        #expect(clean(reply) == "See spoken.go, hosts and notes.md.")
        #expect(clean("Open `/private/project folder/design notes.md` next.") == "Open design notes.md next.")
        #expect(clean("Open `sample/project/folder/another/folder/deep.swift:12` now.") == "Open deep.swift now.")
        #expect(clean(#"Open "/private/project folder/design notes.md" next."#) == "Open design notes.md next.")
        #expect(clean(#"Open `C:\private\folder\report.pdf`."#) == "Open report.pdf.")
    }

    @Test(arguments: [false, true])
    func removesCredentialsBeforeEitherReadingRange(full: Bool) {
        let reply = """
        Start here.
        Password: "violet orchard"
        {"api_key": "made-up-example"}
        key: "first secret line
        second secret line"

        Authorization: Bearer sample.authorization.value
        OPENAI_API_KEY=example-only-value
        Your verification code is 123 456.
        验证码：１２３４５６
        認証コードは654321です。
        **Access token:** `sample-access-value`
        -----BEGIN PRIVATE KEY-----
        fabricated private material
        -----END PRIVATE KEY-----
        End here.
        """
        let spoken = clean(reply, full: full)
        #expect(spoken.hasPrefix("Start here."))
        #expect(spoken.hasSuffix("End here."))
        for value in ["violet", "orchard", "secret line", "made-up", "sample.", "example-only", "123", "456", "１２３", "654321", "sample-access", "fabricated"] {
            #expect(!spoken.contains(value))
        }
    }

    @Test func dropsStandaloneTokensAndUnclosedPrivateKeys() {
        let token = "ghp_" + String(repeating: "a", count: 36)
        let apiKey = "sk-proj-" + String(repeating: "X", count: 35)
        let jwt = "eyJhbGciOiJub25lIn0.eyJzdWIiOiJ0ZXN0In0.testSignature"
        #expect(clean("Start\n\(token)\n\(apiKey)\n\(jwt)\nEnd") == "Start\nEnd")
        #expect(clean("Ready\n-----BEGIN OPENSSH PRIVATE KEY-----\nexample material") == "Ready")
        #expect(clean(#"{"password":"only a test"}"#).isEmpty)
        #expect(clean("Reset your password in Settings.") == "Reset your password in Settings.")
    }

    @Test func stripsArmoredKeysAndCredentialAssignments() {
        let armor = "PGP PRIVATE KEY BLOCK"
        let samples = [
            "-----BEGIN \(armor)-----\nfabricated material\n-----END \(armor)-----",
            "PGPASSWORD=fabricated", "PGSSLPASSWORD: fabricated", "AWS_ACCESS_KEY_ID=fabricated",
            "Proxy-Authorization: Basic fabricated", "Set-Cookie: id=fabricated",
            "curl --user 'example:fabricated phrase' https://example.invalid",
            "curl -u example:fabricated https://example.invalid"
        ]
        for sample in samples {
            let result = clean("Ready.\n\(sample)\nContinue.")
            #expect(!result.contains("fabricated"))
            #expect(result.hasPrefix("Ready.") && result.hasSuffix("Continue."))
        }
        #expect(clean("Ready.\n-----BEGIN \(armor)-----\nfabricated") == "Ready.")
        let ordinary = "max_tokens=4096, OLDPWD=/tmp and monkey=lemur."
        #expect(clean(ordinary) == "max_tokens=4096, OLDPWD=tmp and monkey=lemur.")
    }

    @Test func stripsSharedCredentialFormatsButKeepsTokenUsageSettings() {
        let payload = String(repeating: "Example0", count: 4)
        for prefix in ["sk_live_", "rk_live_", "pk_live_", "npm_", "xoxa-"] {
            #expect(clean("Before\n" + prefix + payload + "\nAfter") == "Before\nAfter")
        }
        for name in ["signature", "X-Hub-Signature-256", "sessionKeyPrevious", "refresh_token_backup", "db.password.old"] {
            #expect(clean("Before\n\(name)=\(payload)\nAfter") == "Before\nAfter")
        }
        for setting in ["max_tokens=4096", "max_output_tokens=8192", "total_tokens=50", "token_count=22",
                        "token_limit=120", "use_token=true", "use_tokens=false"] {
            #expect(clean(setting) == setting)
        }
        #expect(!clean("token_count=\(payload)").contains(payload))
    }

    @Test func stripsOneTimeCodesInAllThreeLanguagesAndPreservesFollowingProse() {
        let samples = [
            "Your code: 123456. Continue.", "Your code：１２３ ４５６. Continue.",
            "Your security code is １２３-４５６. Continue.",
            "您的验证码为123456。Continue.", "１２３４５６ 是您的验证码。Continue.",
            "確認コード：123456。Continue.", "１２３４５６が認証コードです。Continue."
        ]
        for sample in samples {
            let result = clean(sample)
            let digits = result.filter(\.isNumber)
            #expect(digits.isEmpty)
            #expect(result.hasSuffix("Continue."))
        }
        #expect(clean("Code review took 123456 steps.") == "Code review took 123456 steps.")
    }

    @Test func unquotedPathsWithDirectorySpacesKeepOnlyTheFilename() {
        #expect(clean("Open /private/Project Blue/plan.md next.") == "Open plan.md next.")
        #expect(clean("See ~/Team Notes/Release Two/notes draft.md, then retry.") == "See notes draft.md, then retry.")
        #expect(clean(#"Open C:\Project Blue\plan.md next."#) == "Open plan.md next.")
        #expect(clean("Read /etc/hosts and /etc/services today.") == "Read hosts and services today.")
    }

    @Test func recognizesCodesInProseAndCredentialsAcrossMarkup() {
        let reply = """
        Ready.
        543210 is your verification code.
        Your code is 135790.
        **Password**:
        `a private phrase`
        ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAExampleOnly
        Continue.
        """
        let spoken = clean(reply)
        for secret in ["543210", "135790", "private phrase", "AAAAC3"] {
            #expect(!spoken.contains(secret))
        }
        #expect(spoken.hasPrefix("Ready.") && spoken.hasSuffix("Continue."))
        #expect(clean("Basic instructions for today.") == "Basic instructions for today.")
    }

    @Test func removesDynamicPasswordsAndOtherOneTimeCodeLabels() {
        for label in ["动态密码", "動態密碼", "确认码", "安全码", "激活码", "Passcode"] {
            let input = "Ready.\n" + label + " 00912345\nContinue."
            #expect(clean(input) == "Ready.\nContinue.")
        }
        let remainingDigits = clean("００９１２３４５ 是您的动态密码。").filter(\.isNumber)
        #expect(remainingDigits.isEmpty)
        #expect(clean("Meet at 1530.") == "Meet at 1530.")
    }

    @Test func delimitedRelativePathsWithSpacesSpeakOnlyTheFilename() {
        for path in ["Project Blue/plan.md", "Project Blue/Team Notes/release notes.md",
                     #"Project Blue\Team Notes\release notes.md"#] {
            let name = path.split(whereSeparator: { $0 == "/" || $0 == "\\" }).last!
            #expect(clean("Open `\(path)` next.") == "Open \(name) next.")
            #expect(clean("Open \"\(path)\" next.") == "Open \(name) next.")
        }
        #expect(clean("See `https://example.invalid/private/plan.md` now.") == "See now.")
        #expect(clean("Use `and/or` sparingly.") == "Use and/or sparingly.")
    }

    @Test func keepsOrdinaryEnglishConclusionAndRedactsLabeledKeys() {
        let sentence = "The key is to restart the app."
        #expect(clean(sentence) == sentence)
        #expect(clean("Your key is example-only.\nContinue.") == "Your\nContinue.")
        #expect(!clean("Your API key is to keep this private.").contains("private"))
        #expect(!clean("Your key is to keep this private.").contains("private"))
        #expect(!clean("key: example-only").contains("example-only"))
    }

    @Test func limitsTheOpeningIncludingTheCallersNotice() {
        let character = "👩🏽‍💻"
        let exact = String(repeating: character, count: 300)
        #expect(clean(exact) == exact)
        let result = clean(exact + "Z")
        #expect(result.count == 300)
        #expect(result == String(exact.prefix(284)) + "\nRest on screen.")
        #expect(clean(exact + "Z", full: true) == exact + "Z")
    }

    @Test func fullReadingBudgetsTheOmissionAndKeepsTheConclusion() {
        let exact = "Opening" + String(repeating: "文", count: 2983) + "Conclusion"
        #expect(clean(exact, full: true) == exact)
        let reply = "Opening" + String(repeating: "文", count: 5000) + "Conclusion"
        let result = clean(reply, full: true)
        #expect(result.count == 3000)
        #expect(result == reply.prefix(1988) + "\nMiddle omitted.\n" + reply.suffix(995))
    }

    @Test func cleansBeforeCountingAndBoundsEvenAnOversizedNotice() {
        #expect(clean("Start\n```\n" + String(repeating: "x", count: 5000) + "\n```\nEnd") == "Start\nEnd")
        let long = String(repeating: "a", count: 4000)
        for (full, limit) in [(false, 300), (true, 3000)] {
            let result = ReplySpeechText.clean(long, full: full, truncated: long, omitted: long)
            #expect(result.count <= limit)
            let emptyNotice = ReplySpeechText.clean(long, full: full, truncated: "", omitted: "")
            #expect(emptyNotice.count <= limit)
        }
    }
}
