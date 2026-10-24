package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/estetsenko/fog-play/internal/match"
)

var (
	ErrHostTaken      = errors.New("host already has a live session")
	ErrPlayerBusy     = errors.New("player already has a live session")
	ErrBadTransition  = errors.New("session cannot move to that state")
	LiveSessionStates = []string{"matched", "connecting", "active"}
)

type Session struct {
	ID            string     `json:"id"`
	PlayerID      string     `json:"player_id"`
	HostID        string     `json:"host_id"`
	HostOwnerID   string     `json:"-"`
	HostRegion    string     `json:"host_region"`
	Game          string     `json:"game"`
	State         string     `json:"state"`
	FailureReason string     `json:"failure_reason,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
	StartedAt     *time.Time `json:"started_at"`
	EndedAt       *time.Time `json:"ended_at"`
	Minutes       int        `json:"minutes"`
}

// MatchCandidates returns hosts that have the game installed, have sent at
// least one heartbeat, have no live session, and are not owned by the player.
// Live idle/busy status is checked by the caller against presence.
func (s *Store) MatchCandidates(ctx context.Context, gameSlug, playerID string) (gameID string, cands []match.Candidate, err error) {
	err = s.pool.QueryRow(ctx, `SELECT id FROM games WHERE slug = $1`, gameSlug).Scan(&gameID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil, ErrNotFound
	}
	if err != nil {
		return "", nil, err
	}
	rows, err := s.pool.Query(ctx, `
		SELECT h.id, h.region, h.last_seen_at
		FROM hosts h
		JOIN host_games hg ON hg.host_id = h.id AND hg.game_id = $1
		WHERE h.owner_id <> $2
		  AND h.last_seen_at IS NOT NULL
		  AND NOT EXISTS (SELECT 1 FROM sessions s WHERE s.host_id = h.id AND s.state = ANY($3))`,
		gameID, playerID, LiveSessionStates)
	if err != nil {
		return "", nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var c match.Candidate
		if err := rows.Scan(&c.HostID, &c.Region, &c.LastSeenAt); err != nil {
			return "", nil, err
		}
		cands = append(cands, c)
	}
	return gameID, cands, rows.Err()
}

// CreateSession books the host for the player. The partial unique indexes on
// sessions make this safe when two players race for the same host.
func (s *Store) CreateSession(ctx context.Context, playerID, hostID, gameID string) (Session, error) {
	var id string
	err := s.pool.QueryRow(ctx,
		`INSERT INTO sessions (player_id, host_id, game_id) VALUES ($1, $2, $3) RETURNING id`,
		playerID, hostID, gameID).Scan(&id)
	var pg *pgconn.PgError
	if errors.As(err, &pg) && pg.Code == "23505" {
		switch pg.ConstraintName {
		case "sessions_one_live_per_host":
			return Session{}, ErrHostTaken
		case "sessions_one_live_per_player":
			return Session{}, ErrPlayerBusy
		}
	}
	if err != nil {
		return Session{}, err
	}
	return s.SessionByID(ctx, id)
}

const sessionSelect = `
	SELECT s.id, s.player_id, s.host_id, h.owner_id, h.region, g.slug, s.state, s.failure_reason,
	       s.created_at, s.started_at, s.ended_at, s.minutes
	FROM sessions s
	JOIN hosts h ON h.id = s.host_id
	JOIN games g ON g.id = s.game_id `

func scanSession(row pgx.Row) (Session, error) {
	var x Session
	err := row.Scan(&x.ID, &x.PlayerID, &x.HostID, &x.HostOwnerID, &x.HostRegion, &x.Game, &x.State,
		&x.FailureReason, &x.CreatedAt, &x.StartedAt, &x.EndedAt, &x.Minutes)
	if errors.Is(err, pgx.ErrNoRows) {
		return Session{}, ErrNotFound
	}
	return x, err
}

func (s *Store) SessionByID(ctx context.Context, id string) (Session, error) {
	return scanSession(s.pool.QueryRow(ctx, sessionSelect+`WHERE s.id = $1`, id))
}

// LiveHostSession returns the host's current live session, if any.
func (s *Store) LiveHostSession(ctx context.Context, hostID string) (Session, error) {
	return scanSession(s.pool.QueryRow(ctx, sessionSelect+`WHERE s.host_id = $1 AND s.state = ANY($2)`,
		hostID, LiveSessionStates))
}

func (s *Store) SessionsByPlayer(ctx context.Context, playerID string, limit int) ([]Session, error) {
	rows, err := s.pool.Query(ctx, sessionSelect+`WHERE s.player_id = $1 ORDER BY s.created_at DESC LIMIT $2`,
		playerID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Session{}
	for rows.Next() {
		x, err := scanSession(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

// TransitionSession moves a session to state `to` if it is currently in one
// of `from`. Entering "active" stamps started_at; entering "ended" or
// "failed" stamps ended_at and computes billable minutes, rounded up.
func (s *Store) TransitionSession(ctx context.Context, id string, from []string, to, reason string) (Session, error) {
	tag, err := s.pool.Exec(ctx, `
		UPDATE sessions SET
		    state = $3,
		    failure_reason = $4,
		    started_at = CASE WHEN $3 = 'active' THEN now() ELSE started_at END,
		    ended_at = CASE WHEN $3 IN ('ended', 'failed') THEN now() ELSE ended_at END,
		    minutes = CASE WHEN $3 IN ('ended', 'failed') AND started_at IS NOT NULL
		                   THEN GREATEST(1, CEIL(EXTRACT(EPOCH FROM now() - started_at) / 60))::int
		                   ELSE minutes END
		WHERE id = $1 AND state = ANY($2)`, id, from, to, reason)
	if err != nil {
		return Session{}, err
	}
	x, err := s.SessionByID(ctx, id)
	if err != nil {
		return Session{}, err
	}
	if tag.RowsAffected() == 0 {
		return x, ErrBadTransition
	}
	return x, nil
}

// ExpireStaleSessions fails sessions that never became active within maxAge
// and returns the hosts they had booked.
func (s *Store) ExpireStaleSessions(ctx context.Context, maxAge time.Duration) ([]string, error) {
	rows, err := s.pool.Query(ctx, `
		UPDATE sessions SET state = 'failed', failure_reason = 'timed out before streaming started', ended_at = now()
		WHERE state IN ('matched', 'connecting') AND created_at < now() - make_interval(secs => $1)
		RETURNING host_id`, maxAge.Seconds())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var hosts []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		hosts = append(hosts, id)
	}
	return hosts, rows.Err()
}
