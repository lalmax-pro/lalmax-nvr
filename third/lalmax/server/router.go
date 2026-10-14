package server

import "github.com/gin-gonic/gin"

func (s *LalMaxServer) InitRouter(router *gin.Engine) {
	if router == nil {
		return
	}
	router.Use(otelMiddleware())
	router.Use(s.Cors())

	s.initRtcRouter(router)
	s.initFmp4Router(router)

	auth := Authentication(s.configuration().HttpConfig.CtrlAuthWhitelist.Secrets, s.configuration().HttpConfig.CtrlAuthWhitelist.IPs)
	controlHandlers := []gin.HandlerFunc{auth}
	if s.controlAuth != nil {
		controlHandlers = append(controlHandlers, s.protectHTTPControl(func(c *gin.Context) {
			c.Next()
		}))
	}
	s.initHookRouter(router, controlHandlers...)
	s.initStatRouter(router, controlHandlers...)
	s.initCtrlRouter(router, controlHandlers...)
	s.initZlmCompatRouter(router, controlHandlers...)

	s.initFlvProxy(router)
}
