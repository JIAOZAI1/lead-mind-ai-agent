// Package builtin 存放随平台内置、无需额外配置即可被每个租户的 Agent
// 使用的工具。自定义/租户配置的工具（基于 webhook）则放在
// internal/tools 下——参见 PROJECT.md §1.2。
package builtin

import (
	"context"
	"time"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"
)

type currentTimeInput struct {
	// Timezone 是 IANA 时区名称，例如 "Asia/Shanghai"。为空表示 UTC。
	Timezone string `json:"timezone" jsonschema:"description=IANA timezone name (e.g. Asia/Shanghai). Defaults to UTC if omitted."`
}

type currentTimeOutput struct {
	Now string `json:"now"`
}

// NewCurrentTimeTool 返回一个用于查询指定时区当前时间的工具。它没有任何
// 副作用，因此永远不需要走审批路由（PROJECT.md §4.1/§6.2 的审批要求仅
// 针对高风险工具）。
func NewCurrentTimeTool() (tool.InvokableTool, error) {
	return utils.InferTool(
		"current_time",
		"Get the current date and time, optionally in a specific IANA timezone.",
		func(ctx context.Context, in *currentTimeInput) (*currentTimeOutput, error) {
			loc := time.UTC
			if in.Timezone != "" {
				l, err := time.LoadLocation(in.Timezone)
				if err != nil {
					return nil, err
				}
				loc = l
			}
			return &currentTimeOutput{Now: time.Now().In(loc).Format(time.RFC3339)}, nil
		},
	)
}
