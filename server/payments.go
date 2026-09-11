package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"

	"spankki-sandbox/server/internal/spankkipay"
)

// S-Pankki exposes SEPA credit transfers through the OBIE "international payment" endpoints.

type paymentRequest struct {
	CreditorName string `json:"creditorName"`
	CreditorIBAN string `json:"creditorIban"`
	DebtorIBAN   string `json:"debtorIban"`
	Amount       string `json:"amount"`
	Currency     string `json:"currency"`
	Reference    string `json:"reference"`
}

type obAmount struct {
	Amount   string `json:"Amount"`
	Currency string `json:"Currency"`
}

type obAccountRef struct {
	SchemeName     string `json:"SchemeName"`
	Identification string `json:"Identification"`
	Name           string `json:"Name,omitempty"`
}

type obInitiation struct {
	InstructionIdentification string        `json:"InstructionIdentification"`
	EndToEndIdentification    string        `json:"EndToEndIdentification"`
	CurrencyOfTransfer        string        `json:"CurrencyOfTransfer"`
	InstructedAmount          obAmount      `json:"InstructedAmount"`
	DebtorAccount             *obAccountRef `json:"DebtorAccount,omitempty"`
	CreditorAccount           obAccountRef  `json:"CreditorAccount"`
	RemittanceInformation     *struct {
		Unstructured string `json:"Unstructured"`
	} `json:"RemittanceInformation,omitempty"`
}

// Body shape of OBWriteInternationalConsent5 (built by hand; the generated type nests anonymous structs).
type obPaymentConsent struct {
	Data struct {
		Initiation obInitiation `json:"Initiation"`
	} `json:"Data"`
	Risk struct{} `json:"Risk"`
}

func (b *bank) createPaymentConsent(ctx context.Context, appToken string, p paymentRequest) (*spankkipay.OBWriteInternationalConsentResponse6, error) {
	var body obPaymentConsent
	id := newUUID()
	body.Data.Initiation = obInitiation{
		InstructionIdentification: id[:35],
		EndToEndIdentification:    "SANDBOX-" + id[:8],
		CurrencyOfTransfer:        p.Currency,
		InstructedAmount:          obAmount{Amount: p.Amount, Currency: p.Currency},
		CreditorAccount:           obAccountRef{SchemeName: "UK.OBIE.IBAN", Identification: p.CreditorIBAN, Name: p.CreditorName},
	}
	if p.DebtorIBAN != "" {
		body.Data.Initiation.DebtorAccount = &obAccountRef{SchemeName: "UK.OBIE.IBAN", Identification: p.DebtorIBAN}
	}
	if p.Reference != "" {
		body.Data.Initiation.RemittanceInformation = &struct {
			Unstructured string `json:"Unstructured"`
		}{p.Reference}
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	sig, err := b.detachedJWS(raw)
	if err != nil {
		return nil, err
	}
	params := &spankkipay.CreateInternationalPaymentConsentsParams{
		Authorization:   "Bearer " + appToken,
		XIdempotencyKey: newUUID(),
		XJwsSignature:   sig,
	}
	resp, err := b.pisp.CreateInternationalPaymentConsentsWithBodyWithResponse(ctx, params, "application/json", bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	return decode[spankkipay.OBWriteInternationalConsentResponse6]("POST /international-payment-consents", resp.StatusCode(), resp.Body, http.StatusCreated)
}

// paymentConsentRaw keeps Initiation and Risk byte-for-byte, because the payment submission must
// repeat exactly what the user authorised (the bank may have filled in the debtor account).
type paymentConsentRaw struct {
	Data struct {
		ConsentID  string          `json:"ConsentId"`
		Status     string          `json:"Status"`
		Initiation json.RawMessage `json:"Initiation"`
	} `json:"Data"`
	Risk json.RawMessage `json:"Risk"`
}

func (b *bank) getPaymentConsent(ctx context.Context, appToken, consentID string) (*paymentConsentRaw, error) {
	params := &spankkipay.GetInternationalPaymentConsentsConsentIdParams{Authorization: "Bearer " + appToken}
	resp, err := b.pisp.GetInternationalPaymentConsentsConsentIdWithResponse(ctx, consentID, params)
	if err != nil {
		return nil, err
	}
	return decode[paymentConsentRaw]("GET /international-payment-consents/{id}", resp.StatusCode(), resp.Body, http.StatusOK)
}

func (b *bank) fundsConfirmation(ctx context.Context, userToken, consentID string) (*spankkipay.OBWriteFundsConfirmationResponse1, error) {
	params := &spankkipay.GetInternationalPaymentConsentsConsentIdFundsConfirmationParams{Authorization: "Bearer " + userToken}
	resp, err := b.pisp.GetInternationalPaymentConsentsConsentIdFundsConfirmationWithResponse(ctx, consentID, params)
	if err != nil {
		return nil, err
	}
	return decode[spankkipay.OBWriteFundsConfirmationResponse1]("GET /international-payment-consents/{id}/funds-confirmation", resp.StatusCode(), resp.Body, http.StatusOK)
}

func (b *bank) submitPayment(ctx context.Context, userToken string, consent *paymentConsentRaw) (*spankkipay.OBWriteInternationalResponse5, error) {
	risk := consent.Risk
	if len(risk) == 0 {
		risk = json.RawMessage(`{}`)
	}
	raw, err := json.Marshal(map[string]any{
		"Data": map[string]any{"ConsentId": consent.Data.ConsentID, "Initiation": consent.Data.Initiation},
		"Risk": risk,
	})
	if err != nil {
		return nil, err
	}
	sig, err := b.detachedJWS(raw)
	if err != nil {
		return nil, err
	}
	params := &spankkipay.CreateInternationalPaymentsParams{
		Authorization:   "Bearer " + userToken,
		XIdempotencyKey: newUUID(),
		XJwsSignature:   sig,
	}
	resp, err := b.pisp.CreateInternationalPaymentsWithBodyWithResponse(ctx, params, "application/json", bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	return decode[spankkipay.OBWriteInternationalResponse5]("POST /international-payments", resp.StatusCode(), resp.Body, http.StatusCreated)
}

func (b *bank) getPayment(ctx context.Context, appToken, paymentID string) (*spankkipay.OBWriteInternationalResponse5, error) {
	params := &spankkipay.GetInternationalPaymentsInternationalPaymentIdParams{Authorization: "Bearer " + appToken}
	resp, err := b.pisp.GetInternationalPaymentsInternationalPaymentIdWithResponse(ctx, paymentID, params)
	if err != nil {
		return nil, err
	}
	return decode[spankkipay.OBWriteInternationalResponse5]("GET /international-payments/{id}", resp.StatusCode(), resp.Body, http.StatusOK)
}
