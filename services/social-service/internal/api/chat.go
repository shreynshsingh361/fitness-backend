package api

import (
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"

	"fitness/social-service/internal/store"
)

const maxMessageLength = 1000

func (h *handler) listMessages(c *gin.Context) {
	group, ok := h.member(c)
	if !ok {
		return
	}
	// When an admin turns chat off, it is off: often that is because of what was
	// said, so the old messages stop being shown as well.
	if !group.ChatEnabled {
		c.JSON(http.StatusOK, gin.H{"chat_enabled": false, "items": []store.Message{}})
		return
	}

	limit := intQuery(c, "limit", 50)
	if limit < 1 || limit > 100 {
		limit = 50
	}
	var before *time.Time
	if value := c.Query("before"); value != "" {
		t, err := time.Parse(time.RFC3339, value)
		if err != nil {
			invalid(c, map[string]string{"before": "must be a timestamp like 2026-11-07T10:00:00Z"})
			return
		}
		before = &t
	}

	messages, err := h.store.Messages(c.Request.Context(), group.ID, userID(c), before, limit)
	if err != nil {
		serverError(c, err)
		return
	}
	ids := make([]string, 0, len(messages))
	for _, m := range messages {
		ids = append(ids, m.UserID)
	}
	names := h.lookupNames(c, ids)
	for i := range messages {
		messages[i].Name = nameOf(names, messages[i].UserID)
	}
	c.JSON(http.StatusOK, gin.H{"chat_enabled": true, "items": messages})
}

func (h *handler) postMessage(c *gin.Context) {
	group, ok := h.member(c)
	if !ok {
		return
	}
	if !group.ChatEnabled {
		fail(c, http.StatusForbidden, "chat_disabled", "this group's admin has turned chat off", nil)
		return
	}
	var in struct {
		Body string `json:"body"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		fail(c, http.StatusBadRequest, "invalid_json", "request body must be valid JSON", nil)
		return
	}
	body := strings.TrimSpace(in.Body)
	if body == "" || utf8.RuneCountInString(body) > maxMessageLength {
		invalid(c, map[string]string{"body": "must be between 1 and 1000 characters"})
		return
	}

	message, err := h.store.AddMessage(c.Request.Context(), group.ID, userID(c), body)
	if err != nil {
		serverError(c, err)
		return
	}
	names := h.lookupNames(c, []string{userID(c)})
	message.Name = nameOf(names, userID(c))
	c.JSON(http.StatusCreated, message)
}

func (h *handler) removeMessage(c *gin.Context) {
	group, ok := h.member(c)
	if !ok {
		return
	}
	messageID, ok := pathUUID(c, "messageId")
	if !ok {
		return
	}
	ctx := c.Request.Context()
	author, err := h.store.MessageAuthor(ctx, group.ID, messageID)
	if errors.Is(err, store.ErrNotFound) {
		fail(c, http.StatusNotFound, "not_found", "message not found", nil)
		return
	}
	if err != nil {
		serverError(c, err)
		return
	}
	if author != userID(c) && group.MyRole != "admin" {
		fail(c, http.StatusForbidden, "admin_only", "only its author or a group admin can remove a message", nil)
		return
	}
	if err := h.store.RemoveMessage(ctx, group.ID, messageID, userID(c)); err != nil {
		serverError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}
