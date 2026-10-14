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
	if s == nil || s.playAuth == nil {
		return next
	}
	return func(c *gin.Context) {
		if c.Request.Method == http.MethodOptions {
			next(c)
			return
		}

		authorized := false
		wrapped := s.playAuth(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
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
