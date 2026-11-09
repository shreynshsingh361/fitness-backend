package api

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"

	"fitness/auth-service/internal/store"
)

// maxLookupIDs limits one request; callers ask in batches.
const maxLookupIDs = 1000

// lookupUsers gives other services the display names of a list of users. Only the
// name: emails and password hashes never leave this service.
func (h *handler) lookupUsers(c *gin.Context) {
	var in struct {
		IDs []string `json:"ids"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		fail(c, http.StatusBadRequest, "invalid_json", "request body must be valid JSON", nil)
		return
	}
	if len(in.IDs) > maxLookupIDs {
		fail(c, http.StatusUnprocessableEntity, "validation_error", "some fields are invalid",
			map[string]string{"ids": "at most 1000 ids per request"})
		return
	}
	for _, id := range in.IDs {
		if _, err := uuid.Parse(id); err != nil {
			fail(c, http.StatusUnprocessableEntity, "validation_error", "some fields are invalid",
				map[string]string{"ids": "must all be uuids"})
			return
		}
	}
	if len(in.IDs) == 0 {
		c.JSON(http.StatusOK, gin.H{"users": []store.UserName{}})
		return
	}

	users, err := h.store.NamesByIDs(c.Request.Context(), in.IDs)
	if err != nil {
		serverError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"users": users})
}

// verifyPassword lets the gateway confirm it is really you before deleting an account:
// your password, or, for an account that signs in with Firebase, a Firebase sign-in
// from the last few minutes.
func (h *handler) verifyPassword(c *gin.Context) {
	id := c.Param("id")
	if _, err := uuid.Parse(id); err != nil {
		fail(c, http.StatusNotFound, "not_found", "no such user", nil)
		return
	}
	var in struct {
		Password        string `json:"password"`
		FirebaseIDToken string `json:"firebase_id_token"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		fail(c, http.StatusBadRequest, "invalid_json", "request body must be valid JSON", nil)
		return
	}

	user, err := h.store.UserByID(c.Request.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		fail(c, http.StatusNotFound, "not_found", "no such user", nil)
		return
	}
	if err != nil {
		serverError(c, err)
		return
	}
	if h.recentFirebaseSignIn(c.Request.Context(), user, in.FirebaseIDToken) {
		c.Status(http.StatusNoContent)
		return
	}
	if in.Password == "" || bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(in.Password)) != nil {
		fail(c, http.StatusUnauthorized, "invalid_password", "the password is incorrect", nil)
		return
	}
	c.Status(http.StatusNoContent)
}

// userExists answers the gateway's question "is this account still there?": 204 if so, 404 if it
// was deleted (or never existed). No body, and nothing about the person.
func (h *handler) userExists(c *gin.Context) {
	id := c.Param("id")
	if _, err := uuid.Parse(id); err != nil {
		fail(c, http.StatusNotFound, "not_found", "no such user", nil)
		return
	}
	exists, err := h.store.UserExists(c.Request.Context(), id)
	if err != nil {
		serverError(c, err)
		return
	}
	if !exists {
		fail(c, http.StatusNotFound, "not_found", "no such user", nil)
		return
	}
	c.Status(http.StatusNoContent)
}

// deleteUser removes the account and all its sessions. It is safe to repeat.
func (h *handler) deleteUser(c *gin.Context) {
	id := c.Param("id")
	if _, err := uuid.Parse(id); err != nil {
		fail(c, http.StatusNotFound, "not_found", "no such user", nil)
		return
	}
	if err := h.store.DeleteUser(c.Request.Context(), id); err != nil {
		serverError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}
