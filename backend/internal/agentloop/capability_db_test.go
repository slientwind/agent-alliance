package agentloop

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/png"
	"os"
	"reflect"
	"strings"
	"testing"

	"alliance/internal/database"
	"alliance/internal/harness"
	"alliance/internal/knowledge"
	"alliance/internal/llmgateway"
	"alliance/internal/resources"
	"alliance/internal/web"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestCapabilityScopedRunsRespectEnabledTools(t *testing.T) {
	if tools := modelTools([]string{}); len(tools) != 0 {
		t.Fatalf("disabled capabilities surfaced tools: %+v", tools)
	}
	tools := modelTools([]string{"knowledge"})
	if len(tools) != 1 || tools[0].Function.Name != "read_sources" || lookupTool("generate_image").cap != "images" {
		t.Fatalf("incorrect labels for the enabled tools: %+v", tools)
	}
}

func TestCapabilityScopedImagesPostgres(t *testing.T) {
	dsn := os.Getenv("ALLIANCE_LOOKUP_TEST_DATABASE")
	if dsn == "" {
		t.Skip("set ALLIANCE_LOOKUP_TEST_DATABASE to a scratch pgvector database")
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil || !strings.HasPrefix(cfg.ConnConfig.Database, "alliance_lookup_test") {
		t.Fatal("use a scratch database named alliance_lookup_test*")
	}
	ctx := t.Context()
	db, err := database.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	vault, err := resources.NewVault(strings.Repeat("d4", 32))
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	t.Setenv("OBJECT_ROOT", root)
	objects := knowledge.Objects{Root: root}
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := db.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	team, owner, project, provider, model := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	exec("insert into teams(id,name) values($1,$1::uuid::text)", team)
	exec("insert into members(id,email,display_name,password_hash,role) values($1,$2,'image scope test','','member')", owner, owner+"@example.test")
	exec("insert into team_members(team_id,member_id) values($1,$2)", team, owner)
	exec("insert into projects(id,owner,team_id,final_reviewer,title) values($1,$2,$3,$2,'image scope test')", project, owner, team)
	exec("insert into project_memberships(project_id,member_id,can_review) values($1,$2,true)", project, owner)
	exec("insert into model_providers(id,name,kind,endpoint) values($1,$1::uuid::text,'openai','https://fixture.example.test/v1')", provider)
	exec(`insert into model_catalog(id,provider_id,upstream_model,capabilities,context_tokens,max_output_tokens)
	 values($1,$2,'scope-fixture',array['text','tool_call','vision','image'],8192,512)`, model, provider)
	exec(`insert into model_pricing(model_id,effective_at,input_per_1m_micro_cny,output_per_1m_micro_cny,cached_per_1m_micro_cny,per_image_micro_cny)
	 values($1,now(),0,0,0,0)`, model)
	exec("insert into model_tier_routes(tier,model_id,priority) values('default',$1,32767),('vision',$1,32767),('image',$1,32767)", model)
	defer db.Exec(context.Background(), "delete from model_tier_routes where model_id=$1", model)
	var bits bytes.Buffer
	if err := png.Encode(&bits, image.NewRGBA(image.Rect(0, 0, 10, 10))); err != nil {
		t.Fatal(err)
	}
	picture, err := (knowledge.PictureUpload{Name: "scope.png", Data: bits.Bytes()}).Validate()
	if err != nil {
		t.Fatal(err)
	}
	var source string
	err = database.WithTx(ctx, db, func(tx pgx.Tx) error {
		var err error
		source, err = objects.NewPictureSource(ctx, tx, project, owner, picture)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	var revision int
	if err := db.QueryRow(ctx, "select revision from projects where id=$1", project).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	s := Server{DB: db, Vault: vault}
	start := func(enabled []string) (string, error) {
		var run string
		err := database.WithTx(ctx, db, func(tx pgx.Tx) error {
			var err error
			run, err = s.StartTx(ctx, tx, project, owner, uuid.NewString(), StartInput{
				Expected: revision, Goal: "检查所选图片并生成结果", Capabilities: &enabled,
				Sources: []harness.SourceRevision{{ID: source, Revision: 1}},
			})
			return err
		})
		return run, err
	}
	_, err = start([]string{"knowledge"})
	var problem web.Problem
	if !errors.As(err, &problem) || problem.Status != 409 {
		t.Fatalf("unauthorized picture selection returned %v", err)
	}
	run, err := start([]string{"images"})
	if err != nil {
		t.Fatal(err)
	}
	var routed []llmgateway.Tier
	a := Activities{DB: db, Vault: vault, Objects: objects, Gateway: testGateway(func(_ context.Context, _ llmgateway.CallIdentity, request llmgateway.Request) (llmgateway.RoutedResult, error) {
		routed = append(routed, request.Tier)
		var reply resources.Reply
		switch request.Tier {
		case llmgateway.Default:
			var names []string
			for _, tool := range request.Tools {
				names = append(names, tool.Function.Name)
			}
			if !reflect.DeepEqual(names, []string{"read_images", "generate_image"}) {
				t.Errorf("model tool list = %v", names)
			}
			reply = resources.Reply{FinishReason: "tool_calls", ToolCalls: []resources.ToolCall{
				{ID: "inspect", Type: "function", Function: resources.ToolFunction{Name: "read_images", Arguments: `{"prompt":"读取示例图片"}`}},
				{ID: "draw", Type: "function", Function: resources.ToolFunction{Name: "generate_image", Arguments: `{"prompt":"生成示例图片"}`}},
			}}
		case llmgateway.Vision:
			if len(request.Pictures) != 1 || !bytes.Equal(request.Pictures[0].Data, bits.Bytes()) {
				t.Error("vision did not receive the pinned picture")
			}
			reply = resources.Reply{FinishReason: "stop", Text: "读取完成"}
		case llmgateway.Image:
			if request.ImagePrompt != "生成示例图片" || len(request.Pictures) != 0 {
				t.Error("image generation received unintended data")
			}
			reply = resources.Reply{File: &resources.File{Name: "result.png", Mime: "image/png", Data: bits.Bytes()}}
		}
		return llmgateway.RoutedResult{Result: llmgateway.Result{Reply: reply}}, nil
	})}
	if got, err := a.AdvanceTurn(ctx, TurnRef{RunID: run, Turn: 1}); err != nil || got.Tools != 2 {
		t.Fatalf("planning failed: %+v %v", got, err)
	}
	for index := 1; index <= 2; index++ {
		if result, err := a.ExecuteTool(ctx, ToolRef{RunID: run, Turn: 1, Position: index}); err != nil || result.Wait || result.Done {
			t.Fatalf("tool %d = %+v %v", index, result, err)
		}
	}
	var files int
	if err := db.QueryRow(ctx, `select count(*) from artifact_files f join artifacts a on a.id=f.artifact_id
	 where a.run_id=$1 and f.media_type='image/png'`, run).Scan(&files); err != nil || files != 1 {
		t.Fatalf("image was not captured as an artifact: %d %v", files, err)
	}
	if !reflect.DeepEqual(routed, []llmgateway.Tier{llmgateway.Default, llmgateway.Vision, llmgateway.Image}) {
		t.Fatalf("wrong model calls: %v", routed)
	}
}
