package httputil

import "github.com/gin-gonic/gin"

type IHttpHandler interface {
	Root() string
	SetRoutes(pub *gin.RouterGroup, private *gin.RouterGroup, admin *gin.RouterGroup)
}
