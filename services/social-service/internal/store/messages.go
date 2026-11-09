package store

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
)

type Message struct {
	ID        string    `json:"id"`
	UserID    string    `json:"user_id"`
	Name      string    `json:"name"`
	Body      string    `json:"body"`
	Removed   bool      `json:"removed"`
	Mine      bool      `json:"mine"`
	CreatedAt time.Time `json:"created_at"`
}

func (s *Store) AddMessage(ctx context.Context, groupID, userID, body string) (Message, error) {
	m := Message{UserID: userID, Body: body, Mine: true}
	err := s.db.QueryRow(ctx,
		`INSERT INTO group_messages (group_id, user_id, body) VALUES ($1, $2, $3)
		 RETURNING id, created_at`, groupID, userID, body).Scan(&m.ID, &m.CreatedAt)
	return m, err
}

// Messages returns chat messages oldest first, leaving out anyone hidden in either
// direction. `before` loads earlier pages. Name is filled in by the caller.
func (s *Store) Messages(ctx context.Context, groupID, viewerID string, before *time.Time, limit int) ([]Message, error) {
	rows, err := s.db.Query(ctx,
		`SELECT m.id, m.user_id, m.body, m.removed_at IS NOT NULL, m.created_at
		 FROM group_messages m
		 WHERE m.group_id = $1
		   AND ($2::timestamptz IS NULL OR m.created_at < $2)
		   AND NOT EXISTS (
		       SELECT 1 FROM hidden_users h
		       WHERE (h.hider_id = $3 AND h.hidden_id = m.user_id)
		          OR (h.hider_id = m.user_id AND h.hidden_id = $3)
		   )
		 ORDER BY m.created_at DESC, m.id DESC
		 LIMIT $4`, groupID, before, viewerID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	messages := []Message{}
	for rows.Next() {
		var m Message
		if err := rows.Scan(&m.ID, &m.UserID, &m.Body, &m.Removed, &m.CreatedAt); err != nil {
			return nil, err
		}
		m.Mine = m.UserID == viewerID
		messages = append(messages, m)
	}
	// The newest are fetched first so LIMIT keeps the right ones; show them oldest first.
	slices.Reverse(messages)
	return messages, rows.Err()
}

func (s *Store) MessageAuthor(ctx context.Context, groupID, messageID string) (string, error) {
	var author string
	err := s.db.QueryRow(ctx,
		`SELECT user_id FROM group_messages WHERE id = $1 AND group_id = $2`, messageID, groupID).Scan(&author)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	return author, err
}

// RemoveMessage keeps the message's place in the chat but really deletes its text.
func (s *Store) RemoveMessage(ctx context.Context, groupID, messageID, removedBy string) error {
	_, err := s.db.Exec(ctx,
		`UPDATE group_messages SET body = '', removed_at = now(), removed_by = $3
		 WHERE id = $1 AND group_id = $2 AND removed_at IS NULL`, messageID, groupID, removedBy)
	return err
}
