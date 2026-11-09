package api

import (
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/gin-gonic/gin"

	"fitness/social-service/internal/clients"
	"fitness/social-service/internal/leaderboard"
	"fitness/social-service/internal/store"
)

const (
	maxGroupName        = 80
	maxGroupDescription = 500
)

// member loads a group the current user belongs to. To anyone else it answers 404,
// so outsiders cannot even tell the group exists.
func (h *handler) member(c *gin.Context) (store.Group, bool) {
	id, ok := pathUUID(c, "id")
	if !ok {
		return store.Group{}, false
	}
	group, err := h.store.GroupForMember(c.Request.Context(), id, userID(c))
	if errors.Is(err, store.ErrNotFound) {
		fail(c, http.StatusNotFound, "not_found", "group not found", nil)
		return store.Group{}, false
	}
	if err != nil {
		serverError(c, err)
		return store.Group{}, false
	}
	return group, true
}

// admin loads a group the current user is an admin of.
func (h *handler) admin(c *gin.Context) (store.Group, bool) {
	group, ok := h.member(c)
	if !ok {
		return store.Group{}, false
	}
	if group.MyRole != "admin" {
		fail(c, http.StatusForbidden, "admin_only", "only a group admin can do this", nil)
		return store.Group{}, false
	}
	return group, true
}

// reloadGroup answers with the group as it is now, after a change.
func (h *handler) reloadGroup(c *gin.Context, groupID string) {
	group, err := h.store.GroupForMember(c.Request.Context(), groupID, userID(c))
	if err != nil {
		serverError(c, err)
		return
	}
	c.JSON(http.StatusOK, group)
}

// ---------- creating and joining ----------

type createGroupInput struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

func validateGroupText(name, description *string) map[string]string {
	problems := map[string]string{}
	if name != nil {
		*name = strings.TrimSpace(*name)
		if *name == "" || utf8.RuneCountInString(*name) > maxGroupName {
			problems["name"] = "is required and must be at most 80 characters"
		}
	}
	if description != nil {
		*description = strings.TrimSpace(*description)
		if utf8.RuneCountInString(*description) > maxGroupDescription {
			problems["description"] = "must be at most 500 characters"
		}
	}
	return problems
}

func (h *handler) createGroup(c *gin.Context) {
	var in createGroupInput
	if err := c.ShouldBindJSON(&in); err != nil {
		fail(c, http.StatusBadRequest, "invalid_json", "request body must be valid JSON", nil)
		return
	}
	if problems := validateGroupText(&in.Name, &in.Description); len(problems) > 0 {
		invalid(c, problems)
		return
	}
	group, err := h.store.CreateGroup(c.Request.Context(), userID(c), in.Name, in.Description)
	if err != nil {
		serverError(c, err)
		return
	}
	c.JSON(http.StatusCreated, group)
}

func (h *handler) listGroups(c *gin.Context) {
	groups, err := h.store.GroupsForUser(c.Request.Context(), userID(c))
	if err != nil {
		serverError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": groups})
}

func (h *handler) previewGroupInvite(c *gin.Context) {
	preview, err := h.store.GroupPreviewByCode(c.Request.Context(), c.Param("code"), userID(c))
	if errors.Is(err, store.ErrNotFound) {
		fail(c, http.StatusNotFound, "invalid_invite", "this invite link is not valid", nil)
		return
	}
	if err != nil {
		serverError(c, err)
		return
	}
	c.JSON(http.StatusOK, preview)
}

func (h *handler) joinGroup(c *gin.Context) {
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
	groupID, created, err := h.store.JoinGroup(ctx, in.Code, userID(c))
	switch {
	case errors.Is(err, store.ErrNotFound):
		fail(c, http.StatusNotFound, "invalid_invite", "this invite link is not valid", nil)
		return
	case errors.Is(err, store.ErrBanned):
		fail(c, http.StatusForbidden, "removed_from_group", "an admin removed you from this group", nil)
		return
	case errors.Is(err, store.ErrGroupFull):
		fail(c, http.StatusConflict, "group_full", fmt.Sprintf("this group is full: a group can have at most %d members", store.MaxGroupMembers), nil)
		return
	case err != nil:
		serverError(c, err)
		return
	}

	group, err := h.store.GroupForMember(ctx, groupID, userID(c))
	if err != nil {
		serverError(c, err)
		return
	}
	status := http.StatusCreated
	if !created {
		status = http.StatusOK
	}
	c.JSON(status, group)
}

// ---------- one group ----------

func (h *handler) getGroup(c *gin.Context) {
	if group, ok := h.member(c); ok {
		c.JSON(http.StatusOK, group)
	}
}

func (h *handler) patchGroup(c *gin.Context) {
	group, ok := h.admin(c)
	if !ok {
		return
	}
	var in store.GroupPatch
	if err := c.ShouldBindJSON(&in); err != nil {
		fail(c, http.StatusBadRequest, "invalid_json", "request body must be valid JSON", nil)
		return
	}
	if problems := validateGroupText(in.Name, in.Description); len(problems) > 0 {
		invalid(c, problems)
		return
	}
	if err := h.store.UpdateGroup(c.Request.Context(), group.ID, in); err != nil {
		serverError(c, err)
		return
	}
	h.reloadGroup(c, group.ID)
}

func (h *handler) deleteGroup(c *gin.Context) {
	group, ok := h.admin(c)
	if !ok {
		return
	}
	if err := h.store.DeleteGroup(c.Request.Context(), group.ID); err != nil {
		serverError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *handler) setChallenge(c *gin.Context) {
	group, ok := h.admin(c)
	if !ok {
		return
	}
	var in struct {
		Start string `json:"start"`
		End   string `json:"end"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		fail(c, http.StatusBadRequest, "invalid_json", "request body must be valid JSON", nil)
		return
	}
	start, end, problems := validateChallenge(in.Start, in.End)
	if len(problems) > 0 {
		invalid(c, problems)
		return
	}
	if err := h.store.SetChallenge(c.Request.Context(), group.ID, start, end); err != nil {
		serverError(c, err)
		return
	}
	h.reloadGroup(c, group.ID)
}

func (h *handler) clearChallenge(c *gin.Context) {
	group, ok := h.admin(c)
	if !ok {
		return
	}
	if err := h.store.ClearChallenge(c.Request.Context(), group.ID); err != nil {
		serverError(c, err)
		return
	}
	h.reloadGroup(c, group.ID)
}

// ---------- members ----------

// visibleMembers lists a group's members minus anyone hidden in either direction.
func (h *handler) visibleMembers(c *gin.Context, groupID string) ([]string, error) {
	ctx := c.Request.Context()
	ids, err := h.store.MemberIDs(ctx, groupID)
	if err != nil {
		return nil, err
	}
	hidden, err := h.store.HiddenEitherWay(ctx, userID(c))
	if err != nil {
		return nil, err
	}
	visible := make([]string, 0, len(ids))
	for _, id := range ids {
		if !hidden[id] {
			visible = append(visible, id)
		}
	}
	return visible, nil
}

func (h *handler) listMembers(c *gin.Context) {
	group, ok := h.member(c)
	if !ok {
		return
	}
	ctx := c.Request.Context()
	hidden, err := h.store.HiddenEitherWay(ctx, userID(c))
	if err != nil {
		serverError(c, err)
		return
	}
	hide := make([]string, 0, len(hidden))
	for id := range hidden {
		hide = append(hide, id)
	}

	page, limit, offset := pageParams(c)
	members, total, err := h.store.Members(ctx, group.ID, hide, limit, offset)
	if err != nil {
		serverError(c, err)
		return
	}
	ids := make([]string, 0, len(members))
	for _, m := range members {
		ids = append(ids, m.UserID)
	}
	names := h.lookupNames(c, ids)
	for i := range members {
		members[i].Name = nameOf(names, members[i].UserID)
		members[i].IsMe = members[i].UserID == userID(c)
	}
	c.JSON(http.StatusOK, gin.H{"items": members, "page": page, "limit": limit, "total": total})
}

func (h *handler) setMemberRole(c *gin.Context) {
	group, ok := h.admin(c)
	if !ok {
		return
	}
	target, ok := pathUUID(c, "userId")
	if !ok {
		return
	}
	var in struct {
		Role string `json:"role"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		fail(c, http.StatusBadRequest, "invalid_json", "request body must be valid JSON", nil)
		return
	}
	if in.Role != "admin" && in.Role != "member" {
		invalid(c, map[string]string{"role": "must be admin or member"})
		return
	}
	err := h.store.SetRole(c.Request.Context(), group.ID, target, in.Role)
	switch {
	case errors.Is(err, store.ErrNotFound):
		fail(c, http.StatusNotFound, "not_found", "this person is not in the group", nil)
	case errors.Is(err, store.ErrLastAdmin):
		fail(c, http.StatusConflict, "last_admin", "a group must keep at least one admin", nil)
	case err != nil:
		serverError(c, err)
	default:
		c.Status(http.StatusNoContent)
	}
}

func (h *handler) removeMember(c *gin.Context) {
	group, ok := h.admin(c)
	if !ok {
		return
	}
	target, ok := pathUUID(c, "userId")
	if !ok {
		return
	}
	if target == userID(c) {
		fail(c, http.StatusUnprocessableEntity, "cannot_remove_self", "use leave to remove yourself", nil)
		return
	}
	err := h.store.RemoveMember(c.Request.Context(), group.ID, target)
	if errors.Is(err, store.ErrNotFound) {
		fail(c, http.StatusNotFound, "not_found", "this person is not in the group", nil)
		return
	}
	if err != nil {
		serverError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *handler) leaveGroup(c *gin.Context) {
	group, ok := h.member(c)
	if !ok {
		return
	}
	if err := h.store.LeaveGroup(c.Request.Context(), group.ID, userID(c)); err != nil {
		serverError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// ---------- the group leaderboard ----------

type leaderboardRow struct {
	Rank        int    `json:"rank"`
	UserID      string `json:"user_id"`
	Name        string `json:"name"`
	Steps       *int   `json:"steps"`
	StepsHidden bool   `json:"steps_hidden"`
	IsMe        bool   `json:"is_me"`
}

// groupLeaderboard is worked out fresh each time from everyone's steps, so there is
// nothing to start, stop or reset. People come from this service, steps from the
// activity service, names from the auth service.
func (h *handler) groupLeaderboard(c *gin.Context) {
	group, ok := h.member(c)
	if !ok {
		return
	}
	date, ok := queryDay(c)
	if !ok {
		return
	}
	period := c.DefaultQuery("period", leaderboard.Day)
	start, end := challengeDates(group)
	from, to, err := leaderboard.Range(period, date, start, end)
	switch {
	case errors.Is(err, leaderboard.ErrNoChallenge):
		fail(c, http.StatusUnprocessableEntity, "no_challenge", "this group has no fixed-date challenge", nil)
		return
	case err != nil:
		invalid(c, map[string]string{"period": "must be day, yesterday, week, month or challenge"})
		return
	}

	people, err := h.visibleMembers(c, group.ID)
	if err != nil {
		serverError(c, err)
		return
	}

	req := clients.TotalsRequest{
		UserIDs: people,
		From:    from.Format(dateLayout),
		To:      to.Format(dateLayout),
	}
	cutoff := leaderboard.RevealCutoff(timeNow())
	mysteryActive := group.MysteryMode && !to.Before(cutoff)
	if mysteryActive {
		req.RevealBefore = cutoff.Format(dateLayout)
	}
	totals, err := h.steps.Totals(requestContext(c), req)
	if err != nil {
		log.Printf("req=%s step totals unavailable: %v", c.GetHeader("X-Request-ID"), err)
		fail(c, http.StatusServiceUnavailable, "steps_unavailable", "step data is temporarily unavailable", nil)
		return
	}

	// Ranks always use the true totals. That is the point of mystery mode: you can
	// see where you stand, just not by how much.
	steps := make(map[string]int, len(totals))
	for id, total := range totals {
		steps[id] = total.Steps
	}
	rows := leaderboard.Rank(people, steps)

	page, limit, offset := pageParams(c)
	first, last := min(offset, len(rows)), min(offset+limit, len(rows))
	pageRows := rows[first:last]

	me := userID(c)
	wanted := []string{me}
	for _, row := range pageRows {
		if row.UserID != me {
			wanted = append(wanted, row.UserID)
		}
	}
	names := h.lookupNames(c, wanted)

	toEntry := func(row leaderboard.Row) leaderboardRow {
		visible, hidden := leaderboard.Visible(
			row.UserID == me, row.Steps, totals[row.UserID].RevealedSteps,
			mysteryActive, !from.Before(cutoff),
		)
		return leaderboardRow{
			Rank:        row.Rank,
			UserID:      row.UserID,
			Name:        nameOf(names, row.UserID),
			Steps:       visible,
			StepsHidden: hidden,
			IsMe:        row.UserID == me,
		}
	}

	items := make([]leaderboardRow, 0, len(pageRows))
	for _, row := range pageRows {
		items = append(items, toEntry(row))
	}
	var mine *leaderboardRow
	for _, row := range rows {
		if row.UserID == me {
			entry := toEntry(row)
			mine = &entry
			break
		}
	}

	response := gin.H{
		"period":         period,
		"from":           from.Format(dateLayout),
		"to":             to.Format(dateLayout),
		"mystery_active": mysteryActive,
		"page":           page,
		"limit":          limit,
		"total":          len(rows),
		"items":          items,
		"me":             mine,
	}
	if mysteryActive {
		response["revealed_through"] = cutoff.AddDate(0, 0, -1).Format(dateLayout)
	}
	c.JSON(http.StatusOK, response)
}
