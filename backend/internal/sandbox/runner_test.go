package sandbox

import (
	"context"
	"encoding/csv"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestSandboxContainerLimits(t *testing.T) {
	args := containerArguments("fixture", "/tmp/input,dir", "/tmp/output", taskTimeout)
	options := map[string]string{
		"--network": "none", "--user": "65534:65534", "--cap-drop": "ALL",
		"--memory": "768m", "--memory-swap": "768m", "--cpus": "1", "--pids-limit": "64", "--log-driver": "none",
		"--security-opt": "no-new-privileges=true", "--entrypoint": "/usr/bin/timeout",
	}
	for name, expected := range options {
		index := slices.Index(args, name)
		if index < 0 || args[index+1] != expected {
			t.Errorf("%s must be %s", name, expected)
		}
	}
	if !slices.Contains(args, "--read-only") || !slices.Contains(args, "--rm") || slices.Contains(args, "--env") ||
		!slices.Contains(args, "60s") {
		t.Fatalf("sandbox arguments do not enforce limits: %v", args)
	}
	index := slices.Index(args, "--mount")
	fields, err := csv.NewReader(strings.NewReader(args[index+1])).Read()
	if err != nil || !slices.Equal(fields, []string{"type=bind", "source=/tmp/input,dir", "target=/workspace/inputs", "readonly"}) {
		t.Fatalf("invalid input mount: %q %v", fields, err)
	}
	fields, err = csv.NewReader(strings.NewReader(args[index+3])).Read()
	if err != nil || !slices.Equal(fields, []string{"type=bind", "source=/tmp/output", "target=/workspace/outputs"}) {
		t.Fatalf("invalid output mount: %q %v", fields, err)
	}
}

func TestSandboxFileStaging(t *testing.T) {
	input := t.TempDir()
	target := t.TempDir()
	if err := os.WriteFile(filepath.Join(input, "采购.csv"), []byte("item,amount\na,1\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := copyTaskTree(t.Context(), input, target, 0644, 0755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(target, "采购.csv")
	data, err := os.ReadFile(file)
	if err != nil || !strings.Contains(string(data), "a,1") {
		t.Fatalf("input snapshot missing: %v", err)
	}
	if err := os.Symlink(file, filepath.Join(input, "shortcut")); err != nil {
		t.Fatal(err)
	}
	if err := copyTaskTree(t.Context(), input, t.TempDir(), 0600, 0700); err == nil || !strings.Contains(err.Error(), "regular files") {
		t.Fatalf("unexpected symlink copy result: %v", err)
	}
	if err := Run(t.Context(), input, filepath.Join(input, "output")); err == nil || !strings.Contains(err.Error(), "outside") {
		t.Fatalf("a recursive result location was accepted: %v", err)
	}
}

func TestSandboxPublicationDoesNotReplaceExistingDirectory(t *testing.T) {
	root := t.TempDir()
	temp := filepath.Join(root, "validated")
	result := filepath.Join(root, "result")
	for _, dir := range []string{temp, result} {
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(temp, "generated.txt"), []byte("generated"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := publishTaskResults(temp, result); !errors.Is(err, os.ErrExist) {
		t.Fatalf("existing directory was not protected: %v", err)
	}
	entries, err := os.ReadDir(result)
	if err != nil || len(entries) != 0 {
		t.Fatalf("files were added to the existing result directory: %v %v", entries, err)
	}
}

func TestSandboxDockerTask(t *testing.T) {
	if os.Getenv("ALLIANCE_SANDBOX_DOCKER_TEST") != "1" {
		t.Skip("set ALLIANCE_SANDBOX_DOCKER_TEST=1 after building alliance-sandbox:local")
	}
	root := filepath.Join(t.TempDir(), "task with space, and comma")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	input := filepath.Join(root, "inputs")
	if err := os.Mkdir(input, 0700); err != nil {
		t.Fatal(err)
	}
	writeScript := func(code string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(input, "main.py"), []byte(code), 0600); err != nil {
			t.Fatal(err)
		}
	}
	writeScript(`import errno
import os
import pathlib

assert "DATABASE_URL" not in os.environ
assert "RESOURCE_KEY" not in os.environ
assert len(pathlib.Path("/proc/net/route").read_text().splitlines()) == 1
assert not pathlib.Path("/workspace/validated").exists()
try:
    pathlib.Path("/workspace/inputs/blocked").write_text("no")
except OSError as exc:
    assert exc.errno == errno.EROFS
else:
    raise AssertionError("input mount is writable")
pathlib.Path("/workspace/outputs/结果.txt").write_text("已完成")
`)
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	result := filepath.Join(root, "result")
	if err := run(ctx, input, result, 2*time.Second); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(result, "结果.txt"))
	if err != nil || string(body) != "已完成" {
		t.Fatalf("unexpected output: %q %v", body, err)
	}
	if err := run(ctx, input, result, time.Second); !errors.Is(err, os.ErrExist) {
		t.Fatalf("existing output was not protected: %v", err)
	}
	writeScript("import time\ntime.sleep(30)\n")
	timedOut := filepath.Join(root, "timed-out")
	err = run(ctx, input, timedOut, 200*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "limit") {
		t.Fatalf("sandbox did not kill a long-running task: %v", err)
	}
	if _, err := os.Stat(timedOut); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("timed-out results were published: %v", err)
	}
	writeScript("import pathlib\npathlib.Path('/workspace/outputs/link').symlink_to('/etc/passwd')\n")
	badOutput := filepath.Join(root, "unsafe")
	if err = run(ctx, input, badOutput, 2*time.Second); err == nil || !strings.Contains(err.Error(), "regular files") {
		t.Fatalf("sandbox accepted a symbolic link output: %v", err)
	}
}
