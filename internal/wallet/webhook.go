package wallet

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
)

type DepositEvent struct {
	EventID  string `json:"event_id"`
	Event    string `json:"event"`
	WalletID int64  `json:"wallet_id"`
	Amount   int64  `json:"amount"`
}

func Sign(secret, body []byte) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

func (a *App) webhook(r *http.Request) (Result, error) {
	body, err := readBody(r)
	if err != nil {
		return Result{}, err
	}
	supplied, err := hex.DecodeString(r.Header.Get("X-Signature"))
	expected, _ := hex.DecodeString(Sign(a.WebhookSecret, body))
	if err != nil || !hmac.Equal(supplied, expected) {
		return Result{}, problem(401, "invalid_signature", "Invalid webhook signature")
	}
	var event DepositEvent
	if err = parseBody(body, &event); err != nil {
		return Result{}, err
	}
	if event.Event != "deposit.success" || event.EventID == "" || len(event.EventID) > 128 || strings.TrimSpace(event.EventID) != event.EventID || event.Amount <= 0 || event.WalletID <= 0 {
		return Result{}, invalid("Invalid deposit event")
	}
	canonical, err := json.Marshal(event)
	if err != nil {
		return Result{}, err
	}
	hash := digest(canonical)
	tx, err := a.begin(r.Context())
	if err != nil {
		return Result{}, err
	}
	defer tx.Rollback(context.Background())
	tag, err := tx.Exec(r.Context(), `INSERT INTO webhook_events(provider_event_id,payload,request_hash,status) VALUES($1,$2,$3,'processing') ON CONFLICT(provider_event_id) DO NOTHING`, event.EventID, canonical, hash)
	if err != nil {
		return Result{}, err
	}
	if tag.RowsAffected() == 0 {
		var oldHash string
		var out Result
		err = tx.QueryRow(r.Context(), `SELECT request_hash,response_code,response_body FROM webhook_events WHERE provider_event_id=$1`, event.EventID).Scan(&oldHash, &out.Status, &out.Body)
		if err != nil {
			return Result{}, err
		}
		if oldHash != hash {
			return Result{}, conflict("event_conflict", "Event ID was already used with different data")
		}
		return out, nil
	}
	// ponytail: one clearing row serializes deposits; split clearing accounts if measured throughput requires it.
	var clearing int64
	if err = tx.QueryRow(r.Context(), `SELECT id FROM wallets WHERE kind='system'`).Scan(&clearing); err != nil {
		return Result{}, err
	}
	out, err := a.post(r.Context(), tx, movement{clearing, event.WalletID, event.Amount}, "deposit", nil, nil, event.EventID, "")
	if err != nil {
		var p *Problem
		if !errors.As(err, &p) {
			return Result{}, err
		}
		out = failure(err)
	} else {
		out.Status = 200
	}
	_, err = tx.Exec(r.Context(), `UPDATE webhook_events SET status='completed',response_code=$2,response_body=$3,processed_at=now() WHERE provider_event_id=$1`, event.EventID, out.Status, out.Body)
	if err != nil {
		return Result{}, err
	}
	if err = tx.Commit(r.Context()); err != nil {
		return Result{}, err
	}
	return out, nil
}
