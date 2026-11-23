package resources

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"alliance/internal/database"
	"alliance/internal/identity"
	"alliance/internal/web"
	"github.com/jackc/pgx/v5"
)

func (c Catalog) reviseInstructions(w http.ResponseWriter, r *http.Request, u identity.Member) error {
	var in struct {
		Expected     int
		Instructions string
	}
	if err := web.Read(r, &in); err != nil {
		return err
	}
	ctx, id := r.Context(), r.PathValue("id")
	var revision int
	err := database.WithTx(ctx, c.DB, func(tx pgx.Tx) error {
		var err error
		revision, err = c.ReviseInstructionsTx(ctx, tx, u.ID, id, in.Expected, in.Instructions)
		return err
	})
	if err != nil {
		return err
	}
	return web.JSON(w, 201, map[string]any{"id": id, "revision": revision})
}

// ReviseInstructionsTx retains the selected model and credential for a catalog or project draft.
func (c Catalog) ReviseInstructionsTx(ctx context.Context, tx pgx.Tx, actor, id string, base int, instructions string) (int, error) {
	instructions = strings.TrimSpace(instructions)
	if base < 1 || instructions == "" {
		return 0, web.Fail(422, "请选择基础版本并填写 Agent 职责")
	}
	if err := lockTeamAgent(ctx, tx, actor, id, ""); err != nil {
		return 0, err
	}
	var key []byte
	var prior string
	var latest int
	draft := DraftConfig{ID: id, Instructions: instructions, Expected: base}
	err := tx.QueryRow(ctx, `select ar.name,ar.instructions,ar.resource_id,ar.resource_revision,ar.key_override,
 (select max(revision) from agent_revisions where agent_id=ar.agent_id)
 from agent_revisions ar join model_revisions mr on (mr.resource_id,mr.revision)=(ar.resource_id,ar.resource_revision)
 where ar.agent_id=$1 and ar.revision=$2 and mr.operation in ('text','png','vision','image-edit')`, id, base).
		Scan(&draft.Name, &prior, &draft.Resource, &draft.ResourceRevision, &key, &latest)
	if err != nil {
		return 0, err
	}
	if latest != base {
		return 0, web.Fail(409, "Agent 草稿已有新版本")
	}
	if strings.TrimSpace(prior) == instructions {
		return 0, web.Fail(422, "请修改职责后再保存新草稿")
	}
	if key != nil {
		// SaveDraftTx seals the retained override for the new revision.
		draft.Key, err = c.Vault.Open(key, fmt.Sprintf("agent/%s/%d", id, base))
		if err != nil {
			return 0, err
		}
	}
	_, next, err := c.SaveDraftTx(ctx, tx, actor, draft)
	return next, err
}

// Only personal, stand-alone text trials may provide input for an editable retest.
// Project trials and required checks have their own authorization and fixed scope.
const reusableTrial = `v.actor=$2 and v.status in ('passed','failed') and v.check_id is null and v.input_text is not null and v.expected is not null
 and not exists(select from validation_withdrawals wd where wd.trial_id=v.id)
 and exists(select from spend s where s.op_key='validation/'||v.id::text and s.project_id is null)
 and exists(select from agent_revisions ar join model_revisions mr
 on (mr.resource_id,mr.revision)=(ar.resource_id,ar.resource_revision)
 where (ar.agent_id,ar.revision)=(v.agent_id,v.revision) and mr.operation='text')`

func (c Catalog) trialSample(w http.ResponseWriter, r *http.Request, u identity.Member) error {
	var input, expected string
	err := c.DB.QueryRow(r.Context(), `select v.input_text,v.expected from validations v join agents a on a.id=v.agent_id
 join team_members tm on tm.team_id=a.team_id and tm.member_id=$2
 where v.agent_id=$1 and v.id=$3 and not a.disabled and (`+reusableTrial+`)`,
		r.PathValue("id"), u.ID, r.PathValue("trial")).Scan(&input, &expected)
	if err != nil {
		return err
	}
	return web.JSON(w, 200, map[string]string{"input": input, "expected": expected})
}
