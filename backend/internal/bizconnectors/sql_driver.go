package bizconnectors

import (
	"context"
	"crypto/tls"
	"database/sql"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5"
)

func (s Server) readSQL(ctx context.Context, c Connector, credentials Credentials, input ReadRequest) (ReadResult, error) {
	if input.Operation != "" || len(input.Arguments) != 0 || credentials.Username == "" {
		return ReadResult{}, ErrBadQuery
	}
	statement, err := compileReadSQL(input.SQL, c.Provider, c.Settings.AllowedRelations, input.Limit)
	if err != nil {
		return ReadResult{}, err
	}
	queryCtx, stop := context.WithTimeout(ctx, 5*time.Second)
	defer stop()
	if c.Provider == "clickhouse" {
		return s.readClickHouse(queryCtx, c, credentials, statement)
	}
	address, err := endpointURL(c.Endpoint, true)
	if err != nil {
		return ReadResult{}, err
	}
	port := 5432
	if c.Provider == "mysql" || c.Provider == "starrocks" {
		port = 3306
		if c.Provider == "starrocks" {
			port = 9030
		}
	}
	if address.Port() != "" {
		port, err = strconv.Atoi(address.Port())
		if err != nil || port < 1 || port > 65535 {
			return ReadResult{}, ErrBadQuery
		}
	}
	host := address.Hostname()
	dial := func(ctx context.Context, _, _ string) (net.Conn, error) { return s.Egress.Connect(ctx, host, port) }
	if c.Provider == "postgresql" {
		return readPostgres(queryCtx, c, credentials, statement, host, port, dial)
	}
	if c.Provider != "mysql" && c.Provider != "starrocks" {
		return ReadResult{}, ErrBadQuery
	}
	return readMySQL(queryCtx, c, credentials, statement, host, port, dial)
}

type businessDial = func(context.Context, string, string) (net.Conn, error)

func readPostgres(ctx context.Context, c Connector, credentials Credentials, statement governedSQL, host string, port int, dial businessDial) (ReadResult, error) {
	url := url.URL{Scheme: "postgresql", Host: net.JoinHostPort(host, strconv.Itoa(port)), Path: "/" + c.Settings.Database,
		User: url.UserPassword(credentials.Username, credentials.Password), RawQuery: "sslmode=verify-full"}
	config, err := pgx.ParseConfig(url.String())
	if err != nil {
		return ReadResult{}, ErrUnavailable
	}
	config.Fallbacks = nil
	config.DialFunc = dial
	config.LookupFunc = func(context.Context, string) ([]string, error) { return []string{host}, nil }
	config.RuntimeParams["statement_timeout"] = "5000"
	config.RuntimeParams["default_transaction_read_only"] = "on"
	conn, err := pgx.ConnectConfig(ctx, config)
	if err != nil {
		return ReadResult{}, ErrUnavailable
	}
	defer func() {
		stop, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = conn.Close(stop)
	}()
	rows, err := conn.Query(ctx, statement.Text, statement.Args...)
	if err != nil {
		return ReadResult{}, ErrUnavailable
	}
	defer rows.Close()
	result := ReadResult{Rows: []map[string]any{}}
	for _, field := range rows.FieldDescriptions() {
		result.Columns = append(result.Columns, field.Name)
	}
	for rows.Next() {
		values, err := rows.Values()
		if err != nil || !appendSQLRow(&result, values, statement.Limit) {
			return ReadResult{}, ErrUnavailable
		}
	}
	if rows.Err() != nil {
		return ReadResult{}, ErrUnavailable
	}
	return result, nil
}

func readMySQL(ctx context.Context, c Connector, credentials Credentials, statement governedSQL, host string, port int, dial businessDial) (ReadResult, error) {
	config := mysql.NewConfig()
	config.User, config.Passwd, config.DBName = credentials.Username, credentials.Password, c.Settings.Database
	config.Net, config.Addr, config.DialFunc = "tcp", net.JoinHostPort(host, strconv.Itoa(port)), dial
	config.TLS = &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}
	config.Timeout, config.ReadTimeout, config.WriteTimeout = 5*time.Second, 5*time.Second, 5*time.Second
	config.MaxAllowedPacket = maxBusinessReply
	config.Params = map[string]string{"max_execution_time": "5000"}
	if c.Provider == "starrocks" {
		config.Params = map[string]string{"query_timeout": "5"}
	}
	connector, err := mysql.NewConnector(config)
	if err != nil {
		return ReadResult{}, ErrUnavailable
	}
	db := sql.OpenDB(connector)
	defer db.Close()
	rows, err := db.QueryContext(ctx, statement.Text, statement.Args...)
	if err != nil {
		return ReadResult{}, ErrUnavailable
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil || len(columns) > 160 {
		return ReadResult{}, ErrUnavailable
	}
	result := ReadResult{Columns: columns, Rows: []map[string]any{}}
	values, slots := make([]any, len(columns)), make([]any, len(columns))
	for i := range values {
		slots[i] = &values[i]
	}
	for rows.Next() {
		if err := rows.Scan(slots...); err != nil || !appendSQLRow(&result, values, statement.Limit) {
			return ReadResult{}, ErrUnavailable
		}
	}
	if rows.Err() != nil {
		return ReadResult{}, ErrUnavailable
	}
	return result, nil
}

func appendSQLRow(result *ReadResult, values []any, limit int) bool {
	if len(values) != len(result.Columns) || len(values) > 160 || len(result.Rows) >= limit {
		return false
	}
	row := make(map[string]any, len(values))
	budget := 0
	for i, value := range values {
		switch data := value.(type) {
		case []byte:
			budget += len(data)
			value = string(data)
		case string:
			budget += len(data)
		}
		if budget > 8192 {
			return false
		}
		row[result.Columns[i]] = value
	}
	result.Rows = append(result.Rows, row)
	return true
}

func (s Server) readClickHouse(ctx context.Context, c Connector, credentials Credentials, statement governedSQL) (ReadResult, error) {
	query := url.Values{
		"database": {c.Settings.Database}, "readonly": {"1"}, "max_execution_time": {"5"},
		"max_result_rows": {strconv.Itoa(statement.Limit)}, "result_overflow_mode": {"break"},
	}
	for name, value := range statement.HTTP {
		query.Set(name, value)
	}
	r, err := http.NewRequestWithContext(ctx, http.MethodPost, c.Endpoint+"/?"+query.Encode(), strings.NewReader(statement.Text+" FORMAT JSON"))
	if err != nil {
		return ReadResult{}, ErrBadQuery
	}
	r.SetBasicAuth(credentials.Username, credentials.Password)
	response, err := s.businessHTTP(ctx, r)
	if err != nil {
		return ReadResult{}, err
	}
	value, err := boundedBusinessJSON(response)
	if err != nil {
		return ReadResult{}, err
	}
	return readBusinessRows(value, []string{"data"}, statement.Limit)
}
