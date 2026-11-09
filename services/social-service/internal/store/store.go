// Package store holds every SQL query the social service runs.
package store

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrNotFound  = errors.New("not found")
	ErrBanned    = errors.New("removed from this group")
	ErrGroupFull = errors.New("group is full")
	ErrLastAdmin = errors.New("a group must keep at least one admin")
)

type Store struct {
	db *pgxpool.Pool
}

func New(db *pgxpool.Pool) *Store { return &Store{db: db} }

// newCode makes a random, unguessable invite code that is safe to put in a link.
func newCode() (string, error) {
	b := make([]byte, 9)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// scanIDs reads a single column of user ids.
func scanIDs(rows pgx.Rows) ([]string, error) {
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// ---------- friends ----------

// InviteCode returns this user's personal code, creating it the first time.
func (s *Store) InviteCode(ctx context.Context, userID string) (string, error) {
	code, err := newCode()
	if err != nil {
		return "", err
	}
	// The no-op update makes RETURNING give back the existing code on a conflict.
	var stored string
	err = s.db.QueryRow(ctx,
		`INSERT INTO friend_invites (user_id, code) VALUES ($1, $2)
		 ON CONFLICT (user_id) DO UPDATE SET user_id = EXCLUDED.user_id
		 RETURNING code`, userID, code).Scan(&stored)
	return stored, err
}

func (s *Store) InviterByCode(ctx context.Context, code string) (string, error) {
	var userID string
	err := s.db.QueryRow(ctx, `SELECT user_id FROM friend_invites WHERE code = $1`, code).Scan(&userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	return userID, err
}

// pair orders two ids the way the friendships table stores them.
func pair(a, b string) (string, string) {
	if a < b {
		return a, b
	}
	return b, a
}

// AddFriend returns true if this created the friendship, false if it already existed.
func (s *Store) AddFriend(ctx context.Context, a, b string) (bool, error) {
	first, second := pair(a, b)
	tag, err := s.db.Exec(ctx,
		`INSERT INTO friendships (user_a, user_b) VALUES ($1, $2) ON CONFLICT DO NOTHING`, first, second)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

func (s *Store) RemoveFriend(ctx context.Context, a, b string) error {
	first, second := pair(a, b)
	tag, err := s.db.Exec(ctx, `DELETE FROM friendships WHERE user_a = $1 AND user_b = $2`, first, second)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) AreFriends(ctx context.Context, a, b string) (bool, error) {
	first, second := pair(a, b)
	var yes bool
	err := s.db.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM friendships WHERE user_a = $1 AND user_b = $2)`, first, second).Scan(&yes)
	return yes, err
}

func (s *Store) FriendIDs(ctx context.Context, userID string) ([]string, error) {
	rows, err := s.db.Query(ctx,
		`SELECT CASE WHEN user_a = $1 THEN user_b ELSE user_a END
		 FROM friendships WHERE user_a = $1 OR user_b = $1`, userID)
	if err != nil {
		return nil, err
	}
	return scanIDs(rows)
}

// ---------- hiding ----------

func (s *Store) Hide(ctx context.Context, hiderID, hiddenID string) error {
	_, err := s.db.Exec(ctx,
		`INSERT INTO hidden_users (hider_id, hidden_id) VALUES ($1, $2) ON CONFLICT DO NOTHING`, hiderID, hiddenID)
	return err
}

func (s *Store) Unhide(ctx context.Context, hiderID, hiddenID string) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM hidden_users WHERE hider_id = $1 AND hidden_id = $2`, hiderID, hiddenID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// HiddenBy lists the people this user chose to hide, most recent first.
func (s *Store) HiddenBy(ctx context.Context, hiderID string) ([]string, error) {
	rows, err := s.db.Query(ctx,
		`SELECT hidden_id FROM hidden_users WHERE hider_id = $1 ORDER BY created_at DESC`, hiderID)
	if err != nil {
		return nil, err
	}
	return scanIDs(rows)
}

// HiddenEitherWay is everyone this user must not see: people they hid and people
// who hid them. Never expose this to a client, because it reveals who hid them.
func (s *Store) HiddenEitherWay(ctx context.Context, userID string) (map[string]bool, error) {
	rows, err := s.db.Query(ctx,
		`SELECT hidden_id FROM hidden_users WHERE hider_id = $1
		 UNION
		 SELECT hider_id FROM hidden_users WHERE hidden_id = $1`, userID)
	if err != nil {
		return nil, err
	}
	ids, err := scanIDs(rows)
	if err != nil {
		return nil, err
	}
	hidden := make(map[string]bool, len(ids))
	for _, id := range ids {
		hidden[id] = true
	}
	return hidden, nil
}

// ---------- cheers and taunts ----------

type Nudge struct {
	ID         string    `json:"id"`
	FromUserID string    `json:"from_user_id"`
	FromName   string    `json:"from_name"`
	Kind       string    `json:"kind"`
	GroupID    *string   `json:"group_id"`
	GroupName  *string   `json:"group_name"`
	CreatedAt  time.Time `json:"created_at"`
	Read       bool      `json:"read"`
}

func (s *Store) NudgedSince(ctx context.Context, from, to, kind string, since time.Time) (bool, error) {
	var yes bool
	err := s.db.QueryRow(ctx,
		`SELECT EXISTS (
		     SELECT 1 FROM nudges
		     WHERE from_user = $1 AND to_user = $2 AND kind = $3 AND created_at >= $4
		 )`, from, to, kind, since).Scan(&yes)
	return yes, err
}

func (s *Store) AddNudge(ctx context.Context, from, to, kind string, groupID *string) error {
	_, err := s.db.Exec(ctx,
		`INSERT INTO nudges (from_user, to_user, kind, group_id) VALUES ($1, $2, $3, $4)`,
		from, to, kind, groupID)
	return err
}

// Nudges returns cheers and taunts sent to this user, newest first, leaving out
// anyone hidden in either direction. FromName is filled in by the caller.
func (s *Store) Nudges(ctx context.Context, userID string, limit int) ([]Nudge, int, error) {
	const visible = `n.to_user = $1 AND NOT EXISTS (
	    SELECT 1 FROM hidden_users h
	    WHERE (h.hider_id = $1 AND h.hidden_id = n.from_user)
	       OR (h.hider_id = n.from_user AND h.hidden_id = $1))`

	var unread int
	err := s.db.QueryRow(ctx, `SELECT count(*) FROM nudges n WHERE read_at IS NULL AND `+visible, userID).Scan(&unread)
	if err != nil {
		return nil, 0, err
	}

	rows, err := s.db.Query(ctx,
		`SELECT n.id, n.from_user, n.kind, n.group_id, g.name, n.created_at, n.read_at IS NOT NULL
		 FROM nudges n LEFT JOIN groups g ON g.id = n.group_id
		 WHERE `+visible+`
		 ORDER BY n.created_at DESC LIMIT $2`, userID, limit)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	nudges := []Nudge{}
	for rows.Next() {
		var n Nudge
		if err := rows.Scan(&n.ID, &n.FromUserID, &n.Kind, &n.GroupID, &n.GroupName, &n.CreatedAt, &n.Read); err != nil {
			return nil, 0, err
		}
		nudges = append(nudges, n)
	}
	return nudges, unread, rows.Err()
}

func (s *Store) MarkNudgesRead(ctx context.Context, userID string) error {
	_, err := s.db.Exec(ctx, `UPDATE nudges SET read_at = now() WHERE to_user = $1 AND read_at IS NULL`, userID)
	return err
}

// ---------- deleting an account ----------

// DeleteUser removes everything this service knows about a person. Groups they
// were in carry on: another member takes over as admin, or an empty group is deleted.
func (s *Store) DeleteUser(ctx context.Context, userID string) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	rows, err := tx.Query(ctx, `SELECT group_id FROM group_members WHERE user_id = $1`, userID)
	if err != nil {
		return err
	}
	groupIDs, err := scanIDs(rows)
	if err != nil {
		return err
	}
	for _, groupID := range groupIDs {
		if err := leaveGroup(ctx, tx, groupID, userID); err != nil {
			return err
		}
	}

	for _, statement := range []string{
		`DELETE FROM group_messages WHERE user_id = $1`,
		`DELETE FROM group_bans WHERE user_id = $1`,
		`DELETE FROM friendships WHERE user_a = $1 OR user_b = $1`,
		`DELETE FROM friend_invites WHERE user_id = $1`,
		`DELETE FROM hidden_users WHERE hider_id = $1 OR hidden_id = $1`,
		`DELETE FROM nudges WHERE from_user = $1 OR to_user = $1`,
	} {
		if _, err := tx.Exec(ctx, statement, userID); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}
