package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestDataLockContenderHelper(t *testing.T) {
	if os.Getenv("YOLINK_LOCK_CONTENDER") != "1" {
		t.Skip("subprocess only")
	}
	input := bufio.NewReader(os.Stdin)
	if _, err := input.ReadString('\n'); err != nil {
		t.Fatal(err)
	}
	f, err := acquireDataLock(os.Args[len(os.Args)-1])
	if err != nil {
		fmt.Println("blocked")
		return
	}
	defer f.Close()
	fmt.Println("acquired")
	if _, err = input.ReadString('\n'); err != nil {
		t.Fatal(err)
	}
}

func lockContender(t *testing.T, dir string) (*exec.Cmd, io.WriteCloser, <-chan string) {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	cmd := exec.CommandContext(ctx, executable, "-test.run=^TestDataLockContenderHelper$", "--", dir)
	cmd.Env = append(os.Environ(), "YOLINK_LOCK_CONTENDER=1")
	input, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	output, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { input.Close(); _ = cmd.Process.Kill() })
	result := make(chan string, 1)
	go func() {
		scan := bufio.NewScanner(output)
		if scan.Scan() {
			result <- scan.Text()
		} else {
			result <- "missing-result"
		}
	}()
	return cmd, input, result
}

func contenderResult(t *testing.T, result <-chan string) string {
	t.Helper()
	select {
	case value := <-result:
		return value
	case <-time.After(10 * time.Second):
		t.Fatal("contender readiness timeout")
		return ""
	}
}

func TestDataLockKilledHolderCanBeReacquired(t *testing.T) {
	dir := t.TempDir()
	cmd, input, result := lockContender(t, dir)
	if _, err := io.WriteString(input, "start\n"); err != nil {
		t.Fatal(err)
	}
	if value := contenderResult(t, result); value != "acquired" {
		t.Fatal(value)
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err == nil {
		t.Fatal("killed process returned success")
	}
	f, err := acquireDataLock(dir)
	if err != nil {
		t.Fatal("killed holder retained kernel lock", err)
	}
	f.Close()
}

func TestDataLockSimultaneousStartersHaveOneWinner(t *testing.T) {
	dir := t.TempDir()
	a, ai, ar := lockContender(t, dir)
	b, bi, br := lockContender(t, dir)
	if _, err := io.WriteString(ai, "start\n"); err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(bi, "start\n"); err != nil {
		t.Fatal(err)
	}
	av, bv := contenderResult(t, ar), contenderResult(t, br)
	if !((av == "acquired" && bv == "blocked") || (av == "blocked" && bv == "acquired")) {
		t.Fatal("invalid simultaneous outcomes", av, bv)
	}
	if av == "acquired" {
		_, _ = io.WriteString(ai, "release\n")
	} else {
		_, _ = io.WriteString(bi, "release\n")
	}
	if err := a.Wait(); err != nil {
		t.Fatal(err)
	}
	if err := b.Wait(); err != nil {
		t.Fatal(err)
	}
}

func TestDataLockRejectsSecondProcessHelper(t *testing.T) {
	if os.Getenv("YOLINK_LOCK_PROBE") != "1" {
		t.Skip("subprocess only")
	}
	dir := os.Args[len(os.Args)-1]
	f, err := acquireDataLock(dir)
	if err == nil {
		f.Close()
		t.Fatal("second process acquired held lock")
	}
}

func TestDataLockIsExclusiveAndReleasesOnClose(t *testing.T) {
	dir := t.TempDir()
	f, err := acquireDataLock(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, executable, "-test.run=^TestDataLockRejectsSecondProcessHelper$", "--", dir)
	cmd.Env = append(os.Environ(), "YOLINK_LOCK_PROBE=1")
	if result, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("lock probe: %v %s", err, result)
	}
	if err = f.Close(); err != nil {
		t.Fatal(err)
	}
	second, err := acquireDataLock(dir)
	if err != nil {
		t.Fatal("lock did not release", err)
	}
	second.Close()
	if _, err = os.Stat(filepath.Join(dir, ".yolink.lock")); err != nil {
		t.Fatal("lock inode was removed", err)
	}
}

func TestDataLockRejectsDirectory(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, ".yolink.lock"), 0700); err != nil {
		t.Fatal(err)
	}
	if f, err := acquireDataLock(dir); err == nil {
		f.Close()
		t.Fatal("directory used as lock")
	}
}
