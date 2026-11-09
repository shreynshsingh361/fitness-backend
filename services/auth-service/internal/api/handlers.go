package api

import (
	"errors"
	"net/http"
	"net/mail"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/bcrypt"

	"fitness/auth-service/internal/store"
	"fitness/auth-service/internal/token"
)

const refreshTTL = 30 * 24 * time.Hour

type handler struct {
	store    *store.Store
	tokens   *token.Maker
	firebase IDTokenVerifier // nil when FIREBASE_PROJECT_ID is not set
	// legacyPasswords keeps the old sign-up and login (an email nobody checks, and a
	// password kept here). Off in production: sign-up and login go through Firebase.
	legacyPasswords bool
}

// NewRouter lists every request this service accepts. fb may be nil: Firebase
// sign-in then answers 503. legacyPasswords turns the old email-and-password
// sign-up and login on; off, they answer 410.
func NewRouter(s *store.Store, tokens *token.Maker, fb IDTokenVerifier, legacyPasswords bool) *gin.Engine {
	h := &handler{store: s, tokens: tokens, firebase: fb, legacyPasswords: legacyPasswords}

	r := gin.New()
	r.Use(gin.Recovery(), requestLog())

	r.GET("/health", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok", "service": "auth-service"})
	})

	auth := r.Group("/api/v1/auth")
	auth.POST("/register", h.legacyOnly, h.register)
	auth.POST("/login", h.legacyOnly, h.login)
	auth.POST("/firebase", h.firebaseSignIn)
	auth.POST("/refresh", h.refresh)
	auth.POST("/logout", h.logout)
	auth.GET("/me", requireUser(), h.me)
	auth.PATCH("/me", requireUser(), h.updateMe)

	// For other services only. The gateway has no route to /internal, so these
	// cannot be reached from outside (see the gateway's route table).
	internal := r.Group("/internal/users")
	internal.POST("/lookup", h.lookupUsers)
	internal.POST("/:id/verify-password", h.verifyPassword)
	internal.DELETE("/:id", h.deleteUser)
	return r
}

type authResponse struct {
	User         store.User `json:"user"`
	AccessToken  string     `json:"access_token"`
	RefreshToken string     `json:"refresh_token"`
	TokenType    string     `json:"token_type"`
	ExpiresIn    int        `json:"expires_in"` // seconds until the access token expires
}

type registerInput struct {
	Email    string `json:"email"`
	Name     string `json:"name"`
	Password string `json:"password"`
}

type loginInput struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type refreshInput struct {
	RefreshToken string `json:"refresh_token"`
}

// legacyOnly turns the old sign-up and login off. 410 Gone tells an old app version
// plainly that this way in is closed for good.
func (h *handler) legacyOnly(c *gin.Context) {
	if !h.legacyPasswords {
		fail(c, http.StatusGone, "use_firebase", "this way of signing in has ended; update the app and sign in with Google or email", nil)
		c.Abort()
		return
	}
	c.Next()
}

func (h *handler) register(c *gin.Context) {
	var in registerInput
	if err := c.ShouldBindJSON(&in); err != nil {
		fail(c, http.StatusBadRequest, "invalid_json", "request body must be valid JSON", nil)
		return
	}
	in.Email = strings.ToLower(strings.TrimSpace(in.Email))
	in.Name = strings.TrimSpace(in.Name)
	if problems := validateRegistration(in); len(problems) > 0 {
		fail(c, http.StatusUnprocessableEntity, "validation_error", "some fields are invalid", problems)
		return
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(in.Password), bcrypt.DefaultCost)
	if err != nil {
		serverError(c, err)
		return
	}
	user, err := h.store.CreateUser(c.Request.Context(), in.Email, in.Name, string(hash))
	if errors.Is(err, store.ErrEmailTaken) {
		fail(c, http.StatusConflict, "email_taken", "an account with this email already exists", nil)
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
	c.JSON(http.StatusCreated, h.response(user, access, refreshRaw))
}

func (h *handler) login(c *gin.Context) {
	var in loginInput
	if err := c.ShouldBindJSON(&in); err != nil {
		fail(c, http.StatusBadRequest, "invalid_json", "request body must be valid JSON", nil)
		return
	}
	in.Email = strings.ToLower(strings.TrimSpace(in.Email))

	// Same message for "no such user" and "wrong password" so the API does not
	// reveal which emails have accounts.
	badLogin := func() {
		fail(c, http.StatusUnauthorized, "invalid_credentials", "email or password is incorrect", nil)
	}

	user, err := h.store.UserByEmail(c.Request.Context(), in.Email)
	if errors.Is(err, store.ErrNotFound) {
		badLogin()
		return
	}
	if err != nil {
		serverError(c, err)
		return
	}
	if bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(in.Password)) != nil {
		badLogin()
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
	c.JSON(http.StatusOK, h.response(user, access, refreshRaw))
}

func (h *handler) refresh(c *gin.Context) {
	var in refreshInput
	if err := c.ShouldBindJSON(&in); err != nil || in.RefreshToken == "" {
		fail(c, http.StatusBadRequest, "invalid_json", "refresh_token is required", nil)
		return
	}

	// Swap the old refresh token for a new one inside one transaction.
	newRaw, newHash, err := token.NewRefreshToken()
	if err != nil {
		serverError(c, err)
		return
	}
	userID, err := h.store.RotateRefreshToken(c.Request.Context(),
		token.HashRefreshToken(in.RefreshToken), newHash, time.Now().Add(refreshTTL))
	if errors.Is(err, store.ErrNotFound) {
		fail(c, http.StatusUnauthorized, "invalid_refresh_token", "refresh token is invalid, expired or already used", nil)
		return
	}
	if err != nil {
		serverError(c, err)
		return
	}
	user, err := h.store.UserByID(c.Request.Context(), userID)
	if err != nil {
		serverError(c, err)
		return
	}
	access, err := h.tokens.NewAccessToken(user.ID)
	if err != nil {
		serverError(c, err)
		return
	}
	c.JSON(http.StatusOK, h.response(user, access, newRaw))
}

func (h *handler) logout(c *gin.Context) {
	var in refreshInput
	if err := c.ShouldBindJSON(&in); err != nil || in.RefreshToken == "" {
		fail(c, http.StatusBadRequest, "invalid_json", "refresh_token is required", nil)
		return
	}
	if err := h.store.RevokeRefreshToken(c.Request.Context(), token.HashRefreshToken(in.RefreshToken)); err != nil {
		serverError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *handler) me(c *gin.Context) {
	user, err := h.store.UserByID(c.Request.Context(), userID(c))
	if errors.Is(err, store.ErrNotFound) {
		fail(c, http.StatusNotFound, "user_not_found", "this account no longer exists", nil)
		return
	}
	if err != nil {
		serverError(c, err)
		return
	}
	c.JSON(http.StatusOK, user)
}

type updateMeInput struct {
	Name string `json:"name"`
}

// updateMe lets someone change the name shown to friends and in groups.
func (h *handler) updateMe(c *gin.Context) {
	var in updateMeInput
	if err := c.ShouldBindJSON(&in); err != nil {
		fail(c, http.StatusBadRequest, "invalid_json", "request body must be valid JSON", nil)
		return
	}
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" || len([]rune(in.Name)) > 80 {
		fail(c, http.StatusUnprocessableEntity, "validation_error", "some fields are invalid",
			map[string]string{"name": "is required and must be at most 80 characters"})
		return
	}
	user, err := h.store.UpdateName(c.Request.Context(), userID(c), in.Name)
	if errors.Is(err, store.ErrNotFound) {
		fail(c, http.StatusNotFound, "user_not_found", "this account no longer exists", nil)
		return
	}
	if err != nil {
		serverError(c, err)
		return
	}
	c.JSON(http.StatusOK, user)
}

// newTokenPair makes an access token for userID and a fresh refresh token.
func (h *handler) newTokenPair(userID string) (access, refreshRaw, refreshHash string, err error) {
	if access, err = h.tokens.NewAccessToken(userID); err != nil {
		return "", "", "", err
	}
	refreshRaw, refreshHash, err = token.NewRefreshToken()
	return access, refreshRaw, refreshHash, err
}

func (h *handler) response(user store.User, access, refreshRaw string) authResponse {
	return authResponse{
		User:         user,
		AccessToken:  access,
		RefreshToken: refreshRaw,
		TokenType:    "Bearer",
		ExpiresIn:    int(h.tokens.AccessTTL().Seconds()),
	}
}

// validateRegistration returns one message per bad field (empty map = all good).
func validateRegistration(in registerInput) map[string]string {
	problems := map[string]string{}
	if addr, err := mail.ParseAddress(in.Email); err != nil || addr.Address != in.Email {
		problems["email"] = "must be a valid email address"
	}
	if in.Name == "" || len(in.Name) > 80 {
		problems["name"] = "is required and must be at most 80 characters"
	}
	// bcrypt only looks at the first 72 bytes, so longer passwords are refused.
	if len(in.Password) < 8 || len(in.Password) > 72 {
		problems["password"] = "must be between 8 and 72 characters"
	}
	return problems
}
