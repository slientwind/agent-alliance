const uuid = "[a-f\\d]{8}(?:-[a-f\\d]{4}){3}-[a-f\\d]{12}";
const anchors = new RegExp(
  `^(?:(?:review-${uuid}|deliver)-[1-9]\\d{0,9}|feedback-${uuid}-[1-9]\\d{0,9}-[1-9]\\d{0,9}|delivery-(?:distribution|coverage))$`,
  "i",
);
const projectID = new RegExp(`^${uuid}$`, "i");

export function reviewAnchor(value: string | null) {
  return value && anchors.test(value) ? value : "";
}

export function loginReturnPath(location: Pick<Location, "search" | "hash">) {
  const params = new URLSearchParams(location.search);
  const service = params.get("service");
  if (service && projectID.test(service)) return "/?service=" + service;
  const project = location.hash.slice(1);
  if (!projectID.test(project)) return "/";
  const task = reviewAnchor(params.get("task"));
  return (task ? "/?task=" + encodeURIComponent(task) : "/") + location.hash;
}
