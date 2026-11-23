CREATE TABLE departments(id uuid PRIMARY KEY DEFAULT gen_random_uuid(),name text UNIQUE NOT NULL);
ALTER TABLE members ADD COLUMN department_id uuid REFERENCES departments;
ALTER TABLE teams ADD COLUMN budget_owner uuid REFERENCES members;
CREATE TABLE team_rules(id uuid PRIMARY KEY DEFAULT gen_random_uuid(),team_id uuid REFERENCES teams NOT NULL,author uuid REFERENCES members NOT NULL, published_revision integer,disabled boolean NOT NULL DEFAULT false);
CREATE TABLE rule_versions(rule_id uuid REFERENCES team_rules,revision integer NOT NULL,body text NOT NULL,author uuid REFERENCES members NOT NULL,created_at timestamptz NOT NULL DEFAULT now(),PRIMARY KEY(rule_id,revision));
ALTER TABLE team_rules ADD FOREIGN KEY(id,published_revision) REFERENCES rule_versions;
CREATE TABLE plan_rules(project_id uuid NOT NULL,plan_version integer NOT NULL,rule_id uuid NOT NULL,rule_revision integer NOT NULL,
 PRIMARY KEY(project_id,plan_version,rule_id),FOREIGN KEY(project_id,plan_version) REFERENCES plans,FOREIGN KEY(rule_id,rule_revision) REFERENCES rule_versions);
