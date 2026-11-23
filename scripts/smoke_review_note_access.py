"""Exercise review-note reads on historical runs with different input grants."""

from uuid import uuid4


def verify(sql, api, project, owner, team, other_input, own_input, password):
    member, shared, only_visible = (str(uuid4()) for _ in range(3))
    entries = []
    sql(
        f"""
        insert into members(id,email,display_name,password_hash,role)
        values('{member}','notes@sample.invalid','资料维护者',crypt('{password}',gen_salt('bf')),'admin');
        insert into project_memberships(project_id,member_id) values('{project}','{member}');
        update sources set created_by='{member}' where id='{own_input}';
        update projects set stage='review' where id='{project}';
        insert into project_feedback_submissions(id,project_id,author,request_key,fingerprint,original)
        values('{shared}','{project}','{owner}','joint','','共同意见 MIX-801'),
              ('{only_visible}','{project}','{owner}','own','','独立意见 OWN-132');
    """
    )
    for plan, inputs in [(2, []), (3, [own_input]), (4, [own_input, other_input])]:
        run, artifact = (str(uuid4()) for _ in range(2))
        entries.append(artifact)
        sql(
            f"insert into plans(project_id,version,brief) values('{project}',{plan},'归档任务');"
        )
        for source in inputs:
            sql(f"insert into plan_sources values('{project}',{plan},'{source}',1);")
        sql(
            f"""
            insert into runs(id,project_id,plan_version,actor,status,charge_team,request_key,request_hash,max_calls)
            values('{run}','{project}',{plan},'{owner}','completed','{team}','plan{plan}','',1);
            insert into artifacts(id,project_id,run_id,ordinal,name)
            values('{artifact}','{project}','{run}',{plan},'步骤 {plan}');
            insert into artifact_versions values('{artifact}',1,'版本内容','修改依据 {plan}',now());
        """
        )
        for submission, item in (
            [(shared, 1), (shared, 2), (only_visible, 1)]
            if plan == 3
            else [(shared, 1)]
        ):
            sql(
                f"""
                with item as (
                  insert into feedback(artifact_id,base_version,author,reviewer,kind,position,body,state,submission_id,submission_item)
                  values('{artifact}',1,'{owner}','{owner}','suggestion','位置 {plan}','引文 {submission} {item}','suggested','{submission}',{item})
                  returning id)
                insert into feedback_events(feedback_id,revision,actor,action,reason,artifact_version)
                select id,1,'{owner}','open','处理依据 {plan}',1 from item;
            """
            )

    conflict, applied = (str(uuid4()) for _ in range(2))
    sql(
        f"""
        insert into runs(id,project_id,plan_version,actor,status,charge_team,request_key,request_hash,max_calls)
        values('{conflict}','{project}',2,'{owner}','completed','{team}','retained-conflict','',1);
        insert into invocations(run_id,ordinal,status,result)
        values('{conflict}',2,'completed','内部冲突候选');
        insert into change_batches(run_id,feedback_id,feedback_revision,artifact_id,base_version,replacement,disposition)
        select '{conflict}',f.id,1,f.artifact_id,1,'','conflict' from feedback f
        where f.submission_id='{shared}' and f.submission_item=1 and f.artifact_id='{entries[0]}';
        insert into invocation_files(run_id,ordinal,object_hash,filename,media_type)
        select '{conflict}',2,object_hash,'candidate.txt','text/plain' from source_versions where source_id='{own_input}' and version=1;
        insert into runs(id,project_id,plan_version,actor,status,charge_team,request_key,request_hash,max_calls)
        values('{applied}','{project}',2,'{owner}','completed','{team}','prior-revision','',1);
        insert into artifact_versions(artifact_id,version,body,reason) values('{entries[0]}',2,'已核准正文','内部修订说明');
        update artifacts set current_version=2 where id='{entries[0]}';
        insert into change_batches(run_id,feedback_id,feedback_revision,artifact_id,base_version,replacement,disposition,applied_version)
        select '{applied}',f.id,1,f.artifact_id,1,'','applied',2 from feedback f
        where f.submission_id='{shared}' and f.submission_item=1 and f.artifact_id='{entries[0]}';
        insert into trace_grants(run_id,member_id,scope,expires_at)
        select '{conflict}','{member}',scope,now()+interval '1 hour' from unnest(array['content','structure','export']) scope;
    """
    )
    path = "projects/" + project
    file = path + "/change-runs/" + conflict + "/file"
    trace = path + "/runs/" + conflict + "/trace"

    def login(email):
        api("login", {"email": email + "@sample.invalid", "password": password})

    def grant_review(value):
        login("old")
        api(
            path + "/participants",
            dict(member=member, review=value, expected=api(path)["revision"]),
            204,
        )
        login("notes")

    def check(reviewer):
        detail = api(path)
        artifacts = {a["id"]: a for a in detail["artifacts"]}
        for plan, artifact in enumerate(entries, 2):
            a = artifacts[artifact]
            allowed = reviewer or plan != 4
            assert a["reviewNotesReadable"] is allowed
            for item in a["feedback"]:
                same_assignment = (
                    item["submission"] == shared and item["submissionItem"] == 1
                )
                text_allowed = allowed and (reviewer or not same_assignment)
                expected = f"引文 {item['submission']} {item['submissionItem']}"
                assert item["body"] == (expected if text_allowed else None)
                assert item["position"] == (f"位置 {plan}" if text_allowed else None)
                assert item["events"][0]["reason"] == (
                    f"处理依据 {plan}" if text_allowed else None
                )
                assert item["state"] == "suggested"
        for suffix in ["", "?preview=1"]:
            if reviewer:
                assert api(file + suffix) == b"historical dataset 074"
            else:
                api(file + suffix, status=404)
        revision = api(path + "/artifacts/" + entries[0] + "/versions?version=2")[0]
        assert revision["body"] == "已核准正文"
        assert revision["reason"] == ("内部修订说明" if reviewer else None)
        trace_view = api(trace)
        assert trace_view["rights"]["structure"] and trace_view["rights"]["export"]
        assert trace_view["rights"]["content"] is reviewer
        if not reviewer:
            api(trace + "?mode=content&step=2", status=403)
            api(trace + "?mode=export&step=2", status=403)
        originals = detail["feedbackOriginals"]
        assert originals[only_visible] == "独立意见 OWN-132"
        assert originals.get(shared) == ("共同意见 MIX-801" if reviewer else None)
        if not reviewer:
            assert "MIX-801" not in str(detail)

    login("notes")
    check(False)
    assert all(
        not v["readable"]
        for v in api(path + "/sources/" + own_input + "/history")["versions"]
    )
    assert not next(s for s in api(path + "/sources") if s["id"] == own_input)[
        "readable"
    ]
    grant_review(True)
    check(True)
    api(path + "/sources/" + own_input + "/content", status=404)
    grant_review(False)
    check(False)
    sql(
        f"delete from project_memberships where project_id='{project}' and member_id='{member}'"
    )
    api(path, status=404)
    assert (
        sql(
            f"select run_review_notes_readable(run_id,'{member}') from artifacts where id='{entries[1]}'"
        )
        == "f"
    )
    return {
        "runsWithDifferentInputs": 3,
        "jointSubmissionTargets": 3,
        "administratorWithoutReviewScope": "restricted",
        "creatorOfAllWithdrawnInputs": "allowed",
        "creatorOfOneOfTwoWithdrawnInputs": "restricted",
        "authorizedSeparateSubmission": "readable",
        "restrictedConflictFileAndPreview": 404,
        "sharedAssignmentRevisionReasonWithheld": True,
        "restrictedRevisionTraceContent": 403,
        "restrictedRevisionTraceExportWithContent": 403,
        "mixedAssignmentOnReadableArtifact": "restricted",
        "independentlyAssignedItemInSameSubmission": "readable",
        "reviewGrantedThenWithdrawn": True,
        "revokedOriginalDuringAuthorizedReview": 404,
        "projectMembershipEnded": 404,
    }
