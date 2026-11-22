// Copyright 2026 Paul Meyer
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	commonparams "github.com/cloudbase/garm-provider-common/params"
	"github.com/cloudbase/garm/params"

	"github.com/katexochen/garm-reconcile/internal/garm/garmtest"
	"github.com/katexochen/garm-reconcile/internal/reconcile"
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
  webhook_url: http://garm/webhooks
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
var withoutPool, _, _ = strings.Cut(desiredState, "      pools:")

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
update controller (agent_url, callback_url, metadata_url, webhook_url)
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

func TestExport(t *testing.T) {
	srv := garmtest.NewServer(t)
	path := setup(t, desiredState)
	mustExecute(t, "apply", "-url", srv.URL, path)
	passwordFile := filepath.Join(os.Getenv("CREDENTIALS_DIRECTORY"), "admin-password")
	export := []string{"export", "-url", srv.URL, "-username", "admin", "-password-file", passwordFile}

	const placeholder = "<fill in: the email of the admin user>"
	state, stderr, err := execute(t, export...)
	if want := "The exported state is not valid as it is:\nadmin.email: the placeholder " + placeholder + " has to be replaced\n"; err != nil || stderr != want {
		t.Errorf("export: error = %v, stderr = %q, want %q", err, stderr, want)
	}
	writeFile(t, os.Getenv("CREDENTIALS_DIRECTORY"), "garm-admin-password", "hunter2\n")
	writeFile(t, os.Getenv("CREDENTIALS_DIRECTORY"), "github-main-token", "ghp_token\n")
	for _, command := range []string{"validate", "plan", "apply"} {
		_, _, err := execute(t, command, writeFile(t, t.TempDir(), "garm.yaml", state))
		if err == nil || !strings.Contains(err.Error(), "admin.email: the placeholder") {
			t.Errorf("%s with a placeholder = %v, want it to be rejected", command, err)
		}
	}
	state = strings.Replace(state, placeholder, "admin@example.com", 1)
	exported := writeFile(t, t.TempDir(), "garm.yaml", state)
	if stdout := mustExecute(t, "plan", "-url", srv.URL, "-prune", exported); stdout != "garm matches the desired state\n" {
		t.Errorf("plan with the exported state printed %q", stdout)
	}
	srv.State(func(st *garmtest.State) {
		if n := len(st.Mutations); n != 5 {
			t.Errorf("%d requests changed the controller, want only the 5 of the first apply: %v", n, st.Mutations)
		}
	})

	// What garm has but a state file cannot declare is pointed out.
	srv.State(func(st *garmtest.State) { st.Pools[0].Prefix = "CoCo" })
	state, stderr, err = execute(t, export...)
	if err != nil || !strings.Contains(state, "CoCo:") ||
		!strings.Contains(stderr, "\ngithub.organizations.my-org.pools.CoCo") {
		t.Errorf("export: error = %v, stderr = %q, state:\n%s", err, stderr, state)
	}

	tests := map[string]struct {
		args    []string
		wantErr string
	}{
		"no username":      {args: []string{"export", "-password-file", passwordFile}, wantErr: "export requires both -username and -password-file"},
		"no password file": {args: []string{"export", "-username", "admin"}, wantErr: "export requires both -username and -password-file"},
		"profile and user": {args: append(export, "-profile", "garm"), wantErr: "export requires both -username and -password-file"},
		"no profiles":      {args: []string{"export"}, wantErr: "a garm-cli profile is used: garm-cli has no profiles"},
		"missing file":     {args: []string{"export", "-username", "admin", "-password-file", passwordFile + ".missing"}, wantErr: "-password-file: open"},
		"argument":         {args: append(export, "garm.yaml"), wantErr: "export takes no arguments"},
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

func TestExportWithProfile(t *testing.T) {
	srv := garmtest.NewServer(t)
	path := setup(t, desiredState)
	mustExecute(t, "apply", "-url", srv.URL, path)
	passwordFile := filepath.Join(os.Getenv("CREDENTIALS_DIRECTORY"), "admin-password")
	want := mustExecute(t, "export", "-url", srv.URL, "-username", "admin", "-password-file", passwordFile)

	home := t.TempDir()
	t.Setenv("HOME", home)
	profiles := filepath.Join(home, ".local", "share", "garm-cli")
	if err := os.MkdirAll(profiles, 0o700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, profiles, "config.toml", fmt.Sprintf(`active_manager = "local"

[[manager]]
  name = "other"
  base_url = "http://127.0.0.1:1"
  bearer_token = "other-token"

[[manager]]
  name = "local"
  base_url = %[1]q
  bearer_token = %[2]q

[[manager]]
  name = "expired"
  base_url = %[1]q
  bearer_token = "expired-token"
`, srv.URL, garmtest.Token))

	// Garm does not tell who a token belongs to.
	want = strings.Replace(want, "username: admin", "username: '<fill in: the name of the admin user>'", 1)
	for name, args := range map[string][]string{
		"active profile": {"export"},
		"named profile":  {"export", "-profile", "local"},
	} {
		got, stderr, err := execute(t, args...)
		if err != nil || got != want {
			t.Errorf("%s: error = %v, state:\n%s\nwant:\n%s", name, err, got, want)
		}
		if !strings.Contains(stderr, "admin.username: the placeholder") {
			t.Errorf("%s: stderr = %q, want it to point out the placeholder", name, stderr)
		}
	}

	tests := map[string]struct {
		args    []string
		wantErr string
	}{
		"unknown profile":  {args: []string{"export", "-profile", "missing"}, wantErr: "garm-cli has no profile missing"},
		"rejected token":   {args: []string{"export", "-profile", "expired"}, wantErr: "garm rejected the token of the garm-cli profile expired"},
		"unreachable":      {args: []string{"export", "-profile", "other", "-wait", "10ms"}, wantErr: "waiting for garm"},
		"URL with profile": {args: []string{"export", "-url", srv.URL}, wantErr: "a garm-cli profile has its own URL"},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			_, _, err := execute(t, tt.args...)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error = %v, want it to contain %q", err, tt.wantErr)
			}
		})
	}

	fresh := garmtest.NewServer(t)
	writeFile(t, profiles, "config.toml", fmt.Sprintf("[[manager]]\nname = \"fresh\"\nbase_url = %q\nbearer_token = \"none\"\n", fresh.URL))
	if _, _, err := execute(t, "export", "-profile", "fresh"); !errors.Is(err, reconcile.ErrNotInitialized) {
		t.Errorf("export from a new controller = %v, want %v", err, reconcile.ErrNotInitialized)
	}
	if _, _, err := execute(t, "export"); err == nil || !strings.Contains(err.Error(), "garm-cli has no active profile") {
		t.Errorf("export without an active profile = %v", err)
	}
	writeFile(t, profiles, "config.toml", "[[manager")
	if _, _, err := execute(t, "export"); err == nil || !strings.Contains(err.Error(), "config.toml: toml") {
		t.Errorf("export with a malformed configuration of garm-cli = %v", err)
	}
}

func TestRejectedLoginHasItsOwnExitStatus(t *testing.T) {
	srv := garmtest.NewServer(t)
	path := setup(t, desiredState)
	mustExecute(t, "apply", "-url", srv.URL, path)

	writeFile(t, os.Getenv("CREDENTIALS_DIRECTORY"), "admin-password", "changed\n")
	_, _, err := execute(t, "apply", "-url", srv.URL, path)
	if got := exitStatus(err); got != exitLoginRejected {
		t.Errorf("exit status %d for %v, want %d", got, err, exitLoginRejected)
	}

	_, _, err = execute(t, "apply", "-url", "http://127.0.0.1:1", "-wait", "10ms", path)
	if got := exitStatus(err); err == nil || got != 1 {
		t.Errorf("exit status %d for %v, want 1", got, err)
	}
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
	defer func() { _ = conn.Close() }()
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
