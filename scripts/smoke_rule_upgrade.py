"""Verify fixed rule references and read access across an upgrade from schema 039."""

from pathlib import Path
import subprocess
import uuid


def verify_rule_upgrade(pg):
    command = [*pg[:-1], "-i", pg[-1]]
    target = "rule_upgrade_" + uuid.uuid4().hex
    path = Path(__file__).parent / "schema"
    if not path.is_dir():
        path = Path("backend/internal/database/schema")
    older = "\n".join(
        p.read_text() for p in sorted(path.glob("*.sql")) if p.name[:3] <= "039"
    )
    setup = """
    DO $$
    DECLARE t uuid; u uuid; outsider uuid; task uuid; rule_key uuid;
    BEGIN
      INSERT INTO teams(name) VALUES('Fixture') RETURNING id INTO t;
      INSERT INTO members(display_name,email,password_hash,role) VALUES('Owner','owner@example.invalid','','member') RETURNING id INTO u;
      INSERT INTO members(display_name,email,password_hash,role) VALUES('Observer','observer@example.invalid','','admin') RETURNING id INTO outsider;
      INSERT INTO team_members(team_id,member_id) VALUES(t,u);
      INSERT INTO projects(owner,team_id,final_reviewer,title) VALUES(u,t,u,'Sample') RETURNING id INTO task;
      INSERT INTO plans(project_id,version,brief) VALUES(task,1,'Sample');
      INSERT INTO team_rules(team_id,author) VALUES(t,u) RETURNING id INTO rule_key;
      INSERT INTO rule_versions(rule_id,revision,body,author,created_at)
      VALUES(rule_key,1,'old',u,now()-interval '100 days'),(rule_key,2,'published',u,now()-interval '92 days'),(rule_key,3,'draft',u,now());
      UPDATE team_rules SET published_revision=2 WHERE id=rule_key;
      INSERT INTO plan_rules VALUES(task,1,rule_key,1);
    END $$;
    """
    probe = """
    SELECT concat_ws('|',rule_usable(r.id,1,r.author),rule_usable(r.id,2,r.author),rule_usable(r.id,3,r.author),
         rule_usable(r.id,2,(select id from members where display_name='Observer')),
         v.review_at<now(),v.expires_at is null,
         planning_rule_pins(p.id,r.author)=jsonb_build_array(jsonb_build_object('id',r.id,'rev',2)))
    FROM team_rules r JOIN rule_versions v ON(v.rule_id,v.revision)=(r.id,2) JOIN projects p ON p.team_id=r.team_id;
    """
    later = "\n".join(p.read_text() for p in sorted(path.glob("*.sql")) if "041" < p.name[:3] < "052")
    orphan = """
      INSERT INTO team_rules(team_id,author)
      SELECT t.id,m.id FROM teams t,members m WHERE t.name='Fixture' AND m.display_name='Observer';
    """
    maintenance = """
      SELECT concat_ws('|',bool_and(r.maintainer=r.author),count(*) filter(where r.maintenance_vacant),
        count(*) filter(where not r.maintenance_vacant and rule_usable(r.id,1,r.maintainer)))
      FROM team_rules r;
    """
    subprocess.run(command + ["createdb", "-U", "alliance", target], check=True)
    try:
        output = subprocess.run(
            command
            + [
                "psql",
                "-XAtq",
                "-v",
                "ON_ERROR_STOP=1",
                "-U",
                "alliance",
                "-d",
                target,
            ],
            input=older
            + setup
            + (path / "040_rule_validity.sql").read_text()
            + (path / "041_planning_rules.sql").read_text()
            + probe + later + orphan + (path / "052_rule_maintenance.sql").read_text() + maintenance,
            capture_output=True,
            text=True,
        )
        if output.returncode:
            raise RuntimeError(output.stderr[-1200:])
        assert output.stdout.strip().splitlines()[-2:] == ["t|t|f|f|t|t|t", "t|1|1"], output.stdout[
            -150:
        ]
        return dict(
            fromSchema=39,
            maintenanceSchema=52,
            legacyCreatorBackfilled=True,
            departedResponsibilityVacant=True,
            oldPinReadable=True,
            publishedDueButUsable=True,
            draftExcluded=True,
            nonmemberAdminDenied=True,
        )
    finally:
        subprocess.run(command + ["dropdb", "-U", "alliance", target], check=True)


if __name__ == "__main__":
    import json

    print(
        json.dumps(
            verify_rule_upgrade(["docker", "exec", "alliance-workspace-database-1"])
        )
    )
