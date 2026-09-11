import SwiftUI

struct ContentView: View {
    @EnvironmentObject private var model: AppModel

    var body: some View {
        TabView(selection: $model.selectedTab) {
            AccountsTab()
                .tabItem { Label("Accounts", systemImage: "building.columns") }
                .tag(0)
            PayTab()
                .tabItem { Label("Pay", systemImage: "arrow.left.arrow.right") }
                .tag(1)
            ServerTab()
                .tabItem { Label("Server", systemImage: "server.rack") }
                .tag(2)
        }
        .tint(Color(red: 0.04, green: 0.48, blue: 0.29))
        .task { await model.refresh() }
        .sheet(item: $model.authRequest) { request in
            AuthSheet(
                request: request,
                onCallback: { url in Task { await model.completeLogin(callback: url) } },
                onCancel: { model.authRequest = nil }
            )
        }
    }
}

// MARK: - Shared bits

private struct StatusSections: View {
    @EnvironmentObject private var model: AppModel

    var body: some View {
        if let busy = model.busy {
            Section {
                HStack(spacing: 10) {
                    ProgressView()
                    Text(busy).foregroundStyle(.secondary)
                }
            }
        }
        if let error = model.errorMessage {
            Section {
                Text(error)
                    .font(.footnote)
                    .foregroundStyle(.red)
                    .textSelection(.enabled)
            }
        }
    }
}

// MARK: - Accounts

private struct AccountsTab: View {
    @EnvironmentObject private var model: AppModel

    var body: some View {
        NavigationStack {
            List {
                StatusSections()
                if !model.isConnected {
                    Section {
                        VStack(alignment: .leading, spacing: 12) {
                            Text("Connect a sandbox customer")
                                .font(.headline)
                            Text("Log in with a customer's Username and Password from the Crosskey portal → your application → Sandboxes.")
                                .font(.callout)
                                .foregroundStyle(.secondary)
                            Button {
                                Task { await model.connect() }
                            } label: {
                                Label("Connect S-Pankki", systemImage: "link")
                                    .frame(maxWidth: .infinity)
                            }
                            .buttonStyle(.borderedProminent)
                            .controlSize(.large)
                            .disabled(model.busy != nil)
                        }
                        .padding(.vertical, 6)
                    }
                } else {
                    Section("Accounts") {
                        ForEach(model.accounts) { account in
                            NavigationLink(value: account) { AccountRow(account: account) }
                        }
                    }
                }
            }
            .navigationTitle("S-Pankki")
            .navigationDestination(for: Account.self) { TransactionsView(account: $0) }
            .refreshable { await model.refresh() }
            .toolbar {
                if model.isConnected {
                    Menu {
                        Button("Reconnect (new consent)") { Task { await model.connect() } }
                        Button("Disconnect", role: .destructive) { Task { await model.disconnect() } }
                    } label: {
                        Image(systemName: "ellipsis.circle")
                    }
                }
            }
        }
    }
}

private struct AccountRow: View {
    let account: Account

    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            HStack(alignment: .firstTextBaseline) {
                Text(account.displayName).font(.headline)
                Spacer()
                if let balance = account.balance {
                    Text(Format.money(balance))
                        .font(.title3.monospacedDigit().weight(.semibold))
                }
            }
            Text(Format.iban(account.iban))
                .font(.caption.monospaced())
                .foregroundStyle(.secondary)
            Text("\(account.accountType) · \(account.accountSubType)")
                .font(.caption2)
                .foregroundStyle(.secondary)
        }
        .padding(.vertical, 4)
    }
}

private struct TransactionsView: View {
    @EnvironmentObject private var model: AppModel
    let account: Account

    @State private var transactions: [Transaction] = []
    @State private var nextPage: String?
    @State private var loading = false
    @State private var error: String?

    private var totals: (income: Decimal, spent: Decimal) {
        transactions.reduce((Decimal(0), Decimal(0))) { sum, t in
            let value = Decimal(string: t.amount.amount) ?? 0
            return t.isCredit ? (sum.0 + value, sum.1) : (sum.0, sum.1 + value)
        }
    }

    var body: some View {
        List {
            Section {
                if let balance = account.balance {
                    LabeledContent("Balance", value: Format.money(balance))
                }
                LabeledContent("IBAN") {
                    Text(Format.iban(account.iban)).font(.callout.monospaced()).textSelection(.enabled)
                }
                if !transactions.isEmpty {
                    let currency = account.currency
                    LabeledContent("Money in", value: Format.money(Amount(amount: "\(totals.income)", currency: currency)))
                    LabeledContent("Money out", value: Format.money(Amount(amount: "\(totals.spent)", currency: currency), negative: true))
                }
            }

            Section("Transactions") {
                ForEach(Array(transactions.enumerated()), id: \.offset) { entry in
                    TransactionRow(transaction: entry.element)
                }
                if loading {
                    ProgressView().frame(maxWidth: .infinity)
                } else if let nextPage {
                    Button("Load more") { Task { await load(nextPage) } }
                } else if transactions.isEmpty && error == nil {
                    Text("No transactions").foregroundStyle(.secondary)
                }
            }

            if let error {
                Section {
                    Text(error).font(.footnote).foregroundStyle(.red).textSelection(.enabled)
                }
            }
        }
        .navigationTitle(account.displayName)
        .navigationBarTitleDisplayMode(.inline)
        .task { if transactions.isEmpty { await load(nil) } }
        .refreshable {
            transactions = []
            await load(nil)
        }
    }

    private func load(_ page: String?) async {
        loading = true
        defer { loading = false }
        do {
            let result = try await model.transactions(accountId: account.accountId, nextPage: page)
            transactions += result.data.transaction ?? []
            nextPage = result.nextPage
            error = nil
        } catch {
            self.error = error.localizedDescription
        }
    }
}

private struct TransactionRow: View {
    let transaction: Transaction

    var body: some View {
        HStack(alignment: .top) {
            VStack(alignment: .leading, spacing: 3) {
                Text(transaction.title).lineLimit(2)
                Text("\(Format.date(transaction.bookingDateTime)) · \(transaction.status)")
                    .font(.caption)
                    .foregroundStyle(.secondary)
            }
            Spacer()
            Text(Format.money(transaction.amount, negative: !transaction.isCredit))
                .font(.body.monospacedDigit())
                .foregroundStyle(transaction.isCredit ? Color.green : Color.primary)
        }
    }
}

// MARK: - Pay

private struct PayTab: View {
    @EnvironmentObject private var model: AppModel
    @State private var form = PaymentForm()

    var body: some View {
        NavigationStack {
            Form {
                StatusSections()

                Section {
                    Picker("From", selection: $form.debtorIban) {
                        Text("Choose at S-Pankki").tag("")
                        ForEach(model.accounts) { account in
                            Text("\(account.displayName) · \(account.balance.map { Format.money($0) } ?? "")").tag(account.iban)
                        }
                    }
                    if model.accounts.count > 1 {
                        Menu("Send to my own account") {
                            ForEach(model.accounts) { account in
                                Button("\(account.displayName) · \(Format.iban(account.iban))") {
                                    form.creditorName = account.holderName ?? account.displayName
                                    form.creditorIban = account.iban
                                }
                            }
                        }
                    }
                    Button("Use sample recipient (Jane Roe)") {
                        form.creditorName = "Jane Roe"
                        form.creditorIban = "FI4966010005485495"
                    }
                }

                Section("Recipient") {
                    TextField("Name", text: $form.creditorName)
                        .textContentType(.name)
                    TextField("IBAN", text: $form.creditorIban)
                        .textInputAutocapitalization(.characters)
                        .autocorrectionDisabled()
                        .font(.body.monospaced())
                }

                Section("Payment") {
                    HStack {
                        TextField("Amount", text: $form.amount)
                            .keyboardType(.decimalPad)
                        Text(form.currency).foregroundStyle(.secondary)
                    }
                    TextField("Message (optional)", text: $form.reference)
                }

                Section {
                    Button {
                        Task { _ = await model.startPayment(form) }
                    } label: {
                        Text("Continue to S-Pankki approval").frame(maxWidth: .infinity)
                    }
                    .buttonStyle(.borderedProminent)
                    .disabled(model.busy != nil || form.creditorName.isEmpty || form.creditorIban.isEmpty || form.amount.isEmpty)
                } footer: {
                    Text("Needs your application subscribed to the Payment Initiation API (v3.1.7) in the Crosskey portal.")
                }

                if !model.payments.isEmpty {
                    Section("Recent payments") {
                        ForEach(model.payments) { payment in
                            PaymentRow(payment: payment)
                        }
                    }
                }
            }
            .navigationTitle("Pay")
            .refreshable { await model.refresh() }
        }
    }
}

private struct PaymentRow: View {
    @EnvironmentObject private var model: AppModel
    let payment: PaymentRecord

    private var statusColor: Color {
        if payment.error != nil || payment.status.localizedCaseInsensitiveContains("reject") { return .red }
        if payment.status.localizedCaseInsensitiveContains("accept") || payment.status.localizedCaseInsensitiveContains("complete") { return .green }
        return .orange
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 4) {
            HStack {
                Text(payment.creditorName).font(.headline)
                Spacer()
                Text(Format.money(Amount(amount: payment.amount, currency: payment.currency)))
                    .font(.body.monospacedDigit().weight(.semibold))
            }
            HStack {
                Text(payment.status).font(.caption.weight(.semibold)).foregroundStyle(statusColor)
                Spacer()
                if payment.paymentId != nil {
                    Button("Refresh") { Task { await model.refreshPayment(payment) } }
                        .font(.caption)
                        .buttonStyle(.borderless)
                }
            }
            if let error = payment.error {
                Text(error).font(.caption2).foregroundStyle(.red).lineLimit(4)
            }
        }
        .padding(.vertical, 2)
    }
}

// MARK: - Server

private struct ServerTab: View {
    @EnvironmentObject private var model: AppModel
    @State private var url = ""

    var body: some View {
        NavigationStack {
            Form {
                StatusSections()
                Section {
                    TextField(AppModel.defaultServer, text: $url)
                        .keyboardType(.URL)
                        .textInputAutocapitalization(.never)
                        .autocorrectionDisabled()
                    Button("Save and test") { Task { await model.setServer(url) } }
                } header: {
                    Text("Server on your Mac")
                } footer: {
                    Text("Run `go run .` in the server folder on your Mac; the iPhone must be on the same Wi-Fi. If your Mac's IP changes, find it in System Settings → Wi-Fi → Details and update it here.")
                }

                Section("Status") {
                    LabeledContent("Server", value: model.status == nil ? "Not reachable" : "Online")
                    LabeledContent("Bank connection", value: model.isConnected ? "Connected" : "Not connected")
                    if let consent = model.status?.consentId, !consent.isEmpty {
                        LabeledContent("Consent") { Text(consent).font(.caption.monospaced()).textSelection(.enabled) }
                    }
                    if let clientId = model.status?.clientId {
                        LabeledContent("Client ID") { Text(clientId).font(.caption.monospaced()).lineLimit(1) }
                    }
                }
            }
            .navigationTitle("Server")
            .onAppear { url = model.serverURL }
            .refreshable { await model.refresh() }
        }
    }
}
