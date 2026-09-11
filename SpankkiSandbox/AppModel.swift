import Foundation

@MainActor
final class AppModel: ObservableObject {
    struct AuthRequest: Identifiable {
        enum Purpose { case accounts, payment }
        let id = UUID()
        let url: URL
        let redirectUri: String
        let purpose: Purpose
    }

    static let defaultServer = "http://192.168.101.105:8080"

    @Published private(set) var serverURL: String
    @Published private(set) var status: ServerStatus?
    @Published private(set) var accounts: [Account] = []
    @Published private(set) var payments: [PaymentRecord] = []
    @Published private(set) var busy: String?
    @Published var errorMessage: String?
    @Published var authRequest: AuthRequest?
    @Published var selectedTab = 0

    private var client: ServerClient

    var isConnected: Bool { status?.connected == true }

    init() {
        let saved = UserDefaults.standard.string(forKey: "serverURL") ?? Self.defaultServer
        serverURL = saved
        client = ServerClient(baseURL: URL(string: saved) ?? URL(string: Self.defaultServer)!,
                              sessionId: UserDefaults.standard.string(forKey: "sessionId"))
    }

    func setServer(_ value: String) async {
        let trimmed = value.trimmingCharacters(in: .whitespacesAndNewlines).trimmingCharacters(in: CharacterSet(charactersIn: "/"))
        guard let url = URL(string: trimmed), url.scheme != nil, url.host != nil else {
            errorMessage = "That doesn't look like a URL. Example: \(Self.defaultServer)"
            return
        }
        serverURL = trimmed
        UserDefaults.standard.set(trimmed, forKey: "serverURL")
        client = ServerClient(baseURL: url, sessionId: nil)
        saveSession()
        accounts = []
        payments = []
        await refresh()
    }

    func refresh() async {
        errorMessage = nil
        do {
            status = try await client.status()
            if isConnected {
                busy = "Loading accounts…"
                accounts = try await client.accounts()
            } else {
                accounts = []
            }
            if status?.session == true {
                payments = try await client.payments()
            }
        } catch {
            errorMessage = error.localizedDescription
        }
        busy = nil
    }

    // MARK: Connect (account access)

    func connect() async {
        errorMessage = nil
        do {
            try await client.ensureSession()
            saveSession()
            let status = try await client.status()
            authRequest = AuthRequest(url: client.connectURL, redirectUri: status.redirectUri, purpose: .accounts)
        } catch {
            errorMessage = error.localizedDescription
        }
    }

    func disconnect() async {
        busy = "Revoking consent…"
        var failure: String?
        do {
            try await client.disconnect()
        } catch {
            failure = error.localizedDescription
        }
        await refresh()
        if let failure { errorMessage = failure }
    }

    // MARK: Payments

    func startPayment(_ form: PaymentForm) async -> Bool {
        errorMessage = nil
        busy = "Creating payment consent…"
        defer { busy = nil }
        do {
            try await client.ensureSession()
            saveSession()
            let status = try await client.status()
            let url = try await client.startPayment(form)
            authRequest = AuthRequest(url: url, redirectUri: status.redirectUri, purpose: .payment)
            return true
        } catch {
            errorMessage = error.localizedDescription
            return false
        }
    }

    func refreshPayment(_ payment: PaymentRecord) async {
        do {
            try await client.refreshPayment(consentId: payment.consentId)
            payments = try await client.payments()
        } catch {
            errorMessage = error.localizedDescription
        }
    }

    // MARK: Login callback

    func completeLogin(callback: URL) async {
        guard let request = authRequest else { return }
        authRequest = nil
        busy = request.purpose == .payment ? "Submitting payment…" : "Finishing login…"
        var failure: String?
        do {
            try await client.completeLogin(callback: callback)
        } catch {
            failure = error.localizedDescription
        }
        await refresh()
        if let failure { errorMessage = failure }
        if request.purpose == .payment { selectedTab = 1 }
    }

    func transactions(accountId: String, nextPage: String?) async throws -> TransactionsPage {
        try await client.transactions(accountId: accountId, nextPage: nextPage)
    }

    private func saveSession() {
        UserDefaults.standard.set(client.sessionId, forKey: "sessionId")
    }
}
