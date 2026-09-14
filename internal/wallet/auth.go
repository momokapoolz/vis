package wallet

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/http"
	"net/mail"
	"strconv"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"golang.org/x/crypto/bcrypt"
)

const issuer = "wallet-poc"
const audience = "wallet-api"

type credentials struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

func normalizeCredentials(c *credentials) error {
	c.Email = strings.ToLower(strings.TrimSpace(c.Email))
	address, err := mail.ParseAddress(c.Email)
	if err != nil || address.Address != c.Email || len(c.Email) > 254 {
		return invalid("A valid email is required")
	}
	if len(c.Password) < 8 || len(c.Password) > 72 {
		return invalid("Password must be 8-72 bytes")
	}
	return nil
}

func (a *App) authenticate(r *http.Request) (User, error) {
	var user User
	header := r.Header.Get("Authorization")
	if !strings.HasPrefix(header, "Bearer ") {
		return user, problem(401, "unauthorized", "Bearer token required")
	}
	claims := new(jwt.RegisteredClaims)
	_, err := jwt.ParseWithClaims(strings.TrimPrefix(header, "Bearer "), claims, func(t *jwt.Token) (any, error) { return a.JWTSecret, nil },
		jwt.WithValidMethods([]string{"HS256"}), jwt.WithIssuer(issuer), jwt.WithAudience(audience), jwt.WithExpirationRequired(), jwt.WithIssuedAt())
	if err != nil {
		return user, problem(401, "unauthorized", "Invalid or expired access token")
	}
	id, err := strconv.ParseInt(claims.Subject, 10, 64)
	if err != nil || id <= 0 {
		return user, problem(401, "unauthorized", "Invalid token subject")
	}
	err = a.Pool.QueryRow(r.Context(), `SELECT id,email,role FROM users WHERE id=$1`, id).Scan(&user.ID, &user.Email, &user.Role)
	if errors.Is(err, pgx.ErrNoRows) {
		return user, problem(401, "unauthorized", "User no longer exists")
	}
	return user, err
}

func (a *App) tokens(ctx context.Context, tx pgx.Tx, userID int64) (Result, error) {
	now := time.Now()
	claims := jwt.RegisteredClaims{Issuer: issuer, Subject: strconv.FormatInt(userID, 10), Audience: jwt.ClaimStrings{audience}, IssuedAt: jwt.NewNumericDate(now), ExpiresAt: jwt.NewNumericDate(now.Add(15 * time.Minute))}
	access, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(a.JWTSecret)
	if err != nil {
		return Result{}, err
	}
	random := make([]byte, 32)
	if _, err = rand.Read(random); err != nil {
		return Result{}, err
	}
	refresh := hex.EncodeToString(random)
	_, err = tx.Exec(ctx, `INSERT INTO refresh_tokens(user_id,token_digest,expires_at) VALUES($1,$2,$3)`, userID, digest([]byte(refresh)), now.Add(7*24*time.Hour))
	return result(200, map[string]any{"access_token": access, "refresh_token": refresh, "token_type": "Bearer", "expires_in": 900}), err
}

func (a *App) register(r *http.Request) (Result, error) {
	var c credentials
	if err := decode(r, &c); err != nil {
		return Result{}, err
	}
	if err := normalizeCredentials(&c); err != nil {
		return Result{}, err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(c.Password), bcrypt.DefaultCost)
	if err != nil {
		return Result{}, err
	}
	var user User
	err = a.Pool.QueryRow(r.Context(), `INSERT INTO users(email,password_digest) VALUES($1,$2) RETURNING id,email,role`, c.Email, string(hash)).Scan(&user.ID, &user.Email, &user.Role)
	var p *pgconn.PgError
	if errors.As(err, &p) && p.Code == "23505" {
		return Result{}, conflict("email_exists", "Email is already registered")
	}
	return result(201, user), err
}

// Same-cost dummy hash avoids making unknown emails cheap to distinguish.
const dummyHash = "$2a$10$N9qo8uLOickgx2ZMRZoMyeIjZAgcfl7p92ldGxad68LJZdL17lhWy"

func (a *App) login(r *http.Request) (Result, error) {
	var c credentials
	if err := decode(r, &c); err != nil {
		return Result{}, err
	}
	if err := normalizeCredentials(&c); err != nil {
		return Result{}, err
	}
	var id int64
	var hash string
	err := a.Pool.QueryRow(r.Context(), `SELECT id,password_digest FROM users WHERE email=$1`, c.Email).Scan(&id, &hash)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return Result{}, err
	}
	if errors.Is(err, pgx.ErrNoRows) {
		hash = dummyHash
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(c.Password)) != nil || id == 0 {
		return Result{}, problem(401, "invalid_credentials", "Invalid email or password")
	}
	tx, err := a.begin(r.Context())
	if err != nil {
		return Result{}, err
	}
	defer tx.Rollback(context.Background())
	out, err := a.tokens(r.Context(), tx, id)
	if err != nil {
		return Result{}, err
	}
	err = tx.Commit(r.Context())
	return out, err
}

func (a *App) refresh(r *http.Request) (Result, error) {
	var body struct {
		Token string `json:"refresh_token"`
	}
	if err := decode(r, &body); err != nil {
		return Result{}, err
	}
	if len(body.Token) != 64 {
		return Result{}, problem(401, "invalid_refresh_token", "Invalid refresh token")
	}
	tx, err := a.begin(r.Context())
	if err != nil {
		return Result{}, err
	}
	defer tx.Rollback(context.Background())
	var id int64
	err = tx.QueryRow(r.Context(), `UPDATE refresh_tokens SET revoked_at=now() WHERE token_digest=$1 AND revoked_at IS NULL AND expires_at>now() RETURNING user_id`, digest([]byte(body.Token))).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return Result{}, problem(401, "invalid_refresh_token", "Expired or already used refresh token")
	}
	if err != nil {
		return Result{}, err
	}
	out, err := a.tokens(r.Context(), tx, id)
	if err != nil {
		return Result{}, err
	}
	err = tx.Commit(r.Context())
	return out, err
}

func (a *App) SeedUser(ctx context.Context, email, password, role string) (User, error) {
	if role != "customer" && role != "support" && role != "admin" {
		return User{}, invalid("Invalid seed role")
	}
	c := credentials{email, password}
	if err := normalizeCredentials(&c); err != nil {
		return User{}, err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return User{}, err
	}
	_, err = a.Pool.Exec(ctx, `INSERT INTO users(email,password_digest,role) VALUES($1,$2,$3) ON CONFLICT(email) DO NOTHING`, c.Email, string(hash), role)
	if err != nil {
		return User{}, err
	}
	var u User
	err = a.Pool.QueryRow(ctx, `SELECT id,email,role FROM users WHERE email=$1`, c.Email).Scan(&u.ID, &u.Email, &u.Role)
	if err == nil && u.Role != role {
		return u, conflict("seed_role_mismatch", "Existing user has a different role")
	}
	return u, err
}
