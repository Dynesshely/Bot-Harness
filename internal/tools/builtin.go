package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/constant"
	"go/parser"
	"go/token"
	"strconv"
	"time"
)

// Builtins returns the zero-dependency tools. They exist to prove the loop works
// before anything that touches the outside world is enabled.
func Builtins() *Registry {
	r := New()
	r.Register(nowTool{})
	r.Register(calcTool{})
	return r
}

func schema(props string, required ...string) json.RawMessage {
	if required == nil {
		required = []string{}
	}
	out, err := json.Marshal(map[string]any{
		"type":       "object",
		"properties": json.RawMessage("{" + props + "}"),
		"required":   required,
	})
	if err != nil {
		return json.RawMessage(`{"type":"object"}`)
	}
	return out
}

// --- now -------------------------------------------------------------------

type nowTool struct{}

func (nowTool) Name() string { return "now" }

func (nowTool) Description() string {
	return "查询当前日期与时间（含星期）。凡涉及「现在几点」「今天几号」都必须调用它，不要凭记忆猜。"
}

func (nowTool) Parameters() json.RawMessage { return schema("") }

func (nowTool) Risk() Risk { return ReadOnly }

func (nowTool) Group() string { return "util" }

func (nowTool) Run(_ context.Context, _ json.RawMessage, env *Env) (Result, error) {
	now := env.Clock()
	return Result{Text: now.Format("2006-01-02 15:04:05") + " 星期" + weekdayCN(now) +
		"（" + now.Format("MST") + "）"}, nil
}

func weekdayCN(t time.Time) string {
	return [...]string{"日", "一", "二", "三", "四", "五", "六"}[int(t.Weekday())]
}

// --- calc ------------------------------------------------------------------

type calcTool struct{}

func (calcTool) Name() string { return "calc" }

func (calcTool) Description() string {
	return "计算四则运算表达式（支持 + - * / % 与括号）。不要自己心算，涉及数字都调用它。"
}

func (calcTool) Parameters() json.RawMessage {
	return schema(`"expr":{"type":"string","description":"例如 (1234*56+7)/8"}`+``, "expr")
}

func (calcTool) Risk() Risk { return ReadOnly }

func (calcTool) Group() string { return "util" }

func (calcTool) Run(_ context.Context, raw json.RawMessage, _ *Env) (Result, error) {
	var args struct {
		Expr string `json:"expr"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return Result{}, fmt.Errorf("参数需要形如 {\"expr\": \"1+1\"}: %w", err)
	}
	expr := args.Expr
	if expr == "" {
		return Result{}, errors.New("expr 不能为空")
	}
	value, err := eval(expr)
	if err != nil {
		return Result{}, err
	}
	return Result{Text: expr + " = " + value}, nil
}

// eval computes an arithmetic expression without eval: the AST is walked and
// anything that is not a number or + - * / % is rejected.
func eval(expr string) (string, error) {
	node, err := parser.ParseExpr(expr)
	if err != nil {
		return "", fmt.Errorf("表达式无法解析：%v", err)
	}
	v, err := evalNode(node)
	if err != nil {
		return "", err
	}
	if v.Kind() == constant.Int {
		return v.ExactString(), nil
	}
	f, _ := constant.Float64Val(v)
	return strconv.FormatFloat(f, 'g', 10, 64), nil
}

func evalNode(n ast.Expr) (constant.Value, error) {
	switch x := n.(type) {
	case *ast.BasicLit:
		if x.Kind != token.INT && x.Kind != token.FLOAT {
			return nil, errors.New("只支持十进制数字")
		}
		v := constant.MakeFromLiteral(x.Value, x.Kind, 0)
		if v.Kind() == constant.Unknown {
			return nil, errors.New("数字字面量无法解析")
		}
		return v, nil
	case *ast.ParenExpr:
		return evalNode(x.X)
	case *ast.UnaryExpr:
		v, err := evalNode(x.X)
		if err != nil {
			return nil, err
		}
		switch x.Op {
		case token.SUB:
			return constant.UnaryOp(token.SUB, v, 0), nil
		case token.ADD:
			return v, nil
		}
		return nil, errors.New("只支持正负号")
	case *ast.BinaryExpr:
		a, err := evalNode(x.X)
		if err != nil {
			return nil, err
		}
		b, err := evalNode(x.Y)
		if err != nil {
			return nil, err
		}
		switch x.Op {
		case token.ADD, token.SUB, token.MUL, token.QUO, token.REM:
			if (x.Op == token.QUO || x.Op == token.REM) && constant.Sign(b) == 0 {
				return nil, errors.New("除数不能为 0")
			}
			if x.Op == token.REM && (a.Kind() != constant.Int || b.Kind() != constant.Int) {
				return nil, errors.New("% 只支持整数")
			}
			return constant.BinaryOp(a, x.Op, b), nil
		}
		return nil, errors.New("只支持 + - * / %")
	}
	return nil, errors.New("表达式里有不支持的语法")
}
