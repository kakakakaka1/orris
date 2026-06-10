package routes

import (
	"github.com/gin-gonic/gin"

	"github.com/orris-inc/orris/internal/interfaces/http/handlers"
	"github.com/orris-inc/orris/internal/interfaces/http/middleware"
)

// AuthRouteConfig holds dependencies for authentication routes.
type AuthRouteConfig struct {
	AuthHandler    *handlers.AuthHandler
	PasskeyHandler *handlers.PasskeyHandler // may be nil if WebAuthn is not configured
	AuthMiddleware *middleware.AuthMiddleware
	RateLimiter    *middleware.RateLimiter
}

// strictAuthRateLimit caps credential-sensitive endpoints (login, password reset)
// well below the general per-IP limit to slow credential stuffing and password spraying.
const strictAuthRateLimit = 10

// SetupAuthRoutes configures authentication routes.
func SetupAuthRoutes(engine *gin.Engine, cfg *AuthRouteConfig) {
	auth := engine.Group("/auth")
	{
		auth.POST("/register", cfg.RateLimiter.Limit(), cfg.AuthHandler.Register)
		auth.POST("/login", cfg.RateLimiter.LimitN(strictAuthRateLimit), cfg.AuthHandler.Login)
		auth.POST("/verify-email", cfg.RateLimiter.Limit(), cfg.AuthHandler.VerifyEmail)
		auth.GET("/verify-email", cfg.RateLimiter.Limit(), cfg.AuthHandler.VerifyEmail)
		auth.POST("/forgot-password", cfg.RateLimiter.LimitN(strictAuthRateLimit), cfg.AuthHandler.ForgotPassword)
		auth.POST("/reset-password", cfg.RateLimiter.LimitN(strictAuthRateLimit), cfg.AuthHandler.ResetPassword)

		auth.GET("/oauth/:provider", cfg.RateLimiter.Limit(), cfg.AuthHandler.InitiateOAuth)
		auth.GET("/oauth/:provider/callback", cfg.RateLimiter.Limit(), cfg.AuthHandler.HandleOAuthCallback)

		auth.POST("/refresh", cfg.RateLimiter.Limit(), cfg.AuthHandler.RefreshToken)
		auth.POST("/logout", cfg.AuthMiddleware.RequireAuth(), cfg.AuthHandler.Logout)
		auth.GET("/me", cfg.AuthMiddleware.RequireAuth(), cfg.AuthHandler.GetCurrentUser)

		// Passkey (WebAuthn) authentication routes
		if cfg.PasskeyHandler != nil {
			auth.POST("/passkey/register/start", cfg.AuthMiddleware.RequireAuth(), cfg.PasskeyHandler.StartRegistration)
			auth.POST("/passkey/register/finish", cfg.AuthMiddleware.RequireAuth(), cfg.PasskeyHandler.FinishRegistration)
			auth.POST("/passkey/login/start", cfg.RateLimiter.Limit(), cfg.PasskeyHandler.StartAuthentication)
			auth.POST("/passkey/login/finish", cfg.RateLimiter.Limit(), cfg.PasskeyHandler.FinishAuthentication)
			// Passkey signup (new user registration without password)
			auth.POST("/passkey/signup/start", cfg.RateLimiter.Limit(), cfg.PasskeyHandler.StartSignup)
			auth.POST("/passkey/signup/finish", cfg.RateLimiter.Limit(), cfg.PasskeyHandler.FinishSignup)
		}
	}
}
