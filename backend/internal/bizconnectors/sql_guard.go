package bizconnectors

import (
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"

	"github.com/xwb1989/sqlparser"
)

type governedSQL struct {
	Text  string
	Args  []any
	HTTP  map[string]string
	Limit int
}

func approvedRelation(name string) bool {
	parts := strings.Split(name, ".")
	if len(parts) < 1 || len(parts) > 2 {
		return false
	}
	for _, part := range parts {
		lower := strings.ToLower(part)
		if !identifierPattern.MatchString(part) || strings.HasPrefix(lower, "pg_") || strings.HasPrefix(lower, "sqlite_") ||
			slices.Contains([]string{"information_schema", "performance_schema", "mysql", "sys", "system", "pg_catalog", "pg_toast"}, lower) {
			return false
		}
	}
	return true
}

func compileReadSQL(raw, provider string, allowed []string, limit int) (governedSQL, error) {
	if strings.TrimSpace(raw) == "" || len(raw) > 6000 || strings.ContainsRune(raw, 0) {
		return governedSQL{}, ErrBadQuery
	}
	segments, err := sqlparser.SplitStatementToPieces(raw)
	if err != nil || len(segments) != 1 {
		return governedSQL{}, ErrBadQuery
	}
	statement, err := sqlparser.Parse(segments[0])
	selectNode, ok := statement.(*sqlparser.Select)
	if err != nil || !ok || len(selectNode.From) != 1 || len(selectNode.SelectExprs) == 0 || len(selectNode.SelectExprs) > 40 ||
		selectNode.Lock != "" || selectNode.Hints != "" || selectNode.Cache != "" || len(selectNode.Comments) > 0 {
		return governedSQL{}, ErrBadQuery
	}
	entry, ok := selectNode.From[0].(*sqlparser.AliasedTableExpr)
	if !ok || len(entry.Partitions) != 0 || entry.Hints != nil {
		return governedSQL{}, ErrBadQuery
	}
	table, ok := entry.Expr.(sqlparser.TableName)
	if !ok {
		return governedSQL{}, ErrBadQuery
	}
	qualified := table.Name.String()
	if !table.Qualifier.IsEmpty() {
		qualified = table.Qualifier.String() + "." + qualified
	}
	if !approvedRelation(qualified) || !slices.Contains(allowed, qualified) {
		return governedSQL{}, ErrBadQuery
	}
	out := governedSQL{Limit: normalizeLimit(limit), HTTP: map[string]string{}}
	if selectNode.Limit != nil {
		if selectNode.Limit.Offset != nil {
			return governedSQL{}, ErrBadQuery
		}
		literal, ok := selectNode.Limit.Rowcount.(*sqlparser.SQLVal)
		if !ok || literal.Type != sqlparser.IntVal {
			return governedSQL{}, ErrBadQuery
		}
		requested, err := strconv.Atoi(string(literal.Val))
		if err != nil || requested < 1 {
			return governedSQL{}, ErrBadQuery
		}
		out.Limit = min(requested, out.Limit)
	}
	selectNode.Limit = &sqlparser.Limit{Rowcount: sqlparser.NewIntVal([]byte(strconv.Itoa(out.Limit)))}
	if err := sqlparser.Walk(approvedNode, selectNode); err != nil {
		return governedSQL{}, err
	}
	quote := "`"
	if provider == "postgresql" {
		quote = `"`
	}
	var bindErr error
	buffer := sqlparser.NewTrackedBuffer(func(buf *sqlparser.TrackedBuffer, node sqlparser.SQLNode) {
		writeIdent := func(value string) {
			if value != "" {
				buf.WriteString(quote + value + quote)
			}
		}
		switch value := node.(type) {
		case sqlparser.ColIdent:
			writeIdent(value.String())
		case sqlparser.TableIdent:
			writeIdent(value.String())
		case *sqlparser.FuncExpr:
			buf.WriteString(strings.ToUpper(value.Name.String()) + "(")
			if value.Distinct {
				buf.WriteString("DISTINCT ")
			}
			buf.Myprintf("%v", value.Exprs)
			buf.WriteByte(')')
		case *sqlparser.SQLVal:
			parameter, name, err := sqlLiteral(value)
			if err != nil {
				bindErr = err
				return
			}
			out.Args = append(out.Args, parameter)
			slot := strconv.Itoa(len(out.Args))
			switch provider {
			case "postgresql":
				buf.WriteString("$" + slot)
			case "clickhouse":
				buf.WriteString("{arg" + slot + ":" + name + "}")
				out.HTTP["param_arg"+slot] = fmt.Sprint(parameter)
			default:
				buf.WriteByte('?')
			}
		default:
			node.Format(buf)
		}
	})
	buffer.Myprintf("%v", selectNode)
	out.Text = buffer.String()
	if bindErr != nil || len(out.Args) > 150 {
		return governedSQL{}, ErrBadQuery
	}
	return out, nil
}

func approvedNode(node sqlparser.SQLNode) (bool, error) {
	switch item := node.(type) {
	case *sqlparser.Select, *sqlparser.AliasedExpr, *sqlparser.AliasedTableExpr, *sqlparser.StarExpr, sqlparser.TableName,
		sqlparser.TableExprs, sqlparser.SelectExprs, *sqlparser.ColName, *sqlparser.Where, sqlparser.GroupBy, sqlparser.OrderBy,
		*sqlparser.Order, *sqlparser.Limit, sqlparser.BoolVal, *sqlparser.NullVal, sqlparser.ValTuple, *sqlparser.ParenExpr,
		*sqlparser.AndExpr, *sqlparser.OrExpr, *sqlparser.NotExpr, *sqlparser.IsExpr:
		return true, nil
	case sqlparser.TableIdent:
		if item.IsEmpty() || identifierPattern.MatchString(item.String()) {
			return true, nil
		}
	case sqlparser.Comments:
		return false, nil
	case *sqlparser.IndexHints:
		if item == nil {
			return false, nil
		}
	case sqlparser.ColIdent:
		if item.IsEmpty() || identifierPattern.MatchString(item.String()) {
			return true, nil
		}
	case *sqlparser.SQLVal:
		_, _, err := sqlLiteral(item)
		return false, err
	case *sqlparser.ComparisonExpr:
		if item.Escape == nil && slices.Contains([]string{"=", "<", ">", "<=", ">=", "!=", "in", "not in", "like", "not like"}, item.Operator) {
			return true, nil
		}
	case *sqlparser.FuncExpr:
		if item.Qualifier.IsEmpty() && len(item.Exprs) >= 1 && len(item.Exprs) <= 8 &&
			slices.Contains([]string{"avg", "count", "sum", "min", "max", "coalesce", "abs"}, item.Name.Lowered()) {
			return true, nil
		}
	case *sqlparser.BinaryExpr:
		if slices.Contains([]string{"+", "-", "*", "/"}, item.Operator) {
			return true, nil
		}
	case *sqlparser.UnaryExpr:
		if _, numeric := item.Expr.(*sqlparser.SQLVal); numeric && (item.Operator == "+" || item.Operator == "-") {
			return true, nil
		}
	}
	return false, ErrBadQuery
}

func sqlLiteral(value *sqlparser.SQLVal) (any, string, error) {
	switch value.Type {
	case sqlparser.StrVal:
		if len(value.Val) <= 4096 && !strings.ContainsRune(string(value.Val), 0) {
			return string(value.Val), "String", nil
		}
	case sqlparser.IntVal:
		n, err := strconv.ParseInt(string(value.Val), 10, 64)
		if err == nil {
			return n, "Int64", nil
		}
	case sqlparser.FloatVal:
		n, err := strconv.ParseFloat(string(value.Val), 64)
		if err == nil && !math.IsInf(n, 0) && !math.IsNaN(n) {
			return n, "Float64", nil
		}
	}
	return nil, "", ErrBadQuery
}
