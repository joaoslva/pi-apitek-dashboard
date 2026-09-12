package main

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

type User struct {
	ID         int64
	Name       string
	Password   string // argon2id PHC string
	Owner      bool
	Disabled   bool
	Created    time.Time
	PasswordAt time.Time
	Grants     map[string]Level // service id -> use or admin
}

type Session struct {
	Hash    []byte // SHA-256 of the cookie token; the token itself is never stored
	User    *User
	CSRF    string
	Created time.Time
	Seen    time.Time
	IP      string
}

type AuditEntry struct {
	At                        time.Time
	Actor, IP, Action, Detail string
}

type Store struct{ db *sql.DB }

var ErrExists = errors.New("already exists")

const schema = `
CREATE TABLE IF NOT EXISTS users (
	id          INTEGER PRIMARY KEY,
	username    TEXT    NOT NULL UNIQUE,
	password    TEXT    NOT NULL,
	owner       INTEGER NOT NULL DEFAULT 0,
	disabled    INTEGER NOT NULL DEFAULT 0,
	created_at  INTEGER NOT NULL,
	password_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS grants (
	user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	service TEXT    NOT NULL,
	level   TEXT    NOT NULL CHECK (level IN ('use', 'admin')),
	PRIMARY KEY (user_id, service)
);
CREATE TABLE IF NOT EXISTS sessions (
	token_hash BLOB    PRIMARY KEY,
	user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	csrf       TEXT    NOT NULL,
	created_at INTEGER NOT NULL,
	seen_at    INTEGER NOT NULL,
	ip         TEXT    NOT NULL,
	agent      TEXT    NOT NULL
);
CREATE TABLE IF NOT EXISTS audit (
	id     INTEGER PRIMARY KEY,
	at     INTEGER NOT NULL,
	actor  TEXT    NOT NULL,
	ip     TEXT    NOT NULL,
	action TEXT    NOT NULL,
	detail TEXT    NOT NULL
);
`

func openStore(file string) (*Store, error) {
	dsn := "file:" + file +
		"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_pragma=synchronous(NORMAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// One connection: requests are short, and it rules out SQLITE_BUSY between
	// our own goroutines. Never query inside an open rows loop.
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("open database %s (has the gateway service started once?): %w", file, err)
	}
	return &Store{db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) tx(fn func(*sql.Tx) error) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}

const userCols = `id, username, password, owner, disabled, created_at, password_at`

func scanUser(scan func(...any) error) (*User, error) {
	var u User
	var created, pwAt int64
	if err := scan(&u.ID, &u.Name, &u.Password, &u.Owner, &u.Disabled, &created, &pwAt); err != nil {
		return nil, err
	}
	u.Created, u.PasswordAt = time.Unix(created, 0), time.Unix(pwAt, 0)
	return &u, nil
}

func (s *Store) CreateUser(name, hash string, owner bool) error {
	now := time.Now().Unix()
	_, err := s.db.Exec(`INSERT INTO users (username, password, owner, created_at, password_at) VALUES (?, ?, ?, ?, ?)`,
		name, hash, owner, now, now)
	if err != nil && strings.Contains(err.Error(), "UNIQUE") {
		return fmt.Errorf("user %q %w", name, ErrExists)
	}
	return err
}

func (s *Store) UserByName(name string) (*User, error) {
	u, err := scanUser(s.db.QueryRow(`SELECT `+userCols+` FROM users WHERE username = ?`, name).Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	u.Grants, err = s.grants(u.ID)
	return u, err
}

func (s *Store) ListUsers() ([]*User, error) {
	rows, err := s.db.Query(`SELECT ` + userCols + ` FROM users ORDER BY owner DESC, username`)
	if err != nil {
		return nil, err
	}
	var users []*User
	for rows.Next() {
		u, err := scanUser(rows.Scan)
		if err != nil {
			rows.Close()
			return nil, err
		}
		users = append(users, u)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, u := range users {
		if u.Grants, err = s.grants(u.ID); err != nil {
			return nil, err
		}
	}
	return users, nil
}

func (s *Store) grants(userID int64) (map[string]Level, error) {
	rows, err := s.db.Query(`SELECT service, level FROM grants WHERE user_id = ?`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	g := map[string]Level{}
	for rows.Next() {
		var svc, lvl string
		if err := rows.Scan(&svc, &lvl); err != nil {
			return nil, err
		}
		if l, err := parseLevel(lvl); err == nil {
			g[svc] = l
		}
	}
	return g, rows.Err()
}

// SetPassword replaces a password and ends every session of that user.
func (s *Store) SetPassword(userID int64, hash string) error {
	return s.tx(func(tx *sql.Tx) error {
		if _, err := tx.Exec(`UPDATE users SET password = ?, password_at = ? WHERE id = ?`, hash, time.Now().Unix(), userID); err != nil {
			return err
		}
		_, err := tx.Exec(`DELETE FROM sessions WHERE user_id = ?`, userID)
		return err
	})
}

func (s *Store) SetDisabled(userID int64, disabled bool) error {
	return s.tx(func(tx *sql.Tx) error {
		if _, err := tx.Exec(`UPDATE users SET disabled = ? WHERE id = ?`, disabled, userID); err != nil {
			return err
		}
		if !disabled {
			return nil
		}
		_, err := tx.Exec(`DELETE FROM sessions WHERE user_id = ?`, userID)
		return err
	})
}

func (s *Store) DeleteUser(userID int64) error {
	_, err := s.db.Exec(`DELETE FROM users WHERE id = ?`, userID)
	return err
}

// SetGrant sets a user's level on a service; LevelNone removes the grant.
func (s *Store) SetGrant(userID int64, service string, lvl Level) error {
	if lvl == LevelNone {
		_, err := s.db.Exec(`DELETE FROM grants WHERE user_id = ? AND service = ?`, userID, service)
		return err
	}
	if lvl != LevelUse && lvl != LevelAdmin {
		return fmt.Errorf("a grant is use or admin, not %s", lvl)
	}
	_, err := s.db.Exec(`INSERT INTO grants (user_id, service, level) VALUES (?, ?, ?)
		ON CONFLICT (user_id, service) DO UPDATE SET level = excluded.level`, userID, service, lvl.String())
	return err
}

func (s *Store) CreateSession(hash []byte, userID int64, csrf, ip, agent string, now time.Time) error {
	_, err := s.db.Exec(`INSERT INTO sessions (token_hash, user_id, csrf, created_at, seen_at, ip, agent) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		hash, userID, csrf, now.Unix(), now.Unix(), ip, agent)
	return err
}

func (s *Store) Session(hash []byte) (*Session, error) {
	sess := &Session{Hash: hash}
	var created, seen int64
	var scan func(...any) error = func(userTargets ...any) error {
		return s.db.QueryRow(`SELECT s.csrf, s.created_at, s.seen_at, s.ip,
				u.id, u.username, u.password, u.owner, u.disabled, u.created_at, u.password_at
			FROM sessions s JOIN users u ON u.id = s.user_id WHERE s.token_hash = ?`, hash).
			Scan(append([]any{&sess.CSRF, &created, &seen, &sess.IP}, userTargets...)...)
	}
	u, err := scanUser(scan)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if u.Grants, err = s.grants(u.ID); err != nil {
		return nil, err
	}
	sess.User = u
	sess.Created, sess.Seen = time.Unix(created, 0), time.Unix(seen, 0)
	return sess, nil
}

func (s *Store) TouchSession(hash []byte, now time.Time) error {
	_, err := s.db.Exec(`UPDATE sessions SET seen_at = ? WHERE token_hash = ?`, now.Unix(), hash)
	return err
}

func (s *Store) DeleteSession(hash []byte) error {
	_, err := s.db.Exec(`DELETE FROM sessions WHERE token_hash = ?`, hash)
	return err
}

func (s *Store) DeleteUserSessions(userID int64) error {
	_, err := s.db.Exec(`DELETE FROM sessions WHERE user_id = ?`, userID)
	return err
}

func (s *Store) PruneSessions(seenBefore, createdBefore time.Time) error {
	_, err := s.db.Exec(`DELETE FROM sessions WHERE seen_at < ? OR created_at < ?`, seenBefore.Unix(), createdBefore.Unix())
	return err
}

func (s *Store) Audit(actor, ip, action, detail string) error {
	_, err := s.db.Exec(`INSERT INTO audit (at, actor, ip, action, detail) VALUES (?, ?, ?, ?, ?)`,
		time.Now().Unix(), actor, ip, action, detail)
	return err
}

func (s *Store) RecentAudit(limit int) ([]AuditEntry, error) {
	rows, err := s.db.Query(`SELECT at, actor, ip, action, detail FROM audit ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AuditEntry
	for rows.Next() {
		var e AuditEntry
		var at int64
		if err := rows.Scan(&at, &e.Actor, &e.IP, &e.Action, &e.Detail); err != nil {
			return nil, err
		}
		e.At = time.Unix(at, 0)
		out = append(out, e)
	}
	return out, rows.Err()
}

// PruneAudit keeps the newest keep entries.
func (s *Store) PruneAudit(keep int) error {
	_, err := s.db.Exec(`DELETE FROM audit WHERE id <= (SELECT id FROM audit ORDER BY id DESC LIMIT 1 OFFSET ?)`, keep)
	return err
}
