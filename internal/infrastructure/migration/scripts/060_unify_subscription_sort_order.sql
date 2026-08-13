-- +goose Up
-- Unify nodes.sort_order and forward_rules.sort_order into a single value space.
--
-- Subscription output used to be built by concatenating groups: origin nodes first,
-- then system forward rules, then the user's own forward rules. Each group was ordered
-- by its own sort_order, and the two columns were numbered independently -- both default
-- to 0, and forward rules are typically renumbered 1..N by the reorder endpoint while
-- nodes are hand-numbered in the admin UI. Comparing the two columns was meaningless.
--
-- The subscription repository now merges the groups with one stable sort on sort_order,
-- so an origin node can sit between forwarded ones. That requires the columns to share
-- one value space, which this migration establishes.
--
-- Ordering is preserved exactly: rows are renumbered in their current output order using
-- a step of 100, so every subscription renders identically before and after this runs.
-- The step leaves room to insert entries between neighbours without another renumber.

-- Step 1: origin nodes claim the low band, keeping their relative order.
UPDATE nodes n
JOIN (
    SELECT id, ROW_NUMBER() OVER (ORDER BY sort_order ASC, id ASC) AS rn
    FROM nodes
) ranked ON ranked.id = n.id
SET n.sort_order = ranked.rn * 100;

-- Step 2: system forward rules (no owner) follow every origin node, as before.
SET @node_max := (SELECT COALESCE(MAX(sort_order), 0) FROM nodes);

UPDATE forward_rules fr
JOIN (
    SELECT id, ROW_NUMBER() OVER (ORDER BY sort_order ASC, id ASC) AS rn
    FROM forward_rules
    WHERE deleted_at IS NULL
      AND (user_id IS NULL OR user_id = 0)
) ranked ON ranked.id = fr.id
SET fr.sort_order = @node_max + ranked.rn * 100;

-- Step 3: user-owned forward rules trail the system entries, as before.
-- Partitioned by owner: two users' rules never appear in the same subscription, so each
-- user restarts at the same base without colliding.
SET @sys_max := (
    SELECT COALESCE(MAX(sort_order), 0)
    FROM forward_rules
    WHERE deleted_at IS NULL
      AND (user_id IS NULL OR user_id = 0)
);
SET @user_base := GREATEST(@node_max, @sys_max);

UPDATE forward_rules fr
JOIN (
    SELECT id, ROW_NUMBER() OVER (PARTITION BY user_id ORDER BY sort_order ASC, id ASC) AS rn
    FROM forward_rules
    WHERE deleted_at IS NULL
      AND user_id IS NOT NULL
      AND user_id > 0
) ranked ON ranked.id = fr.id
SET fr.sort_order = @user_base + ranked.rn * 100;

-- Soft-deleted rules keep their old values. Restoring one lands it at an arbitrary
-- position, so reorder after any restore.

-- +goose Down
-- Renumber each table into its own compact sequence again. The pre-migration values are
-- not recoverable, but they do not need to be: the old code sorted each group separately
-- and never compared the two columns, so preserving relative order within each group
-- restores the previous behaviour exactly.
UPDATE nodes n
JOIN (
    SELECT id, ROW_NUMBER() OVER (ORDER BY sort_order ASC, id ASC) AS rn
    FROM nodes
) ranked ON ranked.id = n.id
SET n.sort_order = ranked.rn * 100;

UPDATE forward_rules fr
JOIN (
    SELECT id, ROW_NUMBER() OVER (PARTITION BY COALESCE(user_id, 0) ORDER BY sort_order ASC, id ASC) AS rn
    FROM forward_rules
    WHERE deleted_at IS NULL
) ranked ON ranked.id = fr.id
SET fr.sort_order = ranked.rn * 100;
