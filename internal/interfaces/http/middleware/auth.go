package middleware

import (
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/orris-inc/orris/internal/domain/user"
	"github.com/orris-inc/orris/internal/infrastructure/auth"
	"github.com/orris-inc/orris/internal/shared/authorization"
	"github.com/orris-inc/orris/internal/shared/config"
	"github.com/orris-inc/orris/internal/shared/constants"
	"github.com/orris-inc/orris/internal/shared/logger"
	"github.com/orris-inc/orris/internal/shared/utils"
)

// errMissingToken and errMalformedHeader distinguish "no credentials supplied"
// from "credentials supplied in the wrong shape" when reading the request.
var (
	errMissingToken    = errors.New("missing authorization token")
	errMalformedHeader = errors.New("invalid authorization header format")
)

type AuthMiddleware struct {
	jwtService   *auth.JWTService
	userRepo     user.Repository
	sessionRepo  user.SessionRepository
	cookieConfig config.CookieConfig
	logger       logger.Interface
}

func NewAuthMiddleware(
	jwtService *auth.JWTService,
	userRepo user.Repository,
	sessionRepo user.SessionRepository,
	cookieConfig config.CookieConfig,
	logger logger.Interface,
) *AuthMiddleware {
	return &AuthMiddleware{
		jwtService:   jwtService,
		userRepo:     userRepo,
		sessionRepo:  sessionRepo,
		cookieConfig: cookieConfig,
		logger:       logger,
	}
}

func (m *AuthMiddleware) RequireAuth() gin.HandlerFunc {
	return func(c *gin.Context) {
		token, err := extractAccessToken(c)
		if err != nil {
			utils.ErrorResponse(c, http.StatusUnauthorized, err.Error())
			c.Abort()
			return
		}

		claims, foundUser, ok := m.authenticate(c, token)
		if !ok {
			// Every rejection reason collapses into one message so a caller cannot
			// probe which of token, session or account state made the request fail.
			utils.ErrorResponse(c, http.StatusUnauthorized, "invalid or expired token")
			c.Abort()
			return
		}

		setAuthContext(c, claims, foundUser)

		// Auto-refresh: if token is about to expire, generate a new one
		if m.jwtService.ShouldRefresh(claims) {
			m.refreshAccessToken(c, claims, foundUser.Role())
		}

		c.Next()
	}
}

func (m *AuthMiddleware) OptionalAuth() gin.HandlerFunc {
	return func(c *gin.Context) {
		token, err := extractAccessToken(c)
		if err != nil {
			c.Next()
			return
		}

		if claims, foundUser, ok := m.authenticate(c, token); ok {
			setAuthContext(c, claims, foundUser)
		}

		c.Next()
	}
}

// authenticate verifies an access token and resolves the caller's identity.
//
// Beyond the JWT signature it enforces two checks a stateless token cannot express
// on its own, both read fresh on every request:
//
//   - The backing session must still exist and be unexpired. The session row is the
//     revocation record: logout, self-service password change, password reset and
//     admin-initiated reset all delete it. Without this check a leaked token stays
//     usable until it expires, and the auto-refresh below would keep minting
//     replacements indefinitely for an already-revoked session.
//   - The account must still be allowed to act, so suspending a user takes effect
//     immediately rather than at token expiry.
//
// The role is likewise read from the database, so a demotion applies at once.
func (m *AuthMiddleware) authenticate(c *gin.Context, token string) (*auth.Claims, *user.User, bool) {
	claims, err := m.jwtService.Verify(token)
	if err != nil {
		m.logger.Warnw("failed to verify token", "error", err)
		return nil, nil, false
	}

	if claims.TokenType != auth.TokenTypeAccess {
		m.logger.Warnw("token is not an access token", "token_type", claims.TokenType)
		return nil, nil, false
	}

	if claims.SessionID == "" {
		m.logger.Warnw("access token carries no session id", "user_uuid", claims.UserUUID)
		return nil, nil, false
	}

	session, err := m.sessionRepo.GetByID(claims.SessionID)
	if err != nil || session == nil {
		m.logger.Warnw("session not found for access token",
			"user_uuid", claims.UserUUID,
			"session_id", claims.SessionID,
			"error", err,
		)
		return nil, nil, false
	}

	if session.IsExpired() {
		m.logger.Warnw("session has expired",
			"user_uuid", claims.UserUUID,
			"session_id", claims.SessionID,
		)
		return nil, nil, false
	}

	foundUser, err := m.userRepo.GetBySID(c.Request.Context(), claims.UserUUID)
	if err != nil || foundUser == nil {
		m.logger.Warnw("user not found by uuid", "user_uuid", claims.UserUUID, "error", err)
		return nil, nil, false
	}

	// Defensive: a session must belong to the subject the token claims to be.
	if session.UserID != foundUser.ID() {
		m.logger.Warnw("session does not belong to the token subject",
			"user_uuid", claims.UserUUID,
			"session_id", claims.SessionID,
			"session_user_id", session.UserID,
		)
		return nil, nil, false
	}

	if !foundUser.CanPerformActions() {
		m.logger.Warnw("request from an account that cannot perform actions",
			"user_id", foundUser.ID(),
			"status", foundUser.Status(),
		)
		return nil, nil, false
	}

	return claims, foundUser, true
}

// extractAccessToken reads the access token from the cookie, falling back to the
// Authorization header for backward compatibility.
func extractAccessToken(c *gin.Context) (string, error) {
	if token := utils.GetTokenFromCookie(c, utils.AccessTokenCookie); token != "" {
		return token, nil
	}

	authHeader := c.GetHeader("Authorization")
	if authHeader == "" {
		return "", errMissingToken
	}

	parts := strings.SplitN(authHeader, " ", 2)
	if len(parts) != 2 || parts[0] != "Bearer" {
		return "", errMalformedHeader
	}

	return parts[1], nil
}

// setAuthContext publishes the authenticated identity for downstream handlers.
// The role comes from the database rather than the JWT claims so that role changes
// (e.g. admin demotion) take effect immediately.
func setAuthContext(c *gin.Context, claims *auth.Claims, foundUser *user.User) {
	c.Set("user_id", foundUser.ID())
	c.Set("user_uuid", claims.UserUUID)
	c.Set("session_id", claims.SessionID)
	c.Set(constants.ContextKeyUserRole, string(foundUser.Role()))
}

// refreshAccessToken generates a new access token and sets it in the cookie.
// freshRole is the user's current role from the database, ensuring the refreshed
// token reflects any role changes (e.g. demotion from admin).
func (m *AuthMiddleware) refreshAccessToken(c *gin.Context, claims *auth.Claims, freshRole authorization.UserRole) {
	newToken, err := m.jwtService.RefreshAccessToken(claims, freshRole)
	if err != nil {
		m.logger.Warnw("failed to auto-refresh access token", "error", err, "user_uuid", claims.UserUUID)
		return
	}

	// Set the new access token in cookie
	accessMaxAge := m.jwtService.AccessExpMinutes() * 60
	utils.SetAccessTokenCookie(c, m.cookieConfig, newToken, accessMaxAge)

	// Refresh CSRF cookie alongside access token
	utils.SetCSRFCookie(c, m.cookieConfig, accessMaxAge)

	m.logger.Debugw("access token auto-refreshed", "user_uuid", claims.UserUUID)
}
