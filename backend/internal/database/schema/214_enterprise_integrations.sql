create table sandbox_pool (
  singleton boolean primary key default true check (singleton),
  max_jobs integer not null default 2 check (max_jobs between 0 and 32),
  timeout_seconds integer not null default 60 check (timeout_seconds between 5 and 60),
  revision integer not null default 1
);
insert into sandbox_pool default values;

create table sandbox_leases (
  id uuid primary key default gen_random_uuid(),
  expires_at timestamptz not null
);
create index sandbox_leases_expiration on sandbox_leases(expires_at);

create table im_bot_settings (
  provider text primary key check (provider in ('feishu','dingtalk','slack')),
  enabled boolean not null,
  fields jsonb not null,
  encrypted_credentials bytea,
  revision integer not null default 1
);

create table enterprise_knowledge_sources (
  id uuid primary key default gen_random_uuid(),
  provider text not null check (provider in ('feishu','dingtalk','confluence')),
  name text not null,
  settings jsonb not null,
  encrypted_credentials bytea,
  project_id uuid not null references projects(id) on delete cascade,
  configured_by uuid not null references members(id),
  source_id uuid references sources(id) on delete set null,
  published_version integer,
  content_hash text,
  enabled boolean not null,
  revision integer not null default 1,
  status text not null check (status in ('pending','syncing','ready','unavailable','disabled')),
  verdict text not null default '',
  fetched_at timestamptz,
  next_at timestamptz not null default now(),
  lease uuid,
  check ((status='syncing')=(lease is not null)),
  check ((published_version is null)=(content_hash is null))
);
create unique index enterprise_knowledge_published_source on enterprise_knowledge_sources(source_id) where source_id is not null;
create index enterprise_knowledge_schedule on enterprise_knowledge_sources(next_at) where enabled;
