import type { Agent } from "./client";

export function normalizeCapabilityText(text: string) {
  return text.normalize("NFKC").toLowerCase().trim().replace(/\s+/g, " ");
}

const commonTerms = new Set([
  "帮我",
  "一份",
  "一张",
  "需要",
  "一个",
  "请将",
  "希望",
  "可以",
  "我们",
  "生成",
  "制作",
  "完成",
  "准备",
  "处理",
  "使用",
  "助手",
  "智能体",
  "agent",
  "the",
  "for",
  "and",
  "please",
  "create",
  "make",
  "with",
  "from",
]);

function taskTerms(query: string) {
  if (typeof Intl.Segmenter === "function") {
    const segments = new Intl.Segmenter("zh", { granularity: "word" }).segment(query);
    return Array.from(segments).filter(s => s.isWordLike).map(s => s.segment);
  }
  // Older webviews lack word segmentation. Keep Latin identifiers intact and
  // compare adjacent Han characters instead of treating a sentence as one word.
  const runs = query.replace(/\p{Script=Han}+/gu, " $& ").match(/[\p{L}\p{N}]+/gu) || [];
  return runs.flatMap(run => {
    if (!/^\p{Script=Han}/u.test(run)) return [run];
    const chars = Array.from(run);
    return chars.slice(1).map((ch, i) => chars[i] + ch);
  });
}

export function capabilityMatcher(goal: string) {
  const query = normalizeCapabilityText(goal);
  const uniqueTerms = [...new Set(taskTerms(query).filter(term => term.length >= 2 && !commonTerms.has(term)))];
  return (name: string, description: string) => {
    const title = normalizeCapabilityText(name);
    const body = normalizeCapabilityText(description);
    const whole = title.length >= 2 && query.includes(title);
    const words = uniqueTerms.filter(
      (term) => title.includes(term) || body.includes(term),
    );
    const score =
      (whole ? 10 : 0) +
      words.reduce((sum, word) => sum + (title.includes(word) ? 3 : 1), 0);
    return { score, words };
  };
}
const formatTerms = new Set([
  "word", "docx", "excel", "xlsx", "powerpoint", "pptx", "pdf", "png", "csv",
  "文档", "文件", "图片", "文本", "输出", "导出", "交付",
]);

export function substantiveTerms(words: string[]) {
  return words.filter((word) => !formatTerms.has(word));
}

export function recommendedAgents(goal: string, agents: Agent[], teamIDs: string[]) {
  const query = normalizeCapabilityText(goal);
  if (query.length < 4 || !agents.length || !teamIDs.length) return [];
  const match = capabilityMatcher(query);
  return agents
    .flatMap((agent) => {
      const name = agent.publishedName;
      if (!agent.publishedReady || !agent.published || !name || !teamIDs.includes(agent.team))
        return [];
      const title = normalizeCapabilityText(name);
      const { score, words } = match(name, agent.publishedInstructions || "");
      const terms = substantiveTerms(words);
      const namesAgent = title.length >= 3 && query.includes(title);
      return namesAgent || terms.length >= 2 ? [{ agent, terms, score }] : [];
    })
    .sort((a, b) => b.score - a.score)
    .slice(0, 2);
}
