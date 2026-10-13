-- Refresh tokens, so a token can be rotated on every use and revoked before it
-- expires. Only the SHA-256 hash of a token's JWT id is stored: a database read
-- cannot be turned back into a usable token, because the signature is still
-- required.
--
-- family_id groups every token descended from one sign-in. Reuse of a revoked
-- member is treated as theft and revokes the whole family.

CREATE TABLE IF NOT EXISTS refresh_tokens (
	token_hash TEXT PRIMARY KEY,
	family_id  TEXT NOT NULL,
	user_id    UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	issued_at  BIGINT NOT NULL,
	expires_at BIGINT NOT NULL,
	revoked_at BIGINT
);

CREATE INDEX IF NOT EXISTS idx_refresh_tokens_family ON refresh_tokens (family_id);
CREATE INDEX IF NOT EXISTS idx_refresh_tokens_user ON refresh_tokens (user_id);
