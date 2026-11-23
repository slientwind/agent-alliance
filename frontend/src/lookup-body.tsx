import type { Artifact, Project } from "./client";
import type { Source } from "./knowledge";

export function LookupBody({
  artifact,
  project,
  sources,
}: {
  artifact: Artifact;
  project: Project;
  sources: Source[];
}) {
  return (
    <div className="preserve">
      {artifact.body.split("\n").map((line, i) => {
        const mark =
          /^(.*?)【S([1-9]\d*)v([1-9]\d*):([1-9]\d*)-([1-9]\d*)】(.+)$/.exec(
            line,
          );
        if (!mark)
          return (
            <span key={i}>
              {line}
              {"\n"}
            </span>
          );
        const [, prefix, number, version, start, end, text] = mark;
        const pin = project.plan?.sources.filter(s=>!s.receiptStep || s.receiptStep===artifact.ordinal)[Number(number) - 1];
        const source =
          pin &&
          !artifact.sourceChanges.some(
            (change) => change.source === pin.id && change.revoked,
          ) &&
          sources.find((item) => item.id === pin.id && !item.revoked);
        return (
          <span key={i}>
            {prefix}
            {text}{" "}
            {source && (
              <a
                href={`/api/projects/${project.id}/sources/${source.id}/content?version=${version}`}
                aria-label={`查看${source.name} v${version} 原件，第 ${start} 至 ${end} 字`}
              >
                第 {start}–{end} 字 · 查看原件
              </a>
            )}
            {"\n"}
          </span>
        );
      })}
    </div>
  );
}
