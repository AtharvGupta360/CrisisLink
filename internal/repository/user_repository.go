// Package repository is the data-access layer: each type owns the SQL for one
// table and holds the pgx pool. Services depend on repositories, never the
// reverse. Repositories contain NO business logic — just persistence.
package repository

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/AtharvGupta360/CrisisLink/internal/models"
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
func (r *UserRepository) Create(ctx context.Context, u *models.User) error {
	const query = `
		INSERT INTO users (username, email, password, role)
		VALUES ($1, $2, $3, $4)
		RETURNING id::text, created_at, updated_at`
	return r.pool.QueryRow(ctx, query, u.Username, u.Email, u.Password, u.Role).
		Scan(&u.ID, &u.CreatedAt, &u.UpdatedAt)
}

// GetByEmail loads a user by email (login path). Returns pgx.ErrNoRows if absent.
func (r *UserRepository) GetByEmail(ctx context.Context, email string) (*models.User, error) {
	const query = `
		SELECT id::text, username, email, password, role, created_at, updated_at
		FROM users WHERE email = $1`
	var u models.User
	err := r.pool.QueryRow(ctx, query, email).
		Scan(&u.ID, &u.Username, &u.Email, &u.Password, &u.Role, &u.CreatedAt, &u.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &u, nil
}

// GetByID loads a user by id (used later by /me and RBAC lookups).
func (r *UserRepository) GetByID(ctx context.Context, id string) (*models.User, error) {
	const query = `
		SELECT id::text, username, email, password, role, created_at, updated_at
		FROM users WHERE id = $1::uuid`
	var u models.User
	err := r.pool.QueryRow(ctx, query, id).
		Scan(&u.ID, &u.Username, &u.Email, &u.Password, &u.Role, &u.CreatedAt, &u.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &u, nil
}
