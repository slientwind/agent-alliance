export type OnlineMember = { member: string; name: string };

export function PresenceStack({ members }: { members: OnlineMember[] }) {
  if (members.length === 0) return null;
  const visible = members.slice(0, 5);
  const more = members.length - visible.length;
  return <section className="presence" aria-label="在线成员">
    <ul className="presence-stack" aria-label={`${members.length} 人在线`}>
      {visible.map(member => <li key={member.member} title={member.name} aria-label={member.name}>
        <span aria-hidden="true">{Array.from(member.name.trim())[0] || "·"}</span>
      </li>)}
      {more > 0 && <li title={members.slice(visible.length).map(member => member.name).join("、")} aria-label={`另有 ${more} 人在线`}>
        <span aria-hidden="true">+{more}</span>
      </li>}
    </ul>
  </section>;
}
