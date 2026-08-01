package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/cloudwego/eino/callbacks"
	einomodel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// newLoggingHandler 构建一个 eino callbacks.Handler，在每次模型调用的
// 开始/结束/出错时打印请求与响应，便于调试供应商返回的原始内容。
// 通过 callbacks.AppendGlobalHandlers 全局注册，对 Generate 和 Stream
// 两种调用方式均生效。
func newLoggingHandler() callbacks.Handler {
	return callbacks.NewHandlerBuilder().
		OnStartFn(func(ctx context.Context, info *callbacks.RunInfo, input callbacks.CallbackInput) context.Context {
			in := einomodel.ConvCallbackInput(input)
			if in == nil {
				return ctx
			}
			slog.InfoContext(ctx, "model request",
				"component", info.Name,
				"messages", marshalLogValue(in.Messages),
			)
			return ctx
		}).
		OnEndFn(func(ctx context.Context, info *callbacks.RunInfo, output callbacks.CallbackOutput) context.Context {
			out := einomodel.ConvCallbackOutput(output)
			if out == nil {
				return ctx
			}
			slog.InfoContext(ctx, "model response",
				"component", info.Name,
				"message", marshalLogValue(out.Message),
			)
			return ctx
		}).
		OnErrorFn(func(ctx context.Context, info *callbacks.RunInfo, err error) context.Context {
			slog.ErrorContext(ctx, "model error",
				"component", info.Name,
				"error", err,
			)
			return ctx
		}).
		OnEndWithStreamOutputFn(func(ctx context.Context, info *callbacks.RunInfo,
			output *schema.StreamReader[callbacks.CallbackOutput]) context.Context {
			go logStreamOutput(ctx, info.Name, output)
			return ctx
		}).
		Build()
}

// logStreamOutput 消费流式响应并将拼接后的完整消息打印出来。必须在独立的
// goroutine 中读取，避免阻塞真正消费该流的调用方；读取完成后关闭 reader。
func logStreamOutput(ctx context.Context, component string, stream *schema.StreamReader[callbacks.CallbackOutput]) {
	defer stream.Close()

	var chunks []*schema.Message
	for {
		chunk, err := stream.Recv()
		if err != nil {
			break
		}
		out := einomodel.ConvCallbackOutput(chunk)
		if out != nil && out.Message != nil {
			chunks = append(chunks, out.Message)
		}
	}

	msg, err := schema.ConcatMessages(chunks)
	if err != nil {
		slog.ErrorContext(ctx, "model response stream: concat failed", "component", component, "error", err)
		return
	}
	slog.InfoContext(ctx, "model response (stream)",
		"component", component,
		"message", marshalLogValue(msg),
	)
}

// marshalLogValue 把值序列化为 JSON 字符串用于日志输出；序列化失败时退回
// 使用 %+v，保证日志调用本身不会因为编码错误而丢失信息。
func marshalLogValue(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("%+v", v)
	}
	return string(b)
}
