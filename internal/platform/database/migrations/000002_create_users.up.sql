-- users: identity table (auth domain). Owned by the auth layer's repository.
-- Schema aligned with the project's User model convention: username + email
-- unique, bcrypt hash stored in a column literally named `password`, role for
-- RBAC, created_at/updated_at timestamps.
CREATE TABLE users (
    id         UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    username   TEXT        NOT NULL UNIQUE,
    email      TEXT        NOT NULL UNIQUE,
    password   TEXT        NOT NULL,                    -- bcrypt hash, never plaintext
    role       TEXT        NOT NULL DEFAULT 'citizen',  -- RBAC: citizen | dispatcher | admin
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
