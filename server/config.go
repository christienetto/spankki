package main

import (
	"errors"
	"os"
	"strings"
)

type config struct {
	ListenAddr  string
	ClientID    string
	APIKey      string
	RedirectURI string

	TLSCert string
	TLSKey  string

	SigningCert string
	SigningKey  string
	// Optional overrides; by default derived from the signing certificate.
	SigningKID    string
	SigningIssuer string
}

func loadConfig() (config, error) {
	loadDotEnv(".env")
	c := config{
		ListenAddr:    env("LISTEN_ADDR", ":8080"),
		ClientID:      os.Getenv("SPANKKI_CLIENT_ID"),
		APIKey:        os.Getenv("SPANKKI_API_KEY"),
		RedirectURI:   env("SPANKKI_REDIRECT_URI", "http://localhost:8080/callback"),
		TLSCert:       env("SPANKKI_TLS_CERT", "certs/tls.crt"),
		TLSKey:        env("SPANKKI_TLS_KEY", "certs/tls.key"),
		SigningCert:   env("SPANKKI_SIGNING_CERT", "certs/signing.crt"),
		SigningKey:    env("SPANKKI_SIGNING_KEY", "certs/signing.key"),
		SigningKID:    os.Getenv("SPANKKI_SIGNING_KID"),
		SigningIssuer: os.Getenv("SPANKKI_SIGNING_ISSUER"),
	}
	var missing []string
	if c.ClientID == "" {
		missing = append(missing, "SPANKKI_CLIENT_ID")
	}
	if c.APIKey == "" {
		missing = append(missing, "SPANKKI_API_KEY")
	}
	if len(missing) > 0 {
		return c, errors.New("missing " + strings.Join(missing, ", ") + " (copy .env.example to .env and fill it in)")
	}
	return c, nil
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// loadDotEnv sets KEY=VALUE pairs from path without overriding variables already in the environment.
func loadDotEnv(path string) {
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		if _, set := os.LookupEnv(key); !set {
			os.Setenv(key, strings.Trim(strings.TrimSpace(value), `"'`))
		}
	}
}
