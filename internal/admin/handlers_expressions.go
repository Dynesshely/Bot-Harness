package admin

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/dynesshely/bot-harness/internal/store"
)

// 表达方式库的后台接口：列表、手动新增、启停、删除、强化（reinforce）。
//
// 入库与召回都在 core 侧（学习/召回由回复管线驱动），后台只做人工维护与
// 审核：新增一条表达、把学到的行启停、删掉明显学歪的行、给常用的行加权。

const (
	// expressionSituationMaxRunes / expressionStyleMaxRunes 是「情境」与「说法」
	// 的长度上限。它们与学习侧的限制一起防止一条坏数据灌进提示词。
	expressionSituationMaxRunes = 200
	expressionStyleMaxRunes     = 200
	// defaultExpressionPageSize 与其它列表接口保持一致，上限共用 maxPageSize。
	defaultExpressionPageSize = 20
)

// handleListExpressions pages the expression library.
//
// group_openid 为空 = 看全部群；enabled 是可选的三态筛选（不传 = 全部）；
// q 同时匹配 situation 与 style。
func (s *Server) handleListExpressions(w http.ResponseWriter, r *http.Request) {
	f := store.ExpressionFilter{
		GroupOpenID: strings.TrimSpace(r.URL.Query().Get("group_openid")),
		Enabled:     queryBoolPtr(r, "enabled"),
		Query:       strings.TrimSpace(r.URL.Query().Get("q")),
		Page:        clampPage(queryInt(r, "page", 1)),
		PageSize:    clampPageSize(queryInt(r, "page_size", defaultExpressionPageSize), defaultExpressionPageSize),
	}
	items, total, err := s.deps.Store.ListExpressions(r.Context(), f)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	if items == nil {
		items = []store.Expression{}
	}
	writeOK(w, map[string]any{
		"items":     items,
		"total":     total,
		"page":      f.Page,
		"page_size": f.PageSize,
	})
}

// handleAddExpression writes one expression by hand (or returns the existing row
// for the same group + situation + style, so a double click is not a duplicate).
func (s *Server) handleAddExpression(w http.ResponseWriter, r *http.Request) {
	var body struct {
		GroupOpenID string `json:"group_openid"`
		Situation   string `json:"situation"`
		Style       string `json:"style"`
	}
	if err := readJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	situation := strings.TrimSpace(body.Situation)
	style := strings.TrimSpace(body.Style)
	if situation == "" || style == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "situation 与 style 都不能为空")
		return
	}
	if n := len([]rune(situation)); n > expressionSituationMaxRunes {
		writeError(w, http.StatusBadRequest, "invalid_request",
			"situation 最多 "+strconv.Itoa(expressionSituationMaxRunes)+" 个字（当前 "+strconv.Itoa(n)+"）")
		return
	}
	if n := len([]rune(style)); n > expressionStyleMaxRunes {
		writeError(w, http.StatusBadRequest, "invalid_request",
			"style 最多 "+strconv.Itoa(expressionStyleMaxRunes)+" 个字（当前 "+strconv.Itoa(n)+"）")
		return
	}
	row, err := s.deps.Store.AddExpression(r.Context(), store.ExpressionInput{
		GroupOpenID: strings.TrimSpace(body.GroupOpenID),
		Situation:   situation,
		Style:       style,
		// 人工新增的表达默认启用，并标记来源，方便与模型学到的行区分。
		Source:  "admin",
		Enabled: true,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	writeOK(w, row)
}

// handleSetExpressionEnabled flips one expression on or off.
func (s *Server) handleSetExpressionEnabled(w http.ResponseWriter, r *http.Request) {
	id, ok := expressionID(w, r)
	if !ok {
		return
	}
	var body struct {
		Enabled *bool `json:"enabled"`
	}
	if err := readJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if body.Enabled == nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "enabled 不能为空")
		return
	}
	found, err := s.deps.Store.SetExpressionEnabled(r.Context(), id, *body.Enabled)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	if !found {
		writeError(w, http.StatusNotFound, "not_found", "表达方式不存在")
		return
	}
	writeOK(w, map[string]any{"id": id, "enabled": *body.Enabled})
}

// handleDeleteExpression removes one expression.
func (s *Server) handleDeleteExpression(w http.ResponseWriter, r *http.Request) {
	id, ok := expressionID(w, r)
	if !ok {
		return
	}
	found, err := s.deps.Store.DeleteExpression(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	if !found {
		writeError(w, http.StatusNotFound, "not_found", "表达方式不存在")
		return
	}
	writeOK(w, map[string]any{"deleted": 1, "id": id})
}

// handleReinforceExpression bumps the weight / use count of one expression.
//
// 「强化」是人工对模型学习结果的一次校正：被强化的行在召回排序里更靠前。
func (s *Server) handleReinforceExpression(w http.ResponseWriter, r *http.Request) {
	id, ok := expressionID(w, r)
	if !ok {
		return
	}
	found, err := s.deps.Store.ReinforceExpression(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	if !found {
		writeError(w, http.StatusNotFound, "not_found", "表达方式不存在")
		return
	}
	writeOK(w, map[string]any{"id": id, "reinforced": true})
}

// expressionID parses the {id} path value shared by the write endpoints. It
// writes the error response itself and reports whether the caller may continue.
func expressionID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	raw := strings.TrimSpace(r.PathValue("id"))
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "invalid_request", "id 不是有效的表达方式编号")
		return 0, false
	}
	return id, true
}
