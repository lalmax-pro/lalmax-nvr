package server

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	maxlogic "github.com/q191201771/lalmax/logic"
	"github.com/q191201771/naza/pkg/nazalog"
)

func (s *LalMaxServer) initFmp4Router(router *gin.Engine) {
	router.GET("/live/m4s/:streamid", s.protectHTTPPlay(s.HandleHttpFmp4))
	router.GET("/live/hls/:streamid/:type", s.protectHTTPPlay(s.HandleHls))
}

func (s *LalMaxServer) HandleHls(c *gin.Context) {
	if s.hlssvr != nil {
		if !s.authorizeHTTPPlayback(c, "hls", c.Param("streamid")) {
			return
		}
		if s.hlssvr.OnDemandEnabled() {
			key := maxlogic.NewStreamKey(c.Query("app_name"), c.Param("streamid"))
			if _, group := maxlogic.GetGroupManagerInstance().GetGroup(key); group != nil &&
				s.hlssvr.EnsureSessionWithAppName(key.AppName, key.StreamName) {
				group.ReplayCachedGopToHls()
			}
		}
		s.hlssvr.HandleRequest(c)
	} else {
		nazalog.Error("hls is disable")
		c.Status(http.StatusNotFound)
	}
}

func (s *LalMaxServer) HandleHttpFmp4(c *gin.Context) {
	if s.httpfmp4svr != nil {
		streamID := strings.TrimSuffix(c.Param("streamid"), ".mp4")
		if !s.authorizeHTTPPlayback(c, "http-fmp4", streamID) {
			return
		}
		s.httpfmp4svr.HandleRequest(c)
	} else {
		nazalog.Error("http-fmp4 is disable")
		c.Status(http.StatusNotFound)
	}
}
