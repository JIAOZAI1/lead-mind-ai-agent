// Package builtin holds tools shipped with the platform, available to
// every tenant's Agent without extra configuration. Custom/tenant-defined
// tools (webhook-based) live under internal/tools instead — see
// PROJECT.md §1.2.
package builtin

import (
	"context"
	"time"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"
)

type currentTimeInput struct {
	// Timezone is an IANA timezone name, e.g. "Asia/Shanghai". Empty means UTC.
	Timezone string `json:"timezone" jsonschema:"description=IANA timezone name (e.g. Asia/Shanghai). Defaults to UTC if omitted."`
}

type currentTimeOutput struct {
	Now string `json:"now"`
}

// NewCurrentTimeTool returns a tool that reports the current time in a
// given timezone. It has no side effects, so it never needs approval
// routing (PROJECT.md §4.1/§6.2 only applies to high-risk tools).
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
