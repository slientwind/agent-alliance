package agentloop

import (
	"context"
	"errors"
	"strings"
	"unicode/utf8"

	"alliance/internal/brandkit"
	"alliance/internal/harness"
	"alliance/internal/resources"
	"alliance/internal/web"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type documentInput struct {
	Title      string            `json:"title"`
	Content    string            `json:"content"`
	TemplateID string            `json:"templateId"`
	Fields     map[string]string `json:"fields"`
}

func officeFormat(tool string) string {
	switch tool {
	case "create_docx", "create_pptx", "create_xlsx":
		return strings.TrimPrefix(tool, "create_")
	}
	return ""
}

func readDocumentInput(raw string) (documentInput, error) {
	var in documentInput
	if err := decodeToolArgs(raw, &in); err != nil || strings.TrimSpace(in.Title) == "" ||
		utf8.RuneCountInString(in.Title) > 200 || strings.TrimSpace(in.Content) == "" || len(in.Content) > 64<<10 ||
		len(in.Fields) > 40 || strings.ContainsRune(in.Title+in.Content, 0) {
		return documentInput{}, harness.ErrToolArguments
	}
	if in.TemplateID != "" {
		id, err := uuid.Parse(in.TemplateID)
		if err != nil || id == uuid.Nil || id.String() != in.TemplateID {
			return documentInput{}, harness.ErrToolArguments
		}
	}
	total := len(in.Title) + len(in.Content)
	for name, value := range in.Fields {
		if name == "" || len(name) > 48 || name == "title" || name == "content" || strings.ContainsRune(value, 0) {
			return documentInput{}, harness.ErrToolArguments
		}
		total += len(value)
	}
	if total > 96<<10 {
		return documentInput{}, harness.ErrToolArguments
	}
	return in, nil
}

func prepareBrand(ctx context.Context, tx pgx.Tx, task *toolWork, d runData) error {
	format := officeFormat(task.name)
	if format == "" || task.server != "" || task.external != "" {
		return nil
	}
	in, err := readDocumentInput(task.args)
	if err != nil {
		return err
	}
	var space string
	if err := tx.QueryRow(ctx, "select space_id from projects where id=$1", d.project).Scan(&space); err != nil {
		return err
	}
	template, err := brandkit.Resolve(ctx, tx, space, d.actor, format, in.TemplateID)
	if errors.Is(err, pgx.ErrNoRows) {
		if in.TemplateID != "" || len(in.Fields) > 0 {
			return web.Fail(409, "请选择当前空间中有效的官方母版")
		}
		return nil
	}
	if err != nil {
		return err
	}
	task.brand, task.brandSpace, task.document = &template, space, in
	return nil
}

func (a Activities) renderBrand(ctx context.Context, work toolWork) (resources.Reply, error) {
	if a.BrandRenderer == nil {
		return resources.Reply{}, errors.New("官方母版服务暂不可用")
	}
	fields := map[string]string{"title": work.document.Title, "content": work.document.Content}
	for key, value := range work.document.Fields {
		fields[key] = value
	}
	raw, err := a.BrandRenderer.Render(ctx, *work.brand, fields)
	if err != nil {
		return resources.Reply{}, err
	}
	media := map[string]string{
		"docx": "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
		"pptx": "application/vnd.openxmlformats-officedocument.presentationml.presentation",
		"xlsx": "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
	}[work.brand.Format]
	name := strings.NewReplacer("/", " ", "\\", " ", "\n", " ", "\r", " ").Replace(strings.TrimSpace(work.document.Title))
	return resources.Reply{Text: "已使用官方母版生成《" + name + "》。", File: &resources.File{
		Name: name + "." + work.brand.Format, Mime: media, Data: raw,
	}}, nil
}
