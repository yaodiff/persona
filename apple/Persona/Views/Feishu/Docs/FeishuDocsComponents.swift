import SwiftUI
#if canImport(UIKit)
import UIKit
#endif
import PersonaWire

// MARK: - API

extension APIClient {
    /// Like `larkRaw`, but a failure keeps the Lark gateway's error code (`{"error":{"code":…}}`), which the reader needs to
    /// tell "export forbidden" (→ read the JSON tree) from "format unsupported" (→ open in Feishu). `APIClient.request`
    /// only keeps flat `{"error":"…"}` messages, so this does the GET itself.
    func feishuDocsGet<T: Decodable>(_ path: String, query: [String: String] = [:], as: T.Type = T.self) async throws -> T {
        let path = "/api/v1/lark/raw/" + path
        var req = URLRequest(url: url(path, query: query), timeoutInterval: 60)
        config.authorize(&req)
        req.setValue(LanguageStore.shared.launched, forHTTPHeaderField: "Accept-Language")
        for (name, value) in routing(path, query) { req.setValue(value, forHTTPHeaderField: name) }
        let (data, resp) = try await session.data(for: req, delegate: HubRedirects.shared)
        try CloudflareAccessError.check(resp, body: data, access: config.access)
        let status = (resp as? HTTPURLResponse)?.statusCode ?? 0
        guard (200..<300).contains(status) else { throw FeishuDocsFailure(status: status, code: FeishuDocsFailure.code(from: data)) }
        return try JSON.decoder.decode(T.self, from: data)
    }
}

extension Error {
    /// The message a Feishu docs screen shows, or nil for a cancelled load (leaving the page is not an error).
    var feishuDocsMessage: String? {
        if isCancellation { return nil }
        if let e = self as? URLError {
            switch e.code {
            case .notConnectedToInternet, .networkConnectionLost: return String(localized: "No network connection")
            case .timedOut: return String(localized: "The request timed out. Try again later.")
            #if os(macOS)
            case .cannotConnectToHost, .cannotFindHost: return String(localized: "Can't reach the hub. Make sure this Mac can reach it.")
            #else
            case .cannotConnectToHost, .cannotFindHost: return String(localized: "Can't reach the hub. Make sure your phone can reach it.")
            #endif
            default: break
            }
        }
        if self is DecodingError { return String(localized: "Lark returned data Persona can't read.") }
        return localizedDescription
    }
}

// MARK: - Pieces


struct FeishuDocsTag: View {
    var text: String
    var hue: String

    var body: some View {
        Text(text)
            .font(Theme.Font.labelMedium)
            .foregroundStyle(LarkColor.hue(hue))
            .lineLimit(1)
            .padding(.horizontal, Theme.Space.xs)
            .padding(.vertical, 1)
            .background(LarkColor.hue(hue).opacity(0.13), in: .rect(cornerRadius: 3))
            .fixedSize()
    }
}


/// The three states every list shares: first load, failure with retry, nothing there.
struct FeishuDocsListState: View {
    var loading: Bool
    var error: String?
    var empty: (symbol: String, title: String, message: String)
    var retry: () async -> Void

    var body: some View {
        Group {
            if loading {
                ProgressView().controlSize(.large).scaledFrame(maxWidth: .infinity, minHeight: Theme.Size.box260)
            } else if let error {
                EmptyStateView(symbol: Theme.Icon.offline, title: String(localized: "Couldn't Load"), message: error, action: (String(localized: "Retry [Components b8784c]"), { Task { await retry() } }))
            } else {
                EmptyStateView(symbol: empty.symbol, title: empty.title, message: empty.message)
            }
        }
        .frame(maxWidth: .infinity)
        .padding(.top, Theme.Space.xl)
    }
}




enum FeishuDocsLinks {
    /// Opens a document in the Feishu app (universal link) or Safari, bypassing the in-app link handler that would
    /// push the native reader again.
    @MainActor static func openInFeishu(_ url: String) {
        guard let u = URL(string: url) else { return }
        UIApplication.shared.open(u)
    }
}
