package main

import (
	"strings"
	"testing"
)

func TestBillingInitializationFailureExitsWithFailure(t *testing.T) {
	for _, standalone := range []bool{false, true} {
		name := "server"
		if standalone {
			name = "standalone-tui"
		}
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			configPath := writeTestServerConfig(t, dir, 18317)
			args := []string{"--config", configPath, "--local-model"}
			if standalone {
				args = append(args, "--tui", "--standalone")
			}
			command, stdout, stderr := newServerExitCommand(t, dir, args...)
			command.Env = append(command.Env,
				"BILLING_ENABLED=true", "BILLING_JWT_SECRET=", "BILLING_DATABASE_URL=",
				"BILLING_PUBLIC_PORTAL_HOST=", "BILLING_PRIVILEGED_PORTAL_HOST=",
				"BILLING_PRIVILEGED_USER_IDS=", "BILLING_USDT_AUTO_CONFIRM=false",
			)
			errRun := command.Run()
			if command.ProcessState == nil || command.ProcessState.ExitCode() != 1 {
				t.Fatalf("billing initialization must fail startup: err=%v state=%v\nstdout:\n%s\nstderr:\n%s",
					errRun, command.ProcessState, stdout.String(), stderr.String())
			}
			if !strings.Contains(stdout.String()+stderr.String(), "billing initialization failed") {
				t.Fatalf("missing billing startup diagnostic:\nstdout:\n%s\nstderr:\n%s", stdout.String(), stderr.String())
			}
		})
	}
}
