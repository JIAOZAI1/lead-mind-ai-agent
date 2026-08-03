// Package health 提供 HTTP 服务健康检查接口。
package health

import (
	"github.com/gin-gonic/gin"

	"github.com/JIAOZAI1/lead-mind-ai-agent/internal/transport/http/response"
)

// Result 表示健康检查结果。
type Result struct {
	Status string `json:"status"`
}

// Check 返回当前进程的存活状态。
func Check(c *gin.Context) {
	response.Success(c, Result{Status: "ok"})
}
