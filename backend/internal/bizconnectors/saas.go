package bizconnectors

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/google/uuid"
)

const (
	kingdeeQuery    = "/K3Cloud/Kingdee.BOS.WebApi.ServicesStub.DynamicFormService.ExecuteBillQuery.common.kdsvc"
	salesforceQuery = "/services/data/v61.0/query"
	fxiaokeQuery    = "/cgi/crm/v2/data/query"
)

func saasProfile(provider string) (string, []Operation) {
	read := func(name, description, method, path string) Operation {
		return Operation{Name: name, Description: description, Method: method, Path: path, Risk: "read"}
	}
	var path, method, family string
	switch provider {
	case "kingdee":
		family, path, method = "erp", kingdeeQuery, http.MethodPost
	case "yonyou":
		family, path, method = "erp", "/openapi/query", http.MethodGet
	case "fxiaoke":
		family, path, method = "crm", fxiaokeQuery, http.MethodPost
	case "salesforce":
		family, path, method = "crm", salesforceQuery, http.MethodGet
	case "feishu_crm":
		family, path, method = "crm", "/open-apis/bitable/v1/apps/records", http.MethodGet
	case "douyin", "qianniu", "jushuitan":
		return "commerce", commerceOperations(provider)
	default:
		return "", nil
	}
	if family == "erp" {
		return family, []Operation{
			read("inventory_batches", "查询库存批次", method, path),
			read("purchase_requests", "查询采购申请", method, path),
			read("suppliers", "查询供应商档案", method, path),
		}
	}
	return family, []Operation{
		read("opportunities", "查询销售商机", method, path),
		read("customer_receipts", "查询客户回款", method, path),
	}
}

var vendorField = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.]{0,100}$`)

func vendorFields(config, fallback string) ([]string, error) {
	if config == "" {
		config = fallback
	}
	list := strings.Split(config, ",")
	if len(list) == 0 || len(list) > 28 {
		return nil, ErrBadQuery
	}
	for i, name := range list {
		name = strings.TrimSpace(name)
		if !vendorField.MatchString(name) || strings.Contains(name, "..") || strings.HasSuffix(name, ".") {
			return nil, ErrBadQuery
		}
		list[i] = name
	}
	return list, nil
}

func kingdeeFields(c Connector, operation string) ([]string, error) {
	return vendorFields(c.Settings.Resources[operation+"_fields"], map[string]string{
		"inventory_batches": "FID,FMaterialId.FNumber,FLot.FNumber,FBaseQty",
		"purchase_requests": "FID,FBillNo,FDate,FDocumentStatus",
		"suppliers":         "FSupplierId,FNumber,FName,FDocumentStatus",
	}[operation])
}

func saasReadRequest(ctx context.Context, c Connector, secret Credentials, op Operation, input ReadRequest) (*http.Request, Operation, error) {
	if err := validateArguments(op, input.Arguments); err != nil {
		return nil, op, err
	}
	if c.Category == "commerce" {
		return commerceRequest(ctx, c, secret, op, input)
	}
	pageSize := normalizeLimit(input.Limit)
	path := op.Path
	query := make(url.Values)
	var headers = make(http.Header)
	var body any
	switch c.Provider {
	case "kingdee":
		form := c.Settings.Resources[op.Name+"_form"]
		if form == "" {
			form = map[string]string{"inventory_batches": "STK_Inventory", "purchase_requests": "PUR_Requisition", "suppliers": "BD_Supplier"}[op.Name]
		}
		fields, err := kingdeeFields(c, op.Name)
		if err != nil || !identifierPattern.MatchString(form) || secret.Token == "" {
			return nil, op, ErrBadQuery
		}
		data, err := json.Marshal(map[string]any{"FormId": form, "FieldKeys": strings.Join(fields, ","), "Limit": pageSize, "StartRow": 0})
		if err != nil {
			return nil, op, ErrBadQuery
		}
		body = map[string]string{"data": string(data)}
		cookie := http.Cookie{Name: "kdservice-sessionid", Value: secret.Token}
		if cookie.Valid() != nil {
			return nil, op, ErrBadQuery
		}
		headers.Set("Cookie", cookie.String())
	case "yonyou":
		// BIP publishes the concrete operation locations per tenant and app.
		path = c.Settings.Resources[op.Name+"_path"]
		if !approvedPath(path, false) || secret.Token == "" {
			return nil, op, ErrBadQuery
		}
		query.Set("pageIndex", "1")
		query.Set("pageSize", strconv.Itoa(pageSize))
		op.ResultPath = []string{"data", "records"}
		if saved := c.Settings.Resources[op.Name+"_result"]; saved != "" {
			op.ResultPath = strings.Split(saved, ".")
			if len(op.ResultPath) > 8 {
				return nil, op, ErrBadQuery
			}
		}
	case "fxiaoke":
		object := map[string]string{"opportunities": "OpportunityObj", "customer_receipts": "PaymentObj"}[op.Name]
		if secret.Token == "" || !pathValuePattern.MatchString(secret.Account) {
			return nil, op, ErrBadQuery
		}
		query.Set("thirdTraceId", uuid.NewString())
		headers.Set("corpAccessToken", secret.Token)
		headers.Set("currentOpenUserId", secret.Account)
		body = map[string]any{"data": map[string]any{"dataObjectApiName": object, "find_explicit_total_num": false,
			"search_query_info": map[string]any{"limit": min(pageSize, 100), "offset": 0}}}
		op.ResultPath = []string{"data", "dataList"}
	case "salesforce":
		object, defaults := "Opportunity", "Id,Name,Amount,StageName,CloseDate"
		if op.Name == "customer_receipts" {
			object, defaults = "Payment", "Id,Amount,Status,CreatedDate"
		}
		if saved := c.Settings.Resources[op.Name+"_object"]; saved != "" {
			object = saved
		}
		fields, err := vendorFields(c.Settings.Resources[op.Name+"_fields"], defaults)
		if err != nil || !identifierPattern.MatchString(object) || secret.Token == "" {
			return nil, op, ErrBadQuery
		}
		query.Set("q", "SELECT "+strings.Join(fields, ",")+" FROM "+object+" LIMIT "+strconv.Itoa(pageSize))
		op.ResultPath = []string{"records"}
	case "feishu_crm":
		app, table := c.Settings.Resources["app"], c.Settings.Resources[op.Name+"_table"]
		if secret.Token == "" || !pathValuePattern.MatchString(app) || !pathValuePattern.MatchString(table) {
			return nil, op, ErrBadQuery
		}
		path = "/open-apis/bitable/v1/apps/" + app + "/tables/" + table + "/records"
		query.Set("page_size", strconv.Itoa(pageSize))
	default:
		return nil, op, ErrBadQuery
	}
	r, err := vendorRequest(ctx, c.Endpoint, op.Method, path, query, body)
	if err != nil {
		return nil, op, err
	}
	for name, values := range headers {
		r.Header[name] = values
	}
	if c.Provider == "salesforce" || c.Provider == "feishu_crm" || c.Provider == "yonyou" {
		r.Header.Set("Authorization", "Bearer "+secret.Token)
	}
	return r, op, nil
}

func vendorRequest(ctx context.Context, origin, method, path string, query url.Values, payload any) (*http.Request, error) {
	if !approvedPath(path, false) {
		return nil, ErrBadQuery
	}
	var body io.Reader
	if payload != nil {
		data, err := json.Marshal(payload)
		if err != nil {
			return nil, ErrBadQuery
		}
		body = bytes.NewReader(data)
	}
	endpoint := origin + path
	if len(query) > 0 {
		endpoint += "?" + query.Encode()
	}
	r, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return nil, ErrBadQuery
	}
	if body != nil {
		r.Header.Set("Content-Type", "application/json")
	}
	r.Header.Set("Accept", "application/json")
	return r, nil
}

func successCode(raw any, values ...string) bool {
	actual := ""
	switch code := raw.(type) {
	case json.Number:
		actual = code.String()
	case string:
		actual = code
	}
	for _, expected := range values {
		if actual == expected {
			return true
		}
	}
	return false
}

func saasReply(c Connector, op Operation, raw any) (any, error) {
	if c.Provider == "kingdee" {
		fields, err := kingdeeFields(c, op.Name)
		values, ok := raw.([]any)
		if err != nil || !ok {
			return nil, ErrUnavailable
		}
		out := make([]any, 0, len(values))
		for _, value := range values {
			ordered, ok := value.([]any)
			if !ok || len(ordered) != len(fields) {
				return nil, ErrUnavailable
			}
			row := make(map[string]any, len(fields))
			for i, field := range fields {
				row[field] = ordered[i]
			}
			out = append(out, row)
		}
		return out, nil
	}
	object, ok := raw.(map[string]any)
	if !ok {
		return nil, ErrUnavailable
	}
	switch c.Provider {
	case "feishu_crm":
		if !successCode(object["code"], "0") {
			return nil, ErrUnavailable
		}
		data, _ := object["data"].(map[string]any)
		items, _ := data["items"].([]any)
		rows := make([]any, 0, len(items))
		for _, entry := range items {
			record, _ := entry.(map[string]any)
			fields, ok := record["fields"].(map[string]any)
			if !ok {
				return nil, ErrUnavailable
			}
			fields["record_id"] = record["record_id"]
			rows = append(rows, fields)
		}
		return rows, nil
	case "fxiaoke":
		if !successCode(object["errorCode"], "0") && !successCode(object["code"], "0") {
			return nil, ErrUnavailable
		}
	case "yonyou":
		if code, ok := object["code"]; ok && !successCode(code, "0", "200") {
			return nil, ErrUnavailable
		}
	case "douyin", "qianniu", "jushuitan":
		return commerceReply(c.Provider, op.Name, object)
	}
	return raw, nil
}
