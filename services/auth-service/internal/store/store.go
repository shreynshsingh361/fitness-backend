// Package store holds every SQL query the auth service runs.
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
	ErrEmailTaken = errors.New("email already registered")
	ErrNotFound   = errors.New("not found")
	ErrLinked     = errors.New("already linked to another firebase account")
	// ErrTokenReused means an already-swapped refresh token came back: a stolen copy, or a client
	// that lost a reply long ago. All the user's sessions have been ended.
	ErrTokenReused = errors.New("refresh token reused")
)

type User struct {
	ID           string    `json:"id"`
	Email        string    `json:"email"`
	Name         string    `json:"name"`
	CreatedAt    time.Time `json:"created_at"`
	PasswordHash string    `json:"-"` // "-" keeps the hash out of every JSON response
	FirebaseUID  string    `json:"-"` // "" if this account has never signed in with Firebase
}

type Store struct {
	db *pgxpool.Pool
}

// Ping reports whether the database answers. The readiness check uses it.
func (s *Store) Ping(ctx context.Context) error { return s.db.Ping(ctx) }

func New(db *pgxpool.Pool) *Store { return &Store{db: db} }

const userColumns = `id, email, name, password_hash, created_at, coalesce(firebase_uid, '')`

func scanUser(row pgx.Row) (User, error) {
	var u User
	err := row.Scan(&u.ID, &u.Email, &u.Name, &u.PasswordHash, &u.CreatedAt, &u.FirebaseUID)
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, ErrNotFound
	}
	return u, err
}

func (s *Store) CreateUser(ctx context.Context, email, name, passwordHash string) (User, error) {
	u, err := scanUser(s.db.QueryRow(ctx,
		`INSERT INTO users (email, name, password_hash) VALUES ($1, $2, $3) RETURNING `+userColumns,
		email, name, passwordHash))
	if isUniqueViolation(err) {
		return User{}, ErrEmailTaken
	}
	return u, err
}

func (s *Store) UserByEmail(ctx context.Context, email string) (User, error) {
	return scanUser(s.db.QueryRow(ctx, `SELECT `+userColumns+` FROM users WHERE email = $1`, email))
}

func (s *Store) UserByID(ctx context.Context, id string) (User, error) {
	return scanUser(s.db.QueryRow(ctx, `SELECT `+userColumns+` FROM users WHERE id = $1`, id))
}

func (s *Store) UserByFirebaseUID(ctx context.Context, uid string) (User, error) {
	return scanUser(s.db.QueryRow(ctx, `SELECT `+userColumns+` FROM users WHERE firebase_uid = $1`, uid))
}

// CreateFirebaseUser adds someone who signed in with Firebase. They have no password here.
func (s *Store) CreateFirebaseUser(ctx context.Context, email, name, uid string) (User, error) {
	u, err := scanUser(s.db.QueryRow(ctx,
		`INSERT INTO users (email, name, password_hash, firebase_uid) VALUES ($1, $2, '', $3) RETURNING `+userColumns,
		email, name, uid))
	if isUniqueViolation(err) {
		return User{}, ErrEmailTaken
	}
	return u, err
}

// LinkFirebase ties an existing account to a Firebase user, once. ErrLinked if the
// account is already tied to a different Firebase user.
//
// Accounts made with the old sign-up never proved they own their email, so anyone
// could have made one with someone else's address. The Firebase user has proved it.
// So the old password is wiped and every old session ends: from now on only the
// real owner of the email gets in.
func (s *Store) LinkFirebase(ctx context.Context, userID, uid string) (User, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return User{}, err
	}
	defer tx.Rollback(ctx)

	u, err := scanUser(tx.QueryRow(ctx,
		`UPDATE users SET firebase_uid = $2, password_hash = '' WHERE id = $1 AND firebase_uid IS NULL
		 RETURNING `+userColumns, userID, uid))
	if errors.Is(err, ErrNotFound) || isUniqueViolation(err) {
		return User{}, ErrLinked
	}
	if err != nil {
		return User{}, err
	}
	if _, err := tx.Exec(ctx,
		`UPDATE refresh_tokens SET revoked_at = now() WHERE user_id = $1 AND revoked_at IS NULL`, userID); err != nil {
		return User{}, err
	}
	return u, tx.Commit(ctx)
}

// UserExists says whether an account with this id still exists. The gateway asks it so that a
// token issued before the account was deleted stops working at once.
func (s *Store) UserExists(ctx context.Context, id string) (bool, error) {
	var exists bool
	err := s.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM users WHERE id = $1)`, id).Scan(&exists)
	return exists, err
}

// UpdateName changes the name shown to friends and in groups.
func (s *Store) UpdateName(ctx context.Context, id, name string) (User, error) {
	return scanUser(s.db.QueryRow(ctx, `UPDATE users SET name = $2 WHERE id = $1 RETURNING `+userColumns, id, name))
}

func (s *Store) SaveRefreshToken(ctx context.Context, userID, tokenHash string, expiresAt time.Time) error {
	_, err := s.db.Exec(ctx,
		`INSERT INTO refresh_tokens (user_id, token_hash, expires_at) VALUES ($1, $2, $3)`,
		userID, tokenHash, expiresAt)
	return err
}

// reuseGrace is how long after a swap the old token may still be presented without being taken
// for theft: a client that sent a refresh whose reply got lost retries with the same token.
const reuseGrace = 10 * time.Second

// RotateRefreshToken trades an old refresh token for a new one in a single transaction: either the
// old one is revoked AND the new one is saved, or neither happens. A refresh token can only be
// used once.
//
// An old token that was already swapped, presented again after reuseGrace, is taken as a sign of
// theft: every session of that user is revoked and ErrTokenReused is returned. Within the grace
// period, or for a token revoked by sign-out, it is just ErrNotFound.
func (s *Store) RotateRefreshToken(ctx context.Context, oldHash, newHash string, newExpiresAt time.Time) (userID string, err error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx)

	err = tx.QueryRow(ctx,
		`UPDATE refresh_tokens SET revoked_at = now(), rotated_at = now()
		 WHERE token_hash = $1 AND revoked_at IS NULL AND expires_at > now()
		 RETURNING user_id`, oldHash).Scan(&userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", s.reuseOrNotFound(ctx, tx, oldHash)
	}
	if err != nil {
		return "", err
	}

	if _, err := tx.Exec(ctx,
		`INSERT INTO refresh_tokens (user_id, token_hash, expires_at) VALUES ($1, $2, $3)`,
		userID, newHash, newExpiresAt); err != nil {
		return "", err
	}
	return userID, tx.Commit(ctx)
}

// reuseOrNotFound decides what a refresh token that could not be rotated is: a token swapped
// longer than reuseGrace ago (theft: end all that user's sessions and commit that) or just an
// unknown, expired, signed-out or freshly swapped one. It always returns an error.
func (s *Store) reuseOrNotFound(ctx context.Context, tx pgx.Tx, tokenHash string) error {
	var owner string
	err := tx.QueryRow(ctx,
		`SELECT user_id FROM refresh_tokens
		 WHERE token_hash = $1 AND rotated_at < now() - make_interval(secs => $2) AND expires_at > now()`,
		tokenHash, reuseGrace.Seconds()).Scan(&owner)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx,
		`UPDATE refresh_tokens SET revoked_at = now() WHERE user_id = $1 AND revoked_at IS NULL`, owner); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	return ErrTokenReused
}

// RevokeRefreshToken is used by logout. Revoking an unknown token is not an error.
func (s *Store) RevokeRefreshToken(ctx context.Context, tokenHash string) error {
	_, err := s.db.Exec(ctx,
		`UPDATE refresh_tokens SET revoked_at = now() WHERE token_hash = $1 AND revoked_at IS NULL`, tokenHash)
	return err
}

// UserName is all that other services may learn about a person.
type UserName struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// NamesByIDs looks up display names. Unknown ids are simply left out.
func (s *Store) NamesByIDs(ctx context.Context, ids []string) ([]UserName, error) {
	rows, err := s.db.Query(ctx, `SELECT id, name FROM users WHERE id = ANY($1::text[]::uuid[])`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	users := []UserName{}
	for rows.Next() {
		var u UserName
		if err := rows.Scan(&u.ID, &u.Name); err != nil {
			return nil, err
		}
		users = append(users, u)
	}
	return users, rows.Err()
}

// DeleteUser removes the account; its refresh tokens go with it (ON DELETE CASCADE).
func (s *Store) DeleteUser(ctx context.Context, id string) error {
	_, err := s.db.Exec(ctx, `DELETE FROM users WHERE id = $1`, id)
	return err
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}
