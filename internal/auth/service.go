package auth

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/AtharvGupta360/CrisisLink/internal/platform/config"
)

// Sentinel errors the handler maps to HTTP status codes.
var (
	ErrDuplicateUser      = errors.New("username or email already exists")
	ErrInvalidCredentials = errors.New("invalid credentials")
)

// Service holds the auth business logic. It depends on the repository (data) and
// the JWT config (for token issuance).
type Service struct {
	users  *UserRepository
	jwtCfg *config.JWTConfig
}

func NewService(users *UserRepository, jwtCfg *config.JWTConfig) *Service {
	return &Service{users: users, jwtCfg: jwtCfg}
}

// Register hashes the password, creates the user, and returns the user + a token.
// New users default to the 'citizen' role. Duplicate username/email is detected
// via the Postgres UNIQUE-violation error code (23505) — this is race-free,
// unlike a check-then-insert which two concurrent registrations could both pass.
func (s *Service) Register(ctx context.Context, username, email, password string) (*User, string, error) {
	hash, err := HashPassword(password)
	if err != nil {
		return nil, "", err
	}

	u := &User{
		Username: username,
		Email:    email,
		Password: hash,
		Role:     "citizen",
	}
	if err := s.users.Create(ctx, u); err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" { // unique_violation
			return nil, "", ErrDuplicateUser
		}
		return nil, "", fmt.Errorf("create user: %w", err)
	}

	token, err := GenerateToken(u.ID, u.Username, u.Role, s.jwtCfg)
	if err != nil {
		return nil, "", err
	}
	return u, token, nil
}

// Login verifies credentials and returns the user + a token. It returns the SAME
// ErrInvalidCredentials whether the email doesn't exist OR the password is wrong
// — never reveal which, or an attacker can enumerate registered emails.
func (s *Service) Login(ctx context.Context, email, password string) (*User, string, error) {
	u, err := s.users.GetByEmail(ctx, email)
	if err != nil {
		return nil, "", ErrInvalidCredentials
	}
	if !CheckPassword(u.Password, password) {
		return nil, "", ErrInvalidCredentials
	}

	token, err := GenerateToken(u.ID, u.Username, u.Role, s.jwtCfg)
	if err != nil {
		return nil, "", err
	}
	return u, token, nil
}
