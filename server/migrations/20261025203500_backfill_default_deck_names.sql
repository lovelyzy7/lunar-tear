-- +goose Up
-- Default deck names: unify on "Deck N" (N = user_deck_number).
--
-- Background: the client renders a nameless deck (missing IUserDeck row or an
-- empty Name) through CalculatorDeck.GetDefaultDeckName, which composes a
-- localized "type name + number" ("任务2", "クエスト2", ...). The official
-- server seeds explicit "Deck N" names; lunar-tear only seeded deck 1
-- ("Deck 1"), so the initial quest-deck slots 2/3 showed the localized
-- fallback. See zh-l10n/work/deck-fix/DECK-FIX-REPORT.md.
--
-- This one-time migration:
--   1) backfills Name on existing rows whose Name is NULL/empty (non-empty
--      custom names are never touched), and
--   2) materializes the missing initial quest-deck slots 1..3 for users who
--      already have quest decks, so those slots display "Deck 2"/"Deck 3".
-- Both steps only fill missing/empty state, so reruns are no-ops; goose's
-- version table additionally records a single application.

-- 1) Backfill empty/NULL names on existing deck rows (user custom names kept).
UPDATE user_decks
SET name = 'Deck ' || user_deck_number,
    latest_version = CAST(strftime('%s','now') AS INTEGER) * 1000
WHERE name IS NULL OR name = '';

UPDATE user_triple_decks
SET name = 'Deck ' || user_deck_number,
    latest_version = CAST(strftime('%s','now') AS INTEGER) * 1000
WHERE name IS NULL OR name = '';

-- 2) Materialize missing initial quest-deck slots (client constant
--    CalculatorDeck.kInitialDeckListDeckNumbers = 1..3) for every user who
--    already has at least one quest deck (i.e. passed starter-deck seeding).
--    Existing rows - slot 1 with its starter formation, plus any deck the
--    user built or renamed - are never modified here.
INSERT INTO user_decks (
    user_id, deck_type, user_deck_number,
    user_deck_character_uuid01, user_deck_character_uuid02, user_deck_character_uuid03,
    name, power, latest_version
)
SELECT u.user_id, 1, n.n,
       '', '', '',
       'Deck ' || n.n, 0,
       CAST(strftime('%s','now') AS INTEGER) * 1000
FROM users AS u
CROSS JOIN (SELECT 1 AS n UNION ALL SELECT 2 UNION ALL SELECT 3) AS n
WHERE EXISTS (
        SELECT 1 FROM user_decks d
        WHERE d.user_id = u.user_id AND d.deck_type = 1
      )
  AND NOT EXISTS (
        SELECT 1 FROM user_decks d
        WHERE d.user_id = u.user_id AND d.deck_type = 1 AND d.user_deck_number = n.n
      );

-- +goose Down
-- Intentionally irreversible: backfilled names were empty (never user-set),
-- and materialized empty slots can be removed in-game via RemoveDeck. For a
-- full rollback restore the pre-migration files in
-- zh-l10n/work/deck-fix/db-backup/ (game.db + game.db-wal + game.db-shm).
SELECT 1;
