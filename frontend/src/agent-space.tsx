import { lazy, Suspense, type ReactNode } from "react";
import { Link, useSearchParams } from "react-router-dom";
import { workspacePages } from "./workspace-routes";

const EcosystemMarket = lazy(() => import("./ecosystem-market").then(({ EcosystemMarket }) => ({ default: EcosystemMarket })));

export function AgentSpace({ directory }: { directory: ReactNode }) {
  const [params] = useSearchParams();
  const discovery = params.get("view") === "ecosystem";
  const base = workspacePages.library.path;
  return <>
    <nav className="row agent-space-navigation" aria-label="Agent 广场导航">
      <Link to={base} aria-current={!discovery ? "page" : undefined}>企业能力目录</Link>
      <Link to={`${base}?view=ecosystem`} aria-current={discovery ? "page" : undefined}>生态发现市场</Link>
      <Link to={workspacePages.studio.path}>构建助手</Link>
      <Link to={workspacePages.externalConnect.path}>接入外部 Agent</Link>
    </nav>
    <Suspense fallback={<p role="status">正在加载能力目录…</p>}>
      {discovery ? <EcosystemMarket /> : directory}
    </Suspense>
  </>;
}
