// The recovery tool is an operator command. It never starts the application,
// contacts model providers, or promotes a recovered database to production.
package main

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5"
)

type options struct {
	checkOnly                                 bool
	archive, keyFile                          string
	business, temporal, visibility            string
	historyService, visibilityService         string
	objects, delivery, stage, resourceKeyFile string
	retention                                 time.Duration
}

func connect(ctx context.Context, name string) (*pgx.Conn, error) {
	return connectService(ctx, name, "")
}
func connectService(ctx context.Context, name, service string) (*pgx.Conn, error) {
	settings := ""
	if service != "" {
		if !safeName.MatchString(service) {
			return nil, errors.New("非法 PostgreSQL service 名称")
		}
		settings = "service=" + service
	}
	cfg, err := pgx.ParseConfig(settings)
	if err != nil {
		return nil, errors.New("检查 PostgreSQL 连接配置")
	}
	cfg.Database = name
	return pgx.ConnectConfig(ctx, cfg)
}
func key(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Size() > 256 {
		return "", errors.New("需要独立的 32 字节十六进制备份密钥文件")
	}
	raw, err := io.ReadAll(io.LimitReader(f, 257))
	if err != nil {
		return "", err
	}
	value := strings.TrimSpace(string(raw))
	decoded, err := hex.DecodeString(value)
	if err != nil || len(decoded) != 32 {
		return "", errors.New("备份密钥须为 64 位十六进制字符串")
	}
	return strings.ToLower(value), nil
}
func main() {
	log.SetFlags(0)
	if len(os.Args) < 2 {
		log.Fatal("usage: recovery backup|verify|stage|repair-delivery|expire [flags]")
	}
	mode := os.Args[1]
	fs := flag.NewFlagSet(mode, flag.ExitOnError)
	var o options
	fs.StringVar(&o.archive, "archive", "", "encrypted backup file")
	fs.StringVar(&o.keyFile, "key-file", "", "independent backup key file")
	fs.StringVar(&o.business, "business", "alliance", "live business database")
	fs.StringVar(&o.temporal, "temporal", "temporal", "Temporal history database")
	fs.StringVar(&o.visibility, "visibility", "temporal_visibility", "Temporal visibility database")
	fs.StringVar(&o.historyService, "history-service", "", "libpq service for history backup source")
	fs.StringVar(&o.visibilityService, "visibility-service", "", "libpq service for visibility backup source")
	fs.DurationVar(&o.retention, "retain", 30*24*time.Hour, "backup lifetime, e.g. 720h")
	fs.StringVar(&o.objects, "objects", "/data/objects", "live content-addressed object root")
	fs.StringVar(&o.resourceKeyFile, "resource-key-file", "", "optional separate resource vault key for isolated verification")
	fs.BoolVar(&o.checkOnly, "check-only", false, "stage, check and remove the private copies before returning the report")
	fs.StringVar(&o.stage, "stage-dir", "", "new, private staging directory on encrypted storage")
	fs.StringVar(&o.delivery, "delivery", "", "currently confirmed delivery UUID to repair")
	fs.Parse(os.Args[2:])
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, mode, o); err != nil {
		log.Fatal(err)
	}
}
func run(ctx context.Context, mode string, o options) error {
	if o.archive == "" || o.keyFile == "" {
		return errors.New("请提供 -archive 与 -key-file")
	}
	secret, err := key(o.keyFile)
	if err != nil {
		return err
	}
	if mode == "backup" {
		return backup(ctx, o, secret)
	}
	if mode != "stage" && mode != "verify" && mode != "repair-delivery" && mode != "expire" {
		return errors.New("未知的恢复操作")
	}
	initial, err := os.Lstat(o.archive)
	if err != nil {
		return err
	}
	if !initial.Mode().IsRegular() {
		return errors.New("需要普通备份文件")
	}
	bundle, closeBundle, err := readArchive(o.archive, secret)
	if err != nil {
		return err
	}
	defer closeBundle()
	expired := !time.Now().Before(bundle.info.Expires)
	if mode == "expire" {
		if expired {
			current, err := os.Lstat(o.archive)
			if err != nil {
				return err
			}
			if !os.SameFile(initial, current) {
				return errors.New("备份在检查期间已被替换")
			}
			if err = os.Remove(o.archive); err != nil {
				return err
			}
			if err = syncDir(filepath.Dir(o.archive)); err != nil {
				return err
			}
		}
		return json.NewEncoder(os.Stdout).Encode(map[string]any{"removed": expired, "expires": bundle.info.Expires, "enterprise": bundle.info.Enterprise})
	}
	if expired && mode != "verify" {
		return errors.New("备份已超过清单保留期，禁止用于恢复")
	}
	switch mode {
	case "stage":
		return stage(ctx, o, bundle)
	case "repair-delivery":
		return repair(ctx, o, bundle)
	default:
		return json.NewEncoder(os.Stdout).Encode(bundle.info)
	}
}
func pgTool(ctx context.Context, program, db string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, program, args...)
	cmd.Env = append(os.Environ(), "PGDATABASE="+db)
	cmd.Stderr = os.Stderr
	return cmd
}
func syncDir(path string) error {
	d, err := os.Open(filepath.Clean(path))
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
