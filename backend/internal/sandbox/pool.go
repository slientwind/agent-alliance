package sandbox

import (
	"context"
	"errors"
	"net/http"
	"time"

	"alliance/internal/database"
	"alliance/internal/identity"
	"alliance/internal/web"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Pool struct {
	DB *pgxpool.Pool
}

type quota struct {
	MaxJobs  int `json:"maxJobs"`
	Timeout  int `json:"timeoutSeconds"`
	Revision int `json:"revision"`
	Active   int `json:"active"`
}

var ErrPoolFull = errors.New("sandbox pool is at capacity")

func (p Pool) Routes(mux *http.ServeMux, auth identity.Server) {
	mux.Handle("GET /api/admin/integrations/sandbox", auth.Protect(true, p.status))
	mux.Handle("PUT /api/admin/integrations/sandbox", auth.Protect(true, p.configure))
}

func (p Pool) status(w http.ResponseWriter, r *http.Request, _ identity.Member) error {
	var out quota
	err := p.DB.QueryRow(r.Context(), `select max_jobs,timeout_seconds,revision,
		(select count(*) from sandbox_leases where expires_at>clock_timestamp()) from sandbox_pool`).
		Scan(&out.MaxJobs, &out.Timeout, &out.Revision, &out.Active)
	if err != nil {
		return err
	}
	return web.JSON(w, http.StatusOK, out)
}

func (p Pool) configure(w http.ResponseWriter, r *http.Request, actor identity.Member) error {
	var in struct {
		MaxJobs  *int `json:"maxJobs"`
		Timeout  int  `json:"timeoutSeconds"`
		Expected int  `json:"expected"`
	}
	if err := web.ReadLimited(w, r, &in, 1024); err != nil {
		return err
	}
	if in.MaxJobs == nil || *in.MaxJobs < 0 || *in.MaxJobs > 32 || in.Timeout < 5 || in.Timeout > int(taskTimeout.Seconds()) || in.Expected < 1 {
		return web.Fail(422, "请选择 0–32 个并发任务和 5–60 秒的执行时限")
	}
	ctx := r.Context()
	var revision int
	err := database.WithTx(ctx, p.DB, func(tx pgx.Tx) error {
		if err := identity.RequireAdmin(ctx, tx, actor.ID); err != nil {
			return err
		}
		err := tx.QueryRow(ctx, `update sandbox_pool set max_jobs=$1,timeout_seconds=$2,revision=revision+1
			where singleton and revision=$3 returning revision`, *in.MaxJobs, in.Timeout, in.Expected).Scan(&revision)
		if errors.Is(err, pgx.ErrNoRows) {
			return web.Fail(409, "沙箱配额已更新，请刷新")
		}
		if err != nil {
			return err
		}
		return database.Audit(ctx, tx, actor.ID, "sandbox.pool.configure", "sandbox", map[string]int{"maxJobs": *in.MaxJobs, "timeoutSeconds": in.Timeout, "revision": revision})
	})
	if err != nil {
		return err
	}
	return web.JSON(w, http.StatusOK, map[string]int{"revision": revision})
}

func (p Pool) acquire(ctx context.Context) (string, time.Duration, error) {
	var id string
	var timeout int
	err := database.WithTx(ctx, p.DB, func(tx pgx.Tx) error {
		var jobs, active int
		if err := tx.QueryRow(ctx, "select max_jobs,timeout_seconds from sandbox_pool where singleton for update").Scan(&jobs, &timeout); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "delete from sandbox_leases where expires_at<=clock_timestamp()"); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, "select count(*) from sandbox_leases").Scan(&active); err != nil {
			return err
		}
		if active >= jobs {
			return ErrPoolFull
		}
		return tx.QueryRow(ctx, "insert into sandbox_leases(expires_at) values(clock_timestamp()+make_interval(secs=>$1)) returning id", float64(timeout+15)).Scan(&id)
	})
	return id, time.Duration(timeout) * time.Second, err
}

// Run reserves a slot across all hosts connected to the same deployment.
func (p Pool) Run(ctx context.Context, inputs, outputs string) error {
	return p.withSlot(ctx, func(job context.Context, limit time.Duration) error {
		return run(job, inputs, outputs, limit)
	})
}

func (p Pool) withSlot(ctx context.Context, perform func(context.Context, time.Duration) error) error {
	id, limit, err := p.acquire(ctx)
	if err != nil {
		return err
	}
	defer func() {
		release, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_, _ = p.DB.Exec(release, "delete from sandbox_leases where id=$1", id)
	}()
	job, cancel := context.WithTimeout(ctx, limit+5*time.Second)
	defer cancel()
	return perform(job, limit)
}
