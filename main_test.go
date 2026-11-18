package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"flag"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	commonparams "github.com/cloudbase/garm-provider-common/params"
	"github.com/cloudbase/garm/params"

	"github.com/katexochen/garm-reconcile/internal/garm/garmtest"
)

const desiredState = `
admin:
  username: admin
  email: admin@example.com
  password: {_secret: admin-password}
controller:
  metadata_url: http://garm/api/v1/metadata
  callback_url: http://garm/api/v1/callbacks
  agent_url: http://garm/agent
github:
  credentials:
    main:
      endpoint: github.com
      pat:
        oauth2_token: {_secret: github-token}
  organizations:
    my-org:
      credentials_name: main
      pools:
        coco:
          provider_name: incus
          image: ubuntu
          flavor: default
          os_type: linux
          os_arch: amd64
          tags: [coco]
          max_runners: 2
          enabled: true
`

// withoutPool is the desired state after removing the pool.
var withoutPool = desiredState[:strings.Index(desiredState, "      pools:")]

func writeFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// setup writes the desired state and its secrets, which are found through
// $CREDENTIALS_DIRECTORY like in a systemd service.
func setup(t *testing.T, state string) string {
	t.Helper()
	credentials := t.TempDir()
	writeFile(t, credentials, "admin-password", "hunter2\n")
	writeFile(t, credentials, "github-token", "ghp_token\n")
	t.Setenv("CREDENTIALS_DIRECTORY", credentials)
	t.Setenv("NOTIFY_SOCKET", "")
	return writeFile(t, t.TempDir(), "garm.yaml", state)
}

func execute(t *testing.T, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	var out, errOut bytes.Buffer
	app := cli{stdout: &out, stderr: &errOut, pollInterval: time.Millisecond}
	err = app.run(context.Background(), args)
	return out.String(), errOut.String(), err
}

func mustExecute(t *testing.T, args ...string) string {
	t.Helper()
	stdout, stderr, err := execute(t, args...)
	if err != nil {
		t.Fatalf("garm-reconcile %s: %v\n%s", strings.Join(args, " "), err, stderr)
	}
	return stdout
}

func TestValidate(t *testing.T) {
	// The secrets do not need to be available.
	path := writeFile(t, t.TempDir(), "garm.yaml", desiredState)
	if stdout := mustExecute(t, "validate", path); stdout != "" {
		t.Errorf("validate printed %q, want no output", stdout)
	}

	path = writeFile(t, t.TempDir(), "garm.yaml", strings.Replace(desiredState, "max_runners: 2", "max_runners: 0", 1))
	_, _, err := execute(t, "validate", path)
	if err == nil || !strings.Contains(err.Error(), "github.organizations.my-org.pools.coco: max_runners cannot be 0") {
		t.Errorf("validate = %v, want it to reject the pool", err)
	}
}

func TestUsageErrors(t *testing.T) {
	path := setup(t, desiredState)
	tests := map[string]struct {
		args    []string
		wantErr string
	}{
		"no command":         {wantErr: "missing command"},
		"unknown command":    {args: []string{"destroy"}, wantErr: `unknown command "destroy"`},
		"no file":            {args: []string{"validate"}, wantErr: "expected the desired state file"},
		"two files":          {args: []string{"plan", path, path}, wantErr: "expected the desired state file"},
		"unknown flag":       {args: []string{"apply", "-force", path}, wantErr: "flag provided but not defined"},
		"relative URL":       {args: []string{"plan", "-url", "localhost", path}, wantErr: `-url: "localhost" is not an absolute URL`},
		"unreachable API":    {args: []string{"plan", "-url", "http://127.0.0.1:1", "-wait", "10ms", path}, wantErr: "waiting for garm"},
		"missing state file": {args: []string{"apply", filepath.Join(t.TempDir(), "missing.yaml")}, wantErr: "no such file"},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			_, _, err := execute(t, tt.args...)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error = %v, want it to contain %q", err, tt.wantErr)
			}
		})
	}
}

func TestHelp(t *testing.T) {
	if stdout := mustExecute(t, "help"); !strings.HasPrefix(stdout, "Usage: garm-reconcile") {
		t.Errorf("help printed %q, want the usage", stdout)
	}
	_, stderr, err := execute(t, "apply", "-h")
	if !errors.Is(err, flag.ErrHelp) || !strings.Contains(stderr, "-drain-timeout") {
		t.Errorf("apply -h: error = %v, output = %q, want the flags of apply", err, stderr)
	}
	// The error is left to the caller to print.
	_, stderr, err = execute(t, "apply", "-force", "garm.yaml")
	if err == nil || stderr != "" {
		t.Errorf("apply -force: error = %v, output = %q, want only an error", err, stderr)
	}
}

func TestPlanAndApply(t *testing.T) {
	srv := garmtest.NewServer(t)
	path := setup(t, desiredState)

	stdout := mustExecute(t, "plan", "-url", srv.URL, path)
	if !strings.Contains(stdout, "garm is not initialized") {
		t.Errorf("plan on a new controller printed %q", stdout)
	}

	stdout = mustExecute(t, "apply", "-url", srv.URL, path)
	want := `create admin user admin
update controller (agent_url, callback_url, metadata_url)
create github credentials main
create github organization my-org
create github pool my-org/coco
`
	if stdout != want {
		t.Errorf("apply printed:\n%s\nwant:\n%s", stdout, want)
	}
	srv.State(func(st *garmtest.State) {
		if st.Admin.Password != "hunter2" || st.Credentials[0].PAT.OAuth2Token != "ghp_token" {
			t.Errorf("secrets = %q and %q, want the contents of the secret files",
				st.Admin.Password, st.Credentials[0].PAT.OAuth2Token)
		}
	})

	stdout = mustExecute(t, "plan", "-url", srv.URL, "-prune", path)
	if stdout != "garm matches the desired state\n" {
		t.Errorf("plan after apply printed %q", stdout)
	}

	path = setup(t, withoutPool)
	if stdout := mustExecute(t, "apply", "-url", srv.URL, path); stdout != "" {
		t.Errorf("apply without -prune printed %q, want no changes", stdout)
	}
	if stdout := mustExecute(t, "plan", "-url", srv.URL, "-prune", path); stdout != "delete github pool my-org/coco\n" {
		t.Errorf("plan -prune printed %q", stdout)
	}
	if stdout := mustExecute(t, "apply", "-url", srv.URL, "-prune", path); stdout != "delete github pool my-org/coco\n" {
		t.Errorf("apply -prune printed %q", stdout)
	}
	srv.State(func(st *garmtest.State) {
		if len(st.Pools) != 0 {
			t.Errorf("pools = %+v, want none", st.Pools)
		}
	})
}

// TestApplyNotifiesSystemd checks that the service is reported as started
// once the declared state is in place, while pruning still waits for a job.
func TestApplyNotifiesSystemd(t *testing.T) {
	srv := garmtest.NewServer(t)
	mustExecute(t, "apply", "-url", srv.URL, setup(t, desiredState))
	srv.State(func(st *garmtest.State) {
		st.Instances = []*params.Instance{{
			Name: "coco-busy", PoolID: st.Pools[0].ID,
			Status: commonparams.InstanceRunning, RunnerStatus: params.RunnerActive,
		}}
	})
	path := setup(t, withoutPool)

	// An abstract socket works where the path of the temporary directory
	// is too long for a socket address.
	socket := "@garm-reconcile-" + rand.Text()
	conn, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: socket, Net: "unixgram"})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	t.Setenv("NOTIFY_SOCKET", socket)

	// The job finishes when systemd is told what apply waits for.
	notifications := make(chan string, 3)
	go func() {
		defer close(notifications)
		buf := make([]byte, 256)
		for range 3 {
			if err := conn.SetReadDeadline(time.Now().Add(10 * time.Second)); err != nil {
				return
			}
			n, err := conn.Read(buf)
			if err != nil {
				return
			}
			notification := string(buf[:n])
			if strings.HasPrefix(notification, "STATUS=waiting") {
				srv.State(func(st *garmtest.State) { st.Instances = nil })
			}
			notifications <- notification
		}
	}()

	stdout, stderr, err := execute(t, "apply", "-url", srv.URL, "-prune", path)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if stdout != "delete github pool my-org/coco\n" {
		t.Errorf("apply printed %q", stdout)
	}
	if stderr != "waiting for the runners of my-org/coco\n" {
		t.Errorf("apply reported %q as progress", stderr)
	}
	var got []string
	for notification := range notifications {
		got = append(got, notification)
	}
	want := []string{"READY=1", "STATUS=waiting for the runners of my-org/coco", "STATUS="}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("notifications = %q, want %q", got, want)
	}
}

func TestApplyDrainTimeout(t *testing.T) {
	srv := garmtest.NewServer(t)
	mustExecute(t, "apply", "-url", srv.URL, setup(t, desiredState))
	srv.State(func(st *garmtest.State) {
		st.Instances = []*params.Instance{{
			Name: "coco-busy", PoolID: st.Pools[0].ID,
			Status: commonparams.InstanceRunning, RunnerStatus: params.RunnerActive,
		}}
	})

	_, _, err := execute(t, "apply", "-url", srv.URL, "-prune", "-drain-timeout", "50ms", setup(t, withoutPool))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("apply = %v, want it to stop waiting for the busy runner", err)
	}
}
