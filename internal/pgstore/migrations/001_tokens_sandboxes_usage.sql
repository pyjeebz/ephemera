-- Accounts, API tokens, sandboxes and daily usage. Later migrations add
-- GitHub sign-in, sessions, device logins, host join codes and hosts.

CREATE TABLE accounts (
	id         text PRIMARY KEY,
	created_at timestamptz NOT NULL DEFAULT now()
);

-- Only the SHA-256 of a token's secret is stored.
CREATE TABLE tokens (
	id                  text PRIMARY KEY,
	account_id          text NOT NULL REFERENCES accounts (id),
	name                text NOT NULL,
	sha256              text NOT NULL UNIQUE,
	max_sandboxes       integer NOT NULL DEFAULT 0,
	max_minutes_per_day integer NOT NULL DEFAULT 0,
	created_at          timestamptz NOT NULL,
	UNIQUE (account_id, name)
);

-- Running sandboxes have no ended_at. Finished ones stay for the run
-- history. The sandbox's access token is never stored.
CREATE TABLE sandboxes (
	id         text PRIMARY KEY,
	account_id text NOT NULL REFERENCES accounts (id),
	owner      text NOT NULL, -- the token's name when it was made
	host       text NOT NULL,
	host_url   text NOT NULL,
	spec       jsonb NOT NULL,
	created_at timestamptz NOT NULL,
	ended_at   timestamptz,
	end_reason text
);
CREATE INDEX sandboxes_running ON sandboxes (account_id) WHERE ended_at IS NULL;
CREATE INDEX sandboxes_history ON sandboxes (account_id, created_at DESC);

-- Sandbox-seconds per token per UTC day.
CREATE TABLE usage (
	account_id      text NOT NULL REFERENCES accounts (id),
	owner           text NOT NULL,
	day             date NOT NULL,
	sandbox_seconds double precision NOT NULL DEFAULT 0,
	PRIMARY KEY (account_id, owner, day)
);
