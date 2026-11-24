package skills

import (
	"context"

	"alliance/internal/web"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
)

type dependencyNode struct {
	Dependencies []Ref
	Bytes        int
}

func resolveBindings(roots []Ref, publishing string, read func(Ref) (dependencyNode, error)) ([]Ref, int, error) {
	if len(roots) > rootLimit {
		return nil, 0, web.Fail(409, "最多绑定 4 个直接 Skill")
	}
	rootIDs := map[string]bool{}
	for _, root := range roots {
		if rootIDs[root.ID] || !root.valid() {
			return nil, 0, web.Fail(409, "Skill 绑定须使用不重复的 ID 和固定修订")
		}
		rootIDs[root.ID] = true
	}
	found := map[string]Ref{}
	active := map[string]bool{}
	heights := map[string]int{}
	var order []Ref
	total := 0
	var walk func(Ref, int) (int, error)
	walk = func(ref Ref, depth int) (int, error) {
		if depth > depthLimit {
			return 0, web.Fail(409, "Skill 依赖深度超过 6 层")
		}
		if ref.ID == publishing || active[ref.ID] {
			return 0, web.Fail(409, "Skill 依赖含循环")
		}
		if prev, exists := found[ref.ID]; exists {
			if prev != ref {
				return 0, web.Fail(409, "同一 Skill 的依赖修订冲突")
			}
			if depth+heights[ref.ID]-1 > depthLimit {
				return 0, web.Fail(409, "Skill 依赖深度超过 6 层")
			}
			return heights[ref.ID], nil
		}
		if len(found) >= bindingLimit {
			return 0, web.Fail(409, "直接和间接 Skill 总计最多 12 个")
		}
		found[ref.ID] = ref
		active[ref.ID] = true
		node, err := read(ref)
		if err != nil {
			return 0, err
		}
		total += node.Bytes
		if total > contextLimit {
			return 0, web.Fail(409, "Skill 与依赖装载的正文及参考文件合计最多 32 KiB")
		}
		height := 1
		for _, dependency := range node.Dependencies {
			child, err := walk(dependency, depth+1)
			if err != nil {
				return 0, err
			}
			height = max(height, child+1)
		}
		delete(active, ref.ID)
		heights[ref.ID] = height
		order = append(order, ref)
		return height, nil
	}
	depth := 1
	if publishing != "" {
		depth++
	}
	for _, root := range roots {
		if _, err := walk(root, depth); err != nil {
			return nil, 0, err
		}
	}
	return order, total, nil
}

func readDependency(ctx context.Context, tx pgx.Tx, team string, ref Ref) (dependencyNode, error) {
	var node dependencyNode
	var dependencies []byte
	err := tx.QueryRow(ctx, `select coalesce((select jsonb_agg(jsonb_build_object('id',d.needs_skill,'revision',d.needs_revision) order by d.position)
        from skill_dependencies d where (d.skill_id,d.revision)=(r.skill_id,r.revision)),'[]'::jsonb),
        coalesce((select sum(o.size_bytes) from skill_files f join objects o on o.hash=f.object_hash
        where (f.skill_id,f.revision)=(r.skill_id,r.revision) and f.in_context),0)
        from skill_revisions r join skill_packs p on p.id=r.skill_id
        where p.team_id=$1 and r.skill_id=$2 and r.revision=$3 and r.revoked_at is null for share of r`,
		team, ref.ID, ref.Revision).Scan(&dependencies, &node.Bytes)
	if errors.Is(err, pgx.ErrNoRows) {
		return node, web.Fail(409, "Skill 修订已停用、不可用或超出团队范围")
	}
	if err != nil {
		return node, err
	}
	return node, json.Unmarshal(dependencies, &node.Dependencies)
}
