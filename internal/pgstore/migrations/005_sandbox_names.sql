-- What people call a sandbox, like api-tests or amber-dune-42. The central
-- server keeps names unique among an account's running sandboxes; ended
-- ones may share them. Sandboxes from before names have none.

ALTER TABLE sandboxes ADD COLUMN name text NOT NULL DEFAULT '';
