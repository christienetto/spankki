import SwiftUI
import WebKit

struct AuthSheet: View {
    let request: AppModel.AuthRequest
    let onCallback: (URL) -> Void
    let onCancel: () -> Void

    var body: some View {
        NavigationStack {
            AuthWebView(url: request.url, redirectPrefix: request.redirectUri, onCallback: onCallback)
                .ignoresSafeArea(edges: .bottom)
                .navigationTitle("S-Pankki login")
                .navigationBarTitleDisplayMode(.inline)
                .toolbar {
                    ToolbarItem(placement: .cancellationAction) { Button("Cancel", action: onCancel) }
                }
        }
    }
}

/// Loads the bank's login page and intercepts the navigation to the registered redirect URL,
/// so any https redirect registered in the Crosskey portal works without owning that domain.
struct AuthWebView: UIViewRepresentable {
    let url: URL
    let redirectPrefix: String
    let onCallback: (URL) -> Void

    func makeCoordinator() -> Coordinator {
        Coordinator(redirectPrefix: redirectPrefix, onCallback: onCallback)
    }

    func makeUIView(context: Context) -> WKWebView {
        let configuration = WKWebViewConfiguration()
        configuration.websiteDataStore = .nonPersistent()
        let webView = WKWebView(frame: .zero, configuration: configuration)
        webView.navigationDelegate = context.coordinator
        webView.load(URLRequest(url: url))
        return webView
    }

    func updateUIView(_ webView: WKWebView, context: Context) {}

    @MainActor
    final class Coordinator: NSObject, WKNavigationDelegate {
        private let redirectPrefix: String
        private let onCallback: (URL) -> Void
        private var finished = false

        init(redirectPrefix: String, onCallback: @escaping (URL) -> Void) {
            self.redirectPrefix = redirectPrefix
            self.onCallback = onCallback
        }

        func webView(
            _ webView: WKWebView,
            decidePolicyFor navigationAction: WKNavigationAction,
            decisionHandler: @escaping (WKNavigationActionPolicy) -> Void
        ) {
            guard let url = navigationAction.request.url, url.absoluteString.hasPrefix(redirectPrefix) else {
                decisionHandler(.allow)
                return
            }
            decisionHandler(.cancel)
            if !finished {
                finished = true
                onCallback(url)
            }
        }
    }
}
