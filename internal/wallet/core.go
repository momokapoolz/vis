package wallet

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"strconv"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type App struct {
	Pool          *pgxpool.Pool
	JWTSecret     []byte
	WebhookSecret []byte
}

type User struct {
	ID    int64  `json:"id"`
	Email string `json:"email"`
	Role  string `json:"role"`
}

type Problem struct {
	Status        int
	Code, Message string
}

func (e *Problem) Error() string                     { return e.Message }
func problem(status int, code, message string) error { return &Problem{status, code, message} }
func invalid(message string) error                   { return problem(400, "invalid_request", message) }
func conflict(code, message string) error            { return problem(409, code, message) }
func notFound() error                                { return problem(404, "not_found", "Resource not found") }

type Result struct {
	Status int
	Body   []byte
}

func result(status int, value any) Result {
	body, err := json.Marshal(value)
	if err != nil {
		panic(err)
	} // All callers supply JSON-safe values.
	return Result{status, body}
}
func failure(err error) Result {
	var p *Problem
	if !errors.As(err, &p) {
		p = &Problem{http.StatusServiceUnavailable, "temporarily_unavailable", "Request failed; retry with the same key"}
	}
	return result(p.Status, map[string]any{"error": map[string]string{"code": p.Code, "message": p.Message}})
}

type movement struct {
	Source      int64 `json:"source_wallet_id"`
	Destination int64 `json:"destination_wallet_id"`
	Amount      int64 `json:"amount"`
}
type lockedWallet struct {
	ID           int64
	Owner        *int64
	Kind, Status string
}

func (a *App) begin(ctx context.Context) (pgx.Tx, error) {
	tx, err := a.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return nil, err
	}
	if _, err = tx.Exec(ctx, "SET LOCAL lock_timeout = '5s'"); err != nil {
		tx.Rollback(context.Background())
		return nil, err
	}
	return tx, nil
}

func balance(ctx context.Context, tx pgx.Tx, id int64) (int64, error) {
	var raw string
	err := tx.QueryRow(ctx, `SELECT coalesce(sum(CASE WHEN entry_type='credit' THEN amount::numeric ELSE -amount::numeric END),0)::text
		FROM ledger_entries WHERE wallet_id=$1`, id).Scan(&raw)
	if err != nil {
		return 0, err
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, conflict("balance_overflow", "Balance exceeds supported integer range")
	}
	return n, nil
}

// post is the only application path that inserts financial transactions or entries.
// Caller owns the DB transaction, including its idempotency/event record.
func (a *App) post(ctx context.Context, tx pgx.Tx, m movement, kind string, actor *int64, original *int64, reference, reason string) (Result, error) {
	if m.Amount <= 0 || m.Source <= 0 || m.Destination <= 0 || m.Source == m.Destination {
		return Result{}, invalid("Positive amount and two different wallets are required")
	}
	rows, err := tx.Query(ctx, `SELECT id,user_id,kind,status FROM wallets WHERE id=ANY($1::bigint[]) ORDER BY id FOR UPDATE`, []int64{m.Source, m.Destination})
	if err != nil {
		return Result{}, err
	}
	wallets, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (lockedWallet, error) {
		var w lockedWallet
		err := row.Scan(&w.ID, &w.Owner, &w.Kind, &w.Status)
		return w, err
	})
	if err != nil {
		return Result{}, err
	}
	if len(wallets) != 2 {
		return Result{}, notFound()
	}
	var source, destination lockedWallet
	for _, w := range wallets {
		if w.Status != "active" {
			return Result{}, conflict("wallet_blocked", "Wallet is not active")
		}
		if w.ID == m.Source {
			source = w
		} else {
			destination = w
		}
	}
	if kind == "transfer" && (actor == nil || source.Owner == nil || *source.Owner != *actor || destination.Kind != "customer") {
		return Result{}, problem(403, "forbidden", "Transfer requires your source wallet and a customer destination")
	}
	if kind == "deposit" && (source.Kind != "system" || destination.Kind != "customer") {
		return Result{}, invalid("Deposit destination must be a customer wallet")
	}
	// Read AFTER acquiring both locks: a separate READ COMMITTED statement sees the preceding writer's commit.
	sourceBalance, err := balance(ctx, tx, m.Source)
	if err != nil {
		return Result{}, err
	}
	destinationBalance, err := balance(ctx, tx, m.Destination)
	if err != nil {
		return Result{}, err
	}
	if source.Kind == "customer" && sourceBalance < m.Amount {
		return Result{}, conflict("insufficient_funds", "Insufficient funds")
	}
	if sourceBalance < math.MinInt64+m.Amount || destinationBalance > math.MaxInt64-m.Amount {
		return Result{}, conflict("balance_overflow", "Movement exceeds supported balance range")
	}
	var id int64
	err = tx.QueryRow(ctx, `INSERT INTO transactions (transaction_type,actor_user_id,reversed_transaction_id,reference_id,reason)
		VALUES ($1,$2,$3,NULLIF($4,''),NULLIF($5,'')) RETURNING id`, kind, actor, original, reference, reason).Scan(&id)
	if err != nil {
		return Result{}, err
	}
	// Separate statements also make rollback after a failed credit directly testable.
	if _, err = tx.Exec(ctx, `INSERT INTO ledger_entries(transaction_id,wallet_id,entry_type,amount) VALUES($1,$2,'debit',$3)`, id, m.Source, m.Amount); err != nil {
		return Result{}, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO ledger_entries(transaction_id,wallet_id,entry_type,amount) VALUES($1,$2,'credit',$3)`, id, m.Destination, m.Amount); err != nil {
		return Result{}, err
	}
	return result(201, map[string]any{"id": id, "transaction_type": kind, "status": "posted", "source_wallet_id": m.Source, "destination_wallet_id": m.Destination, "amount": m.Amount, "currency": "VND", "reversed_transaction_id": original}), nil
}

func (a *App) reverse(ctx context.Context, tx pgx.Tx, user User, id int64, reason string) (Result, error) {
	var kind string
	err := tx.QueryRow(ctx, `SELECT transaction_type FROM transactions WHERE id=$1 FOR UPDATE`, id).Scan(&kind)
	if errors.Is(err, pgx.ErrNoRows) {
		return Result{}, notFound()
	}
	if err != nil {
		return Result{}, err
	}
	if kind == "reversal" {
		return Result{}, conflict("invalid_reversal", "Cannot reverse a reversal")
	}
	var exists bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM transactions WHERE reversed_transaction_id=$1)`, id).Scan(&exists); err != nil {
		return Result{}, err
	}
	if exists {
		return Result{}, conflict("already_reversed", "Transaction was already reversed")
	}
	var m movement
	err = tx.QueryRow(ctx, `SELECT d.wallet_id,c.wallet_id,d.amount FROM ledger_entries d JOIN ledger_entries c USING(transaction_id)
		WHERE d.transaction_id=$1 AND d.entry_type='credit' AND c.entry_type='debit'`, id).Scan(&m.Source, &m.Destination, &m.Amount)
	if err != nil {
		return Result{}, err
	}
	return a.post(ctx, tx, m, "reversal", &user.ID, &id, "", reason)
}

type Anomaly struct {
	Kind   string `json:"kind"`
	ID     int64  `json:"id"`
	Detail string `json:"detail"`
}
type Report struct {
	Status    string    `json:"status"`
	Anomalies []Anomaly `json:"anomalies"`
}

func reconcile(ctx context.Context, tx pgx.Tx, actor *int64) (Result, error) {
	// A single SQL statement gives every integrity check the same MVCC snapshot.
	rows, err := tx.Query(ctx, `WITH totals AS (
		SELECT t.id,count(e.id) n,count(DISTINCT e.entry_type) sides,count(DISTINCT e.wallet_id) wallets,
		coalesce(sum(CASE WHEN e.entry_type='credit' THEN e.amount::numeric ELSE -e.amount::numeric END),0) delta
		FROM transactions t LEFT JOIN ledger_entries e ON e.transaction_id=t.id GROUP BY t.id
	), balances AS (
		SELECT w.id,coalesce(sum(CASE WHEN e.entry_type='credit' THEN e.amount::numeric ELSE -e.amount::numeric END),0) amount
		FROM wallets w LEFT JOIN ledger_entries e ON e.wallet_id=w.id WHERE w.kind='customer' GROUP BY w.id
	)
	SELECT 'transaction',id,'entries='||n||', delta='||delta FROM totals WHERE n<>2 OR sides<>2 OR wallets<>2 OR delta<>0
	UNION ALL SELECT 'negative_balance',id,amount::text FROM balances WHERE amount<0
	UNION ALL SELECT 'global_balance',0,sum(delta)::text FROM totals HAVING sum(delta)<>0`)
	if err != nil {
		return Result{}, err
	}
	anomalies, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Anomaly, error) {
		var a Anomaly
		e := row.Scan(&a.Kind, &a.ID, &a.Detail)
		return a, e
	})
	if err != nil {
		return Result{}, err
	}
	if anomalies == nil {
		anomalies = []Anomaly{}
	}
	report := Report{"pass", anomalies}
	if len(anomalies) > 0 {
		report.Status = "fail"
	}
	body, err := json.Marshal(report)
	if err != nil {
		return Result{}, err
	}
	var id int64
	err = tx.QueryRow(ctx, `INSERT INTO reconciliation_runs(actor_user_id,status,report) VALUES($1,$2,$3) RETURNING id`, actor, report.Status, body).Scan(&id)
	return result(201, map[string]any{"id": id, "status": report.Status, "report": report}), err
}

func (a *App) Reconcile(ctx context.Context) (Result, error) {
	tx, err := a.begin(ctx)
	if err != nil {
		return Result{}, err
	}
	defer tx.Rollback(context.Background())
	r, err := reconcile(ctx, tx, nil)
	if err != nil {
		return Result{}, err
	}
	err = tx.Commit(ctx)
	return r, err
}
