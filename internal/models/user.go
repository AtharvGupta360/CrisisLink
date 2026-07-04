package models

import "time"

// User is a person who can authenticate. Owned by the auth layer's repository.
// Password holds the bcrypt HASH (never plaintext) and is tagged json:"-" so it
// is NEVER serialized into an API response — even if a handler returns a User,
// the hash cannot leak. The db:"..." tags let pgx scan a row into this struct.
type User struct {
	ID        string    `json:"id" db:"id"`
	Username  string    `json:"username" db:"username"`
	Email     string    `json:"email" db:"email"`
	Password  string    `json:"-" db:"password"`
	Role      string    `json:"role" db:"role"`
	CreatedAt time.Time `json:"createdAt" db:"created_at"`
	UpdatedAt time.Time `json:"updatedAt" db:"updated_at"`
}
