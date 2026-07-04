-- users: identity table owned SOLELY by the auth module (P4 password hashing,
-- P6 RBAC). No other module reads or writes it directly.
--
-- gen_random_uuid() is built into Postgres core (>= 13), so no extension needed.
-- We use a UUID surrogate key (not a serial int) so ids are non-guessable and
-- can be generated client-side later without a round-trip.
CREATE TABLE users (
    id            UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    email         TEXT        NOT NULL UNIQUE,      -- login identity; UNIQUE = no dup accounts
    password_hash TEXT        NOT NULL,             -- bcrypt/argon2 hash (P4) — never the plaintext
    role          TEXT        NOT NULL DEFAULT 'citizen', -- RBAC role (P6): citizen | dispatcher | admin ...
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);
