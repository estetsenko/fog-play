// Package store is the Postgres data access layer.
package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrNotFound = errors.New("not found")
	ErrConflict = errors.New("already exists")
)

type User struct {
	ID           string    `json:"id"`
	Email        string    `json:"email"`
	PasswordHash string    `json:"-"`
	Role         string    `json:"role"`
	CreatedAt    time.Time `json:"created_at"`
}

type Host struct {
	ID         string     `json:"id"`
	OwnerID    string     `json:"owner_id"`
	Name       string     `json:"name"`
	Region     string     `json:"region"`
	GPU        string     `json:"gpu"`
	CPU        string     `json:"cpu"`
	Status     string     `json:"status"`
	LastSeenAt *time.Time `json:"last_seen_at"`
	CreatedAt  time.Time  `json:"created_at"`
	Games      []Game     `json:"games"`
}

type Game struct {
	Slug  string `json:"slug"`
	Title string `json:"title"`
}

// CatalogEntry is a game together with the hosts that have it installed.
type CatalogEntry struct {
	Game
	HostIDs []string
}

type Store struct {
	pool *pgxpool.Pool
}

func New(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

func (s *Store) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }

func (s *Store) CreateUser(ctx context.Context, email, hash, role string) (User, error) {
	u := User{Email: email, PasswordHash: hash, Role: role}
	err := s.pool.QueryRow(ctx,
		`INSERT INTO users (email, password_hash, role) VALUES ($1, $2, $3) RETURNING id, created_at`,
		email, hash, role).Scan(&u.ID, &u.CreatedAt)
	if isUniqueViolation(err) {
		return User{}, ErrConflict
	}
	return u, err
}

func (s *Store) UserByEmail(ctx context.Context, email string) (User, error) {
	return s.scanUser(s.pool.QueryRow(ctx,
		`SELECT id, email, password_hash, role, created_at FROM users WHERE email = $1`, email))
}

func (s *Store) UserByID(ctx context.Context, id string) (User, error) {
	return s.scanUser(s.pool.QueryRow(ctx,
		`SELECT id, email, password_hash, role, created_at FROM users WHERE id = $1`, id))
}

func (s *Store) scanUser(row pgx.Row) (User, error) {
	var u User
	err := row.Scan(&u.ID, &u.Email, &u.PasswordHash, &u.Role, &u.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, ErrNotFound
	}
	return u, err
}

func (s *Store) CreateHost(ctx context.Context, h Host) (Host, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Host{}, err
	}
	defer tx.Rollback(ctx)
	err = tx.QueryRow(ctx,
		`INSERT INTO hosts (owner_id, name, region, gpu, cpu) VALUES ($1, $2, $3, $4, $5)
		 RETURNING id, status, created_at`,
		h.OwnerID, h.Name, h.Region, h.GPU, h.CPU).Scan(&h.ID, &h.Status, &h.CreatedAt)
	if err != nil {
		return Host{}, err
	}
	if err := setHostGames(ctx, tx, h.ID, h.Games); err != nil {
		return Host{}, err
	}
	if h.Games == nil {
		h.Games = []Game{}
	}
	return h, tx.Commit(ctx)
}

// ReplaceHostGames sets the host's installed games, adding unknown games to the catalog.
func (s *Store) ReplaceHostGames(ctx context.Context, hostID string, games []Game) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `DELETE FROM host_games WHERE host_id = $1`, hostID); err != nil {
		return err
	}
	if err := setHostGames(ctx, tx, hostID, games); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func setHostGames(ctx context.Context, tx pgx.Tx, hostID string, games []Game) error {
	for _, g := range games {
		var gameID string
		err := tx.QueryRow(ctx,
			`INSERT INTO games (slug, title) VALUES ($1, $2)
			 ON CONFLICT (slug) DO UPDATE SET slug = EXCLUDED.slug RETURNING id`,
			g.Slug, g.Title).Scan(&gameID)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO host_games (host_id, game_id) VALUES ($1, $2) ON CONFLICT DO NOTHING`,
			hostID, gameID); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) HostByID(ctx context.Context, id string) (Host, error) {
	hosts, err := s.listHosts(ctx, `WHERE h.id = $1`, id)
	if err != nil {
		return Host{}, err
	}
	if len(hosts) == 0 {
		return Host{}, ErrNotFound
	}
	return hosts[0], nil
}

func (s *Store) HostsByOwner(ctx context.Context, ownerID string) ([]Host, error) {
	return s.listHosts(ctx, `WHERE h.owner_id = $1`, ownerID)
}

func (s *Store) listHosts(ctx context.Context, where string, arg any) ([]Host, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT h.id, h.owner_id, h.name, h.region, h.gpu, h.cpu, h.status, h.last_seen_at, h.created_at,
		       COALESCE(array_agg(g.slug ORDER BY g.slug) FILTER (WHERE g.id IS NOT NULL), '{}'),
		       COALESCE(array_agg(g.title ORDER BY g.slug) FILTER (WHERE g.id IS NOT NULL), '{}')
		FROM hosts h
		LEFT JOIN host_games hg ON hg.host_id = h.id
		LEFT JOIN games g ON g.id = hg.game_id
		`+where+`
		GROUP BY h.id
		ORDER BY h.created_at`, arg)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	hosts := []Host{}
	for rows.Next() {
		var h Host
		var slugs, titles []string
		if err := rows.Scan(&h.ID, &h.OwnerID, &h.Name, &h.Region, &h.GPU, &h.CPU, &h.Status,
			&h.LastSeenAt, &h.CreatedAt, &slugs, &titles); err != nil {
			return nil, err
		}
		h.Games = make([]Game, len(slugs))
		for i := range slugs {
			h.Games[i] = Game{Slug: slugs[i], Title: titles[i]}
		}
		hosts = append(hosts, h)
	}
	return hosts, rows.Err()
}

// TouchHost records the status a host reported in its latest heartbeat.
func (s *Store) TouchHost(ctx context.Context, id, status string) error {
	_, err := s.pool.Exec(ctx, `UPDATE hosts SET status = $2, last_seen_at = now() WHERE id = $1`, id, status)
	return err
}

func (s *Store) Catalog(ctx context.Context) ([]CatalogEntry, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT g.slug, g.title, COALESCE(array_agg(hg.host_id::text) FILTER (WHERE hg.host_id IS NOT NULL), '{}')
		FROM games g
		LEFT JOIN host_games hg ON hg.game_id = g.id
		GROUP BY g.id
		ORDER BY g.title`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []CatalogEntry{}
	for rows.Next() {
		var e CatalogEntry
		if err := rows.Scan(&e.Slug, &e.Title, &e.HostIDs); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func isUniqueViolation(err error) bool {
	var pg *pgconn.PgError
	return errors.As(err, &pg) && pg.Code == "23505"
}
