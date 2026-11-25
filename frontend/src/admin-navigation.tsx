import { Activity, Boxes, ClipboardCheck, Settings, Waypoints } from "lucide-react";
import type { WorkspacePage } from "./workspace-routes";

const nav = [
  { id: "admin", name: "管理首页", Icon: Settings },
  { id: "monitor", name: "运行监控", Icon: Activity },
  { id: "gateway", name: "模型网关", Icon: Waypoints },
  { id: "integrations", name: "企业服务", Icon: Boxes },
  { id: "releases", name: "发布审批", Icon: ClipboardCheck },
] as const;

export function AdminNavigation({ active, open }: { active: WorkspacePage; open: (page: WorkspacePage) => void }) {
  return <nav className="admin-navigation" aria-label="企业管理导航">
    {nav.map(({ id, name, Icon }) => <button key={id} type="button" aria-current={active === id ? "page" : undefined} onClick={() => open(id)}>
      <Icon aria-hidden="true" />{name}
    </button>)}
  </nav>;
}
