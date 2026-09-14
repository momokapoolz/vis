package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"wallet/internal/db"
	"wallet/internal/wallet"
)

func required(name string) (string, error) {
	v := os.Getenv(name)
	if v == "" {
		return "", fmt.Errorf("%s is required", name)
	}
	return v, nil
}
func run() error {
	command := "serve"
	if len(os.Args) > 1 {
		command = os.Args[1]
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if command == "migrate" {
		url, err := required("MIGRATION_DATABASE_URL")
		if err != nil {
			return err
		}
		return db.Migrate(url)
	}
	if command == "mock-deposit" {
		return deposit(ctx)
	}
	if command != "serve" && command != "seed" && command != "reconcile" {
		return fmt.Errorf("usage: wallet [serve|migrate|seed|mock-deposit|reconcile]")
	}
	// Privileged seeding is a local CLI operation, never an HTTP role assignment API.
	databaseVariable := "DATABASE_URL"
	if command == "seed" {
		databaseVariable = "MIGRATION_DATABASE_URL"
	}
	url, err := required(databaseVariable)
	if err != nil {
		return err
	}
	config, err := pgxpool.ParseConfig(url)
	if err != nil {
		return err
	}
	config.MaxConns = 10
	config.ConnConfig.ConnectTimeout = 5 * time.Second
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return err
	}
	defer pool.Close()
	if err = pool.Ping(ctx); err != nil {
		return err
	}
	a := &wallet.App{Pool: pool}
	if command == "seed" {
		email, err := required("SEED_EMAIL")
		if err != nil {
			return err
		}
		password, err := required("SEED_PASSWORD")
		if err != nil {
			return err
		}
		role, err := required("SEED_ROLE")
		if err != nil {
			return err
		}
		u, err := a.SeedUser(ctx, email, password, role)
		if err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(u)
	}
	if command == "reconcile" {
		ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		defer cancel()
		out, err := a.Reconcile(ctx)
		if err != nil {
			return err
		}
		fmt.Println(string(out.Body))
		var report struct {
			Status string `json:"status"`
		}
		if err = json.Unmarshal(out.Body, &report); err != nil {
			return err
		}
		if report.Status != "pass" {
			return errors.New("reconciliation found anomalies")
		}
		return nil
	}
	jwtSecret, err := required("JWT_SECRET")
	if err != nil {
		return err
	}
	webhookSecret, err := required("WEBHOOK_SECRET")
	if err != nil {
		return err
	}
	if len(jwtSecret) < 32 || len(webhookSecret) < 32 || jwtSecret == webhookSecret {
		return errors.New("JWT_SECRET and WEBHOOK_SECRET must be distinct and at least 32 bytes")
	}
	a.JWTSecret = []byte(jwtSecret)
	a.WebhookSecret = []byte(webhookSecret)
	addr := os.Getenv("LISTEN_ADDR")
	if addr == "" {
		addr = ":8080"
	}
	server := &http.Server{Addr: addr, Handler: a.Handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 20 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdown); err != nil {
			slog.Error("shutdown failed", "error", err)
		}
	}()
	slog.Info("wallet API listening", "address", addr)
	err = server.ListenAndServe()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func deposit(ctx context.Context) error {
	if len(os.Args) != 5 {
		return errors.New("usage: wallet mock-deposit WALLET_ID AMOUNT EVENT_ID")
	}
	id, err := strconv.ParseInt(os.Args[2], 10, 64)
	if err != nil || id <= 0 {
		return errors.New("invalid wallet ID")
	}
	amount, err := strconv.ParseInt(os.Args[3], 10, 64)
	if err != nil || amount <= 0 {
		return errors.New("invalid amount")
	}
	secret, err := required("WEBHOOK_SECRET")
	if err != nil {
		return err
	}
	body, err := json.Marshal(wallet.DepositEvent{EventID: os.Args[4], Event: "deposit.success", WalletID: id, Amount: amount})
	if err != nil {
		return err
	}
	base := os.Getenv("API_URL")
	if base == "" {
		base = "http://localhost:8080"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/webhooks/mock-provider", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Signature", wallet.Sign([]byte(secret), body))
	client := &http.Client{Timeout: 20 * time.Second}
	response, err := client.Do(req)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if _, err = io.Copy(os.Stdout, io.LimitReader(response.Body, 1<<20)); err != nil {
		return err
	}
	fmt.Println()
	if response.StatusCode >= 300 {
		return fmt.Errorf("deposit failed: HTTP %d", response.StatusCode)
	}
	return nil
}

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, nil)))
	if err := run(); err != nil {
		slog.Error("wallet command failed", "error", err)
		os.Exit(1)
	}
}
