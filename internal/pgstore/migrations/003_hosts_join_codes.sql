-- Hosts that joined an account with a one-time code, and the codes.

CREATE TABLE hosts (
	id         text PRIMARY KEY,
	account_id text NOT NULL REFERENCES accounts (id),
	name       text NOT NULL,
	public_key text NOT NULL, -- Ed25519, base64; the host signs in with it
	joined_at  timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX hosts_account_name ON hosts (account_id, lower(name));

-- Only the SHA-256 of a code is stored. A code is deleted when used.
CREATE TABLE join_codes (
	sha256     text PRIMARY KEY,
	account_id text NOT NULL REFERENCES accounts (id),
	expires_at timestamptz NOT NULL
);

-- Host names are only unique within an account, so sandboxes also keep
-- the host's id.
ALTER TABLE sandboxes ADD COLUMN host_id text NOT NULL DEFAULT '';
