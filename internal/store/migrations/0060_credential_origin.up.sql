-- The API token a credential was created through. NULL when it was not,
-- and once that token is revoked.
ALTER TABLE api_tokens ADD COLUMN created_by_token INTEGER REFERENCES api_tokens(id) ON DELETE SET NULL;
ALTER TABLE ssh_keys ADD COLUMN created_by_token INTEGER REFERENCES api_tokens(id) ON DELETE SET NULL;
