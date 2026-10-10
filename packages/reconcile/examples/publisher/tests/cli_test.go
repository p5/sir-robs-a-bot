package publishertests

import (
	"context"
	"crypto/rand"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestPublisherExecutable(t *testing.T) {
	binary := os.Getenv("PUBLISHER_BINARY")
	if binary == "" {
		t.Fatal("PUBLISHER_BINARY must name the built publisher artifact; use the Buck test target")
	}
	for _, backend := range []string{"postgres", "dynamodb"} {
		t.Run(backend, func(t *testing.T) {
			namespace := "cli-" + rand.Text()
			// Keep this context alive through test cleanup so graceful shutdown
			// can release claims before CommandContext's kill fallback.
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			t.Cleanup(cancel)
			base := []string{"-queue", backend, "-namespace", namespace, "-table", table, "-endpoint", endpoint, "-delay", "100ms"}
			command := func(args ...string) *exec.Cmd {
				cmd := exec.CommandContext(ctx, binary, append(append([]string{}, base...), args...)...)
				cmd.Env = append(os.Environ(), "PUBLISHER_DSN="+dsn, "AWS_ACCESS_KEY_ID=local", "AWS_SECRET_ACCESS_KEY=local")
				return cmd
			}
			run := func(args ...string) string {
				t.Helper()
				output, err := command(args...).CombinedOutput()
				if err != nil {
					t.Fatalf("publisher %v: %v %s", args, err, output)
				}
				return string(output)
			}
			run("migrate")
			run("submit", "one", "published through CLI", "99")
			run("relay")
			// Keep metrics output outside the source tree and avoid pipe
			// backpressure when a host gathers its registry on shutdown.
			metrics, err := os.CreateTemp(t.TempDir(), "metrics-")
			if err != nil {
				t.Fatal(err)
			}
			defer metrics.Close()
			worker := command("work")
			worker.Stdout = metrics
			worker.Stderr = os.Stderr
			if err := worker.Start(); err != nil {
				t.Fatal(err)
			}
			stopped := false
			t.Cleanup(func() {
				if !stopped {
					_ = worker.Process.Signal(syscall.SIGTERM)
					_ = worker.Wait()
				}
			})
			eventually(t, func() bool {
				return strings.Contains(run("inspect", "one"), "desired=1 observed=1 pending=false")
			})
			// Exercise failure and recovery through the same executable.
			run("submit", "one", "")
			run("relay")
			eventually(t, func() bool {
				output := run("inspect", "one")
				return strings.Contains(output, "pending=false failures=1") && strings.Contains(output, "document body is empty")
			})
			// Stop workers before explicitly resetting the diagnostic.
			if err := worker.Process.Signal(syscall.SIGTERM); err != nil {
				t.Fatal(err)
			}
			err = worker.Wait()
			stopped = true
			if err != nil {
				t.Fatal("worker shutdown:", err)
			}
			run("submit", "one", "fixed through CLI")
			run("relay")
			run("redrive", "one")
			output := run("inspect", "one")
			if !strings.Contains(output, "desired=3 observed=1 pending=true failures=0") {
				t.Fatalf("redrive did not preserve desired progress: %s", output)
			}
			data, err := os.ReadFile(metrics.Name())
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(data), "factory_reconcile_operations_total") {
				t.Fatalf("host did not export metrics: %s", data)
			}
			if output, err := command("submit", "bad", "body", "4294967296").CombinedOutput(); err == nil {
				t.Fatalf("CLI accepted overflowing priority: %s", output)
			}
		})
	}
}
