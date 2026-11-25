package bizconnectors

import (
	"encoding/json"
	"go/ast"
	"go/token"
	"math/big"
	"strings"

	"alliance/internal/glossary"
)

type metricAggregate struct{ op, field string }

func viewFormula(raw string, fields map[string]ViewField) (ast.Expr, string, []metricAggregate, error) {
	expression, canonical, ok := glossary.ParseMetricFormula(raw)
	if !ok {
		return nil, "", nil, ErrBadQuery
	}
	var aggregates []metricAggregate
	valid := true
	ast.Inspect(expression, func(node ast.Node) bool {
		switch n := node.(type) {
		case *ast.CallExpr:
			op := strings.ToLower(n.Fun.(*ast.Ident).Name)
			name := n.Args[0].(*ast.Ident).Name
			field, found := fields[name]
			valid = valid && found && (op == "count" || field.Type == "number" || field.Type == "integer")
			aggregates = append(aggregates, metricAggregate{op, name})
			return false
		case *ast.Ident:
			valid = false
		}
		return valid
	})
	if !valid || len(aggregates) == 0 {
		return nil, "", nil, ErrBadQuery
	}
	return expression, canonical, aggregates, nil
}

func numericMetric(raw any) *big.Rat {
	var text string
	switch value := raw.(type) {
	case string:
		text = value
	default:
		encoded, err := json.Marshal(value)
		if err != nil {
			return nil
		}
		text = string(encoded)
	}
	if len(text) == 0 || len(text) > 80 || strings.ContainsAny(text, "/eE") {
		return nil
	}
	number, _ := new(big.Rat).SetString(text)
	return number
}

func formulaValue(expr ast.Expr, values map[metricAggregate]*big.Rat) *big.Rat {
	switch n := expr.(type) {
	case *ast.ParenExpr:
		return formulaValue(n.X, values)
	case *ast.BasicLit:
		return numericMetric(n.Value)
	case *ast.CallExpr:
		return values[metricAggregate{strings.ToLower(n.Fun.(*ast.Ident).Name), n.Args[0].(*ast.Ident).Name}]
	case *ast.UnaryExpr:
		value := formulaValue(n.X, values)
		if value != nil && n.Op == token.SUB {
			return new(big.Rat).Neg(value)
		}
		return value
	case *ast.BinaryExpr:
		left, right := formulaValue(n.X, values), formulaValue(n.Y, values)
		if left == nil || right == nil {
			return nil
		}
		switch n.Op {
		case token.ADD:
			return new(big.Rat).Add(left, right)
		case token.SUB:
			return new(big.Rat).Sub(left, right)
		case token.MUL:
			return new(big.Rat).Mul(left, right)
		case token.QUO:
			if right.Sign() != 0 {
				return new(big.Rat).Quo(left, right)
			}
		}
	}
	return nil
}

func formattedMetric(value *big.Rat) any {
	if value == nil {
		return nil
	}
	return json.Number(strings.TrimRight(strings.TrimRight(value.FloatString(8), "0"), "."))
}

func aggregateRows(rows []map[string]any, field string, operation string) (*big.Rat, error) {
	acc := new(big.Rat)
	var extent *big.Rat
	var count int64
	for _, row := range rows {
		raw := row[field]
		if raw == nil {
			continue
		}
		count++
		if operation == "count" {
			continue
		}
		value := numericMetric(raw)
		if value == nil {
			return nil, ErrUnavailable
		}
		acc.Add(acc, value)
		if extent == nil || (operation == "min" && value.Cmp(extent) < 0) || (operation == "max" && value.Cmp(extent) > 0) {
			extent = value
		}
	}
	if operation == "count" {
		return new(big.Rat).SetInt64(count), nil
	}
	if count == 0 {
		return nil, nil
	}
	switch operation {
	case "min", "max":
		return extent, nil
	case "avg":
		return acc.Quo(acc, new(big.Rat).SetInt64(count)), nil
	default:
		return acc, nil
	}
}
