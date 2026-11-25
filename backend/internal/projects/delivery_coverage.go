package projects

import (
	"alliance/internal/database"
	"alliance/internal/identity"
	"alliance/internal/web"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

type DeliveryRequirement struct {
	Step  int    `json:"step"`
	Label string `json:"label"`
}

type DeliveryContract struct {
	AllowPartial bool                  `json:"allowPartial"`
	Items        []DeliveryRequirement `json:"items"`
}

func deliveryOrdinals(nodes []executionNode) []int {
	ordinals := make([]int, 0, len(nodes))
	for i, node := range nodes {
		followed := (node.phase == "answer" || node.phase == "review_evidence") && i+1 < len(nodes) &&
			(nodes[i+1].phase == "review_evidence" || nodes[i+1].phase == "synthesize")
		if node.phase != "search" && !followed {
			ordinals = append(ordinals, i+1)
		}
	}
	return ordinals
}

func normalizeDeliveryRequirements(contract DeliveryContract, nodes []executionNode) ([]DeliveryRequirement, error) {
	ordinals := deliveryOrdinals(nodes)
	items, err := validateBusinessDeliveryRequirements(contract, len(ordinals))
	for i := range items {
		items[i].Step = ordinals[items[i].Step-1]
	}
	return items, err
}

func validateBusinessDeliveryRequirements(contract DeliveryContract, steps int) ([]DeliveryRequirement, error) {
	if len(contract.Items) > 32 || contract.AllowPartial && len(contract.Items) == 0 {
		return nil, web.Fail(422, "请配置最多 32 个必交项。允许部分交付时至少指定一项")
	}
	items := make([]DeliveryRequirement, 0, len(contract.Items))
	seen := make(map[DeliveryRequirement]bool)
	for _, item := range contract.Items {
		item.Label = strings.TrimSpace(item.Label)
		if item.Step < 1 || item.Step > steps || !validTemplateText(item.Label, 180) {
			return nil, web.Fail(422, "每个必交项都需绑定业务步骤，并填写不超过 180 字的完成条件")
		}
		key := DeliveryRequirement{Step: item.Step, Label: strings.ToLower(item.Label)}
		if seen[key] {
			return nil, web.Fail(422, "同一步骤有重复的必交项")
		}
		seen[key] = true
		items = append(items, item)
	}
	return items, nil
}

func saveDeliveryContract(ctx context.Context, tx pgx.Tx, p Project, version int, contract *DeliveryContract, nodes []executionNode) error {
	if contract == nil {
		var previous bool
		if err := tx.QueryRow(ctx, `select exists(select from plan_delivery_contracts where (project_id,plan_version)=($1,$2))`,
			p.ID, p.PlanVersion).Scan(&previous); err != nil {
			return err
		}
		if previous {
			return web.Fail(409, "请在新版方案中逐项核对原交付约定")
		}
		return nil
	}
	items, err := normalizeDeliveryRequirements(*contract, nodes)
	if err != nil || len(items) == 0 {
		return err
	}
	if _, err := tx.Exec(ctx, "insert into plan_delivery_contracts values($1,$2,$3)", p.ID, version, contract.AllowPartial); err != nil {
		return err
	}
	for i, item := range items {
		if _, err := tx.Exec(ctx, "insert into plan_delivery_requirements values($1,$2,$3,$4,$5)", p.ID, version, i+1, item.Step, item.Label); err != nil {
			return err
		}
	}
	return nil
}

type coverageItem struct {
	Item              int    `json:"item"`
	Run               string `json:"run"`
	Artifact          string `json:"artifact"`
	Version           int    `json:"version"`
	State             string `json:"state"`
	ResponsibleActive bool   `json:"responsibleActive"`
	ContinueWhen      string `json:"continueWhen"`
}

type deliveryCoverage struct {
	AllowPartial bool           `json:"allowPartial"`
	Items        []coverageItem `json:"items"`
}

func readDeliveryCoverage(ctx context.Context, tx pgx.Tx, p Project) (deliveryCoverage, json.RawMessage, error) {
	var raw json.RawMessage
	var value deliveryCoverage
	err := tx.QueryRow(ctx, "select project_delivery_coverage($1,$2)", p.ID, p.PlanVersion).Scan(&raw)
	if err == nil && raw != nil {
		err = json.Unmarshal(raw, &value)
	}
	return value, raw, err
}

func (c deliveryCoverage) authorize(partial bool) error {
	missing, covered := false, false
	for _, item := range c.Items {
		switch item.State {
		case "covered":
			covered = true
		case "missing":
			missing = true
			if !c.AllowPartial || !partial {
				return web.Fail(409, "交付清单还有缺项，请补齐后再确认")
			}
			if !item.ResponsibleActive || item.ContinueWhen == "" {
				return web.Fail(409, "请为每个缺项指定有权接手的负责人及继续条件")
			}
		default:
			return web.Fail(409, "请核对交付清单上的各项要求和当前成果版本")
		}
	}
	if partial && !missing {
		return web.Fail(409, "当前清单没有待办缺项，请刷新后确认完整交付")
	}
	if partial && !covered {
		return web.Fail(409, "至少需要一项已核对通过的必交成果才能部分交付")
	}
	return nil
}

func (c deliveryCoverage) coveredArtifacts() []string {
	var artifacts []string
	for _, item := range c.Items {
		if item.State == "covered" {
			artifacts = append(artifacts, item.Artifact)
		}
	}
	return artifacts
}

type deliveryAssessment struct {
	Expected, Item, Version int
	Run, Artifact           string
	State, Reason           string
	Responsible             string
	ContinueWhen            string
	FileEvidence            *string
	RemoteEvidence          *bool
	UpstreamEvidence        *int
}

func (s Server) assessDeliveryItem(w http.ResponseWriter, r *http.Request, u identity.Member) error {
	var in deliveryAssessment
	if err := web.Read(r, &in); err != nil {
		return err
	}
	in.Reason, in.ContinueWhen = strings.TrimSpace(in.Reason), strings.TrimSpace(in.ContinueWhen)
	if in.Item < 1 || in.Run == "" || !validTemplateText(in.Reason, 1000) ||
		in.State != "covered" && in.State != "missing" ||
		(in.Artifact == "") != (in.Version == 0) || in.Version < 0 ||
		in.State == "covered" && (in.Artifact == "" || in.Responsible != "" || in.ContinueWhen != "") ||
		(in.Responsible == "") != (in.ContinueWhen == "") ||
		in.FileEvidence != nil && (*in.FileEvidence != "" && in.Artifact == "" || len(*in.FileEvidence) > 8192) ||
		in.RemoteEvidence != nil && *in.RemoteEvidence && in.Artifact == "" ||
		in.UpstreamEvidence != nil && (*in.UpstreamEvidence < 0 || *in.UpstreamEvidence > 0 && in.Artifact == "") ||
		utf8.RuneCountInString(in.ContinueWhen) > 1000 || strings.ContainsRune(in.ContinueWhen, 0) {
		return web.Fail(422, "请选择必交项并填写核对依据。指定接手人时请填写继续条件")
	}
	ctx := r.Context()
	err := database.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		p, err := lock(ctx, tx, r.PathValue("id"), u.ID)
		if err != nil {
			return err
		}
		if p.Stage != "review" || p.Revision != in.Expected {
			return web.Fail(409, "项目状态或成果已更新，请核对最新版本")
		}
		var reviewer bool
		if err := tx.QueryRow(ctx, "select can_review from project_members where (project_id,member_id)=($1,$2)", p.ID, u.ID).Scan(&reviewer); err != nil {
			return err
		}
		if !reviewer {
			return web.Fail(403, "需要项目审核权限")
		}
		coverage, _, err := readDeliveryCoverage(ctx, tx, p)
		if err != nil {
			return err
		}
		var current coverageItem
		for _, item := range coverage.Items {
			if item.Item == in.Item {
				current = item
				break
			}
		}
		if current.Run != in.Run || current.Artifact != in.Artifact || current.Version != in.Version {
			return web.Fail(409, "交付清单或成果版本已变化，请重新核对")
		}
		if in.Artifact != "" {
			var readable bool
			if err := tx.QueryRow(ctx, "select distribution_readable($1,$2,$3)", in.Artifact, in.Version, u.ID).Scan(&readable); err != nil {
				return err
			}
			if !readable {
				return web.Fail(403, "当前成果已暂停分发")
			}
		}
		if in.Responsible != "" {
			var eligible bool
			if err := tx.QueryRow(ctx, `select exists(select from project_members pm join members m on m.id=pm.member_id
              where(pm.project_id,pm.member_id)=($1,$2) and pm.recovery_assignment is null and m.kind='person' and m.enabled)`, p.ID, in.Responsible).Scan(&eligible); err != nil {
				return err
			}
			if !eligible {
				return web.Fail(422, "接手人需为有当前项目权限的成员")
			}
		}
		evidence, err := s.deliveryAssessmentEvidence(ctx, tx, p, u.ID, in, current)
		if err != nil {
			return err
		}
		remoteVersion, err := deliveryAssessmentRemoteReceipt(ctx, tx, p, u.ID, in, current)
		if err != nil {
			return err
		}
		upstreamStep, err := deliveryAssessmentUpstreamEvidence(ctx, tx, p, u.ID, in, current)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `insert into delivery_item_assessments(project_id,plan_version,item,run_id,artifact_id,artifact_version,state,reason,responsible,continue_when,reviewer,file_evidence,remote_receipt_version,upstream_evidence_step)
          values($1,$2,$3,$4,nullif($5,'')::uuid,nullif($6,0),$7,$8,nullif($9,'')::uuid,$10,$11,$12,$13,$14)`,
			p.ID, p.PlanVersion, in.Item, in.Run, in.Artifact, in.Version, in.State, in.Reason, in.Responsible, in.ContinueWhen, u.ID, evidence, remoteVersion, upstreamStep); err != nil {
			return err
		}
		return Touch(ctx, tx, p.ID, p.Stage)
	})
	if err != nil {
		return err
	}
	w.WriteHeader(204)
	return nil
}

func deliveryAssessmentRemoteReceipt(ctx context.Context, tx pgx.Tx, p Project, actor string, in deliveryAssessment, current coverageItem) (*int, error) {
	if in.Artifact == "" || (in.RemoteEvidence != nil && !*in.RemoteEvidence) ||
		(in.RemoteEvidence == nil && current.State == "unchecked") {
		return nil, nil
	}
	var version int
	err := tx.QueryRow(ctx, `select remote.version from visible_artifact_remote_receipts($1,$2) remote
      where (remote.artifact_id,remote.version)=($3,$4) and coalesce($5::boolean,(
        select (a.artifact_id,a.remote_receipt_version)=($3,$4) from delivery_item_assessments a
        where (a.project_id,a.plan_version,a.item)=($1,$6,$7) order by a.id desc limit 1
      ),false)`, p.ID, actor, in.Artifact, in.Version, in.RemoteEvidence, p.PlanVersion, in.Item).Scan(&version)
	if err == pgx.ErrNoRows {
		if in.RemoteEvidence != nil {
			return nil, web.Fail(409, "当前成果的第三方回执已不可用，请重新核对")
		}
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &version, nil
}
