package admin

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/dynesshely/bot-harness/internal/core"
	"github.com/dynesshely/bot-harness/internal/store"
)

// handleMergeableGroups lists the groups that still have more than one
// conversation, i.e. the ones a merge would tidy up.
func (s *Server) handleMergeableGroups(w http.ResponseWriter, _ *http.Request) {
	summaries, err := s.deps.Store.MergeableGroups(ctxBg(), 50)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}

	effective := s.deps.Settings.Effective()
	items := make([]map[string]any, 0, len(summaries))
	for _, summary := range summaries {
		// 该群当前生效的上下文模式（全局 + 按群覆盖）
		cfg := s.deps.Settings.GroupConfig(summary.GroupOpenID)
		convs := make([]map[string]any, 0, len(summary.Conversations))
		for _, c := range summary.Conversations {
			convs = append(convs, map[string]any{
				"id":             c.ID,
				"scene":          c.Scene,
				"member_name":    c.MemberName,
				"message_count":  c.MessageCount,
				"token_estimate": c.TokenEstimate,
				"last_active_at": c.LastActiveAt,
			})
		}
		items = append(items, map[string]any{
			"group_openid":           summary.GroupOpenID,
			"group_name":             summary.GroupName,
			"shared_conversation_id": summary.SharedID,
			"context_mode":           cfg.ContextMode,
			"mergeable":              cfg.ContextMode == "shared",
			"conversations":          convs,
		})
	}
	writeOK(w, map[string]any{
		"items":               items,
		"global_mode":         effective.ContextMode,
		"total_groups":        len(items),
		"total_conversations": countConversations(items),
	})
}

func countConversations(items []map[string]any) int {
	total := 0
	for _, item := range items {
		if convs, ok := item["conversations"].([]map[string]any); ok {
			total += len(convs)
		}
	}
	return total
}

// handleMergeGroupConversations folds a group's per-member conversations into
// its shared conversation.
//
// POST /api/groups/{group_openid}/conversations/merge
//
//	{ "target_id": "…", "dry_run": true }
func (s *Server) handleMergeGroupConversations(w http.ResponseWriter, r *http.Request) {
	groupOpenID := r.PathValue("group_openid")
	if groupOpenID == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "缺少 group_openid")
		return
	}

	var body struct {
		TargetID string `json:"target_id"`
		DryRun   *bool  `json:"dry_run"`
	}
	if err := readJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	dryRun := body.DryRun != nil && *body.DryRun

	// 只有该群当前生效的上下文模式是「整群共享」时才允许合并 ——
	// 合并到一份没人使用的共享会话里只会让人困惑。
	cfg := s.deps.Settings.GroupConfig(groupOpenID)
	if cfg.ContextMode != "shared" {
		writeError(w, http.StatusBadRequest, "context_mode_not_shared",
			"该群当前的上下文模式不是「整群共享」，合并后的会话不会被使用。"+
				"请先在「策略配置」里把该群（或全局）切换为整群共享上下文。")
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	targetID := body.TargetID
	if targetID == "" {
		// 找到该群已有的共享会话；没有就先建一个（id 规则与 dispatcher 一致）。
		summaries, err := s.deps.Store.MergeableGroups(ctx, 100)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "store_error", err.Error())
			return
		}
		for _, summary := range summaries {
			if summary.GroupOpenID == groupOpenID {
				targetID = summary.SharedID
				break
			}
		}
		if targetID == "" {
			targetID = core.EncodeConversationID("group-shared:" + groupOpenID)
			if err := s.deps.Store.EnsureConversation(ctx, &store.Conversation{
				ID:          targetID,
				Scene:       store.SceneGroupShared,
				GroupOpenID: groupOpenID,
			}); err != nil {
				writeError(w, http.StatusInternalServerError, "store_error", err.Error())
				return
			}
		}
	}

	result, err := s.deps.Store.MergeGroupConversations(ctx, groupOpenID, targetID, dryRun)
	if err != nil {
		switch {
		case errors.Is(err, store.ErrGroupMismatch):
			writeError(w, http.StatusBadRequest, "group_mismatch", err.Error())
		case errors.Is(err, store.ErrNotFound):
			writeError(w, http.StatusNotFound, "not_found", "该群没有可合并的会话")
		default:
			writeError(w, http.StatusInternalServerError, "store_error", err.Error())
		}
		return
	}

	if !dryRun {
		s.deps.Logger.Warn("merged group conversations",
			"component", "admin",
			"group_openid", groupOpenID,
			"target", result.TargetID,
			"conversations", result.DeletedConversations,
			"messages", result.MovedMessages)
	}
	writeOK(w, result)
}
