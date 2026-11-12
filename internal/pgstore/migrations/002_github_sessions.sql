-- People who sign in with GitHub, and their web sessions.

-- A GitHub account's id is "gh-" and its GitHub user id, which never
-- changes; login, name and avatar are refreshed at each sign-in.
ALTER TABLE accounts
	ADD COLUMN github_id    bigint UNIQUE,
	ADD COLUMN login        text,
	ADD COLUMN name         text,
	ADD COLUMN avatar_url   text,
	ADD COLUMN last_seen_at timestamptz;

-- Only the SHA-256 of a session's secret is stored.
CREATE TABLE sessions (
	sha256     text PRIMARY KEY,
	account_id text NOT NULL REFERENCES accounts (id) ON DELETE CASCADE,
	created_at timestamptz NOT NULL DEFAULT now(),
	expires_at timestamptz NOT NULL
);
CREATE INDEX sessions_expiry ON sessions (expires_at);
