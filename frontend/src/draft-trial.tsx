import { useRef, useState } from "react";
import { AgentChecks } from "./agent-checks";
import { SlideOutlineFields } from "./slide-outline";
import { TrialEvidence } from "./trial-evidence";
import { authorizeTrial } from "./trial-request";
import { PictureTrialField } from "./picture-trial-field";

export function DraftTrial({
  draft,
  busy,
  act,
  refresh,
}: {
  draft: {
    id: string;
    revision: number;
    operation: string;
    payer: string;
    estimate?: string | null;
  };
  busy: boolean;
  act: (action: () => Promise<void>) => Promise<void>;
  refresh: () => Promise<void>;
}) {
  const [input, setInput] = useState("");
  const [expected, setExpected] = useState("");
  const [consent, setConsent] = useState(false);
  const [result, setResult] = useState("");
  const [version, setVersion] = useState(0);
  const entry = useRef<HTMLTextAreaElement>(null);
  return (
    <>
      <form
        className="draft-trial"
        onSubmit={(e) => {
          e.preventDefault();
          const fields = new FormData(e.currentTarget);
          void act(async () => {
            setConsent(false);
            setResult(
              await authorizeTrial(
                draft.id,
                draft.revision,
                String(fields.get("input")),
                expected,
                undefined,
                undefined,
                fields.get("picture") as File | null,
              ),
            );
            setVersion((v) => v + 1);
            await refresh();
          });
        }}
      >
        <fieldset disabled={busy}>
          {draft.operation === "vision" && <PictureTrialField onChange={() => setConsent(false)} />}
          {draft.operation === "pptx" ? (
            <SlideOutlineFields
              name="input"
              initial={[
                { title: "领取设备", body: "经部门负责人确认后领取。" },
                { title: "归还设备", body: "由保管员核对编号并登记。" },
              ]}
            />
          ) : (
            <label>
              试运行输入
              <textarea
                required
                ref={entry}
                name="input"
                value={input}
                placeholder={
                  draft.operation === "aggregate"
                    ? "粘贴含表头和示例行的 CSV"
                    : undefined
                }
                onChange={(e) => {
                  setInput(e.target.value);
                  setConsent(false);
                }}
              />
            </label>
          )}
          <label>
            结果应包含
            <input
              required
              name="expected"
              value={expected}
              onChange={(e) => {
                setExpected(e.target.value);
                setConsent(false);
              }}
            />
          </label>
          <label>
            <input
              type="checkbox"
              required
              checked={consent}
              onChange={(e) => setConsent(e.target.checked)}
            />
            授权本次试运行，{draft.payer}承担费用。
            {draft.estimate == null
              ? "金额未知，无法预先封顶。"
              : "预计 ¥" + draft.estimate + "。"}
          </label>
          <button>运行示例</button>
          {result && (
            <p role="status" className="preserve">
              {result}
            </p>
          )}
        </fieldset>
      </form>
      <TrialEvidence
        agent={draft.id}
        refresh={refresh}
        version={version}
        onReuse={
          draft.operation === "text"
            ? (sample) => {
                setInput(sample.input);
                setExpected(sample.expected);
                setConsent(false);
                setResult("");
                entry.current?.focus();
              }
            : undefined
        }
      />
      {(draft.operation === "text" || draft.operation === "vision") && (
        <AgentChecks
          agent={draft.id}
          revision={draft.revision}
          refresh={refresh}
          pictureInput={draft.operation === "vision"}
        />
      )}
    </>
  );
}
