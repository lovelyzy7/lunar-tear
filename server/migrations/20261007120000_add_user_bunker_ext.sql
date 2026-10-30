-- +goose Up
-- Bunker's additions to the save (store.ExtState: mission-pass points, scene
-- choices, calendar loop, friends, play history, ...), one JSON document per
-- user. Idempotent: safe to re-run; new fields never need a migration.
CREATE TABLE IF NOT EXISTS user_bunker_ext (
    user_id INTEGER NOT NULL PRIMARY KEY REFERENCES users(user_id),
    data    TEXT    NOT NULL DEFAULT '{}'
);

-- +goose Down
-- Destructive: drops the extension save data (mission-pass points etc.).
DROP TABLE IF EXISTS user_bunker_ext;
