package main

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"os"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

type store struct{ *sql.DB }

type user struct {
	TelegramID int64  `json:"telegram_id"`
	FirstName  string `json:"first_name"`
	SteamID    string `json:"steam_id"`
	Key        string `json:"key"`
}

type announcement struct {
	ID        int64  `json:"id"`
	Text      string `json:"text"`
	CreatedAt string `json:"created_at"`
}

func openStore(path string) (*store, error) {
	// SQLite inherits this mode for WAL files; keys and sessions are private data.
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	err = f.Chmod(0600)
	closeErr := f.Close()
	if err != nil {
		return nil, err
	}
	if closeErr != nil {
		return nil, closeErr
	}
	u := url.URL{Scheme: "file", Path: path}
	q := u.Query()
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_pragma", "foreign_keys(1)")
	q.Set("_txlock", "immediate")
	u.RawQuery = q.Encode()
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(4)
	_, err = db.Exec(`
CREATE TABLE IF NOT EXISTS users (
 telegram_id INTEGER PRIMARY KEY, first_name TEXT NOT NULL,
 steam_id TEXT UNIQUE, bot_started INTEGER NOT NULL DEFAULT 0,
 blocked INTEGER NOT NULL DEFAULT 0, created_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS keys (
 id INTEGER PRIMARY KEY, value TEXT NOT NULL UNIQUE,
 telegram_id INTEGER UNIQUE REFERENCES users(telegram_id), issued_at INTEGER
);
CREATE TABLE IF NOT EXISTS group_access (
 telegram_id INTEGER PRIMARY KEY REFERENCES users(telegram_id), granted_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS join_queries (
 telegram_id INTEGER PRIMARY KEY REFERENCES users(telegram_id),
 query_id TEXT NOT NULL UNIQUE, expires_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS join_queries_expiry ON join_queries(expires_at);
CREATE TABLE IF NOT EXISTS sessions (
 hash TEXT PRIMARY KEY, telegram_id INTEGER NOT NULL REFERENCES users(telegram_id), expires_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS steam_flows (
 state TEXT PRIMARY KEY, ticket_hash TEXT NOT NULL UNIQUE,
 telegram_id INTEGER NOT NULL REFERENCES users(telegram_id), expires_at INTEGER NOT NULL,
 handed_off INTEGER NOT NULL DEFAULT 0, consumed INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS steam_nonces (nonce TEXT PRIMARY KEY, expires_at INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS announcements (
 id INTEGER PRIMARY KEY, text TEXT NOT NULL, created_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS deliveries (
 announcement_id INTEGER NOT NULL REFERENCES announcements(id), telegram_id INTEGER NOT NULL REFERENCES users(telegram_id),
 attempts INTEGER NOT NULL DEFAULT 0, next_at INTEGER NOT NULL DEFAULT 0,
 done INTEGER NOT NULL DEFAULT 0, PRIMARY KEY(announcement_id,telegram_id)
);
CREATE INDEX IF NOT EXISTS deliveries_pending ON deliveries(done,next_at);
CREATE TABLE IF NOT EXISTS settings (name TEXT PRIMARY KEY, value INTEGER NOT NULL);
INSERT OR IGNORE INTO settings(name,value) VALUES('offset',0);
INSERT OR IGNORE INTO settings(name,value) VALUES('delivery_cooldown',0);
`)
	if err != nil {
		db.Close()
		return nil, err
	}
	return &store{db}, nil
}

func (s *store) register(ctx context.Context, id int64, name string, bot bool) error {
	_, err := s.ExecContext(ctx, `INSERT INTO users(telegram_id,first_name,bot_started,created_at) VALUES(?,?,?,?)
ON CONFLICT(telegram_id) DO UPDATE SET first_name=excluded.first_name,
bot_started=MAX(users.bot_started,excluded.bot_started),blocked=CASE WHEN excluded.bot_started=1 THEN 0 ELSE users.blocked END`, id, name, bot, time.Now().Unix())
	return err
}

func (s *store) user(ctx context.Context, id int64) (*user, error) {
	u := &user{}
	err := s.QueryRowContext(ctx, `SELECT u.telegram_id,u.first_name,COALESCE(u.steam_id,''),COALESCE(k.value,'') FROM users u LEFT JOIN keys k ON k.telegram_id=u.telegram_id WHERE u.telegram_id=?`, id).Scan(&u.TelegramID, &u.FirstName, &u.SteamID, &u.Key)
	return u, err
}

func (s *store) grantGroupAccess(ctx context.Context, id int64) error {
	_, err := s.ExecContext(ctx, `INSERT INTO group_access(telegram_id,granted_at) VALUES(?,?)
ON CONFLICT(telegram_id) DO UPDATE SET granted_at=excluded.granted_at`, id, time.Now().Unix())
	return err
}

func (s *store) groupEligible(ctx context.Context, id int64) (bool, error) {
	var eligible int
	err := s.QueryRowContext(ctx, `SELECT EXISTS(
 SELECT 1 FROM users u WHERE u.telegram_id=? AND (
  EXISTS(SELECT 1 FROM keys k WHERE k.telegram_id=u.telegram_id)
  OR EXISTS(SELECT 1 FROM group_access g WHERE g.telegram_id=u.telegram_id)
 )
)`, id).Scan(&eligible)
	return eligible != 0, err
}

var errNotLinked = errors.New("steam not linked")
var errConflict = errors.New("identity conflict")

func allocate(ctx context.Context, tx *sql.Tx, id int64) (string, error) {
	_, err := tx.ExecContext(ctx, `UPDATE keys SET telegram_id=?,issued_at=? WHERE id=(SELECT id FROM keys WHERE telegram_id IS NULL ORDER BY id LIMIT 1) AND NOT EXISTS(SELECT 1 FROM keys WHERE telegram_id=?)`, id, time.Now().Unix(), id)
	if err != nil {
		return "", err
	}
	var key string
	err = tx.QueryRowContext(ctx, `SELECT value FROM keys WHERE telegram_id=?`, id).Scan(&key)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return key, err
}

func (s *store) claim(ctx context.Context, id int64) (string, error) {
	tx, err := s.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	var steam string
	if err = tx.QueryRowContext(ctx, `SELECT COALESCE(steam_id,'') FROM users WHERE telegram_id=?`, id).Scan(&steam); err != nil {
		return "", err
	}
	if steam == "" {
		return "", errNotLinked
	}
	key, err := allocate(ctx, tx, id)
	if err != nil {
		return "", err
	}
	return key, tx.Commit()
}

func (s *store) link(ctx context.Context, state, nonce, steam string) (int64, error) {
	tx, err := s.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var id int64
	err = tx.QueryRowContext(ctx, `SELECT telegram_id FROM steam_flows WHERE state=? AND handed_off=1 AND consumed=0 AND expires_at>?`, state, time.Now().Unix()).Scan(&id)
	if err != nil {
		return 0, err
	}
	var existing string
	if err = tx.QueryRowContext(ctx, `SELECT COALESCE(steam_id,'') FROM users WHERE telegram_id=?`, id).Scan(&existing); err != nil {
		return 0, err
	}
	if existing != "" && existing != steam {
		return 0, errConflict
	}
	var owner int64
	err = tx.QueryRowContext(ctx, `SELECT telegram_id FROM users WHERE steam_id=?`, steam).Scan(&owner)
	if err == nil && owner != id {
		return 0, errConflict
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO steam_nonces(nonce,expires_at) VALUES(?,?)`, nonce, time.Now().Add(24*time.Hour).Unix()); err != nil {
		return 0, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE users SET steam_id=? WHERE telegram_id=?`, steam, id); err != nil {
		return 0, err
	}
	if _, err = allocate(ctx, tx, id); err != nil {
		return 0, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE steam_flows SET consumed=1 WHERE state=?`, state); err != nil {
		return 0, err
	}
	return id, tx.Commit()
}

func (s *store) importKeys(ctx context.Context, text string) (int64, error) {
	tx, err := s.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var total int64
	for _, line := range strings.Split(text, "\n") {
		key := strings.TrimSpace(line)
		if key == "" {
			continue
		}
		if len(key) > 512 {
			return 0, errors.New("key too long")
		}
		r, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO keys(value) VALUES(?)`, key)
		if err != nil {
			return 0, err
		}
		n, _ := r.RowsAffected()
		total += n
	}
	return total, tx.Commit()
}

func (s *store) announcements(ctx context.Context) ([]announcement, error) {
	rows, err := s.QueryContext(ctx, `SELECT id,text,created_at FROM announcements ORDER BY id DESC LIMIT 100`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []announcement{}
	for rows.Next() {
		var a announcement
		if err := rows.Scan(&a.ID, &a.Text, &a.CreatedAt); err != nil {
			return nil, err
		}
		items = append(items, a)
	}
	return items, rows.Err()
}

func (s *store) announce(ctx context.Context, text string) (int64, error) {
	tx, err := s.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	r, err := tx.ExecContext(ctx, `INSERT INTO announcements(text,created_at) VALUES(?,?)`, text, time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		return 0, err
	}
	id, err := r.LastInsertId()
	if err != nil {
		return 0, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO deliveries(announcement_id,telegram_id) SELECT ?,telegram_id FROM users WHERE bot_started=1 AND blocked=0`, id)
	if err != nil {
		return 0, err
	}
	return id, tx.Commit()
}
