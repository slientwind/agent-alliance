"""Verify member history and account revisions on an isolated pre-upgrade database."""
import json
from pathlib import Path
import subprocess
from uuid import uuid4


def verify_member_upgrade():
    root = Path(__file__).resolve().parents[1]
    here = Path(__file__).resolve().parent
    schema = here / "schema"
    identity = here / "identity"
    if not schema.is_dir():
        schema = root / "backend/internal/database/schema"
        identity = root / "backend/internal/identity"
    db = "member_upgrade_" + uuid4().hex
    pg = ["docker", "exec", "-i", "alliance-workspace-database-1"]
    admin, member, other = map(str, (uuid4(), uuid4(), uuid4()))

    def sql(statement):
        return subprocess.run(pg + ["psql", "-XAtq", "-v", "ON_ERROR_STOP=1", "-U",
                                    "alliance", "-d", db], input=statement, check=True,
                              text=True, capture_output=True).stdout.strip()

    def list_members():
        return json.loads(sql("PREPARE directory(uuid) AS " + directory + "; EXECUTE directory('" + admin + "');"))

    def history(cursor):
        result = sql("PREPARE history(uuid,uuid,bigint) AS " + query +
                     f"; EXECUTE history('{member}','{admin}',{cursor});")
        ident, rows = result.split("|", 1)
        assert ident == member
        return json.loads(rows)

    directory = (identity / "member_directory.sql").read_text()
    query = (identity / "member_history.sql").read_text()
    subprocess.run(pg + ["createdb", "-U", "alliance", db], check=True)
    try:
        sql("\n".join(p.read_text() for p in sorted(schema.glob("*.sql")) if p.name[:3] <= "069"))
        sql(f"""insert into members(id,email,display_name,password_hash,role) values
             ('{admin}','a@sample.invalid','Manager','!','admin'),('{member}','b@sample.invalid','Member','!','member'),
             ('{other}','c@sample.invalid','Other','!','member');
             insert into audit_events(actor,action,subject,detail)
             select '{admin}','member.change','{member}', jsonb_build_object('reason','Update '||i,'internal_probe','exclude-this')
             from generate_series(1,26) i;
             insert into audit_events(actor,action,subject,detail)
             values ('{admin}','model.change','{member}','{{"reason":"hidden model action"}}'),
                    ('{admin}','member.change','{other}','{{"reason":"hidden other member"}}');""")
        for prefix in ("070","071"):
            sql(next(schema.glob(prefix + "_*.sql")).read_text())
        original = next(r for r in list_members() if r["id"] == member)
        assert original["registration"] is None and original["accountRevision"] == "1"
        assert sql("select count(*) from account_notices") == "0"
        first = history("null")[:20]
        assert len(first) == 20
        sql(f"""update members set role='admin' where id='{member}';
             insert into audit_events(actor,action,subject,detail) values
              ('{admin}','member.change','{member}','{{"oldRole":"member","role":"admin","enabled":true,"oldEnabled":true}}');""")
        assert sql(f"select count(*) from account_notices where member_id='{member}'") == "1"
        second = history(str(int(first[-1]["id"])))
        assert len(second) == 6
        events = first + second
        assert len({e["id"] for e in events}) == 26
        assert [e["reason"] for e in events] == [f"Update {i}" for i in range(26, 0, -1)]
        assert all(set(e) == {"id", "at", "action", "actor", "reason"} for e in events)
        assert next(r for r in list_members() if r["id"] == member)["accountRevision"] == "2"
        sql(f"update members set role='admin' where id='{member}'")
        assert next(r for r in list_members() if r["id"] == member)["accountRevision"] == "2"
        sql(f"update members set role='member' where id='{admin}'")
        assert not sql("PREPARE directory(uuid) AS " + directory + f"; EXECUTE directory('{admin}');")
        assert not sql("PREPARE history(uuid,uuid,bigint) AS " + query + f"; EXECUTE history('{member}','{admin}',null);")
        return {"schemaFrom":69,"schemaTo":71,"oldRegistration":None,"preUpgradeNotifications":0,
                "oldPages":[20,6],"newEventWhilePaging":True,"unrelatedFieldsReturned":False,
                "staleAdminDirectoryVisible":False,"changedAccountRevision":"2","unchangedAccountRevision":"2"}
    finally:
        subprocess.run(pg + ["dropdb", "-U", "alliance", db], check=True)


if __name__ == "__main__":
    print(json.dumps(verify_member_upgrade(), indent=2))
