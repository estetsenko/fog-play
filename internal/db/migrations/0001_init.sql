CREATE TABLE users (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    email         text NOT NULL UNIQUE,
    password_hash text NOT NULL,
    role          text NOT NULL CHECK (role IN ('player', 'host', 'both')),
    created_at    timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE hosts (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    owner_id     uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name         text NOT NULL,
    region       text NOT NULL,
    gpu          text NOT NULL DEFAULT '',
    cpu          text NOT NULL DEFAULT '',
    status       text NOT NULL DEFAULT 'offline' CHECK (status IN ('idle', 'busy', 'offline')),
    last_seen_at timestamptz,
    created_at   timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX hosts_owner_idx ON hosts (owner_id);

CREATE TABLE games (
    id    uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    slug  text NOT NULL UNIQUE,
    title text NOT NULL
);

CREATE TABLE host_games (
    host_id uuid NOT NULL REFERENCES hosts(id) ON DELETE CASCADE,
    game_id uuid NOT NULL REFERENCES games(id) ON DELETE CASCADE,
    PRIMARY KEY (host_id, game_id)
);
CREATE INDEX host_games_game_idx ON host_games (game_id);
