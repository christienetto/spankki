import Foundation

struct ServerStatus: Decodable {
    let connected: Bool
    let session: Bool
    let clientId: String?
    let redirectUri: String
    let consentId: String?
}

struct Amount: Decodable, Hashable {
    let amount: String
    let currency: String
}

struct Balance: Decodable, Hashable {
    let creditDebitIndicator: String
    let type: String
    let amount: Amount
}

struct AccountsResponse: Decodable {
    let accounts: [Account]
}

struct Account: Decodable, Hashable, Identifiable {
    let accountId: String
    let currency: String
    let accountType: String
    let accountSubType: String
    let nickname: String?
    let description: String?
    let account: [Identification]?
    let balances: [Balance]?

    struct Identification: Decodable, Hashable {
        let schemeName: String
        let identification: String
        let name: String?
    }

    var id: String { accountId }
    var displayName: String { nickname ?? account?.first?.name ?? description ?? accountSubType }
    var holderName: String? { account?.first?.name }
    var iban: String { account?.first { $0.schemeName.contains("IBAN") }?.identification ?? account?.first?.identification ?? "" }
    var balance: Balance? { balances?.first { $0.type == "InterimAvailable" } ?? balances?.first }
}

struct TransactionsPage: Decodable {
    let data: Payload
    let nextPage: String?
    struct Payload: Decodable { let transaction: [Transaction]? }
}

struct Transaction: Decodable, Hashable {
    let transactionId: String?
    let creditDebitIndicator: String
    let status: String
    let bookingDateTime: String
    let transactionInformation: String?
    let transactionReference: String?
    let amount: Amount
    let merchantDetails: Merchant?
    let creditorAccount: CashAccount?
    let debtorAccount: CashAccount?

    struct Merchant: Decodable, Hashable { let merchantName: String? }
    struct CashAccount: Decodable, Hashable {
        let name: String?
        let identification: String?
    }

    var isCredit: Bool { creditDebitIndicator == "Credit" }
    var counterparty: String? { (isCredit ? debtorAccount?.name : creditorAccount?.name) ?? merchantDetails?.merchantName }
    var title: String { transactionInformation ?? counterparty ?? "Transaction" }
}

struct PaymentRecord: Decodable, Hashable, Identifiable {
    let consentId: String
    let paymentId: String?
    let status: String
    let createdAt: String
    let amount: String
    let currency: String
    let creditorName: String
    let creditorIban: String
    let reference: String?
    let fundsAvailable: Bool?
    let error: String?

    var id: String { consentId }
}

struct PaymentForm: Encodable {
    var debtorIban = ""
    var creditorName = ""
    var creditorIban = ""
    var amount = "1.00"
    var currency = "EUR"
    var reference = ""
}

extension JSONDecoder {
    /// Open Banking JSON uses PascalCase keys ("AccountId"); map them to Swift's camelCase.
    static let openBanking: JSONDecoder = {
        let decoder = JSONDecoder()
        decoder.keyDecodingStrategy = .custom { path in
            let key = path.last!.stringValue
            return AnyKey(key.prefix(1).lowercased() + key.dropFirst())
        }
        return decoder
    }()
}

private struct AnyKey: CodingKey {
    let stringValue: String
    let intValue: Int? = nil
    init(_ string: String) { stringValue = string }
    init?(stringValue: String) { self.stringValue = stringValue }
    init?(intValue: Int) { return nil }
}

enum Format {
    static func money(_ amount: Amount, negative: Bool = false) -> String {
        var value = Decimal(string: amount.amount) ?? 0
        if negative { value = -value }
        return value.formatted(.currency(code: amount.currency).locale(Locale(identifier: "fi_FI")))
    }

    static func money(_ balance: Balance) -> String {
        money(balance.amount, negative: balance.creditDebitIndicator == "Debit")
    }

    static func date(_ iso: String) -> String {
        let parser = ISO8601DateFormatter()
        parser.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
        var date = parser.date(from: iso)
        if date == nil {
            parser.formatOptions = [.withInternetDateTime]
            date = parser.date(from: iso)
        }
        return date?.formatted(date: .abbreviated, time: .omitted) ?? String(iso.prefix(10))
    }

    static func iban(_ iban: String) -> String {
        stride(from: 0, to: iban.count, by: 4).map {
            let start = iban.index(iban.startIndex, offsetBy: $0)
            return String(iban[start..<(iban.index(start, offsetBy: 4, limitedBy: iban.endIndex) ?? iban.endIndex)])
        }.joined(separator: " ")
    }
}
