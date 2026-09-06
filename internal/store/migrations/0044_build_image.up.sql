-- The container image a build's steps run in, captured when the build is
-- queued so it is the image the config named at that commit (#144). Empty
-- means the runner's configured default.
ALTER TABLE builds ADD COLUMN image TEXT NOT NULL DEFAULT '';
