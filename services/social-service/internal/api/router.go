package api

import (
	"context"
	"log"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"fitness/social-service/internal/clients"
	"fitness/social-service/internal/store"
)

// fallbackName is shown if the auth service does not answer. A leaderboard with
// plain labels is still useful, so that alone is not a reason to fail the request.
const fallbackName = "Member"

// StepReader and NameReader are what this service needs from the activity and auth
// services. Being interfaces, tests could swap in fakes without running those services.
type StepReader interface {
	Totals(ctx context.Context, req clients.TotalsRequest) (map[string]clients.UserTotal, error)
}

type NameReader interface {
	Names(ctx context.Context, ids []string) (map[string]string, error)
}

type handler struct {
	store *store.Store
	steps StepReader
	names NameReader
}

// NewRouter lists every request this service accepts.
func NewRouter(s *store.Store, steps StepReader, names NameReader) *gin.Engine {
	h := &handler{store: s, steps: steps, names: names}

	r := gin.New()
	r.Use(gin.Recovery(), requestLog())

	r.GET("/health", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok", "service": "social-service"})
	})

	v1 := r.Group("/api/v1", requireUser())

	v1.GET("/friends", h.friendsBoard)
	v1.GET("/friends/invite", h.friendInvite)
	v1.GET("/friends/invite/:code", h.previewFriendInvite)
	v1.POST("/friends/accept", h.acceptFriend)
	v1.GET("/friends/hidden", h.hiddenList)
	v1.DELETE("/friends/:id", h.removeFriend)
	v1.POST("/friends/:id/hide", h.hide)
	v1.DELETE("/friends/:id/hide", h.unhide)

	v1.POST("/nudges", h.sendNudge)
	v1.GET("/nudges", h.listNudges)
	v1.POST("/nudges/read", h.readNudges)

	v1.POST("/groups", h.createGroup)
	v1.GET("/groups", h.listGroups)
	v1.GET("/groups/invite/:code", h.previewGroupInvite)
	v1.POST("/groups/join", h.joinGroup)
	v1.GET("/groups/:id", h.getGroup)
	v1.PATCH("/groups/:id", h.patchGroup)
	v1.DELETE("/groups/:id", h.deleteGroup)
	v1.PUT("/groups/:id/challenge", h.setChallenge)
	v1.DELETE("/groups/:id/challenge", h.clearChallenge)
	v1.GET("/groups/:id/leaderboard", h.groupLeaderboard)
	v1.GET("/groups/:id/report.csv", h.groupReport)
	v1.GET("/groups/:id/members", h.listMembers)
	v1.PATCH("/groups/:id/members/:userId", h.setMemberRole)
	v1.DELETE("/groups/:id/members/:userId", h.removeMember)
	v1.POST("/groups/:id/leave", h.leaveGroup)
	v1.GET("/groups/:id/messages", h.listMessages)
	v1.POST("/groups/:id/messages", h.postMessage)
	v1.DELETE("/groups/:id/messages/:messageId", h.removeMessage)

	// For other services only. The gateway has no route to /internal, so this
	// cannot be reached from outside (see the gateway's route table).
	r.DELETE("/internal/users/:id", h.deleteUser)
	return r
}

// requestContext carries the request id along to calls to other services.
func requestContext(c *gin.Context) context.Context {
	return clients.WithRequestID(c.Request.Context(), c.GetHeader("X-Request-ID"))
}

// pathUUID reads a uuid from the path and answers 404 itself if it is not one. It
// also normalizes the id, because friendships are stored in a fixed order.
func pathUUID(c *gin.Context, name string) (string, bool) {
	id, err := uuid.Parse(c.Param(name))
	if err != nil {
		fail(c, http.StatusNotFound, "not_found", "not found", nil)
		return "", false
	}
	return id.String(), true
}

// lookupNames never fails: without the auth service people just get a plain label.
func (h *handler) lookupNames(c *gin.Context, ids []string) map[string]string {
	names, err := h.names.Names(requestContext(c), ids)
	if err != nil {
		log.Printf("req=%s names unavailable: %v", c.GetHeader("X-Request-ID"), err)
		return map[string]string{}
	}
	return names
}

func nameOf(names map[string]string, id string) string {
	if name, ok := names[id]; ok {
		return name
	}
	return fallbackName
}

// pageParams reads ?page=&limit= (defaults 1 and 50, limit capped at 100).
func pageParams(c *gin.Context) (page, limit, offset int) {
	page = intQuery(c, "page", 1)
	limit = intQuery(c, "limit", 50)
	if page < 1 {
		page = 1
	}
	if limit < 1 {
		limit = 50
	}
	if limit > 100 {
		limit = 100
	}
	return page, limit, (page - 1) * limit
}

func (h *handler) deleteUser(c *gin.Context) {
	id, ok := pathUUID(c, "id")
	if !ok {
		return
	}
	if err := h.store.DeleteUser(c.Request.Context(), id); err != nil {
		serverError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}
