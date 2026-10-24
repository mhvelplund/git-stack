package bitbucket

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// A credential helper that hangs keeps git's stderr open after git is
// killed; ExecGitCredential must still return soon after its timeout.
func TestExecGitCredentialTimeoutWithHangingHelper(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found")
	}
	global := filepath.Join(t.TempDir(), "gitconfig")
	if err := os.WriteFile(global, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", global)
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "credential.helper")
	t.Setenv("GIT_CONFIG_VALUE_0", "!sleep 20; :")
	old := gitCredentialTimeout
	gitCredentialTimeout = 500 * time.Millisecond
	t.Cleanup(func() { gitCredentialTimeout = old })

	start := time.Now()
	_, _, err := ExecGitCredential(Host)
	if err == nil {
		t.Fatal("ExecGitCredential succeeded with a hanging helper")
	}
	if d := time.Since(start); d > 10*time.Second {
		t.Fatalf("ExecGitCredential took %v, want about the timeout", d)
	}
}
