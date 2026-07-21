package auth

import (
	"context"

	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/AtharvGupta360/CrisisLink/internal/platform/authz"
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
		Role:     authz.RoleCitizen,
	}
	if err := s.users.Create(ctx, u); err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" { // unique_violation
			return nil, "", ErrDuplicateUser
		}
		return nil, "", fmt.Errorf("create user: %w", err)
	}

	token, err := GenerateToken(u.ID, u.Username, u.Role, deref(u.UnitID), deref(u.ShelterID), s.jwtCfg)
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

	token, err := GenerateToken(u.ID, u.Username, u.Role, deref(u.UnitID), deref(u.ShelterID), s.jwtCfg)
	if err != nil {
		return nil, "", err
	}
	return u, token, nil
}

// deref flattens a nullable binding to the empty string the token uses for
// "not bound". Kept explicit so a nil pointer can never be mistaken for a match:
// authz.Actor treats "" as owning nothing.
func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

var (
	ErrInvalidRole    = errors.New("invalid role")
	ErrBindingMissing = errors.New("this role requires a resource binding")
	ErrUserNotFound   = errors.New("user not found")
)

// AssignRole changes a user's role and resource bindings. Admin-only (enforced by
// the route), and the ONLY way a privileged role is ever granted — registration
// always creates a citizen, so privilege escalation cannot happen through signup.
//
// The binding rules are validated here rather than trusted from the caller:
//   - responder MUST be bound to a unit, shelter_manager MUST be bound to a shelter,
//     otherwise Actor.OwnsUnit/OwnsShelter would always deny and the account would
//     be silently useless.
//   - every other role is FORCED to have no bindings, so a leftover unit_id can
//     never grant access after a demotion.
func (s *Service) AssignRole(ctx context.Context, userID, role string, unitID, shelterID *string) (*User, error) {
	if !authz.IsValid(role) {
		return nil, ErrInvalidRole
	}

	switch role {
	case authz.RoleResponder:
		if unitID == nil || *unitID == "" {
			return nil, ErrBindingMissing
		}
		shelterID = nil
	case authz.RoleShelterManager:
		if shelterID == nil || *shelterID == "" {
			return nil, ErrBindingMissing
		}
		unitID = nil
	default:
		unitID, shelterID = nil, nil
	}

	u, err := s.users.UpdateRoleAndBindings(ctx, userID, role, unitID, shelterID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrUserNotFound
		}
		return nil, err
	}
	return u, nil
}
