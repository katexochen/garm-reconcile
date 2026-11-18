package main

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// providerScript is an external provider without a backend. Garm requires
// pools to reference a configured provider, but never asks this one for
// instances as the pools of the test keep no idle runners.
const providerScript = `#!/bin/sh
case "$GARM_COMMAND" in
  ListInstances) echo '[]' ;;
  *) echo '{}' ;;
esac
`

const garmConfig = `
[jwt_auth]
secret = "uhAVE3d5hiA4f93yPJcVo5xRbvUSbvVUSLGC8tG8WTzxtqtS"
time_to_live = "24h"

[apiserver]
bind = "127.0.0.1"
port = %[1]d
use_tls = false

[database]
backend = "sqlite3"
passphrase = "6ahIZPHobW5SvKR92a8nrahCLsZvWBnD"

[database.sqlite3]
db_file = "%[2]s/garm.db"

[[provider]]
name = "incus"
provider_type = "external"
description = "provider without a backend"

[provider.external]
provider_executable = "%[2]s/provider.sh"
config_file = "%[2]s/provider.toml"
`

// startGarm runs a garm controller for the duration of the test and returns
// its URL. The test is skipped if garm is not installed.
func startGarm(t *testing.T) string {
	t.Helper()
	garm, err := exec.LookPath("garm")
	if err != nil {
		t.Skip("garm is not installed")
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()

	dir := t.TempDir()
	writeFile(t, dir, "provider.toml", "")
	if err := os.WriteFile(filepath.Join(dir, "provider.sh"), []byte(providerScript), 0o700); err != nil {
		t.Fatal(err)
	}
	config := writeFile(t, dir, "config.toml", fmt.Sprintf(garmConfig, port, dir))

	var log strings.Builder
	cmd := exec.Command(garm, "-config", config)
	cmd.Stdout, cmd.Stderr = &log, &log
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		if t.Failed() {
			t.Logf("garm log:\n%s", log.String())
		}
	})
	return fmt.Sprintf("http://127.0.0.1:%d", port)
}

const integrationState = `
admin:
  username: admin
  email: admin@example.com
  password: {_secret: admin-password}
controller:
  metadata_url: %[1]s/api/v1/metadata
  callback_url: %[1]s/api/v1/callbacks
  agent_url: %[1]s/agent
  webhook_url: %[1]s/webhooks
github:
  credentials:
    main:
      description: created by the integration test
      endpoint: github.com
      pat:
        oauth2_token: {_secret: github-token}
  templates:
    custom:
      description: runner install script
      os_type: linux
      data: |
        #!/bin/bash
        echo install
  repositories:
    owner/repo:
      credentials_name: main
      webhook_secret: {_secret: github-token}
  organizations:
    my-org:
      credentials_name: main
      pool_balancer_type: pack
      pools:
        coco:
          provider_name: incus
          image: ubuntu
          flavor: default
          os_type: linux
          os_arch: amd64
          tags: [coco, linux]
          max_runners: 2
          extra_specs:
            disable_updates: true
            extra_packages: [jq]
        plain:
          provider_name: incus
          image: ubuntu
          flavor: default
          os_type: linux
          os_arch: amd64
          tags: [plain]
          max_runners: 1
          template: custom
          enabled: true
          priority: 5
  enterprises:
    corp:
      credentials_name: main
gitea:
  endpoints:
    local:
      api_base_url: http://127.0.0.1:1/api/v1
      base_url: http://127.0.0.1:1
      tools_metadata_url: http://127.0.0.1:1/releases.json
  credentials:
    main:
      endpoint: local
      pat:
        oauth2_token: {_secret: github-token}
  repositories:
    owner/repo:
      credentials_name: main
      pools:
        coco:
          provider_name: incus
          image: ubuntu
          flavor: default
          os_type: linux
          os_arch: amd64
          tags: [coco]
          max_runners: 1
`

// TestAgainstGarm reconciles a real garm controller. It verifies what the
// fake of the unit tests can only assume: the API paths and payloads, and
// that garm reports a reconciled state the way it was declared.
func TestAgainstGarm(t *testing.T) {
	url := startGarm(t)

	// reconcile applies a desired state and checks that garm matches it
	// afterwards.
	reconcile := func(state, wantChanges string) {
		t.Helper()
		// The password must pass the strength check of garm.
		credentials := t.TempDir()
		writeFile(t, credentials, "admin-password", "CbIhvLQTXCCfVROvLcJ2B7A85BGWbasV\n")
		writeFile(t, credentials, "github-token", "ghp_0000000000000000000000000000000000\n")
		t.Setenv("CREDENTIALS_DIRECTORY", credentials)
		t.Setenv("NOTIFY_SOCKET", "")
		path := writeFile(t, t.TempDir(), "garm.yaml", state)

		if got := mustExecute(t, "apply", "-url", url, "-prune", path); got != wantChanges {
			t.Errorf("apply printed:\n%s\nwant:\n%s", got, wantChanges)
		}
		if got := mustExecute(t, "plan", "-url", url, "-prune", path); got != "garm matches the desired state\n" {
			t.Errorf("plan after apply printed:\n%s", got)
		}
		if got := mustExecute(t, "apply", "-url", url, "-prune", path); got != "" {
			t.Errorf("second apply printed:\n%s", got)
		}
	}
	state := fmt.Sprintf(integrationState, url)
	change := func(old, new string) {
		t.Helper()
		if !strings.Contains(state, old) {
			t.Fatalf("the desired state does not contain %q", old)
		}
		state = strings.Replace(state, old, new, 1)
	}

	reconcile(state, `create admin user admin
update controller (agent_url, callback_url, metadata_url, webhook_url)
create github credentials main
create github template custom
create github repository owner/repo
create github organization my-org
create github pool my-org/coco
create github pool my-org/plain
create github enterprise corp
create gitea endpoint local
create gitea credentials main
create gitea repository owner/repo
create gitea pool owner/repo/coco
`)

	change("description: created by the integration test", "description: updated")
	change("      pool_balancer_type: pack\n", "      agent_mode: true\n")
	change("          tags: [coco, linux]\n          max_runners: 2\n", `          tags: [coco]
          max_runners: 4
          enabled: true
          enable_shell: true
          runner_bootstrap_timeout: 30
          github-runner-group: group
`)
	change("            disable_updates: true\n", "")
	change("          enabled: true\n          priority: 5\n", "")
	change("        echo install\n", "        echo changed\n")
	change("      tools_metadata_url: http://127.0.0.1:1/releases.json\n", "      description: local Gitea\n")
	reconcile(state, `update github credentials main (description)
update github template custom (data)
update github organization my-org (pool_balancer_type, agent_mode)
update github pool my-org/coco (enable_shell, enabled, extra_specs, github-runner-group, max_runners, runner_bootstrap_timeout, tags)
update github pool my-org/plain (enabled, priority)
update gitea endpoint local (description, tools_metadata_url)
`)

	// Renaming the credentials replaces them. The rest gets deleted,
	// including everything on Gitea.
	state = strings.ReplaceAll(state, "credentials_name: main", "credentials_name: renamed")
	change("    main:\n", "    renamed:\n")
	state = state[:strings.Index(state, "        plain:")]
	state = state[:strings.Index(state, "  templates:")] + state[strings.Index(state, "  organizations:"):]
	reconcile(state, `create github credentials renamed
update github organization my-org (credentials_name)
delete github pool my-org/plain
delete gitea pool owner/repo/coco
delete github repository owner/repo
delete github enterprise corp
delete github template custom
delete github credentials main
delete gitea repository owner/repo
delete gitea credentials main
delete gitea endpoint local
`)

	// This controller is not configured to manage webhooks.
	change("    my-org:\n", "    my-org:\n      install_webhook: true\n")
	path := writeFile(t, t.TempDir(), "garm.yaml", state)
	_, _, err := execute(t, "apply", "-url", url, path)
	if err == nil || !strings.Contains(err.Error(), "garm is not configured to manage webhooks") {
		t.Errorf("apply with a webhook = %v, want it to report that garm does not manage webhooks", err)
	}
}
