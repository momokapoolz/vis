package wallet

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

const maxBody = 1 << 20

func readBody(r *http.Request) ([]byte, error) {
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		return nil, invalid("Content-Type must be application/json")
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBody+1))
	if err != nil {
		return nil, invalid("Cannot read request body")
	}
	if len(body) > maxBody {
		return nil, problem(413, "body_too_large", "Body exceeds 1 MiB")
	}
	return body, nil
}
func parseBody(body []byte, out any) error {
	body = bytes.TrimSpace(body)
	if len(body) == 0 || body[0] != '{' {
		return invalid("Body must be a JSON object")
	}
	d := json.NewDecoder(bytes.NewReader(body))
	d.DisallowUnknownFields()
	if err := d.Decode(out); err != nil {
		return invalid("Invalid JSON or unsupported fields/types")
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return invalid("Exactly one JSON object is required")
	}
	return nil
}
func decode(r *http.Request, out any) error {
	body, err := readBody(r)
	if err != nil {
		return err
	}
	return parseBody(body, out)
}

type endpoint func(*http.Request, User) (Result, error)

func (a *App) handle(fn endpoint, access string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()
		r = r.WithContext(ctx)
		var u User
		var err error
		var out Result
		if access != "public" {
			u, err = a.authenticate(r)
		}
		if err == nil && access == "staff" && u.Role == "customer" {
			err = problem(403, "forbidden", "Staff access required")
		}
		if err == nil && access == "admin" && u.Role != "admin" {
			err = problem(403, "forbidden", "Admin access required")
		}
		if err == nil {
			out, err = fn(r, u)
		}
		if err != nil {
			var p *Problem
			if !errors.As(err, &p) {
				slog.Error("request failed", "method", r.Method, "path", r.URL.Path, "error", err)
			}
			out = failure(err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if out.Status == 503 {
			w.Header().Set("Retry-After", "1")
		}
		w.WriteHeader(out.Status)
		_, _ = w.Write(out.Body)
	}
}

func (a *App) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", a.handle(func(r *http.Request, _ User) (Result, error) {
		err := a.Pool.Ping(r.Context())
		return result(200, map[string]string{"status": "ok"}), err
	}, "public"))
	mux.HandleFunc("POST /auth/register", a.handle(func(r *http.Request, _ User) (Result, error) { return a.register(r) }, "public"))
	mux.HandleFunc("POST /auth/login", a.handle(func(r *http.Request, _ User) (Result, error) { return a.login(r) }, "public"))
	mux.HandleFunc("POST /auth/refresh", a.handle(func(r *http.Request, _ User) (Result, error) { return a.refresh(r) }, "public"))
	mux.HandleFunc("POST /wallets", a.handle(a.createWallet, "user"))
	mux.HandleFunc("GET /wallets", a.handle(a.listWallets, "user"))
	mux.HandleFunc("GET /wallets/{id}", a.handle(a.getWallet, "user"))
	mux.HandleFunc("GET /wallets/{id}/balance", a.handle(a.getBalance, "user"))
	mux.HandleFunc("GET /wallets/{id}/transactions", a.handle(a.listTransactions, "user"))
	mux.HandleFunc("POST /transfers", a.handle(a.transfer, "user"))
	mux.HandleFunc("GET /transactions/{id}", a.handle(a.getTransaction, "user"))
	mux.HandleFunc("POST /webhooks/mock-provider", a.handle(func(r *http.Request, _ User) (Result, error) { return a.webhook(r) }, "public"))
	mux.HandleFunc("POST /admin/transactions/{id}/reverse", a.handle(a.reverseHTTP, "admin"))
	mux.HandleFunc("POST /admin/reconciliations", a.handle(a.reconcileHTTP, "admin"))
	mux.HandleFunc("GET /admin/reconciliations/{id}", a.handle(a.getReconciliation, "admin"))
	mux.HandleFunc("GET /users", a.handle(a.listUsers, "staff"))
	mux.HandleFunc("GET /users/{id}", a.handle(a.getUser, "staff"))
	mux.HandleFunc("GET /openapi.json", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(openAPI)
	})
	mux.HandleFunc("GET /docs", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, swaggerHTML)
	})
	return mux
}

func pathID(r *http.Request) (int64, error) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		return 0, invalid("ID must be a positive integer")
	}
	return id, nil
}
func paging(r *http.Request) (int, int, error) {
	limit, offset := 20, 0
	var err error
	if raw := r.URL.Query().Get("limit"); raw != "" {
		limit, err = strconv.Atoi(raw)
		if err != nil {
			return 0, 0, invalid("Invalid limit")
		}
	}
	if raw := r.URL.Query().Get("offset"); raw != "" {
		offset, err = strconv.Atoi(raw)
		if err != nil {
			return 0, 0, invalid("Invalid offset")
		}
	}
	if limit < 1 || limit > 100 || offset < 0 {
		return 0, 0, invalid("limit must be 1-100 and offset nonnegative")
	}
	return limit, offset, nil
}

func (a *App) jsonOne(r *http.Request, query string, args ...any) (Result, error) {
	var raw []byte
	err := a.Pool.QueryRow(r.Context(), query, args...).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return Result{}, notFound()
	}
	return Result{200, raw}, err
}
func (a *App) jsonList(r *http.Request, query string, args ...any) (Result, error) {
	rows, err := a.Pool.Query(r.Context(), query, args...)
	if err != nil {
		return Result{}, err
	}
	items, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (json.RawMessage, error) {
		var raw []byte
		err := row.Scan(&raw)
		return json.RawMessage(raw), err
	})
	if items == nil {
		items = []json.RawMessage{}
	}
	return result(200, map[string]any{"items": items}), err
}
func (a *App) walletAccess(r *http.Request, u User, id int64) error {
	var exists bool
	err := a.Pool.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM wallets WHERE id=$1 AND ($2<>'customer' OR user_id=$3))`, id, u.Role, u.ID).Scan(&exists)
	if err != nil {
		return err
	}
	if !exists {
		return notFound()
	}
	return nil
}

func (a *App) createWallet(r *http.Request, u User) (Result, error) {
	var body struct {
		Name     string `json:"name"`
		Currency string `json:"currency"`
	}
	if err := decode(r, &body); err != nil {
		return Result{}, err
	}
	body.Name = strings.TrimSpace(body.Name)
	if body.Currency == "" {
		body.Currency = "VND"
	}
	if body.Currency != "VND" || len(body.Name) > 100 {
		return Result{}, invalid("Only VND is supported; name must be at most 100 bytes")
	}
	return a.mutate(r, u, body, func(tx pgx.Tx) (Result, error) {
		var raw []byte
		err := tx.QueryRow(r.Context(), `INSERT INTO wallets(user_id,name) VALUES($1,$2) RETURNING to_jsonb(wallets)`, u.ID, body.Name).Scan(&raw)
		return Result{201, raw}, err
	})
}
func (a *App) listWallets(r *http.Request, u User) (Result, error) {
	l, o, err := paging(r)
	if err != nil {
		return Result{}, err
	}
	var owner *int64
	if raw := r.URL.Query().Get("user_id"); raw != "" {
		n, e := strconv.ParseInt(raw, 10, 64)
		if e != nil || n <= 0 {
			return Result{}, invalid("Invalid user_id")
		}
		owner = &n
	}
	return a.jsonList(r, `SELECT to_jsonb(w) FROM wallets w WHERE ($1<>'customer' OR user_id=$2) AND ($3::bigint IS NULL OR user_id=$3) ORDER BY id DESC LIMIT $4 OFFSET $5`, u.Role, u.ID, owner, l, o)
}
func (a *App) getWallet(r *http.Request, u User) (Result, error) {
	id, err := pathID(r)
	if err != nil {
		return Result{}, err
	}
	return a.jsonOne(r, `SELECT to_jsonb(w) FROM wallets w WHERE id=$1 AND ($2<>'customer' OR user_id=$3)`, id, u.Role, u.ID)
}
func (a *App) getBalance(r *http.Request, u User) (Result, error) {
	id, err := pathID(r)
	if err != nil {
		return Result{}, err
	}
	if err = a.walletAccess(r, u, id); err != nil {
		return Result{}, err
	}
	tx, err := a.begin(r.Context())
	if err != nil {
		return Result{}, err
	}
	defer tx.Rollback(context.Background())
	n, err := balance(r.Context(), tx, id)
	return result(200, map[string]any{"wallet_id": id, "currency": "VND", "balance": n}), err
}
func (a *App) transfer(r *http.Request, u User) (Result, error) {
	var m movement
	if err := decode(r, &m); err != nil {
		return Result{}, err
	}
	if m.Amount <= 0 || m.Source <= 0 || m.Destination <= 0 || m.Source == m.Destination {
		return Result{}, invalid("Positive amount and two different wallets are required")
	}
	if err := a.walletAccess(r, User{ID: u.ID, Role: "customer"}, m.Source); err != nil {
		return Result{}, err
	}
	return a.mutate(r, u, m, func(tx pgx.Tx) (Result, error) { return a.post(r.Context(), tx, m, "transfer", &u.ID, nil, "", "") })
}
func (a *App) getTransaction(r *http.Request, u User) (Result, error) {
	id, err := pathID(r)
	if err != nil {
		return Result{}, err
	}
	return a.jsonOne(r, `SELECT to_jsonb(t)||jsonb_build_object('entries',(SELECT jsonb_agg(to_jsonb(e) ORDER BY e.id) FROM ledger_entries e WHERE e.transaction_id=t.id))
		FROM transactions t WHERE id=$1 AND ($2<>'customer' OR EXISTS(SELECT 1 FROM ledger_entries e JOIN wallets w ON w.id=e.wallet_id WHERE e.transaction_id=t.id AND w.user_id=$3))`, id, u.Role, u.ID)
}
func (a *App) listTransactions(r *http.Request, u User) (Result, error) {
	id, err := pathID(r)
	if err != nil {
		return Result{}, err
	}
	if err = a.walletAccess(r, u, id); err != nil {
		return Result{}, err
	}
	l, o, err := paging(r)
	if err != nil {
		return Result{}, err
	}
	return a.jsonList(r, `SELECT to_jsonb(t)||jsonb_build_object('entry_type',e.entry_type,'amount',e.amount) FROM ledger_entries e JOIN transactions t ON t.id=e.transaction_id WHERE e.wallet_id=$1 ORDER BY t.id DESC LIMIT $2 OFFSET $3`, id, l, o)
}
func (a *App) reverseHTTP(r *http.Request, u User) (Result, error) {
	id, err := pathID(r)
	if err != nil {
		return Result{}, err
	}
	var body struct {
		Reason string `json:"reason"`
	}
	if err = decode(r, &body); err != nil {
		return Result{}, err
	}
	body.Reason = strings.TrimSpace(body.Reason)
	if len(body.Reason) == 0 || len(body.Reason) > 500 {
		return Result{}, invalid("reason must contain 1-500 bytes")
	}
	return a.mutate(r, u, body, func(tx pgx.Tx) (Result, error) { return a.reverse(r.Context(), tx, u, id, body.Reason) })
}
func (a *App) reconcileHTTP(r *http.Request, u User) (Result, error) {
	var body struct{}
	if err := decode(r, &body); err != nil {
		return Result{}, err
	}
	return a.mutate(r, u, body, func(tx pgx.Tx) (Result, error) { return reconcile(r.Context(), tx, &u.ID) })
}
func (a *App) getReconciliation(r *http.Request, u User) (Result, error) {
	id, err := pathID(r)
	if err != nil {
		return Result{}, err
	}
	return a.jsonOne(r, `SELECT to_jsonb(r) FROM reconciliation_runs r WHERE id=$1`, id)
}
func (a *App) listUsers(r *http.Request, u User) (Result, error) {
	l, o, err := paging(r)
	if err != nil {
		return Result{}, err
	}
	return a.jsonList(r, `SELECT jsonb_build_object('id',id,'email',email,'role',role,'created_at',created_at) FROM users WHERE ($1='' OR email=$1) ORDER BY id DESC LIMIT $2 OFFSET $3`, strings.ToLower(strings.TrimSpace(r.URL.Query().Get("email"))), l, o)
}
func (a *App) getUser(r *http.Request, u User) (Result, error) {
	id, err := pathID(r)
	if err != nil {
		return Result{}, err
	}
	return a.jsonOne(r, `SELECT jsonb_build_object('id',id,'email',email,'role',role,'created_at',created_at) FROM users WHERE id=$1`, id)
}
