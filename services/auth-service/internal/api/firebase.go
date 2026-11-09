package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"fitness/auth-service/internal/firebase"
	"fitness/auth-service/internal/store"
)

// IDTokenVerifier checks a Firebase ID token. *firebase.Verifier is the real one.
type IDTokenVerifier interface {
	Verify(ctx context.Context, raw string) (firebase.User, error)
}

// recentSignIn is how fresh a Firebase sign-in must be to delete an account with it.
const recentSignIn = 5 * time.Minute

type firebaseInput struct {
	IDToken string `json:"id_token"`
	Name    string `json:"name"` // used only when the account is new and Firebase has no name
}

// firebaseSignIn trades a Firebase ID token for this app's own tokens.
//
//   - Someone seen before (by Firebase user id) just signs in.
//   - Someone with an account here from before Firebase (same email) gets that
//     account, now tied to their Firebase user.
//   - Anyone else gets a new account.
//
// The email must be verified. Otherwise anyone could make a Firebase account with
// someone else's email and walk into their account here.
func (h *handler) firebaseSignIn(c *gin.Context) {
	if h.firebase == nil {
		fail(c, http.StatusServiceUnavailable, "firebase_not_configured", "Firebase sign-in is not set up on this server", nil)
		return
	}
	var in firebaseInput
	if err := c.ShouldBindJSON(&in); err != nil || in.IDToken == "" {
		fail(c, http.StatusBadRequest, "invalid_json", "id_token is required", nil)
		return
	}
	fb, err := h.firebase.Verify(c.Request.Context(), in.IDToken)
	if err != nil {
		fail(c, http.StatusUnauthorized, "invalid_firebase_token", "the Firebase sign-in is invalid or expired", nil)
		return
	}
	if fb.Email == "" || !fb.EmailVerified {
		fail(c, http.StatusForbidden, "email_not_verified", "verify your email first, then sign in again", nil)
		return
	}

	user, created, err := h.firebaseUser(c.Request.Context(), fb, in.Name)
	if errors.Is(err, store.ErrLinked) {
		fail(c, http.StatusConflict, "account_linked_elsewhere",
			"this email's account is already tied to another sign-in", nil)
		return
	}
	if err != nil {
		serverError(c, err)
		return
	}

	access, refreshRaw, refreshHash, err := h.newTokenPair(user.ID)
	if err == nil {
		err = h.store.SaveRefreshToken(c.Request.Context(), user.ID, refreshHash, time.Now().Add(refreshTTL))
	}
	if err != nil {
		serverError(c, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	c.JSON(status, h.response(user, access, refreshRaw))
}

// firebaseUser finds or makes the account for a verified Firebase user.
func (h *handler) firebaseUser(ctx context.Context, fb firebase.User, name string) (user store.User, created bool, err error) {
	user, err = h.store.UserByFirebaseUID(ctx, fb.UID)
	if !errors.Is(err, store.ErrNotFound) {
		return user, false, err
	}
	// Twice at most: if two sign-ins race to create the same account, the loser links to it.
	for range 2 {
		user, err = h.store.UserByEmail(ctx, fb.Email)
		if err == nil && user.FirebaseUID == fb.UID {
			return user, false, nil // a sign-in racing this one just made it
		}
		if err == nil {
			user, err = h.store.LinkFirebase(ctx, user.ID, fb.UID)
			return user, false, err
		}
		if !errors.Is(err, store.ErrNotFound) {
			return user, false, err
		}
		user, err = h.store.CreateFirebaseUser(ctx, fb.Email, displayName(fb, name), fb.UID)
		if !errors.Is(err, store.ErrEmailTaken) {
			return user, err == nil, err
		}
	}
	return user, false, err
}

// displayName picks Firebase's name (Google gives one), then the typed one, then
// the part of the email before the @. At most 80 characters, like registration.
func displayName(fb firebase.User, typed string) string {
	name := fb.Name
	if name == "" {
		name = strings.TrimSpace(typed)
	}
	if name == "" {
		name, _, _ = strings.Cut(fb.Email, "@")
	}
	if r := []rune(name); len(r) > 80 {
		name = string(r[:80])
	}
	return name
}

// recentFirebaseSignIn says whether raw is a fresh Firebase sign-in by this user.
// Deleting an account needs one when the account has no password here.
func (h *handler) recentFirebaseSignIn(ctx context.Context, user store.User, raw string) bool {
	if h.firebase == nil || raw == "" || user.FirebaseUID == "" {
		return false
	}
	fb, err := h.firebase.Verify(ctx, raw)
	return err == nil && fb.UID == user.FirebaseUID && time.Since(fb.AuthTime) <= recentSignIn
}
