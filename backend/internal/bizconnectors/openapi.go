package bizconnectors

import (
	"io"
	"net/http"
	"regexp"
	"slices"
	"strings"

	"alliance/internal/identity"
	"alliance/internal/web"
	"go.yaml.in/yaml/v3"
)

type apiSchema struct {
	Ref        string                `yaml:"$ref"`
	Type       string                `yaml:"type"`
	Properties map[string]*apiSchema `yaml:"properties"`
	Required   []string              `yaml:"required"`
}

type apiParameter struct {
	Ref      string     `yaml:"$ref"`
	Name     string     `yaml:"name"`
	In       string     `yaml:"in"`
	Required bool       `yaml:"required"`
	Type     string     `yaml:"type"`
	Schema   *apiSchema `yaml:"schema"`
}

type apiAction struct {
	ID          string         `yaml:"operationId"`
	Summary     string         `yaml:"summary"`
	Description string         `yaml:"description"`
	Parameters  []apiParameter `yaml:"parameters"`
	RequestBody *struct {
		Ref     string `yaml:"$ref"`
		Content map[string]struct {
			Schema *apiSchema `yaml:"schema"`
		} `yaml:"content"`
	} `yaml:"requestBody"`
	ResultPath []string `yaml:"x-alliance-result-path"`
}

type apiPath struct {
	Ref        string         `yaml:"$ref"`
	Parameters []apiParameter `yaml:"parameters"`
	Get        *apiAction     `yaml:"get"`
	Head       *apiAction     `yaml:"head"`
	Post       *apiAction     `yaml:"post"`
	Put        *apiAction     `yaml:"put"`
	Patch      *apiAction     `yaml:"patch"`
	Delete     *apiAction     `yaml:"delete"`
}

type apiDocument struct {
	OpenAPI     string                `yaml:"openapi"`
	Swagger     string                `yaml:"swagger"`
	BasePath    string                `yaml:"basePath"`
	Paths       map[string]apiPath    `yaml:"paths"`
	Definitions map[string]*apiSchema `yaml:"definitions"`
	Components  struct {
		Schemas map[string]*apiSchema `yaml:"schemas"`
	} `yaml:"components"`
}

var operationSeparator = regexp.MustCompile(`[^a-z0-9]+`)

func importOperations(raw []byte) ([]Operation, error) {
	var doc apiDocument
	if yaml.Unmarshal(raw, &doc) != nil || (doc.Swagger != "2.0" && !strings.HasPrefix(doc.OpenAPI, "3.0.")) || len(doc.Paths) > 150 {
		return nil, ErrBadQuery
	}
	paths := make([]string, 0, len(doc.Paths))
	for path := range doc.Paths {
		paths = append(paths, path)
	}
	slices.Sort(paths)
	operations := []Operation{}
	for _, path := range paths {
		item := doc.Paths[path]
		if item.Ref != "" {
			return nil, ErrBadQuery
		}
		actual := strings.TrimRight(doc.BasePath, "/") + path
		if !approvedPath(actual, true) {
			continue
		}
		for _, method := range []struct {
			name   string
			action *apiAction
		}{{"GET", item.Get}, {"HEAD", item.Head}, {"POST", item.Post}, {"PUT", item.Put}, {"PATCH", item.Patch}, {"DELETE", item.Delete}} {
			if method.action == nil {
				continue
			}
			mapped, err := doc.mapOperation(actual, method.name, item.Parameters, *method.action)
			if err != nil {
				return nil, err
			}
			operations = append(operations, mapped)
		}
	}
	if len(operations) == 0 || !validOperations(operations) {
		return nil, ErrBadQuery
	}
	return operations, nil
}

func (doc apiDocument) mapOperation(path, method string, pathParameters []apiParameter, action apiAction) (Operation, error) {
	id := strings.Trim(operationSeparator.ReplaceAllString(strings.ToLower(action.ID), "_"), "_")
	if !operationPattern.MatchString(id) {
		id = strings.ToLower(method) + operationSeparator.ReplaceAllString(strings.ToLower(path), "_")
		id = strings.TrimRight(id[:min(len(id), 64)], "_")
	}
	description := strings.TrimSpace(action.Summary)
	if description == "" {
		description = strings.Join(strings.Fields(action.Description), " ")
	}
	if description == "" {
		description = id
	}
	if len(description) > 850 {
		description = id
	}
	op := Operation{Name: id, Description: description, Method: method, Path: path, Risk: "write", ResultPath: action.ResultPath,
		Parameters: map[string]Parameter{}}
	if method == http.MethodGet || method == http.MethodHead {
		op.Risk = "read"
	}
	combined := append(append([]apiParameter{}, pathParameters...), action.Parameters...)
	for _, field := range combined {
		if field.Ref != "" {
			return Operation{}, ErrBadQuery
		}
		if field.In == "body" && doc.Swagger == "2.0" {
			if err := doc.mapBody(field.Schema, op.Parameters); err != nil {
				return Operation{}, err
			}
			continue
		}
		if field.In != "path" && field.In != "query" {
			if field.Required {
				return Operation{}, ErrBadQuery
			}
			continue
		}
		kind := field.Type
		if field.Schema != nil {
			schema := doc.resolveSchema(field.Schema)
			if schema == nil {
				return Operation{}, ErrBadQuery
			}
			kind = schema.Type
		}
		op.Parameters[field.Name] = Parameter{Type: kind, Location: field.In, Required: field.Required || field.In == "path"}
	}
	if action.RequestBody != nil {
		if action.RequestBody.Ref != "" || doc.mapBody(action.RequestBody.Content["application/json"].Schema, op.Parameters) != nil {
			return Operation{}, ErrBadQuery
		}
	}
	return op, nil
}

func (doc apiDocument) resolveSchema(schema *apiSchema) *apiSchema {
	for range 8 {
		if schema == nil || schema.Ref == "" {
			return schema
		}
		if name, ok := strings.CutPrefix(schema.Ref, "#/components/schemas/"); ok && identifierPattern.MatchString(name) {
			schema = doc.Components.Schemas[name]
		} else if name, ok := strings.CutPrefix(schema.Ref, "#/definitions/"); ok && identifierPattern.MatchString(name) {
			schema = doc.Definitions[name]
		} else {
			return nil
		}
	}
	return nil
}

func (doc apiDocument) mapBody(root *apiSchema, out map[string]Parameter) error {
	root = doc.resolveSchema(root)
	if root == nil || root.Type != "object" || len(root.Properties) > 40 {
		return ErrBadQuery
	}
	for name, leaf := range root.Properties {
		leaf = doc.resolveSchema(leaf)
		if leaf == nil || out[name].Location != "" || !slices.Contains([]string{"string", "integer", "number", "boolean"}, leaf.Type) {
			return ErrBadQuery
		}
		out[name] = Parameter{Type: leaf.Type, Location: "body", Required: slices.Contains(root.Required, name)}
	}
	return nil
}

func (s Server) importOpenAPI(w http.ResponseWriter, r *http.Request, actor identity.Member) error {
	space := r.PathValue("space")
	if !validID(space) {
		return web.Fail(404, "找不到业务空间")
	}
	var role, kind string
	if err := s.DB.QueryRow(r.Context(), "select coalesce(space_role(id,$2),''),kind from spaces where id=$1", space, actor.ID).Scan(&role, &kind); err != nil || role != "space_admin" || (kind != "team" && kind != "commons") {
		return web.Fail(404, "找不到可管理的业务空间")
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 256<<10))
	if err != nil {
		return web.Fail(422, "接口定义超出可导入范围")
	}
	operations, err := importOperations(raw)
	if err != nil {
		return web.Fail(422, "请提供仅使用本地引用和受支持字段的 OpenAPI 或 Swagger 文档")
	}
	return web.JSON(w, http.StatusOK, map[string]any{"operations": operations})
}
