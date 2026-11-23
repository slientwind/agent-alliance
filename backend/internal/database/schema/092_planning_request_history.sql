-- Speech turns and page reloads recover the owner's most recent file selection.
CREATE INDEX planning_request_history ON planning_requests(project_id, actor, created_at DESC, id DESC);
