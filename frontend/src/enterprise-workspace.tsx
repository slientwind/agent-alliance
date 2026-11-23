import { useState } from "react";
import { AccountManagement } from "./account";
import { BudgetPanel } from "./budget";
import type { Member, Model } from "./client";
import { Models } from "./library";
import { ManageScopes } from "./management-scopes";
import { Organization } from "./organization";
import { ReviewChannelHealth } from "./review-reminders";
import { TelemetryHealth, TracePolicies } from "./trace-policy";

const sections = [
  { id: "resources", label: "模型与 Agent" },
  { id: "activity", label: "用户与监控" },
  { id: "advanced", label: "高级设置" },
] as const;
type Section = (typeof sections)[number]["id"];

export function EnterpriseWorkspace({
  user,
  members,
  models,
  configure,
  refreshCatalog,
  refreshMembers,
}: {
  user: Member;
  members: Member[];
  models: Model[];
  configure: () => void;
  refreshCatalog: () => Promise<void>;
  refreshMembers: () => Promise<void>;
}) {
  const [active, setActive] = useState<Section>("resources");
  const [opened, setOpened] = useState<Section[]>(["resources"]);
  function open(section: Section) {
    setActive(section);
    setOpened((previous) => previous.includes(section) ? previous : [...previous, section]);
  }
  return (
    <>
      <nav className="enterprise-sections" aria-label="企业管理分类">
        {sections.map(({ id, label }) => (
          <button
            key={id}
            type="button"
            aria-pressed={active === id}
            onClick={() => open(id)}
          >
            {label}
          </button>
        ))}
      </nav>
      <div hidden={active !== "resources"}>
        <button onClick={configure}>团队首次配置</button>
        <Models models={models} refresh={refreshCatalog} />
      </div>
      {opened.includes("activity") && (
        <div hidden={active !== "activity"}>
          <AccountManagement members={members} reload={refreshMembers} />
          <TelemetryHealth />
          <ReviewChannelHealth />
        </div>
      )}
      {opened.includes("advanced") && (
        <div hidden={active !== "advanced"}>
          <Organization members={members} />
          <ManageScopes members={members} />
          <BudgetPanel user={user} members={members} />
          <TracePolicies members={members} />
        </div>
      )}
    </>
  );
}
