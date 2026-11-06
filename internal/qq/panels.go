package qq

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// 指令面板（panel）是 QQ 开放平台在群里/私聊里展示的可点选指令列表。
//
// 平台限制（实现时必须当真）：
//   - 一个机器人最多 20 个面板，一个面板最多 20 项；
//   - item.name ≤ 14 字符（点击后填入输入框的内容），desc ≤ 30 字符；
//   - 写接口 10 QPM，读接口 30 QPM。
//
// 面板只是平台的展示层：命令本身仍然由消息事件驱动，这里的不同步
// 不会影响命令的执行。

// Panel scopes and target types, as accepted by POST /v2/panels.
const (
	PanelScopeC2C     = "c2c"
	PanelScopeGroup   = "group"
	PanelScopeChannel = "channel"
	PanelScopeDM      = "dm"

	PanelTargetAll      = "all"
	PanelTargetSpecific = "specific"

	PanelItemCommand = "command"
)

// PanelItem is one clickable entry inside a panel.
//
// Name is normalised by the platform: a leading "/" is stripped on write
// ("/help" reads back as "help"). Projections must therefore send the slash-free
// form, otherwise every list comparison sees a change (see
// core.commandPanelItems).
type PanelItem struct {
	Type string `json:"type"`
	Name string `json:"name"`
	Desc string `json:"desc,omitempty"`
	// OnlyAdmin marks the entry as admin-only for the platform's display.
	OnlyAdmin bool `json:"only_admin,omitempty"`
}

// Panel is the payload nested under a panel record.
type Panel struct {
	Items   []PanelItem `json:"items"`
	Remark  string      `json:"remark,omitempty"`
	Version int         `json:"version,omitempty"`
}

// PanelRecord is one element of a panel list.
type PanelRecord struct {
	PanelID    string `json:"panel_id"`
	Scope      string `json:"scope"`
	TargetType string `json:"target_type"`
	Panel      Panel  `json:"panel"`
	Version    int    `json:"version"`
	CreatedAt  string `json:"created_at"`
	UpdatedAt  string `json:"updated_at"`
}

// PanelList is one page of GET /v2/panels.
type PanelList struct {
	Records    []PanelRecord `json:"records"`
	NextCursor string        `json:"next_cursor"`
	IsEnd      bool          `json:"is_end"`
}

// CreatePanelRequest is the body of POST /v2/panels.
type CreatePanelRequest struct {
	Scope      string `json:"scope"`
	TargetType string `json:"target_type"`
	Panel      Panel  `json:"panel"`
}

// panelPayload is the wire shape of the nested panel object. Version is
// deliberately absent: the platform treats it as optimistic-concurrency state,
// and the sync path must not send a stale one (the caller passes no version).
type panelPayload struct {
	Items  []PanelItem `json:"items"`
	Remark string      `json:"remark,omitempty"`
}

func payloadFromPanel(p Panel) panelPayload {
	return panelPayload{Items: p.Items, Remark: p.Remark}
}

// ListPanels fetches one page of panels for a scope.
//
// A non-empty cursor continues a previous page; limit is clamped by the caller
// (the platform allows at most 50). Pagination ends at is_end or an empty
// next_cursor. Errors are returned as *APIError, so callers can inspect the
// platform code with CodeIs.
func (c *Client) ListPanels(ctx context.Context, scope, cursor string, limit int) (*PanelList, error) {
	q := url.Values{}
	if scope != "" {
		q.Set("scope", scope)
	}
	if cursor != "" {
		q.Set("cursor", cursor)
	}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	path := "/v2/panels"
	if encoded := q.Encode(); encoded != "" {
		path += "?" + encoded
	}

	var out PanelList
	if err := c.do(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// CreatePanel creates a panel and returns its platform id.
func (c *Client) CreatePanel(ctx context.Context, req CreatePanelRequest) (string, error) {
	body := struct {
		Scope      string       `json:"scope"`
		TargetType string       `json:"target_type"`
		Panel      panelPayload `json:"panel"`
	}{
		Scope:      req.Scope,
		TargetType: req.TargetType,
		Panel:      payloadFromPanel(req.Panel),
	}

	var out struct {
		PanelID string `json:"panel_id"`
	}
	if err := c.do(ctx, http.MethodPost, "/v2/panels", body, &out); err != nil {
		return "", err
	}
	if strings.TrimSpace(out.PanelID) == "" {
		return "", errors.New("qq api /v2/panels: response missing panel_id")
	}
	return out.PanelID, nil
}

// UpdatePanel replaces a panel's items and returns the new version.
//
// Only the panel object is sent: no version, because the sync path always wants
// "make the panel match the registry" rather than "update only if unchanged".
func (c *Client) UpdatePanel(ctx context.Context, panelID string, panel Panel) (int, error) {
	panelID = strings.TrimSpace(panelID)
	if panelID == "" {
		return 0, errors.New("qq api: update panel: empty panel id")
	}
	body := struct {
		Panel panelPayload `json:"panel"`
	}{Panel: payloadFromPanel(panel)}

	var out struct {
		Version int `json:"version"`
	}
	path := "/v2/panels/" + url.PathEscape(panelID)
	if err := c.do(ctx, http.MethodPut, path, body, &out); err != nil {
		return 0, err
	}
	return out.Version, nil
}

// DeletePanel removes a panel. It is provided for completeness and is not used
// by the automatic sync: that path never deletes a panel it does not own.
func (c *Client) DeletePanel(ctx context.Context, panelID string) error {
	panelID = strings.TrimSpace(panelID)
	if panelID == "" {
		return errors.New("qq api: delete panel: empty panel id")
	}
	path := "/v2/panels/" + url.PathEscape(panelID)
	return c.do(ctx, http.MethodDelete, path, nil, nil)
}
