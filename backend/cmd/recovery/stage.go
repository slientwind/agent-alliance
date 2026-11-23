package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

type stageReport struct {
	Retained                  bool
	Workflows                 workflowReport
	ResourceKeys              keyReport
	Enterprise                string
	Databases                 map[string]string
	Objects                   int
	OpenRuns, UnresolvedCalls int
	Quarantined               bool
	Finished                  time.Time
}

func stage(ctx context.Context, o options, b *bundle) (err error) {
	if o.stage == "" {
		return errors.New("请指定一个尚不存在的隔离目录")
	}
	admin, err := connect(ctx, "postgres")
	if err != nil {
		return err
	}
	defer admin.Close(ctx)
	var nonce [8]byte
	if _, err = rand.Read(nonce[:]); err != nil {
		return err
	}
	if err = os.Mkdir(o.stage, 0700); err != nil {
		return err
	}
	prefix := "alliance_restore_" + hex.EncodeToString(nonce[:]) + "_"
	report := stageReport{Enterprise: b.info.Enterprise, Databases: map[string]string{}, Quarantined: true}
	cleanupStage := func() error {
		cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		var cleanupErr error
		// Only databases created by this invocation can enter this map.
		for _, name := range report.Databases {
			_, dropErr := admin.Exec(cleanup, "drop database if exists "+pgx.Identifier{name}.Sanitize()+" with (force)")
			cleanupErr = errors.Join(cleanupErr, dropErr)
		}
		return errors.Join(cleanupErr, os.RemoveAll(o.stage))
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, cleanupStage())
		}
	}()

	for _, role := range roles {
		name := prefix + role
		ident := pgx.Identifier{name}.Sanitize()
		if _, err = admin.Exec(ctx, "create database "+ident+" template template0"); err != nil {
			return err
		}
		report.Databases[role] = name
		if _, err = admin.Exec(ctx, "revoke connect on database "+ident+" from public"); err != nil {
			return err
		}
		if _, err = admin.Exec(ctx, "alter database "+ident+" set alliance.restore_quarantined='on'"); err != nil {
			return err
		}
		r, err := b.files[role+".dump"].Open()
		if err != nil {
			return err
		}
		cmd := pgTool(ctx, "pg_restore", name, "--no-password", "--dbname="+name, "--single-transaction", "--exit-on-error", "--no-owner", "--no-acl")
		cmd.Stdin = r
		err = cmd.Run()
		r.Close()
		if err != nil {
			return err
		}
		if role == "business" {
			if err = checkBusiness(ctx, name, b, &report, o.resourceKeyFile); err != nil {
				return err
			}
		}
		if _, err = admin.Exec(ctx, "alter database "+ident+" set default_transaction_read_only=on"); err != nil {
			return err
		}
	}
	report.Workflows, err = compareWorkflows(ctx, report.Databases)
	if err != nil {
		return err
	}
	objects := filepath.Join(o.stage, "objects")
	if err = os.Mkdir(objects, 0700); err != nil {
		return err
	}
	for path, f := range b.files {
		if !strings.HasPrefix(path, "objects/") {
			continue
		}
		r, err := f.Open()
		if err != nil {
			return err
		}
		err = writeFile(filepath.Join(objects, path[8:]), r)
		r.Close()
		if err != nil {
			return err
		}
	}
	report.Finished = time.Now().UTC()
	if o.checkOnly {
		if err = cleanupStage(); err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(report)
	}
	report.Retained = true
	raw, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	if err = os.WriteFile(filepath.Join(o.stage, "report.json"), raw, 0600); err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(report)
}
func checkBusiness(ctx context.Context, name string, b *bundle, report *stageReport, keyFile string) error {
	c, err := connect(ctx, name)
	if err != nil {
		return err
	}
	defer c.Close(ctx)
	if _, err = c.Exec(ctx, "create table public.alliance_restore_quarantine(held_at timestamptz not null default now()); insert into public.alliance_restore_quarantine default values"); err != nil {
		return err
	}
	report.ResourceKeys, err = checkKeys(ctx, c, keyFile)
	if err != nil {
		return err
	}
	var enterprise string
	if err = c.QueryRow(ctx, "select default_team from enterprise").Scan(&enterprise); err != nil {
		return err
	}
	if enterprise != report.Enterprise {
		return errors.New("恢复的企业与清单不符")
	}
	if err = c.QueryRow(ctx, "select (select count(*) from runs where status not in ('completed','cancelled','failed')), (select count(*) from invocations where status in ('submitted','unknown','remote'))").Scan(&report.OpenRuns, &report.UnresolvedCalls); err != nil {
		return err
	}
	rows, err := c.Query(ctx, "select hash,size_bytes from objects")
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var hash string
		var size int64
		if err = rows.Scan(&hash, &size); err != nil {
			return err
		}
		f := b.files["objects/"+hash]
		if f == nil || int64(f.UncompressedSize64) != size {
			return errors.New("数据库与文件恢复点不匹配")
		}
		report.Objects++
	}
	if err = rows.Err(); err != nil {
		return err
	}
	if report.Objects != b.info.Objects {
		return errors.New("数据库与备份清单的对象数量不符")
	}
	return nil
}
func writeFile(path string, r io.Reader) error {
	out, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, r)
	if err == nil {
		err = out.Sync()
	}
	return errors.Join(err, out.Close())
}
