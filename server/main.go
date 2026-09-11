// Command server is a sandbox TPP backend for S-Pankki Open Banking (AISP v3.1.6).
// It holds the mTLS and signing certificates, runs the consent + OIDC hybrid flow and exposes
// accounts, balances and transactions as JSON.
package main

import (
	"log/slog"
	"net/http"
	"os"
)

func main() {
	cfg, err := loadConfig()
	if err != nil {
		slog.Error("config", "err", err)
		os.Exit(1)
	}
	b, err := newBank(cfg)
	if err != nil {
		slog.Error("certificates", "err", err)
		os.Exit(1)
	}
	slog.Info("S-Pankki sandbox server",
		"listen", cfg.ListenAddr,
		"redirectUri", cfg.RedirectURI,
		"signingKid", b.kid,
		"signingIssuer", b.issuer,
	)
	if err := http.ListenAndServe(cfg.ListenAddr, newServer(b).routes()); err != nil {
		slog.Error("server", "err", err)
		os.Exit(1)
	}
}
