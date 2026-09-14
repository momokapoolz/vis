package wallet

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"wallet/internal/db"
)

func TestInputAndHMAC(t *testing.T) {
	for _, raw := range []string{`null`, `[]`, `{"amount":1.5}`, `{"amount":9223372036854775808}`, `{"amount":1,"role":"admin"}`, `{"amount":1} {}`} {
		var m movement
		if parseBody([]byte(raw), &m) == nil {
			t.Fatalf("accepted invalid input: %s", raw)
		}
	}
	// RFC-style known vector, independent of the implementation.
	if got := Sign([]byte("key"), []byte("The quick brown fox jumps over the lazy dog")); got != "f7bc83f430538424b13298e6aa6fb143ef4d59a14946175997479dbc2d1a3cd8" {
		t.Fatal(got)
	}
	var c = credentials{" Person@Example.com ", "password123"}
	if err := normalizeCredentials(&c); err != nil || c.Email != "person@example.com" {
		t.Fatal(c, err)
	}
	r := httptest.NewRequest("POST", "/", strings.NewReader(strings.Repeat("x", maxBody+1)))
	r.Header.Set("Content-Type", "application/json")
	if _, err := readBody(r); err == nil {
		t.Fatal("accepted oversized body")
	}
	var spec map[string]any
	if err := json.Unmarshal(openAPI, &spec); err != nil {
		t.Fatal(err)
	}
	if len(spec["paths"].(map[string]any)) < 16 {
		t.Fatal("OpenAPI is incomplete")
	}
}

type harness struct {
	t       *testing.T
	a       *App
	owner   *pgxpool.Pool
	handler http.Handler
	serial  int
}

func setup(t *testing.T) *harness {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("integration requires TEST_DATABASE_URL pointing to a PostgreSQL superuser; see README")
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("wallet_test_%d", time.Now().UnixNano())
	if _, err = admin.Exec(ctx, `DO $$ BEGIN IF NOT EXISTS(SELECT 1 FROM pg_roles WHERE rolname='wallet_app') THEN CREATE ROLE wallet_app NOLOGIN; END IF; END $$`); err != nil {
		t.Fatal(err)
	}
	if _, err = admin.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, err := admin.Exec(ctx, "DROP DATABASE "+pgx.Identifier{name}.Sanitize()+" WITH (FORCE)")
		if err != nil {
			t.Error(err)
		}
		admin.Close(ctx)
	})
	testURL, err := url.Parse(databaseURL)
	if err != nil || (testURL.Scheme != "postgres" && testURL.Scheme != "postgresql") {
		t.Fatal("TEST_DATABASE_URL must be a postgres:// or postgresql:// URL")
	}
	testURL.Path = "/" + name
	// Override any dbname query option as well: never migrate the source database.
	query := testURL.Query()
	query.Del("dbname")
	testURL.RawQuery = query.Encode()
	config, err := pgxpool.ParseConfig(testURL.String())
	if err != nil {
		t.Fatal(err)
	}
	if config.ConnConfig.Database != name {
		t.Fatal("test database isolation failed")
	}
	if err = db.Migrate(config.ConnConfig.ConnString()); err != nil {
		t.Fatal(err)
	}
	owner, err := pgxpool.NewWithConfig(ctx, config.Copy())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(owner.Close)
	config.MaxConns = 10
	config.AfterConnect = func(ctx context.Context, c *pgx.Conn) error { _, err := c.Exec(ctx, "SET ROLE wallet_app"); return err }
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	a := &App{Pool: pool, JWTSecret: []byte(strings.Repeat("j", 32)), WebhookSecret: []byte(strings.Repeat("w", 32))}
	return &harness{t: t, a: a, owner: owner, handler: a.Handler()}
}

func (h *harness) call(method, path, token, key string, body any) *httptest.ResponseRecorder {
	raw, _ := json.Marshal(body)
	return h.raw(method, path, token, key, raw, "")
}
func (h *harness) raw(method, path, token, key string, raw []byte, signature string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, bytes.NewReader(raw))
	r.Header.Set("Content-Type", "application/json")
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	if key != "" {
		r.Header.Set("Idempotency-Key", key)
	}
	if signature != "" {
		r.Header.Set("X-Signature", signature)
	}
	w := httptest.NewRecorder()
	h.handler.ServeHTTP(w, r)
	return w
}
func (h *harness) expect(w *httptest.ResponseRecorder, status int) map[string]json.RawMessage {
	h.t.Helper()
	if w.Code != status {
		h.t.Fatalf("HTTP %d, expected %d: %s", w.Code, status, w.Body.String())
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		h.t.Fatal(err)
	}
	return body
}
func value[T any](t *testing.T, raw json.RawMessage) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	return v
}
func (h *harness) user(role string) (User, string) {
	h.serial++
	email := fmt.Sprintf("user%d@example.com", h.serial)
	u, err := (&App{Pool: h.owner}).SeedUser(context.Background(), email, "password123", role)
	if err != nil {
		h.t.Fatal(err)
	}
	body := h.expect(h.call("POST", "/auth/login", "", "", credentials{email, "password123"}), 200)
	return u, value[string](h.t, body["access_token"])
}
func (h *harness) wallet(token string) int64 {
	h.serial++
	body := h.expect(h.call("POST", "/wallets", token, fmt.Sprintf("wallet-%d", h.serial), map[string]string{}), 201)
	return value[int64](h.t, body["id"])
}
func (h *harness) deposit(id, amount int64, event string) *httptest.ResponseRecorder {
	body, _ := json.Marshal(DepositEvent{event, "deposit.success", id, amount})
	return h.raw("POST", "/webhooks/mock-provider", "", "", body, Sign(h.a.WebhookSecret, body))
}
func (h *harness) balance(token string, id, want int64) {
	h.t.Helper()
	b := h.expect(h.call("GET", fmt.Sprintf("/wallets/%d/balance", id), token, "", nil), 200)
	if got := value[int64](h.t, b["balance"]); got != want {
		h.t.Fatalf("wallet %d balance %d, want %d", id, got, want)
	}
}
func parallel(actions ...func() *httptest.ResponseRecorder) []*httptest.ResponseRecorder {
	out := make([]*httptest.ResponseRecorder, len(actions))
	start := make(chan struct{})
	var ready, done sync.WaitGroup
	ready.Add(len(actions))
	done.Add(len(actions))
	for i, action := range actions {
		go func() { defer done.Done(); ready.Done(); <-start; out[i] = action() }()
	}
	ready.Wait()
	close(start)
	done.Wait()
	return out
}

func TestIntegration(t *testing.T) {
	h := setup(t)
	_, alice := h.user("customer")
	_, bob := h.user("customer")
	_, admin := h.user("admin")
	_, support := h.user("support")
	wa, wb := h.wallet(alice), h.wallet(bob)
	t.Run("demo and idempotency", func(t *testing.T) {
		h.t = t
		h.balance(alice, wa, 0)
		h.expect(h.deposit(wa, 1000000, "demo-deposit"), 200)
		m := movement{wa, wb, 100000}
		first := h.call("POST", "/transfers", alice, "demo-transfer", m)
		b := h.expect(first, 201)
		id := value[int64](t, b["id"])
		retry := h.call("POST", "/transfers", alice, "demo-transfer", m)
		h.expect(retry, 201)
		if retry.Body.String() != first.Body.String() {
			t.Fatal("replay differs")
		}
		h.expect(h.call("POST", "/transfers", alice, "demo-transfer", movement{wa, wb, 1}), 409)
		h.expect(h.call("POST", "/wallets", alice, "demo-transfer", map[string]string{}), 409)
		h.balance(alice, wa, 900000)
		h.balance(bob, wb, 100000)
		original := h.call("GET", fmt.Sprintf("/transactions/%d", id), alice, "", nil)
		h.expect(original, 200)
		reversed := h.expect(h.call("POST", fmt.Sprintf("/admin/transactions/%d/reverse", id), admin, "reverse-demo", map[string]string{"reason": "demo refund"}), 201)
		h.expect(h.call("POST", fmt.Sprintf("/admin/transactions/%d/reverse", id), admin, "reverse-again", map[string]string{"reason": "again"}), 409)
		h.expect(h.call("POST", fmt.Sprintf("/admin/transactions/%d/reverse", value[int64](t, reversed["id"])), admin, "reverse-reversal", map[string]string{"reason": "again"}), 409)
		if got := h.call("GET", fmt.Sprintf("/transactions/%d", id), alice, "", nil); got.Body.String() != original.Body.String() {
			t.Fatal("original transaction changed")
		}
		h.balance(alice, wa, 1000000)
		h.balance(bob, wb, 0)
		b = h.expect(h.call("POST", "/admin/reconciliations", admin, "reconcile-demo", map[string]string{}), 201)
		if value[string](t, b["status"]) != "pass" {
			t.Fatal(string(b["report"]))
		}
	})
	t.Run("authorization and validation", func(t *testing.T) {
		h.t = t
		h.expect(h.call("GET", fmt.Sprintf("/wallets/%d", wa), bob, "", nil), 404)
		h.expect(h.call("GET", fmt.Sprintf("/wallets/%d", wa), support, "", nil), 200)
		h.expect(h.call("GET", "/users", alice, "", nil), 403)
		h.expect(h.call("GET", "/users", support, "", nil), 200)
		h.expect(h.call("POST", "/admin/reconciliations", support, "forbidden", map[string]string{}), 403)
		h.expect(h.call("POST", "/admin/transactions/1/reverse", support, "forbidden", map[string]string{"reason": "no"}), 403)
		h.expect(h.call("POST", "/transfers", bob, "steal", movement{wa, wb, 1}), 404)
		h.expect(h.call("POST", "/transfers", alice, "", movement{wa, wb, 1}), 400)
		for i, m := range []movement{{wa, wa, 1}, {wa, wb, 0}, {wa, wb, -1}} {
			h.expect(h.call("POST", "/transfers", alice, fmt.Sprint("invalid", i), m), 400)
		}
		h.expect(h.call("POST", "/transfers", alice, "too-much", movement{wa, wb, 2000000}), 409)
		h.expect(h.call("POST", "/auth/register", "", "", map[string]string{"email": "hack@example.com", "password": "password123", "role": "admin"}), 400)
		h.expect(h.call("POST", "/wallets", alice, "usd", map[string]string{"currency": "USD"}), 400)
		var clearing int64
		if err := h.owner.QueryRow(context.Background(), "SELECT id FROM wallets WHERE kind='system'").Scan(&clearing); err != nil {
			t.Fatal(err)
		}
		h.expect(h.call("POST", "/transfers", alice, "system-destination", movement{wa, clearing, 1}), 403)
		h.expect(h.call("GET", "/wallets", "bad-token", "", nil), 401)
	})
	t.Run("register and token rotation", func(t *testing.T) {
		h.t = t
		c := credentials{"new@example.com", "password123"}
		h.expect(h.call("POST", "/auth/register", "", "", c), 201)
		h.expect(h.call("POST", "/auth/register", "", "", credentials{"NEW@example.com", "password123"}), 409)
		b := h.expect(h.call("POST", "/auth/login", "", "", c), 200)
		refresh := value[string](t, b["refresh_token"])
		actions := parallel(func() *httptest.ResponseRecorder {
			return h.call("POST", "/auth/refresh", "", "", map[string]string{"refresh_token": refresh})
		}, func() *httptest.ResponseRecorder {
			return h.call("POST", "/auth/refresh", "", "", map[string]string{"refresh_token": refresh})
		})
		if actions[0].Code+actions[1].Code != 601 {
			t.Fatalf("rotation codes %d,%d", actions[0].Code, actions[1].Code)
		}
		for _, w := range actions {
			if w.Code == 200 {
				tokens := h.expect(w, 200)
				h.expect(h.call("GET", "/wallets", value[string](t, tokens["access_token"]), "", nil), 200)
			}
		}
		h.expect(h.call("POST", "/auth/login", "", "", credentials{c.Email, "wrongpassword"}), 401)
		claims := jwt.RegisteredClaims{Issuer: issuer, Subject: "1", Audience: jwt.ClaimStrings{audience}, ExpiresAt: jwt.NewNumericDate(time.Now().Add(-time.Hour))}
		expired, _ := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(h.a.JWTSecret)
		h.expect(h.call("GET", "/wallets", expired, "", nil), 401)
		claims.ExpiresAt = jwt.NewNumericDate(time.Now().Add(time.Hour))
		wrong, _ := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte("wrong"))
		h.expect(h.call("GET", "/wallets", wrong, "", nil), 401)
	})
	t.Run("concurrent spending and duplicate keys", func(t *testing.T) {
		h.t = t
		source, b, c := h.wallet(alice), h.wallet(bob), h.wallet(bob)
		h.expect(h.deposit(source, 100000, "race-funding"), 200)
		out := parallel(func() *httptest.ResponseRecorder {
			return h.call("POST", "/transfers", alice, "race-a", movement{source, b, 80000})
		}, func() *httptest.ResponseRecorder {
			return h.call("POST", "/transfers", alice, "race-b", movement{source, c, 80000})
		})
		if out[0].Code+out[1].Code != 610 {
			t.Fatalf("overspend statuses %d/%d: %s %s", out[0].Code, out[1].Code, out[0].Body, out[1].Body)
		}
		h.balance(alice, source, 20000)
		out = parallel(func() *httptest.ResponseRecorder {
			return h.call("POST", "/transfers", alice, "same-key", movement{source, b, 10000})
		}, func() *httptest.ResponseRecorder {
			return h.call("POST", "/transfers", alice, "same-key", movement{source, b, 10000})
		})
		for _, w := range out {
			h.expect(w, 201)
		}
		if out[0].Body.String() != out[1].Body.String() {
			t.Fatal("duplicate response mismatch")
		}
		h.balance(alice, source, 10000)
		// Opposite directions acquire wallet locks in the same order.
		h.expect(h.deposit(b, 10000, "opposite-funding"), 200)
		out = parallel(func() *httptest.ResponseRecorder {
			return h.call("POST", "/transfers", alice, "opposite-a", movement{source, b, 1000})
		}, func() *httptest.ResponseRecorder {
			return h.call("POST", "/transfers", bob, "opposite-b", movement{b, source, 1000})
		})
		for _, w := range out {
			h.expect(w, 201)
		}
		h.balance(alice, source, 10000)
	})
	t.Run("webhook HMAC replay and overflow", func(t *testing.T) {
		h.t = t
		id := h.wallet(alice)
		body, _ := json.Marshal(DepositEvent{"webhook-race", "deposit.success", id, 100})
		h.expect(h.raw("POST", "/webhooks/mock-provider", "", "", body, "00"), 401)
		h.balance(alice, id, 0)
		out := parallel(func() *httptest.ResponseRecorder { return h.deposit(id, 100, "webhook-race") }, func() *httptest.ResponseRecorder { return h.deposit(id, 100, "webhook-race") })
		for _, w := range out {
			h.expect(w, 200)
		}
		h.balance(alice, id, 100)
		h.expect(h.deposit(id, 101, "webhook-race"), 409)
		h.expect(h.deposit(id, math.MaxInt64, "overflow"), 409)
		h.balance(alice, id, 100)
	})
	t.Run("reversal races and insufficient funds", func(t *testing.T) {
		h.t = t
		s, d := h.wallet(alice), h.wallet(bob)
		dep := h.expect(h.deposit(s, 100, "reverse-funding"), 200)
		b := h.expect(h.call("POST", "/transfers", alice, "drain", movement{s, d, 100}), 201)
		id := value[int64](t, b["id"])
		h.expect(h.call("POST", fmt.Sprintf("/admin/transactions/%d/reverse", value[int64](t, dep["id"])), admin, "refund-empty", map[string]string{"reason": "refund"}), 409)
		path := fmt.Sprintf("/admin/transactions/%d/reverse", id)
		out := parallel(func() *httptest.ResponseRecorder {
			return h.call("POST", path, admin, "reverse-race-a", map[string]string{"reason": "refund"})
		}, func() *httptest.ResponseRecorder {
			return h.call("POST", path, admin, "reverse-race-b", map[string]string{"reason": "refund"})
		})
		if out[0].Code+out[1].Code != 610 {
			t.Fatalf("double reversal: %d,%d", out[0].Code, out[1].Code)
		}
		h.balance(alice, s, 100)
		h.balance(bob, d, 0)
		// Either spending or refund wins; the loser cannot debit the same funds.
		out = parallel(func() *httptest.ResponseRecorder {
			return h.call("POST", "/transfers", alice, "spend-vs-refund", movement{s, d, 100})
		}, func() *httptest.ResponseRecorder {
			return h.call("POST", fmt.Sprintf("/admin/transactions/%d/reverse", value[int64](t, dep["id"])), admin, "refund-vs-spend", map[string]string{"reason": "refund"})
		})
		if out[0].Code+out[1].Code != 610 {
			t.Fatalf("spend/refund: %d,%d", out[0].Code, out[1].Code)
		}
		h.balance(alice, s, 0)
	})
	t.Run("atomicity constraints and immutability", func(t *testing.T) {
		h.t = t
		ctx := context.Background()
		s, d := h.wallet(alice), h.wallet(bob)
		h.expect(h.deposit(s, 100, "atomic-funding"), 200)
		_, err := h.owner.Exec(ctx, `CREATE FUNCTION test_fail_credit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.entry_type='credit' AND NEW.wallet_id=`+fmt.Sprint(d)+` THEN RAISE EXCEPTION 'injected credit failure'; END IF; RETURN NEW; END $$;
		CREATE TRIGGER fail_credit BEFORE INSERT ON ledger_entries FOR EACH ROW EXECUTE FUNCTION test_fail_credit()`)
		if err != nil {
			t.Fatal(err)
		}
		failed := h.call("POST", "/transfers", alice, "atomic", movement{s, d, 100})
		h.expect(failed, 503)
		h.balance(alice, s, 100)
		h.balance(bob, d, 0)
		var n int
		if err = h.owner.QueryRow(ctx, `SELECT count(*) FROM idempotency_keys WHERE key='atomic'`).Scan(&n); err != nil || n != 0 {
			t.Fatal("key persisted after rollback", n, err)
		}
		if _, err = h.owner.Exec(ctx, `DROP TRIGGER fail_credit ON ledger_entries; DROP FUNCTION test_fail_credit()`); err != nil {
			t.Fatal(err)
		}
		h.expect(h.call("POST", "/transfers", alice, "atomic", movement{s, d, 100}), 201)
		tx, err := h.a.begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var id int64
		if err = tx.QueryRow(ctx, `INSERT INTO transactions(transaction_type) VALUES('transfer') RETURNING id`).Scan(&id); err != nil {
			t.Fatal(err)
		}
		if _, err = tx.Exec(ctx, `INSERT INTO ledger_entries(transaction_id,wallet_id,entry_type,amount) VALUES($1,$2,'debit',1)`, id, s); err != nil {
			t.Fatal(err)
		}
		if err = tx.Commit(ctx); err == nil {
			t.Fatal("unbalanced transaction committed")
		}
		if _, err = h.owner.Exec(ctx, `UPDATE ledger_entries SET amount=amount WHERE wallet_id=$1`, s); err == nil {
			t.Fatal("ledger update allowed")
		}
		if _, err = h.owner.Exec(ctx, `DELETE FROM ledger_entries WHERE wallet_id=$1`, s); err == nil {
			t.Fatal("ledger delete allowed")
		}
	})
	t.Run("reconciliation detects corrupt fixture", func(t *testing.T) {
		h.t = t
		ctx := context.Background()
		out, err := h.a.Reconcile(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var check struct {
			Status string `json:"status"`
		}
		json.Unmarshal(out.Body, &check)
		if check.Status != "pass" {
			t.Fatal(string(out.Body))
		}
		// Only this isolated test DB's superuser bypasses triggers to simulate corruption.
		tx, err := h.owner.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback(ctx)
		if _, err = tx.Exec(ctx, "SET LOCAL session_replication_role='replica'"); err != nil {
			t.Fatal(err)
		}
		var id int64
		if err = tx.QueryRow(ctx, `INSERT INTO transactions(transaction_type) VALUES('transfer') RETURNING id`).Scan(&id); err != nil {
			t.Fatal(err)
		}
		if _, err = tx.Exec(ctx, `INSERT INTO ledger_entries(transaction_id,wallet_id,entry_type,amount) VALUES($1,$2,'debit',1)`, id, wb); err != nil {
			t.Fatal(err)
		}
		if err = tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		out, err = h.a.Reconcile(ctx)
		if err != nil {
			t.Fatal(err)
		}
		json.Unmarshal(out.Body, &check)
		if check.Status != "fail" || !bytes.Contains(out.Body, []byte("transaction")) {
			t.Fatal(string(out.Body))
		}
	})
}
