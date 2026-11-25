import { useState, type KeyboardEvent } from "react";
import { BookOpenText, Database, FileText } from "lucide-react";
import type { SpaceOption } from "./eval-preflight";
import { KnowledgeLibrary } from "./knowledge-library";
import { BusinessWorkspace } from "./business-workspace";
import { KnowledgeStandards } from "./knowledge-standards";
import "./knowledge-workbench.css";

const tabs = [
  { key: "library", title: "知识库与雷达", Icon: BookOpenText },
  { key: "business", title: "业务系统与数据视图", Icon: Database },
  { key: "standards", title: "术语口径与交付母版", Icon: FileText },
] as const;
type Tab = typeof tabs[number]["key"];

export type KnowledgeScope = { space: SpaceOption; commons?: SpaceOption };

export function KnowledgeWorkbench({ space, commons }: { space?: SpaceOption; commons?: SpaceOption }) {
  const [tab, select] = useState<Tab>("library");
  function navigate(event: KeyboardEvent<HTMLButtonElement>, index: number) {
    let next: number;
    switch (event.key) {
      case "ArrowLeft": next = (index + tabs.length - 1) % tabs.length; break;
      case "ArrowRight": next = (index + 1) % tabs.length; break;
      case "Home": next = 0; break;
      case "End": next = tabs.length - 1; break;
      default: return;
    }
    event.preventDefault();
    select(tabs[next].key);
    document.getElementById("knowledge-tab-" + tabs[next].key)?.focus();
  }
  if (!space) return <p>请在侧栏选择个人空间或团队空间。</p>;
  const props = { space, commons };
  return <div className="knowledge-workbench">
    <div className="knowledge-scope"><strong>{space.kind === "personal" ? "个人空间" : space.name}</strong>
      {commons && commons.id !== space.id && <span>已继承全企公共底座</span>}</div>
    <div className="knowledge-tabs" role="tablist" aria-label="知识与数据">
      {tabs.map(({ key, title, Icon }, index) => <button key={key} id={"knowledge-tab-" + key} role="tab" type="button"
        aria-selected={tab === key} tabIndex={tab === key ? 0 : -1} aria-controls={"knowledge-panel-" + key}
        onClick={() => select(key)} onKeyDown={event => navigate(event, index)}><Icon size={17} aria-hidden="true" />{title}</button>)}
    </div>
    <div role="tabpanel" id={"knowledge-panel-" + tab} aria-labelledby={"knowledge-tab-" + tab}>
      {tab === "library" && <KnowledgeLibrary key={space.id} {...props} />}
      {tab === "business" && <BusinessWorkspace key={space.id} {...props} />}
      {tab === "standards" && <KnowledgeStandards key={space.id} {...props} />}
    </div>
  </div>;
}
