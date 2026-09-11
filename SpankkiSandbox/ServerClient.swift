import Foundation

private struct ErrorBody: Decodable { let error: String }

struct ServerError: LocalizedError {
    let message: String
    var errorDescription: String? { message }
}

/// Talks to the Go sandbox server on your Mac. The server holds the certificates and does all
/// Open Banking calls; the app only identifies its session with an X-Session-Id header.
final class ServerClient {
    let baseURL: URL
    var sessionId: String?

    private let urlSession: URLSession = {
        let config = URLSessionConfiguration.ephemeral
        config.timeoutIntervalForRequest = 45
        config.httpShouldSetCookies = false
        return URLSession(configuration: config)
    }()

    init(baseURL: URL, sessionId: String?) {
        self.baseURL = baseURL
        self.sessionId = sessionId
    }

    func status() async throws -> ServerStatus {
        try await send("GET", "/api/status")
    }

    /// Makes sure the server knows our session (sessions live in server memory and vanish on restart).
    func ensureSession() async throws {
        if sessionId != nil, try await status().session { return }
        struct Created: Decodable { let sessionId: String }
        let created: Created = try await send("POST", "/api/session")
        sessionId = created.sessionId
    }

    /// Page the web view opens to start the account-access flow (server redirects to S-Pankki).
    var connectURL: URL {
        var components = URLComponents(url: baseURL.appendingPathComponent("connect"), resolvingAgainstBaseURL: false)!
        components.queryItems = [URLQueryItem(name: "sid", value: sessionId)]
        return components.url!
    }

    func accounts() async throws -> [Account] {
        let response: AccountsResponse = try await send("GET", "/api/accounts")
        return response.accounts
    }

    func transactions(accountId: String, nextPage: String?) async throws -> TransactionsPage {
        var path = "/api/accounts/\(accountId.addingPercentEncoding(withAllowedCharacters: .urlPathAllowed) ?? accountId)/transactions"
        if let nextPage, let encoded = nextPage.addingPercentEncoding(withAllowedCharacters: .alphanumerics) {
            path += "?next=\(encoded)"
        }
        return try await send("GET", path)
    }

    func payments() async throws -> [PaymentRecord] {
        try await send("GET", "/api/payments")
    }

    /// Creates a signed payment consent; returns the S-Pankki approval URL.
    func startPayment(_ form: PaymentForm) async throws -> URL {
        struct Redirect: Decodable { let redirect: String }
        let response: Redirect = try await send("POST", "/api/payments", json: try JSONEncoder().encode(form))
        guard let url = URL(string: response.redirect) else { throw ServerError(message: "Bad redirect URL from server") }
        return url
    }

    func refreshPayment(consentId: String) async throws {
        let _: PaymentRecord = try await send("POST", "/api/payments/\(consentId)/refresh")
    }

    /// Hands the intercepted S-Pankki redirect (code in the URL fragment) to the server.
    func completeLogin(callback: URL) async throws {
        let value = callback.absoluteString.addingPercentEncoding(withAllowedCharacters: .alphanumerics) ?? ""
        struct Redirect: Decodable { let redirect: String }
        let _: Redirect = try await send("POST", "/callback", form: "redirect_url=\(value)")
    }

    func disconnect() async throws {
        struct OK: Decodable { let ok: Bool }
        let _: OK = try await send("POST", "/disconnect")
    }

    private func send<T: Decodable>(_ method: String, _ path: String, json: Data? = nil, form: String? = nil) async throws -> T {
        var request = URLRequest(url: URL(string: path, relativeTo: baseURL)!)
        request.httpMethod = method
        if let sessionId { request.setValue(sessionId, forHTTPHeaderField: "X-Session-Id") }
        if let json {
            request.setValue("application/json", forHTTPHeaderField: "Content-Type")
            request.httpBody = json
        } else if let form {
            request.setValue("application/x-www-form-urlencoded", forHTTPHeaderField: "Content-Type")
            request.httpBody = Data(form.utf8)
        }

        let data: Data
        let response: URLResponse
        do {
            (data, response) = try await urlSession.data(for: request)
        } catch {
            throw ServerError(message: "Can't reach the server at \(baseURL.absoluteString). Is it running and is the iPhone on the same Wi-Fi?\n\n\(error.localizedDescription)")
        }
        let status = (response as? HTTPURLResponse)?.statusCode ?? 0
        guard (200..<300).contains(status) else {
            let message = (try? JSONDecoder().decode(ErrorBody.self, from: data).error) ?? String(decoding: data, as: UTF8.self)
            throw ServerError(message: message.isEmpty ? "HTTP \(status)" : message)
        }
        do {
            return try JSONDecoder.openBanking.decode(T.self, from: data)
        } catch {
            throw ServerError(message: "Unexpected response from \(path): \(error)")
        }
    }
}
