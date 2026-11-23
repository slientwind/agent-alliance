with register as (
        select g.id,m.display_name as member,n.name,n.kind,
            g.allow_details as details,g.detail_costs as "detailCosts",g.detail_revision as "detailRevision",
            g.financial_export as "financialExport",g.include_costs as costs,g.allow_export as export,g.export_revision as "exportRevision",g.expires_at as "expiresAt",issuer.display_name as issuer,g.reason,
            case when g.revoked_at is not null or g.expires_at<=now() then least(g.expires_at,g.revoked_at) end as "endedAt",
            case when g.revoked_at is not null and g.revoked_at<=g.expires_at then g.revoked_reason
                 when g.expires_at<=now() then '有效期已结束' end as "endReason",
            g.granted_at as "grantedAt"
        from management_grants g join members m on m.id=g.member_id join members issuer on issuer.id=g.granted_by
        join management_grant_names n on n.id=g.id
    )
    select jsonb_build_object(
      'scopes',(select coalesce(jsonb_agg(to_jsonb(o) order by kind,name,id),'[]') from
        (select id::text,name,'team' as kind from teams union all select id::text,name,'department' from departments
         union all select 'enterprise',name,'enterprise' from enterprise) o),
      'assignments',(select coalesce(jsonb_object_agg(id::text,department_id),'{}') from members where kind='person' and department_id is not null),
      'grants',(select coalesce(jsonb_agg(to_jsonb(r) order by "grantedAt" desc,id),'[]') from register r where "endedAt" is null),
      'ended',(select coalesce(jsonb_agg(to_jsonb(r) order by "endedAt" desc,id),'[]') from
        (select * from register where "endedAt" is not null order by "endedAt" desc,id limit 50) r))
    from members where id=$1 and enabled and role='admin'
