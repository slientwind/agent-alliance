package projects

import (
	"context"

	"alliance/internal/web"
	"github.com/jackc/pgx/v5"
)

func (s Server) addCommentMentions(ctx context.Context, tx pgx.Tx, comment string, members []string) error {
	if len(members) == 0 {
		return nil
	}
	tag, err := tx.Exec(ctx, `insert into artifact_comment_mentions(comment_id,member_id)
	 select $1,m.id from members m where m.id=any($2::uuid[]) and m.kind='person' and artifact_comment_readable($1,m.id)`, comment, members)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != int64(len(members)) {
		return web.Fail(409, "所选成员的成果访问权已变化，请重新选择")
	}
	if s.CommentConnections == nil {
		return nil
	}
	feishu, ding := s.CommentConnections()
	if feishu == "" && ding == "" {
		return nil
	}
	_, err = tx.Exec(ctx, `insert into artifact_comment_deliveries(comment_id,member_id,channel,connection,binding)
	 select m.comment_id,m.member_id,'feishu',f.tenant,f.binding_id
	  from artifact_comment_mentions m join channel_links f on f.member_id=m.member_id and f.tenant=$2
	  where m.comment_id=$1 and $2<>''
	 union all
	 select m.comment_id,m.member_id,'dingtalk',d.connection,d.id
	  from artifact_comment_mentions m join im_bindings d on d.member_id=m.member_id and d.connection=$3
	  where m.comment_id=$1 and $3<>''`, comment, feishu, ding)
	return err
}
