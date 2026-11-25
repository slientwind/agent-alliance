package sandbox

import (
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	image       = "alliance-sandbox:local"
	taskTimeout = 60 * time.Second
	maxEntries  = 256
	maxBytes    = int64(128 << 20)
)

// Run stages a task's inputs, runs main.py, and publishes its files to a new
// directory only after the container stops and the output tree is validated.
func Run(ctx context.Context, inputDir, resultDir string) error {
	return run(ctx, inputDir, resultDir, taskTimeout)
}

func run(ctx context.Context, inputDir, resultDir string, timeout time.Duration) error {
	if inputDir == "" || resultDir == "" {
		return errors.New("provide input and result directories")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	inputDir, err := filepath.Abs(inputDir)
	if err != nil {
		return err
	}
	inputDir, err = filepath.EvalSymlinks(inputDir)
	if err != nil {
		return err
	}
	resultDir, err = filepath.Abs(resultDir)
	if err != nil {
		return err
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(resultDir))
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(inputDir, parent)
	if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return errors.New("the result directory must be outside the input tree")
	}
	resultDir = filepath.Join(parent, filepath.Base(resultDir))
	if _, err := os.Lstat(resultDir); err == nil {
		return fmt.Errorf("result directory: %w", os.ErrExist)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	// Stage outside the input tree and on the same filesystem as the final
	// result so that a successful job publishes its whole output atomically.
	task, err := os.MkdirTemp(filepath.Dir(resultDir), ".alliance-sandbox-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(task)
	inputs, outputs := filepath.Join(task, "inputs"), filepath.Join(task, "outputs")
	if err = os.Mkdir(inputs, 0755); err != nil {
		return err
	}
	if err = os.Mkdir(outputs, 0700); err != nil {
		return err
	}
	// The container's unprivileged uid can access these bind mounts. The host
	// staging parent remains private and is never mounted in the container.
	if err = os.Chmod(inputs, 0755); err != nil {
		return err
	}
	if err = os.Chmod(outputs, 0777); err != nil {
		return err
	}
	if err = copyTaskTree(ctx, inputDir, inputs, 0644, 0755); err != nil {
		return fmt.Errorf("prepare sandbox inputs: %w", err)
	}
	if item, err := os.Stat(filepath.Join(inputs, "main.py")); err != nil || !item.Mode().IsRegular() {
		return errors.New("inputs must include a regular main.py file")
	}
	if err = runContainer(ctx, inputs, outputs, timeout); err != nil {
		return err
	}
	approved := filepath.Join(task, "validated")
	if err = os.Mkdir(approved, 0700); err != nil {
		return err
	}
	if err = copyTaskTree(ctx, outputs, approved, 0600, 0700); err != nil {
		return fmt.Errorf("collect sandbox outputs: %w", err)
	}
	return publishTaskResults(approved, resultDir)
}

func runContainer(ctx context.Context, inputs, outputs string, timeout time.Duration) error {
	name := "alliance-sandbox-" + uuid.NewString()
	jobContext, stop := context.WithTimeout(ctx, timeout+5*time.Second)
	defer stop()
	args := containerArguments(name, inputs, outputs, timeout)
	err := exec.CommandContext(jobContext, "docker", args...).Run()
	if err == nil {
		return nil
	}
	// A disconnected client can leave an independent container running. Its
	// own watchdog still applies, and cleanup here also handles cancellation.
	cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = exec.CommandContext(cleanup, "docker", "rm", "--force", name).Run()
	if jobContext.Err() != nil {
		return jobContext.Err()
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) && (exit.ExitCode() == 124 || exit.ExitCode() == 137) {
		return errors.New("sandbox task reached its time or resource limit")
	}
	return fmt.Errorf("sandbox could not complete the task: %w", err)
}

func containerArguments(name, inputs, outputs string, timeout time.Duration) []string {
	return []string{
		"run", "--rm", "--pull", "never", "--name", name, "--init", "--network", "none",
		"--user", "65534:65534", "--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges=true",
		"--cpus", "1", "--memory", "768m", "--memory-swap", "768m", "--pids-limit", "64",
		"--tmpfs", "/tmp:size=192m,mode=1777", "--ulimit", "fsize=67108864:67108864", "--log-driver", "none",
		"--mount", bindMount(inputs, "/workspace/inputs", true),
		"--mount", bindMount(outputs, "/workspace/outputs", false),
		"--entrypoint", "/usr/bin/timeout", image,
		"--signal=KILL", fmt.Sprintf("%gs", timeout.Seconds()), "python", "-I", "-B", "/workspace/inputs/main.py",
	}
}

func bindMount(source, target string, readOnly bool) string {
	fields := []string{"type=bind", "source=" + source, "target=" + target}
	if readOnly {
		fields = append(fields, "readonly")
	}
	var value strings.Builder
	writer := csv.NewWriter(&value)
	_ = writer.Write(fields)
	writer.Flush()
	return strings.TrimSuffix(value.String(), "\n")
}

func copyTaskTree(ctx context.Context, from, to string, fileMode, dirMode fs.FileMode) error {
	root, err := os.OpenRoot(from)
	if err != nil {
		return err
	}
	defer root.Close()
	if info, err := root.Stat("."); err != nil || !info.IsDir() {
		return errors.New("select a task directory")
	}
	remaining := maxBytes
	count := 0
	return fs.WalkDir(root.FS(), ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err = ctx.Err(); err != nil {
			return err
		}
		if name == "." {
			return nil
		}
		count++
		if count > maxEntries {
			return errors.New("the task contains too many files or folders")
		}
		target := filepath.Join(to, filepath.FromSlash(name))
		if entry.IsDir() {
			if err = os.Mkdir(target, dirMode); err != nil {
				return err
			}
			return os.Chmod(target, dirMode)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return errors.New("the task can only exchange regular files")
		}
		n, err := copyTaskFile(root, name, target, remaining, fileMode)
		remaining -= n
		return err
	})
}

func copyTaskFile(root *os.Root, name, target string, remaining int64, mode fs.FileMode) (int64, error) {
	input, err := root.Open(filepath.FromSlash(name))
	if err != nil {
		return 0, err
	}
	defer input.Close()
	info, err := input.Stat()
	if err != nil {
		return 0, err
	}
	if !info.Mode().IsRegular() {
		return 0, errors.New("the task can only exchange regular files")
	}
	if info.Size() > remaining {
		return 0, errors.New("the task files exceed the allowed size")
	}
	output, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return 0, err
	}
	n, copyErr := io.Copy(output, io.LimitReader(input, remaining+1))
	modeErr := output.Chmod(mode)
	closeErr := output.Close()
	if n > remaining {
		return 0, errors.New("the task files exceed the allowed size")
	}
	return n, errors.Join(copyErr, modeErr, closeErr)
}
