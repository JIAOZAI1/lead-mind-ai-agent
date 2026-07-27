package handler

import (
	"context"
	"log/slog"

	"github.com/JIAOZAI1/lead-mind-ai-agent/internal/memory"
	pkgschema "github.com/JIAOZAI1/lead-mind-ai-agent/pkg/schema"
)

// loadHistory 读取喂给 agent 的对话上下文，并在短期记忆（Redis）副本
// 已经过期时，回退到持久化的完整对话记录（MySQL transcript）重建上下文。
//
// 之所以需要回退，是因为 shortterm.LoadHistory 在 key 不存在时返回空
// 切片而非错误（这对全新会话是正确的），于是"从未有过历史的新会话"和
// "有历史、只是 Redis 副本过期了的旧会话"在返回值上无法区分。判定依据
// 只能来自调用方已知的 isNewSession：凡是 Sessions.Create 过的会话，
// 第一轮结束后必然写入过 Redis，所以"旧会话 + 空历史"必定意味着 TTL
// 已过期，而不是这个会话真的没聊过任何内容。
func (d AgentDeps) loadHistory(ctx context.Context, tenantCode, userID, sessionID string, isNewSession bool) ([]pkgschema.Message, error) {
	history, err := d.ShortTerm.LoadHistory(ctx, tenantCode, sessionID)
	if err != nil {
		return nil, err
	}
	if isNewSession || len(history) > 0 {
		return history, nil
	}

	return d.rebuildHistoryFromTranscript(ctx, tenantCode, userID, sessionID), nil
}

// rebuildHistoryFromTranscript 用持久化对话记录重建模型上下文。
//
// 这条路径上的任何失败都只降级为"空历史"而不是让请求失败：此时用户
// 只是在一个旧会话里继续发消息，能拿到一个缺少上下文的回复，总好过
// 拿到 500。与之相对，shortterm.LoadHistory 的失败仍然直接返回错误
// （见 loadHistory 的调用方）——那是基础设施异常，不是可预期的
// TTL 过期。
func (d AgentDeps) rebuildHistoryFromTranscript(ctx context.Context, tenantCode, userID, sessionID string) []pkgschema.Message {
	turns, err := d.Transcript.ListTurns(ctx, tenantCode, sessionID)
	if err != nil {
		slog.WarnContext(ctx, "history: transcript rebuild failed, continuing without prior context",
			"tenant_code", tenantCode,
			"user_id", userID,
			"session_id", sessionID,
			"error", err,
		)
		return nil
	}
	if len(turns) == 0 {
		return nil
	}

	restored := make([]pkgschema.Message, len(turns))
	for i, t := range turns {
		restored[i] = t.Message
	}

	// transcript 存的是未压缩的原始消息，条数可能远超上下文窗口，
	// 必须过一遍与 handler 落盘时相同的 Compact 逻辑再喂给模型。
	compacted := memory.Compact(ctx, d.Compaction, restored)

	// Compact 在摘要模型不可用时会降级为硬截断，若阈值配置得过小
	// （极端情况如零值 CompactionConfig），截断后可能一条不剩——那等于
	// 白读了一遍 transcript，回退路径反而退化成它本要修复的"模型失忆"。
	// 这种情况下宁可原样使用未压缩的历史：条数偏多最多让模型少记几轮，
	// 而空上下文是必然失忆。
	if len(compacted) == 0 {
		slog.WarnContext(ctx, "history: compaction emptied the rebuilt context, falling back to raw transcript",
			"tenant_code", tenantCode,
			"user_id", userID,
			"session_id", sessionID,
			"transcript_messages", len(restored),
		)
		compacted = restored
	}

	// 顺手把重建结果写回 Redis 续热缓存，让同一个会话的后续轮次不必
	// 重复读 MySQL。写失败同样只记日志不中断——缓存没续上只是下一轮
	// 再重建一次，不影响本轮回复的正确性。
	if err := d.ShortTerm.ReplaceHistory(ctx, tenantCode, sessionID, compacted); err != nil {
		slog.WarnContext(ctx, "history: failed to re-warm short-term cache after transcript rebuild",
			"tenant_code", tenantCode,
			"user_id", userID,
			"session_id", sessionID,
			"error", err,
		)
	}

	slog.InfoContext(ctx, "history: rebuilt model context from transcript after short-term ttl expiry",
		"tenant_code", tenantCode,
		"user_id", userID,
		"session_id", sessionID,
		"transcript_messages", len(restored),
		"context_messages", len(compacted),
	)
	return compacted
}
