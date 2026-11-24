package skills

import (
	"alliance/internal/knowledge"
	"alliance/internal/web"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

// Bind compiles an exact dependency list for this immutable Agent revision.
func Bind(ctx context.Context, tx pgx.Tx, team, agent string, revision int, selected []Ref) error {
	order, _, err := resolveBindings(selected, "", func(ref Ref) (dependencyNode, error) {
		return readDependency(ctx, tx, team, ref)
	})
	if err != nil {
		return err
	}
	direct := map[Ref]bool{}
	for _, root := range selected {
		direct[root] = true
	}
	for i, ref := range order {
		if _, err := tx.Exec(ctx, `insert into agent_skill_bindings(agent_id,revision,skill_id,skill_revision,position,root)
            values($1,$2,$3,$4,$5,$6)`, agent, revision, ref.ID, ref.Revision, i, direct[ref]); err != nil {
			return err
		}
	}
	return nil
}

type Context struct {
	Ref      Ref               `json:"-"`
	Name     string            `json:"name"`
	Revision int               `json:"revision"`
	Files    map[string]string `json:"files"`
}

type pinnedSkill struct {
	Ref          Ref
	Name         string
	Allowed      bool
	Root         bool
	Terms        []string
	Dependencies []Ref
	Files        []struct{ Path, Hash string }
}

// Load reads only the files selected by the immutable package manifest.
// Locks on the revisions serialize this read with administrative revocation.
func Load(ctx context.Context, q interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, objects knowledge.Objects, agent string, revision int, actor string, task *string) ([]Context, error) {
	var raw []byte
	err := q.QueryRow(ctx, `with locked as materialized (
        select p.name,s.skill_id,s.revision,b.position,b.root,s.activation_terms,
          s.revoked_at is null and exists(select from team_members m join members u on u.id=m.member_id and u.enabled
             where m.team_id=p.team_id and m.member_id=$3) as allowed
        from agent_skill_bindings b join skill_revisions s on(s.skill_id,s.revision)=(b.skill_id,b.skill_revision)
        join skill_packs p on p.id=s.skill_id where (b.agent_id,b.revision)=($1,$2) for share of s
    )
    select coalesce(jsonb_agg(jsonb_build_object('name',l.name,'ref',jsonb_build_object('id',l.skill_id,'revision',l.revision),'allowed',l.allowed,
        'root',l.root,'terms',l.activation_terms,
        'dependencies',array(select jsonb_build_object('id',d.needs_skill,'revision',d.needs_revision)
           from skill_dependencies d where (d.skill_id,d.revision)=(l.skill_id,l.revision) order by d.position),
        'files',(select jsonb_agg(jsonb_build_object('path',f.path,'hash',f.object_hash) order by f.path)
                 from skill_files f where (f.skill_id,f.revision)=(l.skill_id,l.revision) and f.in_context))
        order by l.position),'[]'::jsonb) from locked l`, agent, revision, actor).Scan(&raw)
	if err != nil {
		return nil, err
	}
	var rows []pinnedSkill
	if err = json.Unmarshal(raw, &rows); err != nil {
		return nil, err
	}
	chosen, err := selectBindings(rows, task)
	if err != nil {
		return nil, err
	}
	var result []Context
	total := 0
	for _, skill := range chosen {
		selected := Context{Ref: skill.Ref, Name: skill.Name, Revision: skill.Ref.Revision, Files: make(map[string]string, len(skill.Files))}
		for _, file := range skill.Files {
			body, err := readObject(objects, file.Hash)
			if err != nil {
				return nil, web.Fail(409, "Skill 正文缺失或内容校验失败")
			}
			total += len(body)
			if total > contextLimit {
				return nil, web.Fail(409, "Skill 上下文超过容量")
			}
			selected.Files[file.Path] = body
		}
		if selected.Files[entry] == "" {
			return nil, web.Fail(409, "Skill 缺少 SKILL.md")
		}
		result = append(result, selected)
	}
	return result, nil
}

func readObject(objects knowledge.Objects, hash string) (string, error) {
	file, err := objects.Open(hash)
	if err != nil {
		return "", err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, FileLimit+1))
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	if len(data) > FileLimit || !utf8.Valid(data) || hex.EncodeToString(sum[:]) != hash {
		return "", errors.New("invalid skill object")
	}
	return string(data), nil
}
