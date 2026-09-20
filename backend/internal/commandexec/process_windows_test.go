//go:build windows

package commandexec

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

// stubTaskkill swaps taskkill for a script that records being run, so a test can
// tell whether a tree kill shelled out at all.
func stubTaskkill(t *testing.T) (marker string, restore func()) {
	t.Helper()
	dir := t.TempDir()
	marker = filepath.Join(dir, "taskkill-invoked")
	script := filepath.Join(dir, "taskkill.cmd")
	body := "@echo off\r\necho invoked> \"" + marker + "\"\r\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatalf("write taskkill stub: %v", err)
	}
	previous := taskkillCommand
	taskkillCommand = script
	return marker, func() { taskkillCommand = previous }
}

func taskkillStubRan(marker string) bool {
	_, err := os.Stat(marker)
	return err == nil
}

// Once a process has exited its id is no longer ours, and Windows recycles ids.
// Addressing the stale id with `taskkill /T /F` destroys whatever tree holds it
// now, which is how a cancelled command used to take the gateway down with it.
func TestKillTreeDoesNotAddressExitedProcess(t *testing.T) {
	marker, restore := stubTaskkill(t)
	defer restore()

	exited := make(chan struct{})
	close(exited)
	proc := &platformProcess{
		pi:     windows.ProcessInformation{ProcessId: 4242},
		exited: exited,
	}

	if err := proc.KillTree(); err != nil {
		t.Fatalf("KillTree on an exited process = %v, want nil", err)
	}
	if taskkillStubRan(marker) {
		t.Fatal("KillTree ran taskkill for a process that had already exited")
	}
}

// The positive control: without it the test above would also pass if the stub
// mechanism were simply broken.
func TestKillTreeAddressesLiveProcess(t *testing.T) {
	marker, restore := stubTaskkill(t)
	defer restore()

	proc := &platformProcess{
		pi:     windows.ProcessInformation{ProcessId: 4242},
		exited: make(chan struct{}),
	}

	// The stub exits successfully, so KillTree returns without falling back to
	// Terminate and no real process is signalled.
	if err := proc.KillTree(); err != nil {
		t.Fatalf("KillTree on a live process = %v, want nil", err)
	}
	if !taskkillStubRan(marker) {
		t.Fatal("KillTree did not run taskkill for a live process")
	}
}

// A process that exits while the tree kill is in flight must not be terminated
// by id afterwards: the fallback would address the same stale id.
func TestKillTreeDoesNotFallBackAfterProcessExited(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "taskkill.cmd")
	if err := os.WriteFile(script, []byte("@echo off\r\nexit /b 1\r\n"), 0o755); err != nil {
		t.Fatalf("write taskkill stub: %v", err)
	}
	previous := taskkillCommand
	taskkillCommand = script
	defer func() { taskkillCommand = previous }()

	exited := make(chan struct{})
	close(exited)
	proc := &platformProcess{
		pi:     windows.ProcessInformation{ProcessId: 4242},
		exited: exited,
		// A non-zero handle would make the fallback observable, but reaching it
		// at all is the failure; Terminate on handle 0 is a no-op.
	}

	if err := proc.KillTree(); err != nil {
		t.Fatalf("KillTree = %v, want nil", err)
	}
}

func TestWindowsPipeDecodeSelection(t *testing.T) {
	if shouldDecodeWindowsPipeOutput("cmd.exe") {
		t.Fatalf("cmd output is forced to UTF-8 with chcp 65001 and should not be GB18030 decoded")
	}
	if !shouldDecodeWindowsPipeOutput("powershell.exe") {
		t.Fatalf("Windows PowerShell 5.1 output should be GB18030 decoded")
	}
	if shouldDecodeWindowsPipeOutput("pwsh") {
		t.Fatalf("pwsh output should remain UTF-8")
	}
}

func TestWindowsLongShellStdoutDecodeSelection(t *testing.T) {
	if shouldDecodeWindowsLongShellStdout("cmd.exe") {
		t.Fatalf("cmd long shell output is forced to UTF-8 with chcp 65001 and should not be GB18030 decoded")
	}
	if !shouldDecodeWindowsLongShellStdout("powershell.exe") {
		t.Fatalf("Windows PowerShell 5.1 stdout should be GB18030 decoded")
	}
	if shouldDecodeWindowsLongShellStdout("pwsh") {
		t.Fatalf("pwsh long shell output should remain UTF-8")
	}
}
