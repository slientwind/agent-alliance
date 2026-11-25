const uuid = "[a-f\\d]{8}(?:-[a-f\\d]{4}){3}-[a-f\\d]{12}";
const anchors = new RegExp(
  `^(?:(?:review-${uuid}|deliver)-[1-9]\\d{0,9}|feedback-${uuid}-[1-9]\\d{0,9}-[1-9]\\d{0,9}|mention-${uuid}|delivery-(?:distribution|coverage))$`,
  "i",
);
const projectID = new RegExp(`^${uuid}$`, "i");

export function reviewAnchor(value: string | null) {
  return value && anchors.test(value) ? value : "";
}

export function mutationAnchor(hash: string) {
  return RegExp(`^#mutation-${uuid}$`, "i").test(hash) ? hash.slice(1).toLowerCase() : "";
}

export function loginReturnPath(location: Pick<Location, "search" | "hash"> & Partial<Pick<Location, "pathname">>) {
  const params = new URLSearchParams(location.search);
  const service = params.get("service");
  if (service && projectID.test(service)) return "/?service=" + service;
  const route = location.pathname?.match(/^\/chat\/([^/]+)\/?$/)?.[1];
  const project = route ?? location.hash.slice(1);
  if (!projectID.test(project)) {
    if (location.pathname && /^\/(?:inbox|knowledge|agents(?:\/connect)?|automations|admin(?:\/(?:monitor|models|integrations|releases|members|governance))?)$/.test(location.pathname)) return location.pathname;
    return "/";
  }
  const task = reviewAnchor(params.get("task"));
  if (route) {
    const mutation = mutationAnchor(location.hash);
    return "/chat/" + project + (task ? "?task=" + encodeURIComponent(task) : "") + (mutation ? "#" + mutation : "");
  }
  return (task ? "/?task=" + encodeURIComponent(task) : "/") + location.hash;
}
