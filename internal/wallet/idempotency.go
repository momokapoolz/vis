package wallet

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"
)

func digest(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }

func (a *App) mutate(r *http.Request, user User, payload any, action func(pgx.Tx) (Result, error)) (Result, error) {
	key := r.Header.Get("Idempotency-Key")
	if len(key) == 0 || len(key) > 128 || strings.TrimSpace(key) != key {
		return Result{}, invalid("Idempotency-Key must contain 1-128 characters without surrounding whitespace")
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return Result{}, err
	}
	hash := digest(append([]byte(r.Method+"\n"+r.URL.Path+"\n"), body...))
	tx, err := a.begin(r.Context())
	if err != nil {
		return Result{}, err
	}
	defer tx.Rollback(context.Background())
	tag, err := tx.Exec(r.Context(), `INSERT INTO idempotency_keys(user_id,key,request_hash,status) VALUES($1,$2,$3,'processing') ON CONFLICT(user_id,key) DO NOTHING`, user.ID, key, hash)
	if err != nil {
		return Result{}, err
	}
	if tag.RowsAffected() == 0 {
		var oldHash string
		var out Result
		err = tx.QueryRow(r.Context(), `SELECT request_hash,response_code,response_body FROM idempotency_keys WHERE user_id=$1 AND key=$2`, user.ID, key).Scan(&oldHash, &out.Status, &out.Body)
		if err != nil {
			return Result{}, err
		}
		if oldHash != hash {
			return Result{}, conflict("idempotency_conflict", "Key was already used for another request")
		}
		return out, nil
	}
	out, err := action(tx)
	if err != nil {
		var p *Problem
		if !errors.As(err, &p) {
			return Result{}, err
		}
		out = failure(err)
	}
	_, err = tx.Exec(r.Context(), `UPDATE idempotency_keys SET status='completed',response_code=$3,response_body=$4 WHERE user_id=$1 AND key=$2`, user.ID, key, out.Status, out.Body)
	if err != nil {
		return Result{}, err
	}
	if err = tx.Commit(r.Context()); err != nil {
		return Result{}, err
	}
	return out, nil
}
