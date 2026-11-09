package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// MaxGroupMembers is the size limit of one group: a board of up to 100 people stays easy
// to read on a phone.
const MaxGroupMembers = 100

type Group struct {
	ID             string    `json:"id"`
	Name           string    `json:"name"`
	Description    string    `json:"description"`
	InviteCode     string    `json:"invite_code"`
	MysteryMode    bool      `json:"mystery_mode"`
	ChatEnabled    bool      `json:"chat_enabled"`
	TauntsEnabled  bool      `json:"taunts_enabled"`
	ChallengeStart *string   `json:"challenge_start"` // YYYY-MM-DD
	ChallengeEnd   *string   `json:"challenge_end"`
	CreatedAt      time.Time `json:"created_at"`
	MemberCount    int       `json:"member_count"`
	MyRole         string    `json:"my_role"`
}

// GroupPatch holds the fields to change. A nil field stays as it is.
type GroupPatch struct {
	Name          *string `json:"name"`
	Description   *string `json:"description"`
	MysteryMode   *bool   `json:"mystery_mode"`
	ChatEnabled   *bool   `json:"chat_enabled"`
	TauntsEnabled *bool   `json:"taunts_enabled"`
}

type GroupPreview struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	MemberCount int    `json:"member_count"`
	IsMember    bool   `json:"is_member"`
}

type Member struct {
	UserID   string    `json:"user_id"`
	Name     string    `json:"name"`
	Role     string    `json:"role"`
	JoinedAt time.Time `json:"joined_at"`
	IsMe     bool      `json:"is_me"`
}

// groupSelect reads groups as seen by one member: $1 is that member's user id.
const groupSelect = `
	SELECT g.id, g.name, g.description, g.invite_code,
	       g.mystery_mode, g.chat_enabled, g.taunts_enabled,
	       g.challenge_start::text, g.challenge_end::text, g.created_at,
	       (SELECT count(*) FROM group_members m WHERE m.group_id = g.id),
	       me.role
	FROM groups g
	JOIN group_members me ON me.group_id = g.id AND me.user_id = $1`

func scanGroup(row pgx.Row) (Group, error) {
	var g Group
	err := row.Scan(&g.ID, &g.Name, &g.Description, &g.InviteCode,
		&g.MysteryMode, &g.ChatEnabled, &g.TauntsEnabled,
		&g.ChallengeStart, &g.ChallengeEnd, &g.CreatedAt, &g.MemberCount, &g.MyRole)
	if errors.Is(err, pgx.ErrNoRows) {
		return Group{}, ErrNotFound
	}
	return g, err
}

// GroupForMember returns a group only if this user is in it. For anyone else it
// is ErrNotFound, so outsiders cannot even tell the group exists.
func (s *Store) GroupForMember(ctx context.Context, groupID, userID string) (Group, error) {
	return scanGroup(s.db.QueryRow(ctx, groupSelect+` WHERE g.id = $2`, userID, groupID))
}

func (s *Store) GroupsForUser(ctx context.Context, userID string) ([]Group, error) {
	rows, err := s.db.Query(ctx, groupSelect+` ORDER BY lower(g.name), g.id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	groups := []Group{}
	for rows.Next() {
		g, err := scanGroup(rows)
		if err != nil {
			return nil, err
		}
		groups = append(groups, g)
	}
	return groups, rows.Err()
}

// CreateGroup makes the group and its first admin in one transaction.
func (s *Store) CreateGroup(ctx context.Context, creatorID, name, description string) (Group, error) {
	code, err := newCode()
	if err != nil {
		return Group{}, err
	}

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return Group{}, err
	}
	defer tx.Rollback(ctx)

	var groupID string
	err = tx.QueryRow(ctx,
		`INSERT INTO groups (name, description, invite_code, created_by) VALUES ($1, $2, $3, $4) RETURNING id`,
		name, description, code, creatorID).Scan(&groupID)
	if err != nil {
		return Group{}, err
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO group_members (group_id, user_id, role) VALUES ($1, $2, 'admin')`, groupID, creatorID); err != nil {
		return Group{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Group{}, err
	}
	return s.GroupForMember(ctx, groupID, creatorID)
}

func (s *Store) GroupPreviewByCode(ctx context.Context, code, userID string) (GroupPreview, error) {
	var p GroupPreview
	err := s.db.QueryRow(ctx,
		`SELECT g.id, g.name, g.description,
		        (SELECT count(*) FROM group_members m WHERE m.group_id = g.id),
		        EXISTS (SELECT 1 FROM group_members m WHERE m.group_id = g.id AND m.user_id = $2)
		 FROM groups g WHERE g.invite_code = $1`, code, userID).
		Scan(&p.ID, &p.Name, &p.Description, &p.MemberCount, &p.IsMember)
	if errors.Is(err, pgx.ErrNoRows) {
		return GroupPreview{}, ErrNotFound
	}
	return p, err
}

// JoinGroup adds a user by invite code. The bool is true if they newly joined.
func (s *Store) JoinGroup(ctx context.Context, code, userID string) (string, bool, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return "", false, err
	}
	defer tx.Rollback(ctx)

	// Locking the group row makes joins wait in line, so two people joining at the
	// same moment cannot both squeeze in as the last member.
	var groupID string
	err = tx.QueryRow(ctx, `SELECT id FROM groups WHERE invite_code = $1 FOR UPDATE`, code).Scan(&groupID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, ErrNotFound
	}
	if err != nil {
		return "", false, err
	}

	var isMember, isBanned bool
	var count int
	err = tx.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM group_members WHERE group_id = $1 AND user_id = $2),
		        EXISTS (SELECT 1 FROM group_bans WHERE group_id = $1 AND user_id = $2),
		        (SELECT count(*) FROM group_members WHERE group_id = $1)`,
		groupID, userID).Scan(&isMember, &isBanned, &count)
	if err != nil {
		return "", false, err
	}
	switch {
	case isMember:
		return groupID, false, nil
	case isBanned:
		return "", false, ErrBanned
	case count >= MaxGroupMembers:
		return "", false, ErrGroupFull
	}

	if _, err := tx.Exec(ctx, `INSERT INTO group_members (group_id, user_id) VALUES ($1, $2)`, groupID, userID); err != nil {
		return "", false, err
	}
	return groupID, true, tx.Commit(ctx)
}

func (s *Store) UpdateGroup(ctx context.Context, groupID string, p GroupPatch) error {
	_, err := s.db.Exec(ctx,
		`UPDATE groups SET
		     name               = COALESCE($2, name),
		     description        = COALESCE($3, description),
		     mystery_mode       = COALESCE($4, mystery_mode),
		     chat_enabled       = COALESCE($5, chat_enabled),
		     taunts_enabled     = COALESCE($6, taunts_enabled)
		 WHERE id = $1`,
		groupID, p.Name, p.Description, p.MysteryMode, p.ChatEnabled, p.TauntsEnabled)
	return err
}

func (s *Store) SetChallenge(ctx context.Context, groupID string, start, end time.Time) error {
	_, err := s.db.Exec(ctx,
		`UPDATE groups SET challenge_start = $2, challenge_end = $3 WHERE id = $1`, groupID, start, end)
	return err
}

func (s *Store) ClearChallenge(ctx context.Context, groupID string) error {
	_, err := s.db.Exec(ctx,
		`UPDATE groups SET challenge_start = NULL, challenge_end = NULL WHERE id = $1`, groupID)
	return err
}

func (s *Store) DeleteGroup(ctx context.Context, groupID string) error {
	_, err := s.db.Exec(ctx, `DELETE FROM groups WHERE id = $1`, groupID)
	return err
}

// ---------- members ----------

func (s *Store) MemberIDs(ctx context.Context, groupID string) ([]string, error) {
	rows, err := s.db.Query(ctx, `SELECT user_id FROM group_members WHERE group_id = $1`, groupID)
	if err != nil {
		return nil, err
	}
	return scanIDs(rows)
}

// IsMember answers without reading the whole member list.
func (s *Store) IsMember(ctx context.Context, groupID, userID string) (bool, error) {
	var yes bool
	err := s.db.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM group_members WHERE group_id = $1 AND user_id = $2)`,
		groupID, userID).Scan(&yes)
	return yes, err
}

// Members returns one page of members, admins first. Name is filled in by the
// caller. People in `hide` are left out, and `total` does not count them.
func (s *Store) Members(ctx context.Context, groupID string, hide []string, limit, offset int) ([]Member, int, error) {
	const filter = `group_id = $1 AND NOT (user_id::text = ANY($2::text[]))`

	// A nil slice would reach SQL as NULL, and "NOT (x = ANY(NULL))" is never true,
	// which would silently hide every member.
	if hide == nil {
		hide = []string{}
	}

	var total int
	if err := s.db.QueryRow(ctx, `SELECT count(*) FROM group_members WHERE `+filter, groupID, hide).Scan(&total); err != nil {
		return nil, 0, err
	}

	rows, err := s.db.Query(ctx,
		`SELECT user_id, role, joined_at FROM group_members WHERE `+filter+`
		 ORDER BY role = 'admin' DESC, joined_at, user_id LIMIT $3 OFFSET $4`,
		groupID, hide, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	members := []Member{}
	for rows.Next() {
		var m Member
		if err := rows.Scan(&m.UserID, &m.Role, &m.JoinedAt); err != nil {
			return nil, 0, err
		}
		members = append(members, m)
	}
	return members, total, rows.Err()
}

// SetRole changes a member's role, refusing to leave the group with no admin.
func (s *Store) SetRole(ctx context.Context, groupID, userID, role string) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, `SELECT 1 FROM groups WHERE id = $1 FOR UPDATE`, groupID); err != nil {
		return err
	}
	if role != "admin" {
		var otherAdmins int
		err := tx.QueryRow(ctx,
			`SELECT count(*) FROM group_members WHERE group_id = $1 AND role = 'admin' AND user_id <> $2`,
			groupID, userID).Scan(&otherAdmins)
		if err != nil {
			return err
		}
		if otherAdmins == 0 {
			return ErrLastAdmin
		}
	}
	tag, err := tx.Exec(ctx,
		`UPDATE group_members SET role = $3 WHERE group_id = $1 AND user_id = $2`, groupID, userID, role)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return tx.Commit(ctx)
}

// RemoveMember takes someone out and stops them rejoining with the invite link.
func (s *Store) RemoveMember(ctx context.Context, groupID, userID string) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	tag, err := tx.Exec(ctx, `DELETE FROM group_members WHERE group_id = $1 AND user_id = $2`, groupID, userID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO group_bans (group_id, user_id) VALUES ($1, $2) ON CONFLICT DO NOTHING`, groupID, userID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) LeaveGroup(ctx context.Context, groupID, userID string) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	if err := leaveGroup(ctx, tx, groupID, userID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// leaveGroup keeps a group working after someone goes: if no admin is left the
// longest-standing member takes over, and a group with nobody left is deleted.
func leaveGroup(ctx context.Context, tx pgx.Tx, groupID, userID string) error {
	if _, err := tx.Exec(ctx, `SELECT 1 FROM groups WHERE id = $1 FOR UPDATE`, groupID); err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `DELETE FROM group_members WHERE group_id = $1 AND user_id = $2`, groupID, userID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}

	var members, admins int
	err = tx.QueryRow(ctx,
		`SELECT count(*), count(*) FILTER (WHERE role = 'admin') FROM group_members WHERE group_id = $1`,
		groupID).Scan(&members, &admins)
	if err != nil {
		return err
	}
	switch {
	case members == 0:
		_, err = tx.Exec(ctx, `DELETE FROM groups WHERE id = $1`, groupID)
	case admins == 0:
		_, err = tx.Exec(ctx,
			`UPDATE group_members SET role = 'admin'
			 WHERE group_id = $1 AND user_id = (
			     SELECT user_id FROM group_members WHERE group_id = $1 ORDER BY joined_at, user_id LIMIT 1
			 )`, groupID)
	}
	return err
}
