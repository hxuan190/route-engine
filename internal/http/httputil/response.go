package httputil

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

type Response struct {
	Success bool        `json:"success"`
	Data    interface{} `json:"data,omitempty"`
	Error   string      `json:"error,omitempty"`
}

func Success(c *gin.Context, data interface{}) {
	c.JSON(http.StatusOK, Response{
		Success: true,
		Data:    data,
	})
}

func Error(c *gin.Context, status int, err string) {
	c.JSON(status, Response{
		Success: false,
		Error:   err,
	})
}

func BadRequest(c *gin.Context, err string) {
	Error(c, http.StatusBadRequest, err)
}

func InternalError(c *gin.Context, err string) {
	Error(c, http.StatusInternalServerError, err)
}

func NotFound(c *gin.Context, err string) {
	Error(c, http.StatusNotFound, err)
}

// Aliases for compatibility
func HandleSuccess(c *gin.Context, data interface{}) {
	Success(c, data)
}

func HandleBadRequest(c *gin.Context, err string) {
	BadRequest(c, err)
}

func HandleNotFound(c *gin.Context, err string) {
	NotFound(c, err)
}

func HandleInternalError(c *gin.Context, err string) {
	InternalError(c, err)
}
