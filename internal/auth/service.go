package auth

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

var (
	ErrCredentials = errors.New("invalid credentials")
	ErrRateLimited = errors.New("login temporarily rate limited")
	ErrIdentity    = errors.New("username and email must be nonempty and unique")
)

type User struct {
	ID                 int64
	Username           string
	Email              string
	IsAdmin            bool
	IsActive           bool
	MustChangePassword bool
	Lang               string
	Timezone           string
}

type Service struct {
	db        *sql.DB
	limiter   *LoginLimiter
	dummyHash string
}

func NewService(db *sql.DB) (*Service, error) {
	hash, err := HashPassword("dummy-password-not-an-account")
	if err != nil {
		return nil, fmt.Errorf("initialize password verifier: %w", err)
	}
	return &Service{db: db, limiter: NewLoginLimiter(), dummyHash: hash}, nil
}

type CreateUserParams struct {
	Username           string
	Email              string
	Password           string
	IsAdmin            bool
	MustChangePassword bool
	Lang               string
	Timezone           string
}

// CreateUser atomically creates an active account and its reserved defaults.
// Setup supplies its chosen password; admin/reset callers use RandomPassword.
func (s *Service) CreateUser(ctx context.Context, p CreateUserParams, now time.Time) (*User, error) {
	p.Username, p.Email = strings.TrimSpace(p.Username), strings.TrimSpace(p.Email)
	if p.Username == "" || p.Email == "" {
		return nil, ErrIdentity
	}
	if err := validatePreferences(p.Lang, p.Timezone); err != nil {
		return nil, err
	}
	hash, err := HashPassword(p.Password)
	if err != nil {
		return nil, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("create user: %w", err)
	}
	defer tx.Rollback()
	u, err := insertUser(ctx, tx, p, hash, now)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit user: %w", err)
	}
	return u, nil
}

// insertUser expects trimmed, nonempty identities and an already computed
// hash, so callers avoid running bcrypt while holding the write lock.
func insertUser(ctx context.Context, tx *sql.Tx, p CreateUserParams, hash string, now time.Time) (*User, error) {
	// Also prevent username/email cross-collisions, so login is unambiguous.
	var exists bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(
		SELECT 1 FROM users WHERE username IN (?, ?) OR email IN (?, ?))`,
		p.Username, p.Email, p.Username, p.Email).Scan(&exists); err != nil {
		return nil, fmt.Errorf("check user identity: %w", err)
	}
	if exists {
		return nil, ErrIdentity
	}
	res, err := tx.ExecContext(ctx, `INSERT INTO users
		(username, email, password_hash, is_admin, must_change_password, lang, timezone, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, NULLIF(?, ''), NULLIF(?, ''), ?, ?)`,
		p.Username, p.Email, hash, p.IsAdmin, p.MustChangePassword, p.Lang, p.Timezone, now.Unix(), now.Unix())
	if err != nil {
		return nil, fmt.Errorf("insert user: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, fmt.Errorf("read user id: %w", err)
	}
	for _, query := range []string{
		`INSERT INTO collections (owner_id, name, is_reserved, created_at) VALUES (?, 'Default', 1, ?)`,
		`INSERT INTO tags (owner_id, name, is_reserved, created_at) VALUES (?, 'Default', 1, ?)`,
	} {
		if _, err := tx.ExecContext(ctx, query, id, now.Unix()); err != nil {
			return nil, fmt.Errorf("create reserved default: %w", err)
		}
	}
	return &User{ID: id, Username: p.Username, Email: p.Email, IsAdmin: p.IsAdmin,
		IsActive: true, MustChangePassword: p.MustChangePassword, Lang: p.Lang, Timezone: p.Timezone}, nil
}

// Authenticate applies the same error and bcrypt work to unknown and inactive users.
// ip must be the peer IP, not an untrusted forwarding header.
func (s *Service) Authenticate(ctx context.Context, ip, account, password string, now time.Time) (*User, error) {
	account = strings.TrimSpace(account)
	if !s.limiter.Allow(ip, account, now) {
		return nil, ErrRateLimited
	}
	var u User
	var hash string
	err := s.db.QueryRowContext(ctx, `SELECT id, username, email, password_hash,
		is_admin, is_active, must_change_password, COALESCE(lang, ''), COALESCE(timezone, '')
		FROM users WHERE username = ? OR email = ?`, account, account).Scan(
		&u.ID, &u.Username, &u.Email, &hash, &u.IsAdmin, &u.IsActive,
		&u.MustChangePassword, &u.Lang, &u.Timezone)
	missing := errors.Is(err, sql.ErrNoRows)
	if err != nil && !missing {
		return nil, fmt.Errorf("authenticate user: %w", err)
	}
	if missing {
		hash = s.dummyHash
	}
	ok, err := CheckPassword(hash, password)
	if err != nil {
		return nil, fmt.Errorf("verify password hash: %w", err)
	}
	if missing || !ok || !u.IsActive {
		s.limiter.Failure(ip, account, now)
		return nil, ErrCredentials
	}
	s.limiter.Success(ip, account)
	return &u, nil
}
