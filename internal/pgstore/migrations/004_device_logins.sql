-- Signing a CLI in through the browser, and telling device tokens from
-- API tokens.

ALTER TABLE tokens ADD COLUMN kind text NOT NULL DEFAULT 'api';

-- A sign-in waiting for approval. Only the SHA-256 of the device code is
-- stored; the CLI keeps the code. The token is made when the CLI collects
-- it, so it is never stored here.
CREATE TABLE device_logins (
	device_hash text PRIMARY KEY,
	user_code   text NOT NULL UNIQUE,
	device      text NOT NULL,
	account_id  text REFERENCES accounts (id) ON DELETE CASCADE,
	status      text NOT NULL DEFAULT 'pending',
	created_at  timestamptz NOT NULL,
	expires_at  timestamptz NOT NULL
);
