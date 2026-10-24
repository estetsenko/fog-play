CREATE TABLE sessions (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    player_id      uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    host_id        uuid NOT NULL REFERENCES hosts(id) ON DELETE CASCADE,
    game_id        uuid NOT NULL REFERENCES games(id),
    state          text NOT NULL DEFAULT 'matched'
                   CHECK (state IN ('matched', 'connecting', 'active', 'ended', 'failed')),
    failure_reason text NOT NULL DEFAULT '',
    created_at     timestamptz NOT NULL DEFAULT now(),
    started_at     timestamptz,
    ended_at       timestamptz,
    -- Billable minutes, rounded up; set when an active session ends.
    minutes        integer NOT NULL DEFAULT 0
);

-- A host serves and a player plays at most one live session at a time.
-- The matcher relies on these to avoid double-booking under concurrency.
CREATE UNIQUE INDEX sessions_one_live_per_host ON sessions (host_id)
    WHERE state IN ('matched', 'connecting', 'active');
CREATE UNIQUE INDEX sessions_one_live_per_player ON sessions (player_id)
    WHERE state IN ('matched', 'connecting', 'active');
CREATE INDEX sessions_player_idx ON sessions (player_id, created_at DESC);
