-- What a sandbox's host said when it ended: the last command's exit code
-- and how many connections were allowed and blocked. Null when unknown:
-- the sandbox is still running, its host is older and didn't say, or, for
-- exit_code, no command ran. Never the commands, host names or output.

ALTER TABLE sandboxes
	ADD COLUMN exit_code           integer,
	ADD COLUMN connections_allowed integer,
	ADD COLUMN connections_blocked integer;
