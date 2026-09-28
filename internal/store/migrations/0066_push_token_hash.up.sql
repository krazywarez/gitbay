-- APNs tokens are sealed with a random nonce (internal/seal), so two
-- stores of one token differ; lookups and the re-registration upsert go
-- by this SHA-256 of the token instead. Rows from before it are filled
-- by Store.ResealSecrets.
ALTER TABLE push_devices ADD COLUMN token_hash TEXT;
CREATE UNIQUE INDEX push_devices_token_hash ON push_devices(token_hash);
