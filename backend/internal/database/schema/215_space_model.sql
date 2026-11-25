CREATE TABLE spaces (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  kind text NOT NULL CHECK (kind IN ('personal','team','commons')),
  owner_member_id uuid REFERENCES members(id),
  team_id uuid REFERENCES teams(id),
  slug text NOT NULL UNIQUE CHECK (slug ~ '^[a-z0-9][a-z0-9-]{0,127}$'),
  name text NOT NULL CHECK (char_length(btrim(name)) BETWEEN 1 AND 120),
  description text NOT NULL DEFAULT '' CHECK (char_length(description) <= 1000),
  custom_instructions text NOT NULL DEFAULT '' CHECK (char_length(custom_instructions) <= 8000),
  default_template_id uuid,
  archived_at timestamptz,
  revision integer NOT NULL DEFAULT 1 CHECK (revision > 0),
  UNIQUE(id,kind),
  CHECK (
    (kind='personal' AND owner_member_id IS NOT NULL AND team_id IS NULL) OR
    (kind='team' AND owner_member_id IS NULL AND team_id IS NOT NULL) OR
    (kind='commons' AND owner_member_id IS NULL AND team_id IS NULL)
  )
);
CREATE UNIQUE INDEX spaces_one_personal ON spaces(owner_member_id) WHERE kind='personal';
CREATE UNIQUE INDEX spaces_one_default_per_team ON spaces(team_id) WHERE kind='team';
CREATE UNIQUE INDEX spaces_one_commons ON spaces(kind) WHERE kind='commons';

CREATE TABLE space_members (
  space_id uuid NOT NULL,
  member_id uuid NOT NULL REFERENCES members(id),
  role text NOT NULL CHECK (role IN ('space_admin','member','guest')),
  granted_by uuid REFERENCES members(id),
  created_at timestamptz NOT NULL DEFAULT now(),
  space_kind text GENERATED ALWAYS AS ('team'::text) STORED,
  PRIMARY KEY(space_id,member_id),
  FOREIGN KEY(space_id,space_kind) REFERENCES spaces(id,kind) ON DELETE CASCADE
);
CREATE INDEX space_members_by_member ON space_members(member_id,space_id);

-- The enterprise role applies to Company Commons. Each team grants its own
-- roles and a personal space uses the current owner's identity exclusively.
CREATE FUNCTION space_role(target uuid, actor uuid) RETURNS text
LANGUAGE sql STABLE STRICT AS $$
  SELECT CASE s.kind
    WHEN 'personal' THEN CASE WHEN s.owner_member_id=$2 THEN 'space_admin' END
    WHEN 'team' THEN sm.role
    WHEN 'commons' THEN CASE WHEN m.role='admin' THEN 'space_admin' ELSE 'member' END
  END
  FROM spaces s
  JOIN members m ON m.id=$2 AND m.enabled AND m.departed_at IS NULL AND m.kind='person'
  LEFT JOIN space_members sm ON sm.space_id=s.id AND sm.member_id=m.id
  WHERE s.id=$1 AND s.archived_at IS NULL
$$;
