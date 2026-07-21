DROP TRIGGER IF EXISTS audit_log_no_update ON audit_log;
DROP FUNCTION IF EXISTS audit_log_is_append_only();
DROP TABLE IF EXISTS audit_log;
