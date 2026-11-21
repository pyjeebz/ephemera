-- When each token last made a request. The server writes it at most once
-- a minute per token. Null for a token never used.

ALTER TABLE tokens ADD COLUMN last_used_at timestamptz;
