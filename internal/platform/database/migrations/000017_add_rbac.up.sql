-- RBAC: constrain the role vocabulary and bind users to the resources they own.
--
-- TWO problems are fixed here, and they are different:
--
-- 1. ROLE INTEGRITY. The role column previously accepted ANY string — the allowed
--    values existed only in a comment. Every other invariant in this schema has a
--    structural guard (the partial unique index on dispatches, the capacity CHECKs
--    on shelters and transports); authorization had none. Now the database itself
--    refuses an unknown role, so no code path can invent one.
--
-- 2. OBJECT OWNERSHIP. A role alone answers "what KIND of user are you", never
--    "is this YOUR resource". Without ownership, a responder role would let ANY
--    responder heartbeat ANY unit and advance ANYONE's dispatch — role-checked and
--    still broken (OWASP A01, object-level authorization). Binding a responder to a
--    unit and a shelter manager to a shelter is what makes the second question
--    answerable.
--
-- Both columns are NULLABLE because they only apply to two of the five roles:
-- citizens, operators and admins are not bound to a single resource.

-- Normalise anything that predates the constraint (dev data used ad-hoc roles).
UPDATE users SET role = 'citizen'
 WHERE role NOT IN ('citizen', 'responder', 'shelter_manager', 'operator', 'admin');

ALTER TABLE users
    ADD CONSTRAINT users_role_check
        CHECK (role IN ('citizen', 'responder', 'shelter_manager', 'operator', 'admin')),
    ADD COLUMN unit_id    UUID REFERENCES units (id),
    ADD COLUMN shelter_id UUID REFERENCES shelters (id);

-- Partial indexes: only a minority of users are bound to a resource, so the index
-- carries only those rows.
CREATE INDEX idx_users_unit ON users (unit_id) WHERE unit_id IS NOT NULL;
CREATE INDEX idx_users_shelter ON users (shelter_id) WHERE shelter_id IS NOT NULL;
