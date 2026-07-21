package common

import "github.com/gin-gonic/gin"

// APIResponse is the single JSON envelope every endpoint returns, so clients can
// rely on one shape: {success, message, data?, error?}. `error` carries a stable
// machine-readable code (e.g. "VALIDATION_ERROR"), distinct from the human message.
type APIResponse struct {
	Success bool        `json:"success"`
	Message string      `json:"message"`
	Data    interface{} `json:"data,omitempty"`
	Error   string      `json:"error,omitempty"`
}

// Success writes a success envelope.
func Success(c *gin.Context, statusCode int, message string, data interface{}) {
	c.JSON(statusCode, APIResponse{Success: true, Message: message, Data: data})
}

// Error writes a failure envelope with a stable error code.
func Error(c *gin.Context, statusCode int, message string, errCode string) {
	c.JSON(statusCode, APIResponse{Success: false, Message: message, Error: errCode})
}
