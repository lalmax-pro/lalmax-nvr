package server

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

func (s *LalMaxServer) authorizeHTTPPlayback(c *gin.Context, protocol, streamID string) bool {
	if s.playbackAuthorizer == nil {
		return true
	}
	if err := s.playbackAuthorizer(protocol, streamID, c.Query("token")); err != nil {
		c.Status(http.StatusUnauthorized)
		return false
	}
	return true
}

// protectHTTPPlay applies the application authentication middleware to a
// direct media-play request while retaining Gin's response writer and route
// context. OPTIONS is left public so browser clients can complete CORS
// preflight before sending authenticated WHEP requests.
func (s *LalMaxServer) protectHTTPPlay(next gin.HandlerFunc) gin.HandlerFunc {
	if s == nil {
		return next
	}
	return protectHTTPMiddleware(s.playAuth, true, next)
}

func (s *LalMaxServer) protectHTTPControl(next gin.HandlerFunc) gin.HandlerFunc {
	if s == nil {
		return next
	}
	return protectHTTPMiddleware(s.controlAuth, true, next)
}

func protectHTTPMiddleware(auth func(http.Handler) http.Handler, allowOptions bool, next gin.HandlerFunc) gin.HandlerFunc {
	if auth == nil {
		return next
	}
	return func(c *gin.Context) {
		if allowOptions && c.Request.Method == http.MethodOptions {
			next(c)
			return
		}

		authorized := false
		wrapped := auth(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			authorized = true
			c.Request = r
			next(c)
		}))
		wrapped.ServeHTTP(c.Writer, c.Request)
		if !authorized {
			c.Abort()
		}
	}
}
