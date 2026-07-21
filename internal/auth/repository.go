// Package repository is the data-access layer: each type owns the SQL for one
// table and holds the pgx pool. Services depend on repositories, never the
// reverse. Repositories contain NO business logic — just persistence.
package auth

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
)

type UserRepository struct {
	pool *pgxpool.Pool
}

func NewUserRepository(pool *pgxpool.Pool) *UserRepository {
	return &UserRepository{pool: pool}
}

// Create inserts a user and fills in the DB-generated fields (id, timestamps) via
// RETURNING. id is cast to text so it scans cleanly into the Go string field.
// We do NOT pre-check for duplicates here — the UNIQUE constraints on
// username/email are the race-free guard; the caller inspects the DB error.
func (r *UserRepository) Create(ctx context.Context, u *User) error {
	const query = `
		INSERT INTO users (username, email, password, role)
		VALUES ($1, $2, $3, $4)
		RETURNING id::text, created_at, updated_at`
	return r.pool.QueryRow(ctx, query, u.Username, u.Email, u.Password, u.Role).
		Scan(&u.ID, &u.CreatedAt, &u.UpdatedAt)
}

// GetByEmail loads a user by email (login path). Returns pgx.ErrNoRows if absent.
func (r *UserRepository) GetByEmail(ctx context.Context, email string) (*User, error) {
	const query = `
		SELECT id::text, username, email, password, role, unit_id::text, shelter_id::text, created_at, updated_at
		FROM users WHERE email = $1`
	var u User
	err := r.pool.QueryRow(ctx, query, email).
		Scan(&u.ID, &u.Username, &u.Email, &u.Password, &u.Role, &u.UnitID, &u.ShelterID, &u.CreatedAt, &u.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &u, nil
}

// GetByID loads a user by id (used later by /me and RBAC lookups).
func (r *UserRepository) GetByID(ctx context.Context, id string) (*User, error) {
	const query = `
		SELECT id::text, username, email, password, role, unit_id::text, shelter_id::text, created_at, updated_at
		FROM users WHERE id = $1::uuid`
	var u User
	err := r.pool.QueryRow(ctx, query, id).
		Scan(&u.ID, &u.Username, &u.Email, &u.Password, &u.Role, &u.UnitID, &u.ShelterID, &u.CreatedAt, &u.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &u, nil
}

// UpdateRoleAndBindings sets a user's role and their resource bindings.
//
// Both bindings are written every time, including to NULL, so demoting a responder
// to a citizen cannot leave a stale unit_id behind that a later promotion would
// silently resurrect. Authorization state must be fully replaced, never patched.
func (r *UserRepository) UpdateRoleAndBindings(ctx context.Context, userID, role string, unitID, shelterID *string) (*User, error) {
	const q = `
		UPDATE users SET role = $2, unit_id = $3::uuid, shelter_id = $4::uuid, updated_at = now()
		WHERE id = $1::uuid
		RETURNING id::text, username, email, password, role, unit_id::text, shelter_id::text, created_at, updated_at`
	var u User
	if err := r.pool.QueryRow(ctx, q, userID, role, unitID, shelterID).Scan(
		&u.ID, &u.Username, &u.Email, &u.Password, &u.Role, &u.UnitID, &u.ShelterID, &u.CreatedAt, &u.UpdatedAt,
	); err != nil {
		return nil, err
	}
	return &u, nil
}
