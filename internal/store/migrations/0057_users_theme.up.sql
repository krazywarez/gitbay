-- The web colour scheme the account asked for. system follows the
-- browser's prefers-color-scheme; light and dark override it (#232).
ALTER TABLE users ADD COLUMN theme TEXT NOT NULL DEFAULT 'system';
