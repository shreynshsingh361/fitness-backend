package api

import (
	"errors"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"fitness/social-service/internal/clients"
	"fitness/social-service/internal/leaderboard"
	"fitness/social-service/internal/store"
)

// nudgeCooldown is how long before you can send the same person the same kind again.
const nudgeCooldown = time.Hour

// ---------- invites ----------

func (h *handler) friendInvite(c *gin.Context) {
	code, err := h.store.InviteCode(c.Request.Context(), userID(c))
	if err != nil {
		serverError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"code": code})
}

func (h *handler) previewFriendInvite(c *gin.Context) {
	ctx := c.Request.Context()
	inviter, err := h.store.InviterByCode(ctx, c.Param("code"))
	if errors.Is(err, store.ErrNotFound) {
		fail(c, http.StatusNotFound, "invalid_invite", "this invite link is not valid", nil)
		return
	}
	if err != nil {
		serverError(c, err)
		return
	}
	already, err := h.store.AreFriends(ctx, userID(c), inviter)
	if err != nil {
		serverError(c, err)
		return
	}
	names := h.lookupNames(c, []string{inviter})
	c.JSON(http.StatusOK, gin.H{
		"inviter_name":    nameOf(names, inviter),
		"is_self":         inviter == userID(c),
		"already_friends": already,
	})
}

func (h *handler) acceptFriend(c *gin.Context) {
	var in struct {
		Code string `json:"code"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		fail(c, http.StatusBadRequest, "invalid_json", "request body must be valid JSON", nil)
		return
	}
	in.Code = strings.TrimSpace(in.Code)
	if in.Code == "" {
		invalid(c, map[string]string{"code": "is required"})
		return
	}

	ctx := c.Request.Context()
	inviter, err := h.store.InviterByCode(ctx, in.Code)
	if errors.Is(err, store.ErrNotFound) {
		fail(c, http.StatusNotFound, "invalid_invite", "this invite link is not valid", nil)
		return
	}
	if err != nil {
		serverError(c, err)
		return
	}
	if inviter == userID(c) {
		fail(c, http.StatusUnprocessableEntity, "own_invite", "this is your own invite link", nil)
		return
	}

	created, err := h.store.AddFriend(ctx, userID(c), inviter)
	if err != nil {
		serverError(c, err)
		return
	}
	status := http.StatusCreated
	if !created {
		status = http.StatusOK
	}
	names := h.lookupNames(c, []string{inviter})
	c.JSON(status, gin.H{"user_id": inviter, "name": nameOf(names, inviter)})
}

// ---------- the friends board ----------

type boardRow struct {
	Rank   int    `json:"rank"`
	UserID string `json:"user_id"`
	Name   string `json:"name"`
	Steps  int    `json:"steps"`
	IsMe   bool   `json:"is_me"`
}

func (h *handler) friendsBoard(c *gin.Context) {
	me := userID(c)
	period := c.DefaultQuery("period", leaderboard.Day)
	switch period {
	case leaderboard.Day, leaderboard.Yesterday, leaderboard.Week, leaderboard.Month:
	default:
		invalid(c, map[string]string{"period": "must be day, yesterday, week or month"})
		return
	}
	date, ok := queryDay(c)
	if !ok {
		return
	}
	from, to, err := leaderboard.Range(period, date, nil, nil)
	if err != nil {
		serverError(c, err)
		return
	}

	ctx := c.Request.Context()
	friends, err := h.store.FriendIDs(ctx, me)
	if err != nil {
		serverError(c, err)
		return
	}
	hidden, err := h.store.HiddenEitherWay(ctx, me)
	if err != nil {
		serverError(c, err)
		return
	}

	people := []string{me}
	for _, id := range friends {
		if !hidden[id] {
			people = append(people, id)
		}
	}

	// If step data is unavailable, still answer: the list is how people hide or
	// remove a friend, and that should keep working.
	steps := map[string]int{}
	totals, err := h.steps.Totals(requestContext(c), clients.TotalsRequest{
		UserIDs: people,
		From:    from.Format(dateLayout),
		To:      to.Format(dateLayout),
	})
	stepsAvailable := err == nil
	if err != nil {
		log.Printf("req=%s step totals unavailable: %v", c.GetHeader("X-Request-ID"), err)
	}
	for id, total := range totals {
		steps[id] = total.Steps
	}

	names := h.lookupNames(c, people)
	rows := leaderboard.Rank(people, steps)
	items := make([]boardRow, 0, len(rows))
	for _, row := range rows {
		items = append(items, boardRow{
			Rank:   row.Rank,
			UserID: row.UserID,
			Name:   nameOf(names, row.UserID),
			Steps:  row.Steps,
			IsMe:   row.UserID == me,
		})
	}
	c.JSON(http.StatusOK, gin.H{
		"period":          period,
		"from":            from.Format(dateLayout),
		"to":              to.Format(dateLayout),
		"steps_available": stepsAvailable,
		"items":           items,
	})
}

func (h *handler) removeFriend(c *gin.Context) {
	id, ok := pathUUID(c, "id")
	if !ok {
		return
	}
	err := h.store.RemoveFriend(c.Request.Context(), userID(c), id)
	if errors.Is(err, store.ErrNotFound) {
		fail(c, http.StatusNotFound, "not_found", "this person is not your friend", nil)
		return
	}
	if err != nil {
		serverError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// ---------- hiding ----------

func (h *handler) hide(c *gin.Context) {
	id, ok := pathUUID(c, "id")
	if !ok {
		return
	}
	if id == userID(c) {
		fail(c, http.StatusUnprocessableEntity, "cannot_hide_self", "you cannot hide yourself", nil)
		return
	}
	// Any id is accepted without checking it belongs to someone, so this cannot be
	// used to find out which ids are real accounts.
	if err := h.store.Hide(c.Request.Context(), userID(c), id); err != nil {
		serverError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *handler) unhide(c *gin.Context) {
	id, ok := pathUUID(c, "id")
	if !ok {
		return
	}
	err := h.store.Unhide(c.Request.Context(), userID(c), id)
	if errors.Is(err, store.ErrNotFound) {
		fail(c, http.StatusNotFound, "not_found", "you have not hidden this person", nil)
		return
	}
	if err != nil {
		serverError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *handler) hiddenList(c *gin.Context) {
	ids, err := h.store.HiddenBy(c.Request.Context(), userID(c))
	if err != nil {
		serverError(c, err)
		return
	}
	names := h.lookupNames(c, ids)

	type item struct {
		UserID string `json:"user_id"`
		Name   string `json:"name"`
	}
	items := make([]item, 0, len(ids))
	for _, id := range ids {
		items = append(items, item{UserID: id, Name: nameOf(names, id)})
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}

// ---------- cheers and taunts ----------

type nudgeInput struct {
	ToUserID string  `json:"to_user_id"`
	Kind     string  `json:"kind"`
	GroupID  *string `json:"group_id"`
}

func validateNudge(in nudgeInput, me string) map[string]string {
	problems := map[string]string{}
	if _, err := uuid.Parse(in.ToUserID); err != nil {
		problems["to_user_id"] = "must be a uuid"
	} else if in.ToUserID == me {
		problems["to_user_id"] = "you cannot send one to yourself"
	}
	if in.Kind != "cheer" && in.Kind != "taunt" {
		problems["kind"] = "must be cheer or taunt"
	}
	if in.GroupID != nil {
		if _, err := uuid.Parse(*in.GroupID); err != nil {
			problems["group_id"] = "must be a uuid"
		}
	}
	return problems
}

func (h *handler) sendNudge(c *gin.Context) {
	var in nudgeInput
	if err := c.ShouldBindJSON(&in); err != nil {
		fail(c, http.StatusBadRequest, "invalid_json", "request body must be valid JSON", nil)
		return
	}
	me := userID(c)
	if problems := validateNudge(in, me); len(problems) > 0 {
		invalid(c, problems)
		return
	}

	// Every "you may not" below looks the same from outside, so this endpoint
	// cannot be used to discover who exists or who has hidden you.
	notAllowed := func() {
		fail(c, http.StatusNotFound, "not_found", "you can only send these to friends or people in your group", nil)
	}

	ctx := c.Request.Context()
	hidden, err := h.store.HiddenEitherWay(ctx, me)
	if err != nil {
		serverError(c, err)
		return
	}
	if hidden[in.ToUserID] {
		notAllowed()
		return
	}

	if in.GroupID != nil {
		group, err := h.store.GroupForMember(ctx, *in.GroupID, me)
		if errors.Is(err, store.ErrNotFound) {
			notAllowed()
			return
		}
		if err != nil {
			serverError(c, err)
			return
		}
		together, err := h.store.IsMember(ctx, *in.GroupID, in.ToUserID)
		if err != nil {
			serverError(c, err)
			return
		}
		if !together {
			notAllowed()
			return
		}
		if in.Kind == "taunt" && !group.TauntsEnabled {
			fail(c, http.StatusForbidden, "taunts_disabled", "this group's admin has turned taunts off", nil)
			return
		}
	} else {
		friends, err := h.store.AreFriends(ctx, me, in.ToUserID)
		if err != nil {
			serverError(c, err)
			return
		}
		if !friends {
			notAllowed()
			return
		}
	}

	recent, err := h.store.NudgedSince(ctx, me, in.ToUserID, in.Kind, time.Now().Add(-nudgeCooldown))
	if err != nil {
		serverError(c, err)
		return
	}
	if recent {
		fail(c, http.StatusTooManyRequests, "too_many_nudges", "you already sent this person one recently", nil)
		return
	}
	if err := h.store.AddNudge(ctx, me, in.ToUserID, in.Kind, in.GroupID); err != nil {
		serverError(c, err)
		return
	}
	c.Status(http.StatusCreated)
}

func (h *handler) listNudges(c *gin.Context) {
	limit := intQuery(c, "limit", 20)
	if limit < 1 || limit > 100 {
		limit = 20
	}
	nudges, unread, err := h.store.Nudges(c.Request.Context(), userID(c), limit)
	if err != nil {
		serverError(c, err)
		return
	}
	ids := make([]string, 0, len(nudges))
	for _, n := range nudges {
		ids = append(ids, n.FromUserID)
	}
	names := h.lookupNames(c, ids)
	for i := range nudges {
		nudges[i].FromName = nameOf(names, nudges[i].FromUserID)
	}
	c.JSON(http.StatusOK, gin.H{"unread": unread, "items": nudges})
}

func (h *handler) readNudges(c *gin.Context) {
	if err := h.store.MarkNudgesRead(c.Request.Context(), userID(c)); err != nil {
		serverError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}
