package main

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
	"time"

	"spankki-sandbox/server/internal/spankki"
	"spankki-sandbox/server/internal/spankkipay"
)

const (
	apiHostname = "s-pankki-api-sandbox.crosskey.io"
	apiHost     = "https://" + apiHostname
	aispURL     = apiHost + "/open-banking/v3.1.6/aisp"
	pispURL     = apiHost + "/open-banking/v3.1.7/pisp"
	tokenURL    = apiHost + "/open-banking/v2.0/oidc/token"
	authURL     = "https://s-pankki-sandbox.crosskey.io/open-banking/v4.0/oidc/auth"

	scopeAccounts = "accounts openid"
	scopePayments = "payments openid"
)

var permissions = []spankki.OBReadConsent1DataPermissions{
	spankki.OBReadConsent1DataPermissionsReadAccountsBasic,
	spankki.OBReadConsent1DataPermissionsReadAccountsDetail,
	spankki.OBReadConsent1DataPermissionsReadBalances,
	spankki.OBReadConsent1DataPermissionsReadTransactionsBasic,
	spankki.OBReadConsent1DataPermissionsReadTransactionsCredits,
	spankki.OBReadConsent1DataPermissionsReadTransactionsDebits,
	spankki.OBReadConsent1DataPermissionsReadTransactionsDetail,
}

// apiError carries S-Pankki's response body so failures are debuggable.
type apiError struct {
	Op     string
	Status int
	Body   string
}

func (e *apiError) Error() string {
	return fmt.Sprintf("%s: HTTP %d: %s", e.Op, e.Status, strings.TrimSpace(e.Body))
}

type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
	Scope        string `json:"scope"`
}

// bank talks to the S-Pankki sandbox. All calls to the API host use mutual TLS.
type bank struct {
	cfg        config
	http       *http.Client
	calls      *callRecorder
	aisp       *spankki.ClientWithResponses
	pisp       *spankkipay.ClientWithResponses
	signingKey *rsa.PrivateKey
	kid        string
	issuer     string
}

func newBank(cfg config) (*bank, error) {
	tlsCert, err := tls.LoadX509KeyPair(cfg.TLSCert, cfg.TLSKey)
	if err != nil {
		return nil, fmt.Errorf("TLS client certificate: %w", err)
	}
	signingCert, err := readCertificate(cfg.SigningCert)
	if err != nil {
		return nil, fmt.Errorf("signing certificate: %w", err)
	}
	signingKey, err := readRSAKey(cfg.SigningKey)
	if err != nil {
		return nil, fmt.Errorf("signing key: %w", err)
	}
	if !signingKey.PublicKey.Equal(signingCert.PublicKey) {
		return nil, errors.New("signing key does not match the signing certificate")
	}

	kid := cfg.SigningKID
	if kid == "" {
		kid = signingCert.SerialNumber.String()
	}
	issuer := cfg.SigningIssuer
	if issuer == "" {
		if issuer, err = subjectDN(signingCert); err != nil {
			return nil, fmt.Errorf("signing certificate subject: %w", err)
		}
	}

	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{Certificates: []tls.Certificate{tlsCert}, MinVersion: tls.VersionTLS12}
	calls := &callRecorder{next: transport}
	httpClient := &http.Client{Transport: calls, Timeout: 30 * time.Second}

	b := &bank{cfg: cfg, http: httpClient, calls: calls, signingKey: signingKey, kid: kid, issuer: issuer}
	if b.aisp, err = spankki.NewClientWithResponses(aispURL,
		spankki.WithHTTPClient(httpClient), spankki.WithRequestEditorFn(b.commonHeaders)); err != nil {
		return nil, err
	}
	if b.pisp, err = spankkipay.NewClientWithResponses(pispURL,
		spankkipay.WithHTTPClient(httpClient), spankkipay.WithRequestEditorFn(b.commonHeaders)); err != nil {
		return nil, err
	}
	return b, nil
}

func (b *bank) commonHeaders(_ context.Context, req *http.Request) error {
	req.Header.Set("x-api-key", b.cfg.APIKey)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("x-fapi-interaction-id", newUUID())
	return nil
}

// --- OIDC token endpoint (client authentication is the mTLS certificate + client_id) ---

func (b *bank) clientCredentials(ctx context.Context, scope string) (*tokenResponse, error) {
	q := url.Values{"client_id": {b.cfg.ClientID}, "grant_type": {"client_credentials"}, "scope": {scope}}
	t, err := b.token(ctx, "client_credentials", tokenURL+"?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	// S-Pankki silently drops scopes for APIs the application isn't subscribed to.
	want := strings.Fields(scope)[0]
	if t.Scope != "" && !slices.Contains(strings.Fields(t.Scope), want) {
		return nil, fmt.Errorf("S-Pankki did not grant the %q scope (granted %q): subscribe your application to the %s API in the Crosskey portal", want, t.Scope, apiNames[want])
	}
	return t, nil
}

var apiNames = map[string]string{"accounts": "Account and Transaction", "payments": "Payment Initiation"}

func (b *bank) exchangeCode(ctx context.Context, code string) (*tokenResponse, error) {
	return b.token(ctx, "authorization_code", tokenURL, url.Values{
		"code":         {code},
		"grant_type":   {"authorization_code"},
		"redirect_uri": {b.cfg.RedirectURI},
		"client_id":    {b.cfg.ClientID},
	})
}

func (b *bank) refresh(ctx context.Context, refreshToken string) (*tokenResponse, error) {
	return b.token(ctx, "refresh_token", tokenURL, url.Values{
		"refresh_token": {refreshToken},
		"grant_type":    {"refresh_token"},
		"client_id":     {b.cfg.ClientID},
	})
}

func (b *bank) token(ctx context.Context, grant, endpoint string, form url.Values) (*tokenResponse, error) {
	body := io.Reader(http.NoBody)
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=UTF-8")
	resp, err := b.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("POST /token (%s): %w", grant, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return decode[tokenResponse]("POST /token ("+grant+")", resp.StatusCode, raw, http.StatusOK)
}

// authorizationURL builds the OIDC hybrid-flow login URL. The PS256-signed request object ties the
// login to a consent through openbanking_intent_id, e.g. urn:s-pankki:account:{ConsentId}.
func (b *bank) authorizationURL(intentID, scope, state, nonce string) (string, error) {
	claims, err := json.Marshal(map[string]any{
		"id_token": map[string]any{
			"openbanking_intent_id": map[string]any{"value": intentID, "essential": true},
		},
	})
	if err != nil {
		return "", err
	}
	now := time.Now().Unix()
	requestObject, err := b.signPS256(map[string]any{
		"iss":                 b.issuer,
		"client_id":           b.cfg.ClientID,
		"redirect_uri":        b.cfg.RedirectURI,
		"scope":               scope,
		"state":               state,
		"nonce":               nonce,
		"claims":              string(claims), // Crosskey expects claims as a JSON string
		"alt_rp_display_name": "",
		"country_hint":        "",
		"nbf":                 now,
		"exp":                 now + 3600,
	})
	if err != nil {
		return "", err
	}
	q := url.Values{
		"request":       {requestObject},
		"scope":         {scope},
		"response_type": {"code id_token"},
		"redirect_uri":  {b.cfg.RedirectURI},
		"state":         {state},
		"nonce":         {nonce},
		"client_id":     {b.cfg.ClientID},
	}
	return authURL + "?" + q.Encode(), nil
}

// --- Account access consent ---

func (b *bank) createConsent(ctx context.Context, appToken string) (*spankki.OBReadConsentResponse1, error) {
	// The spec's OBReadConsent1 omits Risk, but the OBIE standard (and Crosskey's Postman example) sends it.
	body := struct {
		spankki.OBReadConsent1
		Risk struct{} `json:"Risk"`
	}{}
	body.Data.Permissions = permissions
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	params := &spankki.CreateAccountAccessConsentsParams{Authorization: "Bearer " + appToken}
	resp, err := b.aisp.CreateAccountAccessConsentsWithBodyWithResponse(ctx, params, "application/json", bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	return decode[spankki.OBReadConsentResponse1]("POST /account-access-consents", resp.StatusCode(), resp.Body, http.StatusCreated)
}

func (b *bank) getConsent(ctx context.Context, appToken, consentID string) (*spankki.OBReadConsentResponse1, error) {
	params := &spankki.GetAccountAccessConsentsConsentIdParams{Authorization: "Bearer " + appToken}
	resp, err := b.aisp.GetAccountAccessConsentsConsentIdWithResponse(ctx, consentID, params)
	if err != nil {
		return nil, err
	}
	return decode[spankki.OBReadConsentResponse1]("GET /account-access-consents/{id}", resp.StatusCode(), resp.Body, http.StatusOK)
}

func (b *bank) deleteConsent(ctx context.Context, appToken, consentID string) error {
	params := &spankki.DeleteAccountAccessConsentsConsentIdParams{Authorization: "Bearer " + appToken}
	resp, err := b.aisp.DeleteAccountAccessConsentsConsentIdWithResponse(ctx, consentID, params)
	if err != nil {
		return err
	}
	if resp.StatusCode() != http.StatusNoContent {
		return &apiError{Op: "DELETE /account-access-consents/" + consentID, Status: resp.StatusCode(), Body: string(resp.Body)}
	}
	return nil
}

// --- Account information (user access token) ---

func (b *bank) accounts(ctx context.Context, token string) ([]spankki.OBAccount6, error) {
	resp, err := b.aisp.GetAccountsWithResponse(ctx, &spankki.GetAccountsParams{Authorization: "Bearer " + token})
	if err != nil {
		return nil, err
	}
	out, err := decode[spankki.OBReadAccount6]("GET /accounts", resp.StatusCode(), resp.Body, http.StatusOK)
	if err != nil || out.Data.Account == nil {
		return nil, err
	}
	return *out.Data.Account, nil
}

func (b *bank) balances(ctx context.Context, token, accountID string) (*spankki.OBReadBalance1, error) {
	params := &spankki.GetAccountsAccountIdBalancesParams{Authorization: "Bearer " + token}
	resp, err := b.aisp.GetAccountsAccountIdBalancesWithResponse(ctx, accountID, params)
	if err != nil {
		return nil, err
	}
	return decode[spankki.OBReadBalance1]("GET /accounts/{id}/balances", resp.StatusCode(), resp.Body, http.StatusOK)
}

func (b *bank) transactions(ctx context.Context, token, accountID string, from, to *time.Time) (*spankki.OBReadTransaction6, error) {
	params := &spankki.GetAccountsAccountIdTransactionsParams{
		Authorization:       "Bearer " + token,
		FromBookingDateTime: from,
		ToBookingDateTime:   to,
	}
	resp, err := b.aisp.GetAccountsAccountIdTransactionsWithResponse(ctx, accountID, params)
	if err != nil {
		return nil, err
	}
	return decode[spankki.OBReadTransaction6]("GET /accounts/{id}/transactions", resp.StatusCode(), resp.Body, http.StatusOK)
}

// transactionsPage follows a Links.Next URL returned by S-Pankki.
func (b *bank) transactionsPage(ctx context.Context, token, pageURL string) (*spankki.OBReadTransaction6, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, pageURL, nil)
	if err != nil {
		return nil, err
	}
	b.commonHeaders(ctx, req)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := b.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return decode[spankki.OBReadTransaction6]("GET /accounts/{id}/transactions (next page)", resp.StatusCode, raw, http.StatusOK)
}

// decode checks the status and unmarshals the body. The generated parsers only fill typed fields on
// an exact Content-Type match, so decoding here keeps us independent of charset variations.
func decode[T any](op string, status int, body []byte, want int) (*T, error) {
	if status != want {
		return nil, &apiError{Op: op, Status: status, Body: string(body)}
	}
	var v T
	if err := json.Unmarshal(body, &v); err != nil {
		return nil, fmt.Errorf("%s: decode response: %w", op, err)
	}
	return &v, nil
}

// --- Crypto helpers ---

func (b *bank) signPS256(claims map[string]any) (string, error) {
	header, err := json.Marshal(map[string]string{"alg": "PS256", "kid": b.kid})
	if err != nil {
		return "", err
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	input := b64(header) + "." + b64(payload)
	sig, err := b.pss([]byte(input))
	if err != nil {
		return "", err
	}
	return input + "." + b64(sig), nil
}

// detachedJWS signs a request body for the x-jws-signature header (OBIE message signing with
// b64=false, so the raw body is signed and left out of the token: "header..signature").
func (b *bank) detachedJWS(body []byte) (string, error) {
	header, err := json.Marshal(map[string]any{
		"alg":                           "PS256",
		"kid":                           b.kid,
		"b64":                           false,
		"http://openbanking.org.uk/iat": time.Now().Unix() - 1,
		"http://openbanking.org.uk/iss": b.issuer,
		"http://openbanking.org.uk/tan": apiHostname,
		"crit":                          []string{"b64", "http://openbanking.org.uk/iat", "http://openbanking.org.uk/tan", "http://openbanking.org.uk/iss"},
	})
	if err != nil {
		return "", err
	}
	sig, err := b.pss(append([]byte(b64(header)+"."), body...))
	if err != nil {
		return "", err
	}
	return b64(header) + ".." + b64(sig), nil
}

func (b *bank) pss(input []byte) ([]byte, error) {
	digest := sha256.Sum256(input)
	return rsa.SignPSS(rand.Reader, b.signingKey, crypto.SHA256, digest[:], &rsa.PSSOptions{SaltLength: rsa.PSSSaltLengthEqualsHash})
}

func b64(data []byte) string { return base64.RawURLEncoding.EncodeToString(data) }

// subjectDN renders the subject like Crosskey's reference client (jsrsasign onelineToLDAP with the
// first "," replaced by ", "), e.g. "CN=My App, O=Org,C=FI". Override with SPANKKI_SIGNING_ISSUER.
func subjectDN(cert *x509.Certificate) (string, error) {
	var rdn pkix.RDNSequence
	if _, err := asn1.Unmarshal(cert.RawSubject, &rdn); err != nil {
		return "", err
	}
	return strings.Replace(rdn.String(), ",", ", ", 1), nil
}

func readCertificate(path string) (*x509.Certificate, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	for block, rest := pem.Decode(data); block != nil; block, rest = pem.Decode(rest) {
		if block.Type == "CERTIFICATE" {
			return x509.ParseCertificate(block.Bytes)
		}
	}
	return nil, fmt.Errorf("no PEM certificate in %s", path)
}

func readRSAKey(path string) (*rsa.PrivateKey, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, fmt.Errorf("no PEM key in %s", path)
	}
	if key, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return key, nil
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("%s: unsupported key format (need unencrypted PKCS#1 or PKCS#8): %w", path, err)
	}
	key, ok := parsed.(*rsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("%s: PS256 needs an RSA key, got %T", path, parsed)
	}
	return key, nil
}

func newUUID() string {
	b := make([]byte, 16)
	rand.Read(b)
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}
