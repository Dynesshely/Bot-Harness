package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

// 表达方式库：从真实群聊里学「什么情境用什么说法」，按需召回并注入回复提示词。
//
// 学习与召回都在 core 侧；store 只负责持久化与查询。唯一键
// (group_openid, situation, style) 让重复学习天然收敛为 use_count 累加，
// 不需要在调用方做去重表。

// ExpressionInput is one learned or hand-written expression.
type ExpressionInput struct {
	GroupOpenID string `json:"group_openid"`
	Situation   string `json:"situation"`
	Style       string `json:"style"`
	Source      string `json:"source"`
	Enabled     bool   `json:"enabled"`
	Embedding   string `json:"-"`
	Dims        int    `json:"-"`
}

// Expression is one row of the expression library. Embedding is deliberately
// not part of the JSON contract: it is large, internal, and can be recomputed.
type Expression struct {
	ID          int64  `json:"id"`
	GroupOpenID string `json:"group_openid"`
	Situation   string `json:"situation"`
	Style       string `json:"style"`
	Source      string `json:"source"`
	UseCount    int    `json:"use_count"`
	Enabled     bool   `json:"enabled"`
	Dims        int    `json:"dims"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
	Embedding   string `json:"-"`
}

// ExpressionFilter narrows the admin list. Zero values mean "no filter".
type ExpressionFilter struct {
	GroupOpenID string
	Enabled     *bool
	Query       string
	Page        int
	PageSize    int
}

const (
	expressionSituationMaxRunes = 200
	expressionStyleMaxRunes     = 200
	expressionListMaxPageSize   = 200
	expressionGroupFetchLimit   = 500
)

// AddExpression inserts one row, or bumps use_count when the same group could
// already say the same thing. The returned row is always the current one, so a
// double click in the admin panel does not create a duplicate.
func (s *Store) AddExpression(ctx context.Context, in ExpressionInput) (Expression, error) {
	in.GroupOpenID = strings.TrimSpace(in.GroupOpenID)
	in.Situation = strings.TrimSpace(in.Situation)
	in.Style = strings.TrimSpace(in.Style)
	in.Source = strings.TrimSpace(in.Source)
	if in.Source == "" {
		in.Source = "learn"
	}
	if in.Situation == "" {
		return Expression{}, errors.New("store: expression situation is required")
	}
	if in.Style == "" {
		return Expression{}, errors.New("store: expression style is required")
	}
	if n := utf8.RuneCountInString(in.Situation); n > expressionSituationMaxRunes {
		return Expression{}, fmt.Errorf("store: expression situation exceeds %d runes", expressionSituationMaxRunes)
	}
	if n := utf8.RuneCountInString(in.Style); n > expressionStyleMaxRunes {
		return Expression{}, fmt.Errorf("store: expression style exceeds %d runes", expressionStyleMaxRunes)
	}
	now := nowRFC3339()
	if _, err := s.execContext(ctx, `
INSERT INTO expressions (group_openid, situation, style, source, embedding, dims, use_count, enabled, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, 1, ?, ?, ?)
ON CONFLICT (group_openid, situation, style)
DO UPDATE SET use_count = expressions.use_count + 1,
              source = CASE WHEN excluded.source <> '' THEN excluded.source ELSE expressions.source END,
              updated_at = excluded.updated_at`,
		in.GroupOpenID, in.Situation, in.Style, in.Source, in.Embedding, in.Dims, boolToInt(in.Enabled), now, now); err != nil {
		return Expression{}, err
	}
	row, err := s.expressionByKey(ctx, in.GroupOpenID, in.Situation, in.Style)
	if err != nil {
		return Expression{}, err
	}
	return row, nil
}

func (s *Store) expressionByKey(ctx context.Context, groupOpenID, situation, style string) (Expression, error) {
	rows, err := s.queryContext(ctx, `
SELECT id, group_openid, situation, style, source, use_count, enabled, dims, created_at, updated_at, embedding
FROM expressions WHERE group_openid = ? AND situation = ? AND style = ?`, groupOpenID, situation, style)
	if err != nil {
		return Expression{}, err
	}
	defer rows.Close()
	items, err := scanExpressions(rows)
	if err != nil {
		return Expression{}, err
	}
	if len(items) == 0 {
		return Expression{}, ErrNotFound
	}
	return items[0], nil
}

// ListExpressions pages the library for the admin panel.
func (s *Store) ListExpressions(ctx context.Context, f ExpressionFilter) ([]Expression, int, error) {
	if f.Page < 1 {
		f.Page = 1
	}
	if f.PageSize < 1 || f.PageSize > expressionListMaxPageSize {
		f.PageSize = 20
	}
	where := []string{"1=1"}
	args := []any{}
	if g := strings.TrimSpace(f.GroupOpenID); g != "" {
		where = append(where, "group_openid = ?")
		args = append(args, g)
	}
	if f.Enabled != nil {
		where = append(where, "enabled = ?")
		args = append(args, boolToInt(*f.Enabled))
	}
	if q := strings.TrimSpace(f.Query); q != "" {
		where = append(where, "(situation LIKE ? OR style LIKE ?)")
		like := "%" + q + "%"
		args = append(args, like, like)
	}
	cond := strings.Join(where, " AND ")
	var total int
	if err := s.queryRowContext(ctx, "SELECT COUNT(*) FROM expressions WHERE "+cond, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	pageArgs := append(append([]any{}, args...), f.PageSize, (f.Page-1)*f.PageSize)
	rows, err := s.queryContext(ctx, `
SELECT id, group_openid, situation, style, source, use_count, enabled, dims, created_at, updated_at, embedding
FROM expressions WHERE `+cond+`
ORDER BY use_count DESC, id DESC
LIMIT ? OFFSET ?`, pageArgs...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	items, err := scanExpressions(rows)
	if err != nil {
		return nil, 0, err
	}
	return items, total, nil
}

// ExpressionsForGroup returns the candidates a group may use: its own rows plus
// the global pool (group_openid = ”), enabled only.
func (s *Store) ExpressionsForGroup(ctx context.Context, groupOpenID string, limit int) ([]Expression, error) {
	if limit <= 0 || limit > expressionGroupFetchLimit {
		limit = expressionGroupFetchLimit
	}
	rows, err := s.queryContext(ctx, `
SELECT id, group_openid, situation, style, source, use_count, enabled, dims, created_at, updated_at, embedding
FROM expressions
WHERE enabled = 1 AND (group_openid = ? OR group_openid = '')
ORDER BY use_count DESC, id DESC
LIMIT ?`, strings.TrimSpace(groupOpenID), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanExpressions(rows)
}

// SetExpressionEnabled flips one row. found is false when the id does not exist.
func (s *Store) SetExpressionEnabled(ctx context.Context, id int64, enabled bool) (bool, error) {
	res, err := s.execContext(ctx, `UPDATE expressions SET enabled = ?, updated_at = ? WHERE id = ?`,
		boolToInt(enabled), nowRFC3339(), id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// DeleteExpression removes one row.
func (s *Store) DeleteExpression(ctx context.Context, id int64) (bool, error) {
	res, err := s.execContext(ctx, `DELETE FROM expressions WHERE id = ?`, id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// ReinforceExpression bumps use_count so a hand-approved row ranks higher.
func (s *Store) ReinforceExpression(ctx context.Context, id int64) (bool, error) {
	res, err := s.execContext(ctx, `UPDATE expressions SET use_count = use_count + 1, updated_at = ? WHERE id = ?`,
		nowRFC3339(), id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// UpdateExpressionEmbedding stores the vector (JSON array) and its dimension.
func (s *Store) UpdateExpressionEmbedding(ctx context.Context, id int64, embedding string, dims int) error {
	_, err := s.execContext(ctx, `UPDATE expressions SET embedding = ?, dims = ?, updated_at = ? WHERE id = ?`,
		embedding, dims, nowRFC3339(), id)
	return err
}

// CountExpressions reports the library size, used by the admin overview.
func (s *Store) CountExpressions(ctx context.Context) (int, error) {
	var n int
	err := s.queryRowContext(ctx, `SELECT COUNT(*) FROM expressions`).Scan(&n)
	return n, err
}

func scanExpressions(rows interface {
	Next() bool
	Scan(dest ...any) error
	Err() error
}) ([]Expression, error) {
	out := []Expression{}
	for rows.Next() {
		var (
			e       Expression
			enabled int
		)
		if err := rows.Scan(&e.ID, &e.GroupOpenID, &e.Situation, &e.Style, &e.Source, &e.UseCount,
			&enabled, &e.Dims, &e.CreatedAt, &e.UpdatedAt, &e.Embedding); err != nil {
			return nil, err
		}
		e.Enabled = enabled == 1
		out = append(out, e)
	}
	return out, rows.Err()
}
