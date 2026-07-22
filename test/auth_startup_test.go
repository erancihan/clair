package test

import (
	"os"
	"os/exec"
	"strings"
	"testing"

	authentication "github.com/erancihan/clair/internal/server/authentication"
)

// authStartupSubprocessEnv guards the subprocess re-execution below so the child
// run returns immediately instead of recursing.
const authStartupSubprocessEnv = "GO_AUTH_STARTUP_SUBPROCESS"

// TestSessionKeyStartup verifies the fail-closed signing-key policy. The
// authentication package resolves its cookie signing key at package
// initialization time, so a production process without SESSION_KEY must crash on
// startup (before it can serve requests). This is checked by re-executing the
// test binary under controlled environments.
func TestSessionKeyStartup(t *testing.T) {
	if os.Getenv(authStartupSubprocessEnv) == "1" {
		// Inner run: if the process got this far, package init did not panic.
		return
	}

	// Build a clean base environment for the child, stripping the variables we
	// want to control so a parent-set value can't leak in.
	baseEnv := func() []string {
		env := []string{authStartupSubprocessEnv + "=1"}
		for _, e := range os.Environ() {
			switch {
			case strings.HasPrefix(e, "SESSION_KEY="),
				strings.HasPrefix(e, "APP_ENV="),
				strings.HasPrefix(e, authStartupSubprocessEnv+"="):
				continue
			}
			env = append(env, e)
		}
		return env
	}

	run := func(extra ...string) (string, error) {
		cmd := exec.Command(os.Args[0], "-test.run", "TestSessionKeyStartup")
		cmd.Env = append(baseEnv(), extra...)
		out, err := cmd.CombinedOutput()
		return string(out), err
	}

	t.Run("production without SESSION_KEY panics on startup", func(t *testing.T) {
		out, err := run("APP_ENV=production") // SESSION_KEY intentionally absent
		if err == nil {
			t.Fatalf("expected the process to fail closed (non-zero exit), got success:\n%s", out)
		}
		if !strings.Contains(out, "SESSION_KEY") {
			t.Fatalf("expected a panic mentioning SESSION_KEY, got:\n%s", out)
		}
	})

	t.Run("production with SESSION_KEY starts", func(t *testing.T) {
		out, err := run("APP_ENV=production", "SESSION_KEY=a-strong-production-signing-key")
		if err != nil {
			t.Fatalf("expected a clean start with SESSION_KEY set, got %v:\n%s", err, out)
		}
	})

	t.Run("development without SESSION_KEY starts via dev fallback", func(t *testing.T) {
		out, err := run("APP_ENV=development") // dev fallback key
		if err != nil {
			t.Fatalf("expected a clean start in development, got %v:\n%s", err, out)
		}
	})
}

// TestSecureCookies verifies SecureCookies tracks the production environment.
func TestSecureCookies(t *testing.T) {
	t.Setenv("APP_ENV", "production")
	if !authentication.SecureCookies() {
		t.Error("expected SecureCookies() to be true in production")
	}

	t.Setenv("APP_ENV", "development")
	if authentication.SecureCookies() {
		t.Error("expected SecureCookies() to be false outside production")
	}
}
