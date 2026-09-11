package main

import (
	"context"
	"crypto/rand"
	"embed"
	"encoding/json"
	"errors"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"spankki-sandbox/server/internal/spankki"
)

//go:embed web
var webFiles embed.FS

var errNotConnected = errors.New("not connected: click Connect first")

type pendingAuth struct {
	kind      string // "accounts" or "payment"
	state     string
	consentID string
	payment   *paymentRecord
}

type paymentRecord struct {
	ConsentID      string    `json:"consentId"`
	PaymentID      string    `json:"paymentId,omitempty"`
	Status         string    `json:"status"`
	CreatedAt      time.Time `json:"createdAt"`
	Amount         string    `json:"amount"`
	Currency       string    `json:"currency"`
	CreditorName   string    `json:"creditorName"`
	CreditorIBAN   string    `json:"creditorIban"`
	DebtorIBAN     string    `json:"debtorIban,omitempty"`
	Reference      string    `json:"reference,omitempty"`
	FundsAvailable *bool     `json:"fundsAvailable,omitempty"`
	Error          string    `json:"error,omitempty"`
}

type session struct {
	mu        sync.Mutex
	pending   *pendingAuth
	consentID string
	access    string
	refresh   string
	expiresAt time.Time
	payments  []*paymentRecord
}

// setTokens must be called with s.mu held.
func (s *session) setTokens(t *tokenResponse) {
	s.access = t.AccessToken
	if t.RefreshToken != "" {
		s.refresh = t.RefreshToken
	}
	expiresIn := t.ExpiresIn
	if expiresIn == 0 {
		expiresIn = 180
	}
	s.expiresAt = time.Now().Add(time.Duration(expiresIn) * time.Second)
}

type server struct {
	bank     *bank
	mu       sync.Mutex
	sessions map[string]*session
}

func newServer(b *bank) *server {
	return &server{bank: b, sessions: map[string]*session{}}
}

func (s *server) routes() http.Handler {
	web, _ := fs.Sub(webFiles, "web")
	mux := http.NewServeMux()
	mux.Handle("GET /", http.FileServerFS(web))
	mux.HandleFunc("GET /connect", s.connect)
	mux.HandleFunc("GET /callback", s.callbackPage)
	mux.HandleFunc("POST /callback", s.callback)
	mux.HandleFunc("POST /disconnect", s.disconnect)
	mux.HandleFunc("POST /api/session", s.createSession)
	mux.HandleFunc("GET /api/status", s.status)
	mux.HandleFunc("GET /api/consent", s.consent)
	mux.HandleFunc("POST /api/token/refresh", s.forceRefresh)
	mux.HandleFunc("GET /api/accounts", s.accounts)
	mux.HandleFunc("GET /api/accounts/{id}/transactions", s.transactions)
	mux.HandleFunc("GET /api/payments", s.listPayments)
	mux.HandleFunc("POST /api/payments", s.startPayment)
	mux.HandleFunc("POST /api/payments/{consentId}/refresh", s.refreshPayment)
	mux.HandleFunc("GET /api/log", s.callLog)
	mux.HandleFunc("POST /api/log/clear", s.clearLog)
	return mux
}

// session finds the caller's session: browsers use the "sid" cookie, the iPhone app sends an
// X-Session-Id header (or ?sid= when it opens /connect in a web view).
func (s *server) session(w http.ResponseWriter, r *http.Request, create bool) *session {
	ids := []string{r.Header.Get("X-Session-Id"), r.URL.Query().Get("sid")}
	if c, err := r.Cookie("sid"); err == nil {
		ids = append(ids, c.Value)
	}
	s.mu.Lock()
	for _, id := range ids {
		if sess := s.sessions[id]; id != "" && sess != nil {
			s.mu.Unlock()
			return sess
		}
	}
	s.mu.Unlock()
	if !create {
		return nil
	}
	_, sess := s.newSession(w)
	return sess
}

func (s *server) newSession(w http.ResponseWriter) (string, *session) {
	id := rand.Text()
	sess := &session{}
	s.mu.Lock()
	s.sessions[id] = sess
	s.mu.Unlock()
	http.SetCookie(w, &http.Cookie{Name: "sid", Value: id, Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode})
	return id, sess
}

func (s *server) createSession(w http.ResponseWriter, r *http.Request) {
	id, _ := s.newSession(w)
	writeJSON(w, http.StatusOK, map[string]string{"sessionId": id})
}

// --- Account access flow ---

// Steps 1–3: app token → account-access consent → redirect the user to S-Pankki's login.
func (s *server) connect(w http.ResponseWriter, r *http.Request) {
	sess := s.session(w, r, true)
	ctx := r.Context()

	appToken, err := s.bank.clientCredentials(ctx, scopeAccounts)
	if err != nil {
		s.failPage(w, err)
		return
	}
	slog.Info("1. POST /token grant_type=client_credentials → app token")

	consent, err := s.bank.createConsent(ctx, appToken.AccessToken)
	if err != nil {
		s.failPage(w, err)
		return
	}
	consentID := consent.Data.ConsentId
	slog.Info("2. POST /account-access-consents", "consentId", consentID, "status", consent.Data.Status)

	state := rand.Text()
	loginURL, err := s.bank.authorizationURL("urn:s-pankki:account:"+consentID, scopeAccounts, state, rand.Text())
	if err != nil {
		s.failPage(w, err)
		return
	}
	sess.mu.Lock()
	sess.pending = &pendingAuth{kind: "accounts", state: state, consentID: consentID}
	sess.mu.Unlock()

	slog.Info("3. redirecting user to S-Pankki login", "openbanking_intent_id", "urn:s-pankki:account:"+consentID)
	http.Redirect(w, r, loginURL, http.StatusFound)
}

// The hybrid flow returns code/id_token/state in the URL fragment, which never reaches a server,
// so this page forwards it. If the redirect went elsewhere, the user can paste that URL instead.
var callbackHTML = template.Must(template.New("callback").Parse(`<!doctype html>
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>S-Pankki login</title>
<style>body{font:16px system-ui;max-width:640px;margin:40px auto;padding:0 16px}textarea{width:100%}</style>
<p id="msg">Finishing login…</p>
<form id="paste" hidden>
  <p>Paste the full URL S-Pankki redirected you to (including everything after <code>#</code>):</p>
  <textarea name="redirect_url" rows="6"></textarea>
  <p><button>Continue</button></p>
</form>
<script>
const msg = document.getElementById("msg"), form = document.getElementById("paste");
function finish(body) {
  msg.hidden = false; form.hidden = true; msg.textContent = "Finishing login…";
  fetch("/callback", {method: "POST", headers: {"Content-Type": "application/x-www-form-urlencoded"}, body})
    .then(async r => { const t = await r.text(); if (r.ok) location.replace(JSON.parse(t).redirect); else msg.textContent = t; });
}
form.onsubmit = e => { e.preventDefault(); finish("redirect_url=" + encodeURIComponent(form.redirect_url.value)); };
const params = location.hash.slice(1) || location.search.slice(1);
if (params) finish(params); else { msg.hidden = true; form.hidden = false; }
</script>`))

func (s *server) callbackPage(w http.ResponseWriter, r *http.Request) {
	callbackHTML.Execute(w, nil)
}

// Steps 4–5: validate state, exchange the code, then finish whichever flow was pending.
func (s *server) callback(w http.ResponseWriter, r *http.Request) {
	sess := s.session(w, r, false)
	if sess == nil {
		http.Error(w, "no session: start again from the dashboard", http.StatusBadRequest)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	params := r.PostForm
	if pasted := params.Get("redirect_url"); pasted != "" {
		params = paramsFromURL(pasted)
	}

	sess.mu.Lock()
	pending := sess.pending
	sess.pending = nil
	sess.mu.Unlock()

	if e := params.Get("error"); e != "" {
		if pending != nil && pending.payment != nil {
			s.recordPayment(sess, pending.payment, "Rejected", "login failed: "+e+" "+params.Get("error_description"))
			writeJSON(w, http.StatusOK, map[string]string{"redirect": "/#payments"})
			return
		}
		http.Error(w, "login failed: "+e+" "+params.Get("error_description"), http.StatusBadRequest)
		return
	}
	if pending == nil || params.Get("state") != pending.state {
		http.Error(w, "state mismatch: start again from the dashboard", http.StatusBadRequest)
		return
	}
	code := params.Get("code")
	if code == "" {
		http.Error(w, "no authorization code in redirect", http.StatusBadRequest)
		return
	}
	slog.Info("4. authorization code received (must be exchanged within 30s)", "flow", pending.kind)

	token, err := s.bank.exchangeCode(r.Context(), code)
	if err != nil {
		if pending.payment != nil {
			s.recordPayment(sess, pending.payment, "Failed", err.Error())
			writeJSON(w, http.StatusOK, map[string]string{"redirect": "/#payments"})
			return
		}
		s.failPage(w, err)
		return
	}
	slog.Info("5. POST /token grant_type=authorization_code → user token", "flow", pending.kind, "expiresIn", token.ExpiresIn)

	if pending.kind == "payment" {
		s.completePayment(r.Context(), sess, pending.payment, token.AccessToken)
		writeJSON(w, http.StatusOK, map[string]string{"redirect": "/#payments"})
		return
	}
	sess.mu.Lock()
	sess.setTokens(token)
	sess.consentID = pending.consentID
	sess.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]string{"redirect": "/"})
}

func (s *server) disconnect(w http.ResponseWriter, r *http.Request) {
	if sess := s.session(w, r, false); sess != nil {
		sess.mu.Lock()
		consentID := sess.consentID
		sess.access, sess.refresh, sess.consentID = "", "", ""
		sess.mu.Unlock()
		if consentID != "" {
			if err := s.revoke(r.Context(), consentID); err != nil {
				slog.Warn("revoking consent failed", "err", err)
			} else {
				slog.Info("DELETE /account-access-consents → revoked", "consentId", consentID)
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *server) revoke(ctx context.Context, consentID string) error {
	appToken, err := s.bank.clientCredentials(ctx, scopeAccounts)
	if err != nil {
		return err
	}
	return s.bank.deleteConsent(ctx, appToken.AccessToken, consentID)
}

// --- Dashboard JSON API ---

func (s *server) status(w http.ResponseWriter, r *http.Request) {
	out := map[string]any{"connected": false, "session": false, "clientId": s.bank.cfg.ClientID, "redirectUri": s.bank.cfg.RedirectURI}
	if sess := s.session(w, r, false); sess != nil {
		out["session"] = true
		sess.mu.Lock()
		if sess.access != "" {
			out["connected"] = true
			out["consentId"] = sess.consentID
			out["tokenExpiresAt"] = sess.expiresAt
			out["canRefresh"] = sess.refresh != ""
		}
		sess.mu.Unlock()
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *server) consent(w http.ResponseWriter, r *http.Request) {
	sess := s.session(w, r, false)
	consentID := ""
	if sess != nil {
		sess.mu.Lock()
		consentID = sess.consentID
		sess.mu.Unlock()
	}
	if consentID == "" {
		writeError(w, errNotConnected)
		return
	}
	appToken, err := s.bank.clientCredentials(r.Context(), scopeAccounts)
	if err != nil {
		writeError(w, err)
		return
	}
	consent, err := s.bank.getConsent(r.Context(), appToken.AccessToken, consentID)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, consent)
}

func (s *server) forceRefresh(w http.ResponseWriter, r *http.Request) {
	sess := s.session(w, r, false)
	if sess == nil {
		writeError(w, errNotConnected)
		return
	}
	sess.mu.Lock()
	sess.expiresAt = time.Time{}
	sess.mu.Unlock()
	if _, err := s.userToken(r.Context(), sess); err != nil {
		writeError(w, err)
		return
	}
	s.status(w, r)
}

type accountWithBalances struct {
	spankki.OBAccount6
	Balances any `json:"Balances"`
}

func (s *server) accounts(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	token, err := s.userToken(ctx, s.session(w, r, false))
	if err != nil {
		writeError(w, err)
		return
	}
	list, err := s.bank.accounts(ctx, token)
	if err != nil {
		writeError(w, err)
		return
	}
	out := make([]accountWithBalances, 0, len(list))
	for _, account := range list {
		balances, err := s.bank.balances(ctx, token, account.AccountId)
		if err != nil {
			writeError(w, err)
			return
		}
		out = append(out, accountWithBalances{OBAccount6: account, Balances: balances.Data.Balance})
	}
	slog.Info("6. GET /accounts + /accounts/{id}/balances", "accounts", len(out))
	writeJSON(w, http.StatusOK, map[string]any{"Accounts": out})
}

type transactionsPage struct {
	spankki.OBReadTransaction6
	NextPage string `json:"NextPage,omitempty"`
}

func (s *server) transactions(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	token, err := s.userToken(ctx, s.session(w, r, false))
	if err != nil {
		writeError(w, err)
		return
	}
	accountID := r.PathValue("id")
	q := r.URL.Query()

	var page *spankki.OBReadTransaction6
	if next := q.Get("next"); next != "" {
		u, err := url.Parse(next)
		if err != nil || u.Scheme != "https" || u.Host != apiHostname || !strings.Contains(u.Path, "/accounts/"+accountID+"/transactions") {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid next page URL"})
			return
		}
		page, err = s.bank.transactionsPage(ctx, token, next)
		if err != nil {
			writeError(w, err)
			return
		}
	} else {
		from, err := parseDay(q.Get("from"), false)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "from: " + err.Error()})
			return
		}
		to, err := parseDay(q.Get("to"), true)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "to: " + err.Error()})
			return
		}
		if page, err = s.bank.transactions(ctx, token, accountID, from, to); err != nil {
			writeError(w, err)
			return
		}
	}

	out := transactionsPage{OBReadTransaction6: *page}
	if page.Links != nil && page.Links.Next != nil {
		out.NextPage = *page.Links.Next
	}
	count := 0
	if page.Data.Transaction != nil {
		count = len(*page.Data.Transaction)
	}
	slog.Info("7. GET /accounts/{id}/transactions", "count", count, "hasNext", out.NextPage != "")
	writeJSON(w, http.StatusOK, out)
}

func parseDay(value string, endOfDay bool) (*time.Time, error) {
	if value == "" {
		return nil, nil
	}
	day, err := time.Parse(time.DateOnly, value)
	if err != nil {
		return nil, err
	}
	if endOfDay {
		day = day.Add(24*time.Hour - time.Second)
		// S-Pankki rejects intervals reaching into the future with 403.
		if now := time.Now().UTC(); day.After(now) {
			day = now
		}
	}
	return &day, nil
}

// userToken returns a valid account-access token, refreshing it when it is about to expire.
// Tokens from the code exchange live 3 minutes; refreshed ones only see the last 90 days of transactions.
func (s *server) userToken(ctx context.Context, sess *session) (string, error) {
	if sess == nil {
		return "", errNotConnected
	}
	sess.mu.Lock()
	defer sess.mu.Unlock()
	if sess.access == "" {
		return "", errNotConnected
	}
	if time.Until(sess.expiresAt) < 15*time.Second && sess.refresh != "" {
		token, err := s.bank.refresh(ctx, sess.refresh)
		if err != nil {
			return "", err
		}
		sess.setTokens(token)
		slog.Info("POST /token grant_type=refresh_token → new user token")
	}
	return sess.access, nil
}

// --- Payments ---

var (
	amountPattern = regexp.MustCompile(`^\d{1,13}(\.\d{1,2})?$`)
	ibanPattern   = regexp.MustCompile(`^[A-Z]{2}\d{2}[A-Z0-9]{8,30}$`)
)

// startPayment creates a signed payment consent and returns the S-Pankki login URL that approves it.
func (s *server) startPayment(w http.ResponseWriter, r *http.Request) {
	sess := s.session(w, r, true)
	var p paymentRequest
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
		return
	}
	p.CreditorIBAN = strings.ToUpper(strings.ReplaceAll(p.CreditorIBAN, " ", ""))
	p.DebtorIBAN = strings.ToUpper(strings.ReplaceAll(p.DebtorIBAN, " ", ""))
	p.Amount = strings.ReplaceAll(strings.TrimSpace(p.Amount), ",", ".")
	p.CreditorName = strings.TrimSpace(p.CreditorName)
	p.Reference = strings.TrimSpace(p.Reference)
	if p.Currency == "" {
		p.Currency = "EUR"
	}
	switch {
	case p.CreditorName == "" || len(p.CreditorName) > 70:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "recipient name is required (max 70 characters)"})
		return
	case !ibanPattern.MatchString(p.CreditorIBAN):
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "recipient IBAN looks invalid"})
		return
	case p.DebtorIBAN != "" && !ibanPattern.MatchString(p.DebtorIBAN):
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "from-account IBAN looks invalid"})
		return
	case !amountPattern.MatchString(p.Amount):
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "amount must look like 12.50"})
		return
	case len(p.Reference) > 140:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "message is too long (max 140 characters)"})
		return
	}

	ctx := r.Context()
	appToken, err := s.bank.clientCredentials(ctx, scopePayments)
	if err != nil {
		writeError(w, err)
		return
	}
	slog.Info("P1. POST /token grant_type=client_credentials scope=payments → app token")

	consent, err := s.bank.createPaymentConsent(ctx, appToken.AccessToken, p)
	if err != nil {
		writeError(w, err)
		return
	}
	consentID := consent.Data.ConsentId
	slog.Info("P2. POST /international-payment-consents (signed)", "consentId", consentID, "status", consent.Data.Status)

	state := rand.Text()
	loginURL, err := s.bank.authorizationURL("urn:s-pankki:payment:"+consentID, scopePayments, state, rand.Text())
	if err != nil {
		writeError(w, err)
		return
	}
	record := &paymentRecord{
		ConsentID: consentID, Status: string(consent.Data.Status), CreatedAt: time.Now(),
		Amount: p.Amount, Currency: p.Currency, CreditorName: p.CreditorName,
		CreditorIBAN: p.CreditorIBAN, DebtorIBAN: p.DebtorIBAN, Reference: p.Reference,
	}
	sess.mu.Lock()
	sess.pending = &pendingAuth{kind: "payment", state: state, consentID: consentID, payment: record}
	sess.mu.Unlock()
	slog.Info("P3. redirecting user to S-Pankki to approve the payment", "openbanking_intent_id", "urn:s-pankki:payment:"+consentID)
	writeJSON(w, http.StatusOK, map[string]string{"redirect": loginURL})
}

// completePayment runs after the user approved: read the consent, check funds, submit the payment.
func (s *server) completePayment(ctx context.Context, sess *session, rec *paymentRecord, userToken string) {
	appToken, err := s.bank.clientCredentials(ctx, scopePayments)
	if err != nil {
		s.recordPayment(sess, rec, "Failed", err.Error())
		return
	}
	consent, err := s.bank.getPaymentConsent(ctx, appToken.AccessToken, rec.ConsentID)
	if err != nil {
		s.recordPayment(sess, rec, "Failed", err.Error())
		return
	}
	slog.Info("P6. GET /international-payment-consents/{id}", "status", consent.Data.Status)

	if funds, err := s.bank.fundsConfirmation(ctx, userToken, rec.ConsentID); err != nil {
		slog.Warn("funds confirmation failed (continuing)", "err", err)
	} else if funds.Data.FundsAvailableResult != nil {
		available := funds.Data.FundsAvailableResult.FundsAvailable
		rec.FundsAvailable = &available
		slog.Info("P7. GET /international-payment-consents/{id}/funds-confirmation", "fundsAvailable", available)
	}

	payment, err := s.bank.submitPayment(ctx, userToken, consent)
	if err != nil {
		s.recordPayment(sess, rec, "Failed", err.Error())
		return
	}
	rec.PaymentID = payment.Data.InternationalPaymentId
	slog.Info("P8. POST /international-payments (signed)", "paymentId", rec.PaymentID, "status", payment.Data.Status)
	s.recordPayment(sess, rec, string(payment.Data.Status), "")
}

func (s *server) recordPayment(sess *session, rec *paymentRecord, status, errMsg string) {
	rec.Status, rec.Error = status, errMsg
	if errMsg != "" {
		slog.Error("payment failed", "consentId", rec.ConsentID, "err", errMsg)
	}
	sess.mu.Lock()
	sess.payments = append(sess.payments, rec)
	sess.mu.Unlock()
}

func (s *server) listPayments(w http.ResponseWriter, r *http.Request) {
	out := []paymentRecord{}
	if sess := s.session(w, r, false); sess != nil {
		sess.mu.Lock()
		for i := len(sess.payments) - 1; i >= 0; i-- {
			out = append(out, *sess.payments[i])
		}
		sess.mu.Unlock()
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *server) refreshPayment(w http.ResponseWriter, r *http.Request) {
	sess := s.session(w, r, false)
	var rec *paymentRecord
	if sess != nil {
		sess.mu.Lock()
		for _, p := range sess.payments {
			if p.ConsentID == r.PathValue("consentId") {
				rec = p
			}
		}
		sess.mu.Unlock()
	}
	if rec == nil || rec.PaymentID == "" {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no submitted payment with that consent"})
		return
	}
	appToken, err := s.bank.clientCredentials(r.Context(), scopePayments)
	if err != nil {
		writeError(w, err)
		return
	}
	payment, err := s.bank.getPayment(r.Context(), appToken.AccessToken, rec.PaymentID)
	if err != nil {
		writeError(w, err)
		return
	}
	sess.mu.Lock()
	rec.Status = string(payment.Data.Status)
	sess.mu.Unlock()
	writeJSON(w, http.StatusOK, rec)
}

// --- API log ---

func (s *server) callLog(w http.ResponseWriter, r *http.Request) {
	after, _ := strconv.Atoi(r.URL.Query().Get("after"))
	writeJSON(w, http.StatusOK, s.bank.calls.list(after))
}

func (s *server) clearLog(w http.ResponseWriter, r *http.Request) {
	s.bank.calls.clear()
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// --- helpers ---

func (s *server) failPage(w http.ResponseWriter, err error) {
	slog.Error("open banking call failed", "err", err)
	http.Error(w, err.Error(), http.StatusBadGateway)
}

func paramsFromURL(raw string) url.Values {
	out := url.Values{}
	u, err := url.Parse(raw)
	if err != nil {
		return out
	}
	for _, part := range []string{u.RawQuery, u.EscapedFragment()} {
		values, _ := url.ParseQuery(part)
		for k, v := range values {
			out[k] = v
		}
	}
	return out
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, err error) {
	status := http.StatusBadGateway
	if errors.Is(err, errNotConnected) {
		status = http.StatusUnauthorized
	} else {
		slog.Error("open banking call failed", "err", err)
	}
	writeJSON(w, status, map[string]string{"error": err.Error()})
}
