package bizconnectors

import (
	"context"
	"crypto/hmac"
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
)

var vendorChinaZone = time.FixedZone("Asia/Shanghai", 8*60*60)

func commerceOperations(provider string) []Operation {
	path := map[string]string{"douyin": "/order/searchList", "qianniu": "/router/rest", "jushuitan": "/api/open/query.aspx"}[provider]
	var result []Operation
	for _, entry := range []struct{ key, name string }{
		{"orders", "查询实时订单"}, {"sku_returns", "查询 SKU 退货记录"},
		{"negative_reviews", "查询差评记录"}, {"after_sales", "查询售后工单"},
	} {
		parameters := map[string]Parameter{
			"from": {Type: "integer", Location: "query"}, "until": {Type: "integer", Location: "query"},
		}
		result = append(result, Operation{Name: entry.key, Description: entry.name, Method: http.MethodPost, Path: path, Risk: "read", Parameters: parameters})
	}
	return result
}

func commerceRange(input ReadRequest, now time.Time) (time.Time, time.Time, error) {
	from, until := now.Add(-24*time.Hour), now
	for name, out := range map[string]*time.Time{"from": &from, "until": &until} {
		if value, ok := input.Arguments[name]; ok {
			number, err := strconv.ParseInt(fmt.Sprint(value), 10, 64)
			if err != nil || number < 1 || number > 4102444800 {
				return time.Time{}, time.Time{}, ErrBadQuery
			}
			*out = time.Unix(number, 0)
		}
	}
	if !from.Before(until) || until.Sub(from) > 7*24*time.Hour || until.After(now.Add(time.Minute)) {
		return time.Time{}, time.Time{}, ErrBadQuery
	}
	return from.In(vendorChinaZone), until.In(vendorChinaZone), nil
}

func commerceRequest(ctx context.Context, c Connector, secret Credentials, op Operation, input ReadRequest) (*http.Request, Operation, error) {
	if secret.AppKey == "" || secret.AppSecret == "" || secret.Token == "" {
		return nil, op, ErrBadQuery
	}
	now := time.Now().In(vendorChinaZone)
	from, until, err := commerceRange(input, now)
	if err != nil {
		return nil, op, err
	}
	limit := min(normalizeLimit(input.Limit), 50)
	const stamp = "2006-01-02 15:04:05"
	switch c.Provider {
	case "douyin":
		method, path := "order.searchList", "/order/searchList"
		payload := map[string]any{"page": 0, "size": limit, "update_time_start": from.Unix(), "update_time_end": until.Unix()}
		switch op.Name {
		case "sku_returns", "after_sales":
			method, path = "afterSale.List", "/afterSale/List"
			payload = map[string]any{"page": 0, "size": limit, "update_start_time": from.Unix(), "update_end_time": until.Unix()}
		case "negative_reviews":
			method, path = c.Settings.Resources["negative_reviews_method"], c.Settings.Resources["negative_reviews_path"]
			if !approvedPath(path, false) || !vendorField.MatchString(method) || !readVendorOperation(method) || strings.ReplaceAll(strings.TrimPrefix(path, "/"), "/", ".") != method {
				return nil, op, ErrBadQuery
			}
			payload["score"] = "bad"
		}
		data, err := json.Marshal(payload)
		if err != nil {
			return nil, op, ErrBadQuery
		}
		timestamp := now.Format(stamp)
		signature := hmac.New(sha256.New, []byte(secret.AppSecret))
		signature.Write([]byte(secret.AppSecret + "app_key" + secret.AppKey + "method" + method + "param_json" + string(data) + "timestamp" + timestamp + "v2" + secret.AppSecret))
		query := url.Values{"app_key": {secret.AppKey}, "method": {method}, "access_token": {secret.Token}, "timestamp": {timestamp},
			"v": {"2"}, "sign_method": {"hmac-sha256"}, "sign": {hex.EncodeToString(signature.Sum(nil))}, "param_json": {string(data)}}
		req, err := vendorRequest(ctx, c.Endpoint, http.MethodGet, path, query, nil)
		return req, op, err
	case "qianniu":
		method := "taobao.trades.sold.get"
		payload := url.Values{"app_key": {secret.AppKey}, "session": {secret.Token}, "timestamp": {now.Format(stamp)}, "v": {"2.0"},
			"format": {"json"}, "sign_method": {"md5"}, "page_no": {"1"}, "page_size": {strconv.Itoa(limit)}}
		payload.Set("start_created", from.Format(stamp))
		payload.Set("end_created", until.Format(stamp))
		switch op.Name {
		case "sku_returns", "after_sales":
			method = "taobao.refunds.receive.get"
			payload.Set("fields", "refund_id,tid,oid,total_fee,refund_fee,status,created,modified,reason")
		case "negative_reviews":
			method = "taobao.traderates.get"
			payload.Set("fields", "tid,oid,result,content,created,item_title,role")
			payload.Set("rate_type", "get")
			payload.Set("role", "seller")
			payload.Set("result", "bad")
		default:
			payload.Set("fields", "tid,status,created,modified,payment,orders")
		}
		payload.Set("method", method)
		names := make([]string, 0, len(payload))
		for name := range payload {
			names = append(names, name)
		}
		slices.Sort(names)
		var signed strings.Builder
		signed.WriteString(secret.AppSecret)
		for _, name := range names {
			signed.WriteString(name)
			signed.WriteString(payload.Get(name))
		}
		signed.WriteString(secret.AppSecret)
		digest := md5.Sum([]byte(signed.String()))
		payload.Set("sign", strings.ToUpper(hex.EncodeToString(digest[:])))
		r, err := http.NewRequestWithContext(ctx, http.MethodPost, c.Endpoint+"/router/rest", strings.NewReader(payload.Encode()))
		if err != nil {
			return nil, op, ErrBadQuery
		}
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		return r, op, nil
	case "jushuitan":
		method := "jushuitan.order.list.query"
		if op.Name == "sku_returns" || op.Name == "after_sales" {
			method = "refund.single.query"
		} else if op.Name == "negative_reviews" {
			method = c.Settings.Resources["negative_reviews_method"]
			if !vendorField.MatchString(method) || !readVendorOperation(method) {
				return nil, op, ErrBadQuery
			}
		}
		stampUTC := strconv.FormatInt(now.Unix(), 10)
		digest := md5.Sum([]byte(method + secret.AppKey + "token" + secret.Token + "ts" + stampUTC + secret.AppSecret))
		r, err := vendorRequest(ctx, c.Endpoint, http.MethodPost, "/api/open/query.aspx", nil,
			map[string]any{"page_index": 1, "page_size": limit, "modified_begin": from.Format(stamp), "modified_end": until.Format(stamp)})
		if err == nil {
			r.URL.RawQuery = "method=" + url.QueryEscape(method) + "&partnerid=" + url.QueryEscape(secret.AppKey) +
				"&token=" + url.QueryEscape(secret.Token) + "&ts=" + stampUTC + "&sign=" + hex.EncodeToString(digest[:])
		}
		return r, op, err
	}
	return nil, op, ErrBadQuery
}

func readVendorOperation(method string) bool {
	lower := strings.ToLower(method)
	return strings.HasSuffix(lower, ".query") || strings.HasSuffix(lower, ".list") || strings.HasSuffix(lower, ".get") || strings.HasSuffix(lower, ".searchlist")
}

func commerceReply(provider, operation string, object map[string]any) (any, error) {
	var containers []map[string]any
	var candidates []string
	switch provider {
	case "douyin":
		if !successCode(object["code"], "10000", "0") {
			return nil, ErrUnavailable
		}
		data, _ := object["data"].(map[string]any)
		containers = append(containers, data)
		candidates = map[string][]string{"orders": {"shop_order_list", "order_list"}, "negative_reviews": {"review_list", "comments"},
			"sku_returns": {"aftersale_list", "after_sale_list"}, "after_sales": {"aftersale_list", "after_sale_list"}}[operation]
	case "qianniu":
		wrapper, component, key := "trades_sold_get_response", "trades", "trade"
		if operation == "after_sales" || operation == "sku_returns" {
			wrapper, component, key = "refunds_receive_get_response", "refunds", "refund"
		} else if operation == "negative_reviews" {
			wrapper, component, key = "traderates_get_response", "trade_rates", "trade_rate"
		}
		data, ok := object[wrapper].(map[string]any)
		if !ok {
			return nil, ErrUnavailable
		}
		rows, _ := data[component].(map[string]any)
		containers, candidates = []map[string]any{rows, data}, []string{key}
	case "jushuitan":
		if !successCode(object["code"], "0") && !successCode(object["issuccess"], "true") && object["issuccess"] != true {
			return nil, ErrUnavailable
		}
		if data, ok := object["data"].(map[string]any); ok {
			containers = append(containers, data)
		}
		containers = append(containers, object)
		candidates = map[string][]string{"orders": {"orders", "datas"}, "sku_returns": {"refunds", "datas"}, "after_sales": {"refunds", "datas"},
			"negative_reviews": {"reviews", "datas"}}[operation]
	}
	for _, wrapper := range containers {
		for _, key := range candidates {
			if items, ok := wrapper[key].([]any); ok {
				return items, nil
			}
		}
		if successCode(wrapper["total"], "0") || successCode(wrapper["total_results"], "0") {
			return []any{}, nil
		}
	}
	return nil, ErrUnavailable
}
