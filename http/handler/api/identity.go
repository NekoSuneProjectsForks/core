package api

import (
	"net/http"

	"github.com/datarhei/core/v16/http/api"
	"github.com/datarhei/core/v16/users"

	jwtgo "github.com/golang-jwt/jwt/v5"
	"github.com/labstack/echo/v4"
)

// Identity returns the authenticated username and whether that user is an
// admin, based on the JWT subject claim stored on the echo context by the
// access-token middleware. If no registry is configured, multi-tenant users
// aren't in use and every caller is treated as admin, since ownership and
// quotas only make sense once named users exist.
func Identity(c echo.Context, registry users.Registry, bootstrapUsername string) (string, bool) {
	if registry == nil {
		return "", true
	}

	token, ok := c.Get("user").(*jwtgo.Token)
	if !ok {
		return "", false
	}

	username, err := token.Claims.GetSubject()
	if err != nil {
		return "", false
	}

	if username == bootstrapUsername {
		return username, true
	}

	if u, ok := registry.GetByUsername(username); ok {
		return username, u.IsAdmin()
	}

	return username, false
}

// RequireAdmin is a middleware that rejects any request not made by an admin.
func RequireAdmin(registry users.Registry, bootstrapUsername string) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			if _, isAdmin := Identity(c, registry, bootstrapUsername); !isAdmin {
				return api.Err(http.StatusForbidden, "Forbidden", "admin privileges required")
			}

			return next(c)
		}
	}
}
