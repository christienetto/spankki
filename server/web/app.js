"use strict";

const $ = (sel) => document.querySelector(sel);
const $$ = (sel) => [...document.querySelectorAll(sel)];
const esc = (v) => String(v ?? "").replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]));

const state = {
  status: null,
  accounts: [],
  selectedId: null,
  tx: [],
  next: "",
  type: "all",
  payments: [],
  log: [],
  logAfter: 0,
  logOpen: new Set(),
};

async function api(path, opts = {}) {
  const res = await fetch(path, { ...opts, headers: { "Content-Type": "application/json", ...(opts.headers || {}) } });
  const text = await res.text();
  let data;
  try { data = text ? JSON.parse(text) : {}; } catch { data = { error: text }; }
  if (!res.ok) {
    const err = new Error(data.error || res.statusText);
    err.status = res.status;
    throw err;
  }
  return data;
}

function toast(message) {
  const el = $("#toast");
  el.textContent = message;
  el.hidden = false;
  clearTimeout(toast.timer);
  toast.timer = setTimeout(() => (el.hidden = true), 4500);
}

// ---------- formatting ----------

function money(amount, currency = "EUR", sign = 1) {
  const n = sign * Number(amount);
  try {
    return new Intl.NumberFormat("fi-FI", { style: "currency", currency }).format(n);
  } catch {
    return `${n.toFixed(2)} ${currency}`;
  }
}
const fmtDate = (iso) => (iso ? new Date(iso).toLocaleDateString("fi-FI") : "–");
const fmtDateTime = (iso) => (iso ? new Date(iso).toLocaleString("fi-FI") : "–");
const fmtTime = (iso) => new Date(iso).toLocaleTimeString("fi-FI");
const dayISO = (d) => d.toISOString().slice(0, 10);
const pretty = (s) => {
  if (!s) return "";
  try { return JSON.stringify(typeof s === "string" ? JSON.parse(s) : s, null, 2); } catch { return s; }
};

const accountName = (a) => a.Nickname || a.Account?.[0]?.Name || a.Description || a.AccountSubType;
const accountIBAN = (a) => (a.Account || []).find((x) => /IBAN/.test(x.SchemeName))?.Identification || a.Account?.[0]?.Identification || "";
const groupIBAN = (iban) => iban.replace(/(.{4})/g, "$1 ").trim();
const pickBalance = (a) => (a.Balances || []).find((b) => b.Type === "InterimAvailable") || (a.Balances || [])[0];
const balanceSign = (b) => (b?.CreditDebitIndicator === "Debit" ? -1 : 1);

const isCredit = (t) => t.CreditDebitIndicator === "Credit";
function txCounterparty(t) {
  const acc = isCredit(t) ? t.DebtorAccount : t.CreditorAccount;
  return { name: acc?.Name || t.MerchantDetails?.MerchantName || "", id: acc?.Identification || "" };
}
function txDescription(t) {
  return t.TransactionInformation || t.MerchantDetails?.MerchantName || txCounterparty(t).name ||
    t.ProprietaryBankTransactionCode?.Code || t.BankTransactionCode?.Code || "Transaction";
}

// ---------- status / session ----------

async function loadStatus() {
  state.status = await api("/api/status");
  renderStatus();
}

function renderStatus() {
  const s = state.status;
  const pill = $("#status-pill");
  pill.textContent = s.connected ? "Connected" : "Not connected";
  pill.className = "pill " + (s.connected ? "ok" : "");
  $("#btn-connect").hidden = s.connected;
  $("#btn-disconnect").hidden = !s.connected;
  $("#btn-refresh-token").hidden = !s.connected || !s.canRefresh;
  $("#connect-hero").hidden = s.connected;
  $("#client-info").innerHTML = `
    <dt>Client ID</dt><dd class="mono">${esc(s.clientId)}</dd>
    <dt>Redirect URI</dt><dd class="mono">${esc(s.redirectUri)}</dd>
    <dt>Consent</dt><dd class="mono">${esc(s.consentId || "–")}</dd>`;
  tickTimer();
}

function tickTimer() {
  const s = state.status;
  const el = $("#token-timer");
  if (!s?.connected || !s.tokenExpiresAt) { el.textContent = ""; return; }
  const left = Math.round((new Date(s.tokenExpiresAt) - Date.now()) / 1000);
  el.textContent = left > 0
    ? `Token ${Math.floor(left / 60)}:${String(left % 60).padStart(2, "0")}`
    : "Token expired · refreshes on next call";
}
setInterval(tickTimer, 1000);

$("#btn-connect").onclick = () => (location.href = "/connect");
$("#btn-disconnect").onclick = async () => {
  if (!confirm("Revoke the consent at S-Pankki and disconnect?")) return;
  await api("/disconnect", { method: "POST" });
  state.accounts = []; state.tx = []; state.selectedId = null;
  await loadStatus();
  renderAccounts();
  toast("Disconnected and consent revoked.");
};
$("#btn-refresh-token").onclick = async () => {
  try {
    state.status = await api("/api/token/refresh", { method: "POST" });
    renderStatus();
    toast("Access token refreshed (refreshed tokens see the last 90 days of transactions).");
  } catch (e) { toast(e.message); }
};

// ---------- accounts ----------

async function loadAccounts() {
  $("#accounts").innerHTML = `<div class="panel muted">Loading accounts…</div>`;
  try {
    const data = await api("/api/accounts");
    state.accounts = data.Accounts || [];
    if (!state.accounts.find((a) => a.AccountId === state.selectedId)) state.selectedId = state.accounts[0]?.AccountId || null;
    renderAccounts();
    fillPaymentAccounts();
    if (state.selectedId) loadTransactions();
  } catch (e) {
    if (e.status === 401) { await loadStatus(); state.accounts = []; renderAccounts(); return; }
    $("#accounts").innerHTML = `<div class="panel error">${esc(e.message)}</div>`;
  }
}

function renderAccounts() {
  const el = $("#accounts");
  $("#tx-panel").hidden = !state.selectedId;
  if (!state.accounts.length) {
    el.innerHTML = state.status?.connected ? `<div class="panel muted">No accounts in this consent.</div>` : "";
    return;
  }
  el.innerHTML = state.accounts.map((a) => {
    const b = pickBalance(a);
    const iban = accountIBAN(a);
    const booked = (a.Balances || []).find((x) => x.Type === "InterimBooked");
    return `<button class="account ${a.AccountId === state.selectedId ? "selected" : ""}" data-id="${esc(a.AccountId)}">
      <div class="top"><span class="name">${esc(accountName(a))}</span><span class="type">${esc(a.AccountType)} · ${esc(a.AccountSubType)}</span></div>
      <div class="balance">${b ? esc(money(b.Amount.Amount, b.Amount.Currency, balanceSign(b))) : "–"}</div>
      <div class="sub"><span>${esc(b?.Type || "")}</span><span>${booked ? "Booked " + esc(money(booked.Amount.Amount, booked.Amount.Currency, balanceSign(booked))) : ""}</span></div>
      <div class="iban mono small">${esc(groupIBAN(iban))} ${iban ? `<span class="copy" data-copy="${esc(iban)}" title="Copy IBAN">⧉</span>` : ""}</div>
    </button>`;
  }).join("");
}

$("#accounts").addEventListener("click", (e) => {
  const copy = e.target.closest("[data-copy]");
  if (copy) {
    e.stopPropagation();
    navigator.clipboard.writeText(copy.dataset.copy).then(() => toast("IBAN copied"));
    return;
  }
  const card = e.target.closest(".account");
  if (!card) return;
  state.selectedId = card.dataset.id;
  renderAccounts();
  loadTransactions();
});

// ---------- transactions ----------

async function loadTransactions(more = false) {
  const acc = state.accounts.find((a) => a.AccountId === state.selectedId);
  if (!acc) return;
  $("#tx-account-label").textContent = `${accountName(acc)} · ${groupIBAN(accountIBAN(acc))}`;
  const params = new URLSearchParams();
  if (more && state.next) params.set("next", state.next);
  else {
    if ($("#tx-from").value) params.set("from", $("#tx-from").value);
    if ($("#tx-to").value) params.set("to", $("#tx-to").value);
  }
  $("#tx-note").textContent = "Loading…";
  $("#tx-more").hidden = true;
  try {
    const page = await api(`/api/accounts/${encodeURIComponent(acc.AccountId)}/transactions?${params}`);
    const items = page.Data?.Transaction || [];
    state.tx = more ? state.tx.concat(items) : items;
    state.next = page.NextPage || "";
    $("#tx-note").textContent = "";
    renderTransactions();
  } catch (e) {
    let msg = e.message;
    if (/HTTP 403/.test(msg)) msg += "\n\nTip: after a token refresh S-Pankki only allows the last 90 days. Narrow the dates or reconnect for full history.";
    $("#tx-note").innerHTML = `<div class="error">${esc(msg)}</div>`;
    if (!more) { state.tx = []; renderTransactions(false); }
  }
}

function filteredTx() {
  const q = $("#tx-search").value.trim().toLowerCase();
  return state.tx.filter((t) => {
    if (state.type === "in" && !isCredit(t)) return false;
    if (state.type === "out" && isCredit(t)) return false;
    if (!q) return true;
    const cp = txCounterparty(t);
    return [txDescription(t), cp.name, cp.id, t.Amount?.Amount, t.TransactionReference, t.Status].join(" ").toLowerCase().includes(q);
  });
}

function renderTransactions(updateNote = true) {
  const list = filteredTx();
  const currency = list[0]?.Amount?.Currency || "EUR";
  let sumIn = 0, sumOut = 0;
  for (const t of list) {
    const n = Number(t.Amount.Amount);
    if (isCredit(t)) sumIn += n; else sumOut += n;
  }
  $("#st-count").textContent = list.length + (state.next ? "+" : "");
  $("#st-in").textContent = money(sumIn, currency);
  $("#st-out").textContent = money(-sumOut, currency);
  const net = $("#st-net");
  net.textContent = money(sumIn - sumOut, currency);
  net.className = "value " + (sumIn - sumOut >= 0 ? "pos" : "neg");
  renderChart(list, currency);

  $("#tx-body").innerHTML = list.map((t, i) => {
    const cp = txCounterparty(t);
    const credit = isCredit(t);
    return `<tr class="tx" data-i="${i}">
      <td>${esc(fmtDate(t.BookingDateTime))}</td>
      <td><div class="desc">${esc(txDescription(t))}</div>${t.TransactionReference ? `<div class="sub">${esc(t.TransactionReference)}</div>` : ""}</td>
      <td class="hide-sm">${esc(cp.name)}${cp.id ? `<div class="sub mono">${esc(cp.id)}</div>` : ""}</td>
      <td class="hide-sm"><span class="pill ${t.Status === "Booked" ? "ok" : "warn"}">${esc(t.Status)}</span></td>
      <td class="num ${credit ? "pos" : ""}"><b>${esc(money(t.Amount.Amount, t.Amount.Currency, credit ? 1 : -1))}</b>
        ${t.Balance ? `<div class="sub">Bal. ${esc(money(t.Balance.Amount.Amount, t.Balance.Amount.Currency, balanceSign(t.Balance)))}</div>` : ""}</td>
    </tr>`;
  }).join("");
  $("#tx-more").hidden = !state.next;
  if (updateNote && !list.length) $("#tx-note").textContent = state.tx.length ? "No transactions match the filters." : "No transactions in this period.";
}

$("#tx-body").addEventListener("click", (e) => {
  const row = e.target.closest("tr.tx");
  if (!row) return;
  const next = row.nextElementSibling;
  if (next?.classList.contains("detail")) { next.remove(); return; }
  const t = filteredTx()[Number(row.dataset.i)];
  row.insertAdjacentHTML("afterend", `<tr class="detail"><td colspan="5"><pre class="json">${esc(pretty(t))}</pre></td></tr>`);
});

function renderChart(list, currency) {
  const months = new Map();
  for (const t of list) {
    const key = (t.BookingDateTime || "").slice(0, 7);
    if (!key) continue;
    const m = months.get(key) || { in: 0, out: 0 };
    if (isCredit(t)) m.in += Number(t.Amount.Amount); else m.out += Number(t.Amount.Amount);
    months.set(key, m);
  }
  const keys = [...months.keys()].sort().slice(-12);
  const el = $("#tx-chart");
  if (keys.length === 0) { el.innerHTML = ""; return; }
  const max = Math.max(...keys.flatMap((k) => [months.get(k).in, months.get(k).out]), 1);
  const W = 720, H = 150, pad = 22, slot = (W - 10) / keys.length, bw = Math.min(26, slot / 2.8);
  const bars = keys.map((k, i) => {
    const m = months.get(k), x = 5 + i * slot + slot / 2;
    const hIn = (m.in / max) * (H - pad - 8), hOut = (m.out / max) * (H - pad - 8);
    const label = new Date(k + "-01").toLocaleDateString("fi-FI", { month: "short", year: keys.length > 6 ? "2-digit" : undefined });
    return `<g><title>${esc(k)}: in ${esc(money(m.in, currency))}, out ${esc(money(m.out, currency))}</title>
      <rect x="${x - bw - 1}" y="${H - pad - hIn}" width="${bw}" height="${Math.max(hIn, 1)}" rx="3" fill="var(--pos)"/>
      <rect x="${x + 1}" y="${H - pad - hOut}" width="${bw}" height="${Math.max(hOut, 1)}" rx="3" fill="var(--neg)" opacity=".75"/>
      <text x="${x}" y="${H - 6}" text-anchor="middle" font-size="11" fill="var(--muted)">${esc(label)}</text></g>`;
  }).join("");
  el.innerHTML = `<svg viewBox="0 0 ${W} ${H}" preserveAspectRatio="none" role="img" aria-label="Money in and out per month">
      <line x1="0" x2="${W}" y1="${H - pad}" y2="${H - pad}" stroke="var(--line)"/>${bars}</svg>
    <div class="legend"><span><i style="background:var(--pos)"></i>Money in</span><span><i style="background:var(--neg);opacity:.75"></i>Money out</span><span>per month (loaded transactions)</span></div>`;
}

$$("[data-range]").forEach((btn) => btn.addEventListener("click", () => {
  const r = btn.dataset.range;
  if (r === "all") { $("#tx-from").value = ""; $("#tx-to").value = ""; }
  else { $("#tx-from").value = dayISO(new Date(Date.now() - Number(r) * 864e5)); $("#tx-to").value = dayISO(new Date()); }
  $$("[data-range]").forEach((b) => b.classList.toggle("on", b === btn));
  loadTransactions();
}));
$("#tx-apply").onclick = () => { $$("[data-range]").forEach((b) => b.classList.remove("on")); loadTransactions(); };
$("#tx-more").onclick = () => loadTransactions(true);
$("#tx-search").addEventListener("input", () => renderTransactions());
$$("[data-type]").forEach((btn) => btn.addEventListener("click", () => {
  state.type = btn.dataset.type;
  $$("[data-type]").forEach((b) => b.classList.toggle("on", b === btn));
  renderTransactions();
}));

$("#tx-export").onclick = () => {
  const rows = [["Date", "Description", "Counterparty", "Counterparty account", "Status", "Amount", "Currency", "TransactionId"]];
  for (const t of filteredTx()) {
    const cp = txCounterparty(t);
    rows.push([(t.BookingDateTime || "").slice(0, 10), txDescription(t), cp.name, cp.id, t.Status,
      (isCredit(t) ? "" : "-") + t.Amount.Amount, t.Amount.Currency, t.TransactionId || ""]);
  }
  const csv = rows.map((r) => r.map((v) => `"${String(v ?? "").replace(/"/g, '""')}"`).join(",")).join("\r\n");
  const url = URL.createObjectURL(new Blob([csv], { type: "text/csv;charset=utf-8" }));
  const a = Object.assign(document.createElement("a"), { href: url, download: `transactions-${state.selectedId}.csv` });
  a.click();
  URL.revokeObjectURL(url);
};

// ---------- payments ----------

function fillPaymentAccounts() {
  const from = $("#pay-from"), quick = $("#pay-quick");
  const current = from.value;
  from.innerHTML = `<option value="">Choose at S-Pankki during approval</option>` + state.accounts.map((a) => {
    const b = pickBalance(a);
    return `<option value="${esc(accountIBAN(a))}">${esc(accountName(a))} · ${esc(groupIBAN(accountIBAN(a)))}${b ? " · " + esc(money(b.Amount.Amount, b.Amount.Currency, balanceSign(b))) : ""}</option>`;
  }).join("");
  from.value = current;
  quick.innerHTML = `<option value="">—</option>` +
    state.accounts.map((a) => `<option value="own:${esc(a.AccountId)}">My account: ${esc(accountName(a))} (${esc(groupIBAN(accountIBAN(a)))})</option>`).join("") +
    `<option value="sample">Sample recipient from Crosskey docs (Jane Roe)</option>`;
}

$("#pay-quick").onchange = (e) => {
  const v = e.target.value;
  if (v === "sample") {
    $("#pay-name").value = "Jane Roe";
    $("#pay-iban").value = "FI4966010005485495";
  } else if (v.startsWith("own:")) {
    const a = state.accounts.find((x) => x.AccountId === v.slice(4));
    $("#pay-name").value = a.Account?.[0]?.Name || accountName(a);
    $("#pay-iban").value = accountIBAN(a);
  }
  if (!$("#pay-ref").value) $("#pay-ref").value = "Sandbox test payment";
};

$("#pay-form").onsubmit = async (e) => {
  e.preventDefault();
  const err = $("#pay-error");
  err.hidden = true;
  const btn = $("#pay-submit");
  btn.disabled = true;
  btn.textContent = "Creating payment consent…";
  try {
    const res = await api("/api/payments", {
      method: "POST",
      body: JSON.stringify({
        debtorIban: $("#pay-from").value,
        creditorName: $("#pay-name").value,
        creditorIban: $("#pay-iban").value,
        amount: $("#pay-amount").value,
        currency: $("#pay-currency").value,
        reference: $("#pay-ref").value,
      }),
    });
    location.href = res.redirect;
  } catch (ex) {
    let msg = ex.message;
    if (/invalid_scope|invalid_client|HTTP 40[13]/.test(msg)) msg += "\n\nIs your application subscribed to the Payment Initiation API (v3.1.7)? Subscriptions can take a minute to become active.";
    err.textContent = msg;
    err.hidden = false;
    btn.disabled = false;
    btn.textContent = "Continue to S-Pankki approval";
  }
};

async function loadPayments() {
  state.payments = await api("/api/payments");
  renderPayments();
}

function paymentPill(status) {
  if (/Completed|Accepted|Authorised|Settle/i.test(status)) return "ok";
  if (/Reject|Fail/i.test(status)) return "bad";
  return "warn";
}

function renderPayments() {
  $("#pay-empty").hidden = state.payments.length > 0;
  $("#pay-body").innerHTML = state.payments.map((p) => `
    <tr>
      <td>${esc(fmtDateTime(p.createdAt))}${p.reference ? `<div class="sub">${esc(p.reference)}</div>` : ""}</td>
      <td>${esc(p.creditorName)}${p.fundsAvailable !== undefined ? `<div class="sub">Funds available: ${p.fundsAvailable ? "yes" : "no"}</div>` : ""}</td>
      <td class="hide-sm mono small">${esc(groupIBAN(p.creditorIban))}</td>
      <td class="num"><b>${esc(money(p.amount, p.currency))}</b></td>
      <td><span class="pill ${paymentPill(p.status)}">${esc(p.status)}</span>
        ${p.paymentId ? `<div class="sub mono" title="${esc(p.paymentId)}">${esc(p.paymentId.slice(0, 13))}…</div>` : ""}
        ${p.error ? `<div class="error small">${esc(p.error)}</div>` : ""}</td>
      <td>${p.paymentId ? `<button class="btn ghost small" data-refresh="${esc(p.consentId)}">Refresh</button>` : ""}</td>
    </tr>`).join("");
}

$("#pay-body").addEventListener("click", async (e) => {
  const btn = e.target.closest("[data-refresh]");
  if (!btn) return;
  btn.disabled = true;
  try {
    await api(`/api/payments/${encodeURIComponent(btn.dataset.refresh)}/refresh`, { method: "POST" });
    await loadPayments();
    toast("Payment status updated");
  } catch (ex) { toast(ex.message); btn.disabled = false; }
});

// ---------- consent ----------

async function loadConsent() {
  const el = $("#consent-body");
  if (!state.status?.connected) { el.innerHTML = `<p class="muted">Not connected. Use <b>New consent</b> to create one.</p>`; return; }
  el.innerHTML = `<p class="muted">Loading…</p>`;
  try {
    const c = await api("/api/consent");
    const d = c.Data || {};
    el.innerHTML = `
      <dl class="kv">
        <dt>Status</dt><dd><span class="pill ${d.Status === "Authorised" ? "ok" : d.Status === "Revoked" || d.Status === "Rejected" ? "bad" : "warn"}">${esc(d.Status)}</span></dd>
        <dt>Consent ID</dt><dd class="mono">${esc(d.ConsentId)}</dd>
        <dt>Created</dt><dd>${esc(fmtDateTime(d.CreationDateTime))}</dd>
        <dt>Status updated</dt><dd>${esc(fmtDateTime(d.StatusUpdateDateTime))}</dd>
        <dt>Expires</dt><dd>${esc(d.ExpirationDateTime ? fmtDateTime(d.ExpirationDateTime) : "Not set")}</dd>
        <dt>Transaction window</dt><dd>${esc(d.TransactionFromDateTime ? fmtDate(d.TransactionFromDateTime) : "open")} → ${esc(d.TransactionToDateTime ? fmtDate(d.TransactionToDateTime) : "open")}</dd>
        <dt>Permissions</dt><dd><div class="chips">${(d.Permissions || []).map((p) => `<span class="chip">${esc(p)}</span>`).join("")}</div></dd>
      </dl>
      <details style="margin-top:14px"><summary class="muted small">Raw response</summary><pre class="json">${esc(pretty(c))}</pre></details>`;
  } catch (e) {
    el.innerHTML = `<div class="error">${esc(e.message)}</div>`;
  }
}
$("#consent-reload").onclick = loadConsent;
$("#consent-revoke").onclick = () => $("#btn-disconnect").click();

// ---------- API log ----------

async function pollLog() {
  try {
    const fresh = await api(`/api/log?after=${state.logAfter}`);
    if (fresh.length) {
      state.log = fresh.concat(state.log).slice(0, 200);
      state.logAfter = state.log[0].id;
      renderLog();
    }
  } catch { /* server restarting */ }
}

function renderLog() {
  const q = $("#log-filter").value.trim().toLowerCase();
  const list = state.log.filter((e) => !q || `${e.method} ${e.url} ${e.status}`.toLowerCase().includes(q));
  $("#log-empty").hidden = list.length > 0;
  const count = $("#log-count");
  count.textContent = state.log.length;
  count.hidden = state.log.length === 0;
  $("#log-list").innerHTML = list.map((e) => {
    const u = new URL(e.url);
    const path = u.pathname.replace(/^\/open-banking/, "") + (u.search ? "?…" : "");
    const open = state.logOpen.has(e.id);
    return `<div class="log-entry">
      <button class="log-row" data-log="${e.id}">
        <span class="time muted small">${esc(fmtTime(e.time))}</span>
        <span class="method ${esc(e.method)}">${esc(e.method)}</span>
        <span class="path mono">${esc(path)}${e.signed ? `<span class="tag">JWS</span>` : ""}</span>
        <span class="status s${String(e.status)[0]}">${e.status || "ERR"}</span>
        <span class="dur muted small">${e.durationMs} ms</span>
      </button>
      ${open ? `<div class="log-detail">
        <div><h4>URL</h4><div class="mono small">${esc(e.url)}</div></div>
        ${e.interactionId ? `<div><h4>x-fapi-interaction-id</h4><div class="mono small">${esc(e.interactionId)}</div></div>` : ""}
        ${e.error ? `<div class="error">${esc(e.error)}</div>` : ""}
        ${e.requestBody ? `<div><h4>Request body</h4><pre class="json">${esc(pretty(e.requestBody))}</pre></div>` : ""}
        ${e.responseBody ? `<div><h4>Response body</h4><pre class="json">${esc(pretty(e.responseBody))}</pre></div>` : ""}
      </div>` : ""}
    </div>`;
  }).join("");
}

$("#log-list").addEventListener("click", (e) => {
  const row = e.target.closest("[data-log]");
  if (!row) return;
  const id = Number(row.dataset.log);
  state.logOpen.has(id) ? state.logOpen.delete(id) : state.logOpen.add(id);
  renderLog();
});
$("#log-filter").addEventListener("input", renderLog);
$("#log-clear").onclick = async () => {
  await api("/api/log/clear", { method: "POST" });
  state.log = [];
  renderLog();
};
setInterval(() => { if ($("#log-live").checked && !document.hidden) pollLog(); }, 2500);

// ---------- routing ----------

function route() {
  const tab = (location.hash.slice(1) || "overview").split("?")[0];
  const valid = ["overview", "payments", "consent", "log"].includes(tab) ? tab : "overview";
  for (const v of ["overview", "payments", "consent", "log"]) $(`#view-${v}`).hidden = v !== valid;
  $$(".tabs a").forEach((a) => a.classList.toggle("active", a.dataset.tab === valid));
  if (valid === "consent") loadConsent();
  if (valid === "payments") loadPayments();
  if (valid === "log") pollLog();
}
window.addEventListener("hashchange", route);

(async function init() {
  try {
    await loadStatus();
  } catch (e) {
    toast("Server not reachable: " + e.message);
    return;
  }
  route();
  pollLog();
  if (state.status.connected) loadAccounts();
  else renderAccounts();
  loadPayments();
})();
