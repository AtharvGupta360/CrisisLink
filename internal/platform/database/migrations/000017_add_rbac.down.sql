DROP INDEX IF EXISTS idx_users_shelter;
DROP INDEX IF EXISTS idx_users_unit;

ALTER TABLE users
    DROP COLUMN IF EXISTS shelter_id,
    DROP COLUMN IF EXISTS unit_id,
    DROP CONSTRAINT IF EXISTS users_role_check;
