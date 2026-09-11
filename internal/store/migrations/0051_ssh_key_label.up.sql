-- A name for the key, shown next to its fingerprint. Defaults to the
-- comment field of the authorized_keys line when the key is added.
ALTER TABLE ssh_keys ADD COLUMN label TEXT NOT NULL DEFAULT '';
