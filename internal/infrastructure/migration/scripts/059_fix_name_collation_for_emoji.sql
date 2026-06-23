-- +goose Up
-- Fix name uniqueness so emoji-prefixed names are distinguished.
--
-- Under utf8mb4_general_ci all supplementary-plane characters (every emoji and
-- regional-indicator flag) share one collation weight, so names like the
-- HK-flag "RFC" and the JP-flag "RFC" compare as equal. This falsely trips the
-- uniqueness checks on the two name columns that enforce it:
--   * nodes.name          -- UNIQUE index + ExistsByName; the proxy name is an
--                            identifier in Clash/Surge subscription output.
--   * forward_agents.name -- app-level ExistsByName uniqueness (admin UX).
--
-- Switching these columns to utf8mb4_0900_ai_ci (the MySQL 8.0 default, based on
-- Unicode 9.0 UCA) assigns distinct weights to emoji so the flag prefixes stay
-- distinct, while keeping case/accent-insensitive matching for the human-readable
-- name. Any implicit/unique index on the column is rebuilt with the new collation.
-- The conversion is always safe upward: 0900_ai_ci is finer than general_ci, so
-- rows that were unique before remain unique.
ALTER TABLE nodes
    MODIFY name VARCHAR(100) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci NOT NULL;

ALTER TABLE forward_agents
    MODIFY name VARCHAR(100) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci NOT NULL;

-- +goose Down
-- Revert to the original collation. This may fail if rows now exist whose names
-- collide under utf8mb4_general_ci (e.g. two names differing only by emoji prefix);
-- such rows must be renamed before rolling back.
ALTER TABLE nodes
    MODIFY name VARCHAR(100) CHARACTER SET utf8mb4 COLLATE utf8mb4_general_ci NOT NULL;

ALTER TABLE forward_agents
    MODIFY name VARCHAR(100) CHARACTER SET utf8mb4 COLLATE utf8mb4_general_ci NOT NULL;
