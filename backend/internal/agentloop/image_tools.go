package agentloop

import (
	"context"
	"strings"

	"alliance/internal/capability"
	"alliance/internal/harness"
	"alliance/internal/llmgateway"
	"alliance/internal/resources"
)

const imagePromptSchema = `{"type":"object","properties":{"prompt":{"type":"string","maxLength":2000}},"required":["prompt"],"additionalProperties":false}`

var readImages = registeredTool{"read_images", "读取本次已选择的图片中的信息。", imagePromptSchema, harness.ReadOnly, capability.Images}
var generateImage = registeredTool{"generate_image", "根据说明生成一张图片，保存为本次成果。", imagePromptSchema, harness.FileGeneration, capability.Images}

func imageToolByName(name string) registeredTool {
	switch name {
	case readImages.name:
		return readImages
	case generateImage.name:
		return generateImage
	default:
		return registeredTool{}
	}
}

func (a Activities) imageTools(ctx context.Context, run string) ([]resources.ToolDefinition, error) {
	var definitions []resources.ToolDefinition
	vision, err := llmgateway.EnabledTier(ctx, a.DB, llmgateway.Vision)
	if err != nil {
		return nil, err
	}
	if vision {
		var pictures bool
		if err := a.DB.QueryRow(ctx, `select exists(select from run_source_inputs where run_id=$1 and picture_hash is not null)`,
			run).Scan(&pictures); err != nil {
			return nil, err
		}
		if pictures {
			definitions = append(definitions, readImages.definition())
		}
	}
	image, err := llmgateway.EnabledTier(ctx, a.DB, llmgateway.Image)
	if err != nil {
		return nil, err
	}
	if image {
		definitions = append(definitions, generateImage.definition())
	}
	return definitions, nil
}

func (a Activities) imageAction(ctx context.Context, ref ToolRef, task toolWork) (resources.Reply, error) {
	var args struct{ Prompt string }
	if err := decodeToolArgs(task.args, &args); err != nil || strings.TrimSpace(args.Prompt) == "" ||
		len([]rune(args.Prompt)) > 2000 || strings.ContainsRune(args.Prompt, 0) {
		return resources.Reply{}, harness.ErrToolArguments
	}
	req := llmgateway.Request{Tier: llmgateway.Image, ImagePrompt: args.Prompt}
	if task.name == readImages.name {
		if len(task.pictures) == 0 {
			return resources.Reply{}, harness.ErrToolArguments
		}
		req = llmgateway.Request{Tier: llmgateway.Vision, Pictures: task.pictures,
			Messages: []resources.Message{{Role: "user", Content: args.Prompt}}}
	}
	response, err := a.Gateway.Call(ctx, llmgateway.CallIdentity{
		OperationKey: actionScope(ref), TeamID: task.team, MemberID: task.actor, RunID: ref.RunID,
		Turn: (ref.Turn-1)*maxToolsPerTurn + ref.Position,
	}, req)
	if err != nil {
		return resources.Reply{}, err
	}
	out := response.Reply
	if task.name == generateImage.name && out.File != nil && strings.TrimSpace(out.Text) == "" {
		out.Text = "已生成图片，可在成果中查看。"
	}
	return out, nil
}
