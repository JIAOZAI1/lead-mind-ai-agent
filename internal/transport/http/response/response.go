// Package response 统一封装 HTTP 和 SSE 响应，避免各处理器自行定义返回格式。
package response

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

const (
	// CodeSuccess 表示请求已成功处理。
	CodeSuccess = 0

	messageSuccess = "success"
)

// Body 是所有 HTTP 和 SSE 响应共用的响应信封。
type Body struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data"`
}

// Success 返回 HTTP 200 成功响应。
func Success(c *gin.Context, data any) {
	c.JSON(http.StatusOK, successBody(data))
}

// Error 返回指定 HTTP 状态码的错误响应。
// 在引入独立业务错误码前，响应 code 与 HTTP 状态码保持一致，便于客户端稳定判断错误类别。
func Error(c *gin.Context, httpStatus int, message string) {
	c.JSON(httpStatus, errorBody(httpStatus, message))
}

// SSESuccess 返回使用统一信封封装的 SSE 成功事件。
func SSESuccess(c *gin.Context, event string, data any) {
	c.SSEvent(event, successBody(data))
}

// SSEError 返回使用统一信封封装的 SSE 错误事件。
// SSE 开始发送后无法再修改 HTTP 状态码，因此 code 单独携带事件错误类别。
func SSEError(c *gin.Context, event string, code int, message string) {
	c.SSEvent(event, errorBody(code, message))
}

func successBody(data any) Body {
	return Body{
		Code:    CodeSuccess,
		Message: messageSuccess,
		Data:    data,
	}
}

func errorBody(code int, message string) Body {
	return Body{
		Code:    code,
		Message: message,
		Data:    nil,
	}
}
