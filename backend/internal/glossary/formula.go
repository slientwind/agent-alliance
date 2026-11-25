package glossary

import (
	"bytes"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"regexp"
	"strings"
)

var businessIdentifier = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)
var decimal = regexp.MustCompile(`^(?:0|[1-9][0-9]{0,17})(?:\.[0-9]{1,8})?$`)

const maxFormulaNodes = 40

// A formula describes arithmetic over an approved view's fields. It cannot
// contain selectors, SQL expressions, statements, or arbitrary calls.
func metricFormula(raw string) (string, bool) {
	if raw == "" || len(raw) > 500 || strings.Contains(raw, "//") || strings.Contains(raw, "/*") {
		return "", false
	}
	value, err := parser.ParseExpr(raw)
	if err != nil {
		return "", false
	}
	nodes := 0
	if !validExpression(value, 0, &nodes) {
		return "", false
	}
	var formatted bytes.Buffer
	if format.Node(&formatted, token.NewFileSet(), value) != nil {
		return "", false
	}
	return formatted.String(), true
}

func validExpression(value ast.Expr, depth int, nodes *int) bool {
	*nodes = *nodes + 1
	if depth > 10 || *nodes > maxFormulaNodes {
		return false
	}
	switch expression := value.(type) {
	case *ast.Ident:
		return businessIdentifier.MatchString(expression.Name)
	case *ast.BasicLit:
		return (expression.Kind == token.FLOAT || expression.Kind == token.INT) && decimal.MatchString(expression.Value)
	case *ast.ParenExpr:
		return validExpression(expression.X, depth+1, nodes)
	case *ast.UnaryExpr:
		return (expression.Op == token.ADD || expression.Op == token.SUB) && validExpression(expression.X, depth+1, nodes)
	case *ast.BinaryExpr:
		switch expression.Op {
		case token.ADD, token.SUB, token.MUL, token.QUO:
			return validExpression(expression.X, depth+1, nodes) && validExpression(expression.Y, depth+1, nodes)
		}
	case *ast.CallExpr:
		name, ok := expression.Fun.(*ast.Ident)
		if !ok || len(expression.Args) != 1 || expression.Ellipsis.IsValid() {
			return false
		}
		switch strings.ToLower(name.Name) {
		case "sum", "avg", "count", "min", "max":
			field, ok := expression.Args[0].(*ast.Ident)
			return ok && businessIdentifier.MatchString(field.Name)
		}
	}
	return false
}
