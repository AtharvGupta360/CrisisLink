package auth

import "time"

// User is a person who can authenticate. Owned by the auth layer's repository.
// Password holds the bcrypt HASH (never plaintext) and is tagged json:"-" so it
// is NEVER serialized into an API response — even if a handler returns a User,
// the hash cannot leak. The db:"..." tags let pgx scan a row into this struct.
type User struct {
	ID       string `json:"id" db:"id"`
	Username string `json:"username" db:"username"`
	Email    string `json:"email" db:"email"`
	Password string `json:"-" db:"password"`
	Role     string `json:"role" db:"role"`

	// Ownership bindings. A responder is bound to one unit and a shelter manager to
	// one shelter; every other role leaves these nil. They are what let
	// authorization ask "is this YOUR resource", not merely "what role are you".
	UnitID    *string   `json:"unitId,omitempty" db:"unit_id"`
	ShelterID *string   `json:"shelterId,omitempty" db:"shelter_id"`
	CreatedAt time.Time `json:"createdAt" db:"created_at"`
	UpdatedAt time.Time `json:"updatedAt" db:"updated_at"`
}
