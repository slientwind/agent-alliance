package projects

import "alliance/internal/web"

type executionNode struct {
	agent        string
	revision     *int
	phase        string
	dependencies []int
}

// Business steps reference earlier business steps. A dependency on a lookup
// points at its answer, even if another branch runs alongside the search.
func executionNodes(in PlanInput) ([]executionNode, error) {
	count := len(in.Agents)
	if in.AgentRevisions != nil && len(in.AgentRevisions) != count {
		return nil, web.Fail(422, "每个步骤都需要一个 Agent 版本")
	}
	if in.Dependencies != nil && len(in.Dependencies) != count {
		return nil, web.Fail(422, "每个节点必须有依赖列表")
	}
	lookup := map[int]bool{}
	if in.SourceLookup {
		if count != 1 || len(in.LookupSteps) > 0 {
			return nil, web.Fail(422, "请在组合方案中指定要检索的业务步骤")
		}
		in.LookupSteps = []int{1}
	}
	for _, ordinal := range in.LookupSteps {
		if ordinal < 1 || ordinal > count || lookup[ordinal] || len(in.Sources) == 0 || len(in.AgentRevisions) != count || in.AgentRevisions[ordinal-1] < 1 {
			return nil, web.Fail(422, "原文核对需要有效的步骤、固定的 Agent 版本及可读资料")
		}
		lookup[ordinal] = true
	}
	calculate := map[int]bool{}
	for _, ordinal := range in.CalculationSteps {
		if !lookup[ordinal] || calculate[ordinal] {
			return nil, web.Fail(422, "金额计算需要绑定已选择的原文核对步骤")
		}
		calculate[ordinal] = true
	}
	followUp := map[int]bool{}
	for _, ordinal := range in.FollowUpSteps {
		if !lookup[ordinal] || calculate[ordinal] || followUp[ordinal] {
			return nil, web.Fail(422, "依据新增原文继续作答需要原文问答步骤")
		}
		followUp[ordinal] = true
	}
	reviewEvidence := map[int]bool{}
	for _, ordinal := range in.EvidenceReviewSteps {
		if !followUp[ordinal] || reviewEvidence[ordinal] {
			return nil, web.Fail(422, "再次核对需要绑定已选择新增原文续答的步骤")
		}
		reviewEvidence[ordinal] = true
	}
	expanded := count + len(lookup) + len(followUp) + len(reviewEvidence)
	if expanded > 12 {
		return nil, web.Fail(422, "方案展开后最多 12 次能力调用")
	}
	result := make([]executionNode, 0, expanded)
	last := make([]int, count+1)
	for i, id := range in.Agents {
		node := executionNode{agent: id}
		if in.AgentRevisions != nil {
			node.revision = &in.AgentRevisions[i]
		}
		if in.Dependencies == nil {
			if i > 0 {
				node.dependencies = []int{last[i]}
			}
		} else {
			seen := map[int]bool{}
			for _, parent := range in.Dependencies[i] {
				if parent < 1 || parent > i || seen[parent] {
					return nil, web.Fail(422, "依赖必须是唯一且在当前步骤之前的步骤")
				}
				seen[parent] = true
				node.dependencies = append(node.dependencies, last[parent])
			}
		}
		if lookup[i+1] {
			node.phase = "search"
			result = append(result, node)
			node.phase = "answer"
			if calculate[i+1] {
				node.phase = "answer_calc"
			}
			node.dependencies = []int{len(result)}
		}
		result = append(result, node)
		if reviewEvidence[i+1] {
			node.phase = "review_evidence"
			node.dependencies = []int{len(result)}
			result = append(result, node)
		}
		if followUp[i+1] {
			node.phase = "synthesize"
			node.dependencies = []int{len(result)}
			result = append(result, node)
		}
		last[i+1] = len(result)
	}
	return result, nil
}
