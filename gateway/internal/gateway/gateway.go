// Package gateway is the single front door of the system.
//
// The browser only ever talks to the gateway. For each request it:
//
//  1. gives the request an id (X-Request-ID) so logs can be followed across services,
//  2. answers CORS preflight requests,
//  3. checks the JWT on every route that is not public,
//  4. DELETES any X-User-ID header the client sent, then sets its own from the token,
//  5. forwards the request to the service that owns that URL.
//
// Step 4 is the security heart of the design: the services behind the gateway
// believe X-User-ID, so nobody but the gateway may be able to set it.
package gateway

import (
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

const tokenIssuer = "fitness-auth"

type Config struct {
	JWTSecret    string
	AuthURL      string
	WorkoutURL   string
	NutritionURL string
	ActivityURL  string
	SocialURL    string
	CORSOrigins  string
	// TrustedProxies are the proxies in front of the gateway (a hosting platform's
	// load balancer), as IPs or CIDRs. Only they may say who the client is with
	// X-Forwarded-For; without any, the client is whoever connected.
	TrustedProxies []string
	// AuthAttemptsPerMinute limits sign-in, sign-up and account deletion per IP. 0 = 20.
	AuthAttemptsPerMinute int
}

type route struct {
	prefix string // matches this path and everything below it
	public bool   // true = no login needed
	name   string // which service, for the logs
	proxy  *httputil.ReverseProxy
}

type gateway struct {
	secret   []byte
	attempts *limiter // sign-in, sign-up and account deletion, per IP
	routes   []route
	services map[string]string // service name -> base URL (for health checks, the dashboard, account deletion)
	client   *http.Client
}

// New builds the gateway's HTTP handler.
func New(cfg Config) (*gin.Engine, error) {
	auth, err := newProxy(cfg.AuthURL)
	if err != nil {
		return nil, fmt.Errorf("AUTH_URL: %w", err)
	}
	workout, err := newProxy(cfg.WorkoutURL)
	if err != nil {
		return nil, fmt.Errorf("WORKOUT_URL: %w", err)
	}
	nutrition, err := newProxy(cfg.NutritionURL)
	if err != nil {
		return nil, fmt.Errorf("NUTRITION_URL: %w", err)
	}
	activity, err := newProxy(cfg.ActivityURL)
	if err != nil {
		return nil, fmt.Errorf("ACTIVITY_URL: %w", err)
	}
	social, err := newProxy(cfg.SocialURL)
	if err != nil {
		return nil, fmt.Errorf("SOCIAL_URL: %w", err)
	}

	g := &gateway{
		secret:   []byte(cfg.JWTSecret),
		attempts: newLimiter(attemptsPerMinute(cfg.AuthAttemptsPerMinute), time.Minute),
		// The first matching prefix wins, so specific public routes come first.
		//
		// This table is also a security boundary. The services' /internal routes
		// are not in it, so nothing from outside can ever reach them.
		routes: []route{
			{"/api/v1/auth/register", true, "auth", auth},
			{"/api/v1/auth/login", true, "auth", auth},
			{"/api/v1/auth/firebase", true, "auth", auth},
			{"/api/v1/auth/refresh", true, "auth", auth},
			{"/api/v1/auth/logout", true, "auth", auth},
			{"/api/v1/auth", false, "auth", auth},

			{"/api/v1/exercises", false, "workout", workout},
			{"/api/v1/workouts", false, "workout", workout},
			{"/api/v1/records", false, "workout", workout},
			{"/api/v1/plans", false, "workout", workout},

			{"/api/v1/foods", false, "nutrition", nutrition},
			{"/api/v1/recipes", false, "nutrition", nutrition},
			{"/api/v1/diary", false, "nutrition", nutrition},
			{"/api/v1/goals", false, "nutrition", nutrition},
			{"/api/v1/water", false, "nutrition", nutrition},
			{"/api/v1/nutrition", false, "nutrition", nutrition},

			{"/api/v1/steps", false, "activity", activity},

			{"/api/v1/friends", false, "social", social},
			{"/api/v1/nudges", false, "social", social},
			{"/api/v1/groups", false, "social", social},
		},
		services: map[string]string{
			"auth":      strings.TrimRight(cfg.AuthURL, "/"),
			"workout":   strings.TrimRight(cfg.WorkoutURL, "/"),
			"nutrition": strings.TrimRight(cfg.NutritionURL, "/"),
			"activity":  strings.TrimRight(cfg.ActivityURL, "/"),
			"social":    strings.TrimRight(cfg.SocialURL, "/"),
		},
		client: &http.Client{Timeout: 5 * time.Second},
	}

	r := gin.New()
	if err := r.SetTrustedProxies(cfg.TrustedProxies); err != nil {
		return nil, fmt.Errorf("TRUSTED_PROXIES: %w", err)
	}
	r.Use(gin.Recovery(), requestID(), cors(cfg.CORSOrigins), requestLog())

	r.GET("/health", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok", "service": "gateway"})
	})
	r.GET("/health/services", g.serviceHealth)
	r.GET("/api/v1/dashboard", g.dashboard)
	r.POST("/api/v1/account/delete", g.deleteAccount)

	// Everything else is looked up in the route table.
	r.NoRoute(g.forward)
	return r, nil
}

// limitedPaths are where someone could guess passwords or make accounts in bulk.
var limitedPaths = map[string]bool{
	"/api/v1/auth/register": true,
	"/api/v1/auth/login":    true,
	"/api/v1/auth/firebase": true,
}

func attemptsPerMinute(n int) int {
	if n <= 0 {
		return 20
	}
	return n
}

// forward finds the service that owns the path and proxies the request to it.
func (g *gateway) forward(c *gin.Context) {
	rt, ok := g.match(c.Request.URL.Path)
	if !ok || hasDotSegment(c.Request.URL.Path) {
		fail(c, http.StatusNotFound, "not_found", "no such endpoint", nil)
		return
	}

	if limitedPaths[c.Request.URL.Path] && !g.attempts.limit(c) {
		return
	}

	// Never trust a user id that came from outside. Public routes get none at all.
	c.Request.Header.Del("X-User-ID")
	if !rt.public {
		userID, err := g.authenticate(c.Request)
		if err != nil {
			c.Header("WWW-Authenticate", `Bearer realm="fitness"`)
			fail(c, http.StatusUnauthorized, "unauthorized", "missing, invalid or expired access token", nil)
			return
		}
		c.Request.Header.Set("X-User-ID", userID)
	}
	c.Set("upstream", rt.name)
	rt.proxy.ServeHTTP(c.Writer, c.Request)
}

func (g *gateway) match(path string) (route, bool) {
	for _, rt := range g.routes {
		if path == rt.prefix || strings.HasPrefix(path, rt.prefix+"/") {
			return rt, true
		}
	}
	return route{}, false
}

// hasDotSegment spots paths like /api/v1/workouts/../../internal/users/x. They start
// with a public prefix but climb out of it, so they must never be forwarded.
func hasDotSegment(path string) bool {
	for _, segment := range strings.Split(path, "/") {
		if segment == ".." || segment == "." {
			return true
		}
	}
	return false
}

// authenticate checks "Authorization: Bearer <jwt>" and returns the user id inside it.
func (g *gateway) authenticate(r *http.Request) (string, error) {
	raw, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || raw == "" {
		return "", errors.New("missing bearer token")
	}
	claims := &jwt.RegisteredClaims{}
	_, err := jwt.ParseWithClaims(raw, claims,
		func(*jwt.Token) (any, error) { return g.secret, nil },
		jwt.WithValidMethods([]string{"HS256"}),
		jwt.WithIssuer(tokenIssuer),
		jwt.WithExpirationRequired(),
	)
	if err != nil {
		return "", err
	}
	if _, err := uuid.Parse(claims.Subject); err != nil {
		return "", errors.New("token subject is not a user id")
	}
	return claims.Subject, nil
}

func newProxy(rawURL string) (*httputil.ReverseProxy, error) {
	target, err := url.Parse(rawURL)
	if err != nil || target.Scheme == "" || target.Host == "" {
		return nil, fmt.Errorf("%q is not a valid URL (expected something like http://host:port)", rawURL)
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.ResponseHeaderTimeout = 10 * time.Second

	return &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(target)
			pr.SetXForwarded()
		},
		Transport: transport,
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			log.Printf("req=%s upstream %s unreachable: %v", r.Header.Get("X-Request-ID"), target.Host, err)
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte(`{"code":"bad_gateway","message":"a backend service is not responding"}`))
		},
	}, nil
}

// ---------- middleware ----------

func requestID() gin.HandlerFunc {
	return func(c *gin.Context) {
		id := uuid.NewString() // always ours; a client-supplied id is ignored
		c.Request.Header.Set("X-Request-ID", id)
		c.Header("X-Request-ID", id)
		c.Next()
	}
}

// cors lets the browser app (served from another origin, e.g. localhost:3000)
// call this API. Browsers block such calls unless the server opts in.
func cors(allowed string) gin.HandlerFunc {
	origins := map[string]bool{}
	for _, o := range strings.Split(allowed, ",") {
		if o = strings.TrimSpace(o); o != "" {
			origins[o] = true
		}
	}
	return func(c *gin.Context) {
		origin := c.GetHeader("Origin")
		if origins[origin] {
			h := c.Writer.Header()
			h.Set("Access-Control-Allow-Origin", origin)
			h.Add("Vary", "Origin")
			h.Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
			h.Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
			h.Set("Access-Control-Expose-Headers", "X-Request-ID, Content-Disposition")
			h.Set("Access-Control-Max-Age", "600")
		}
		if c.Request.Method == http.MethodOptions {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}
		c.Next()
	}
}

func requestLog() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()
		upstream := c.GetString("upstream")
		if upstream == "" {
			upstream = "-"
		}
		log.Printf("req=%s %s %s -> %d via=%s (%s)",
			c.Request.Header.Get("X-Request-ID"), c.Request.Method, c.Request.URL.Path,
			c.Writer.Status(), upstream, time.Since(start).Round(time.Millisecond))
	}
}

// ---------- errors ----------

// Same error shape as every service.
type errorBody struct {
	Code    string            `json:"code"`
	Message string            `json:"message"`
	Details map[string]string `json:"details,omitempty"`
}

func fail(c *gin.Context, status int, code, message string, details map[string]string) {
	c.AbortWithStatusJSON(status, errorBody{Code: code, Message: message, Details: details})
}
