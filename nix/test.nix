# Copyright 2026 Paul Meyer
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

# Runs garm-reconcile against GARM with a local Gitea as the forge and Incus
# as the provider, so that pools get real runners that execute real jobs. The
# setup follows the garm-incus test of nixpkgs: everything runs offline, with
# a stub in place of the release feed of the Gitea runner.
#
# Each scenario is a desired state in `states`, which the test applies by
# calling garm-reconcile.

{
  lib,
  pkgs,
  garm-reconcile,
  ...
}:

let
  releases = import "${pkgs.path}/nixos/release.nix" {
    configuration =
      { pkgs, ... }:
      {
        documentation.enable = lib.mkForce false;
        documentation.nixos.enable = lib.mkForce false;
        system.installer.channel.enable = lib.mkForce false;
        environment.etc."nix/registry.json".text = lib.mkForce "{}";

        # GARM passes the runner install script as cloud-init user data.
        services.cloud-init.enable = true;
        services.cloud-init.settings.datasource_list = [ "LXD" ];

        systemd.tmpfiles.rules = [
          "L /dev/lxd - - - - /dev/incus"
          "L+ /bin/bash - - - - ${lib.getExe pkgs.bash}"
        ];

        users.groups.runner = { };
        users.users.runner = {
          isNormalUser = true;
          group = "runner";
          extraGroups = [ "wheel" ];
          shell = pkgs.bash;
        };
        security.sudo.wheelNeedsPassword = false;

        systemd.settings.Manager.DefaultEnvironment = "PATH=/run/current-system/sw/bin";

        environment.systemPackages = [
          pkgs.gitea-actions-runner
          pkgs.curl
        ];
      };
  };
  system = pkgs.stdenv.hostPlatform.system;
  incusImageMeta =
    releases.incusContainerMeta.${system} + "/tarball/nixos-image-lxc-*-${system}.tar.xz";
  incusImageRoot = releases.incusContainerImage.${system} + "/nixos-lxc-image-${system}.squashfs";

  # The host as seen from the runner containers.
  hostIP = "10.0.10.1";
  garmURL = "http://${hostIP}:9997";

  # /etc/systemd/system is read-only on NixOS, so the runner unit goes to
  # /run and is started without being enabled.
  runnerInstallTemplate =
    pkgs.runCommand "gitea-runner-install-nixos.tmpl" { inherit (pkgs.garm) src; }
      ''
        sed -e 's|/etc/systemd/system|/run/systemd/system|g' \
            -e 's|systemctl enable --now|systemctl start|g' \
            $src/internal/templates/userdata/gitea_linux_userdata.tmpl > $out
        grep -q /run/systemd/system $out
      '';

  # GARM replaces a runner that is gone when it next checks on its runners,
  # which it only does every five minutes.
  garm = pkgs.garm.overrideAttrs (old: {
    postPatch = old.postPatch or "" + ''
      substituteInPlace runner/common/pool.go \
        --replace-fail "PoolReapTimeoutInterval   = 5 * time.Minute" "PoolReapTimeoutInterval   = 10 * time.Second"
    '';
  });

  adminPassword = "CbIhvLQTXCCfVROvLcJ2B7A85BGWbasV";

  pool = {
    provider_name = "incus";
    image = "nixos";
    flavor = "default";
    os_type = "linux";
    os_arch = "amd64";
    tags = [ "nixos-test" ];
    template = "nixos";
    min_idle_runners = 1;
    max_runners = 1;
    enabled = true;
  };

  template = {
    os_type = "linux";
    data._secret = "${runnerInstallTemplate}";
  };

  repository = {
    credentials_name = "test";
    # GARM learns from the webhook which runner executes a job. Without
    # it, it takes a busy runner for idle and removes it on request, as
    # Gitea does not refuse to delete a runner that executes a job.
    install_webhook = true;
    pools.first = pool;
  };

  base = {
    admin = {
      username = "admin";
      email = "admin@example.com";
      password._secret = "/etc/garm-admin-password";
    };
    controller = {
      metadata_url = "${garmURL}/api/v1/metadata";
      callback_url = "${garmURL}/api/v1/callbacks";
      agent_url = "${garmURL}/agent";
      webhook_url = "${garmURL}/webhooks";
    };
    gitea = {
      endpoints.local = {
        api_base_url = "http://${hostIP}:3000";
        base_url = "http://${hostIP}:3000";
        tools_metadata_url = "http://${hostIP}:8080/releases.json";
      };
      # The token is created by the test.
      credentials.test = {
        endpoint = "local";
        pat.oauth2_token._secret = "/run/gitea-token";
      };
      templates.nixos = template;
      repositories."test/test" = repository;
    };
  };

  # The base state with some sections of gitea replaced.
  withGitea = sections: base // { gitea = base.gitea // sections; };
  withPools =
    pools:
    withGitea {
      repositories."test/test" = repository // {
        inherit pools;
      };
    };

  movedPool = pool // {
    provider_name = "incus-other";
  };

  # The sections of gitea after its credentials got another name.
  renamed = {
    credentials.renamed = base.gitea.credentials.test;
    repositories."test/test" = repository // {
      credentials_name = "renamed";
      pools.second = movedPool;
    };
  };

  states = lib.mapAttrs (name: pkgs.writers.writeJSON "${name}.json") {
    initial = base;
    # Settings that garm applies to a pool in place.
    updated = withPools {
      first = pool // {
        tags = [
          "nixos-test"
          "updated"
        ];
        priority = 5;
        runner_bootstrap_timeout = 30;
      };
    };
    retemplated = withGitea {
      templates.other = template;
      repositories."test/test" = repository // {
        pools.first = pool // {
          template = "other";
        };
      };
    };
    replaced = withPools { second = pool; };
    # Two runners, of which the test keeps one busy.
    widened = withPools {
      second = pool // {
        min_idle_runners = 2;
        max_runners = 2;
      };
    };
    moved = withPools { second = movedPool; };
    renamed = withGitea renamed;
    relocated = withGitea (
      renamed
      // {
        endpoints.local = base.gitea.endpoints.local // {
          base_url = "http://${hostIP}:3001";
        };
      }
    );
    adopted = withGitea (
      renamed
      // {
        repositories."test/test" = renamed.repositories."test/test" // {
          pools = {
            second = movedPool;
            adopted = pool;
          };
        };
      }
    );
    # Nothing but the controller itself.
    empty = removeAttrs base [ "gitea" ];
  };
in

{
  name = "garm-reconcile";

  nodes.machine =
    { pkgs, ... }:
    {
      virtualisation = {
        cores = 2;
        memorySize = 4096;
        diskSize = 8192;
        incus = {
          enable = true;
          preseed = {
            networks = [
              {
                name = "incusbr0";
                type = "bridge";
                config = {
                  "ipv4.address" = "${hostIP}/24";
                  "ipv4.nat" = "true";
                };
              }
            ];
            profiles = [
              {
                name = "default";
                devices = {
                  eth0 = {
                    name = "eth0";
                    network = "incusbr0";
                    type = "nic";
                  };
                  root = {
                    path = "/";
                    pool = "default";
                    type = "disk";
                  };
                };
              }
            ];
            storage_pools = [
              {
                name = "default";
                driver = "dir";
              }
            ];
          };
        };
      };
      networking.nftables.enable = true;
      networking.firewall.trustedInterfaces = [ "incusbr0" ];

      services.gitea = {
        enable = true;
        settings.server = {
          HTTP_ADDR = "0.0.0.0";
          ROOT_URL = "http://${hostIP}:3000/";
        };
        settings.service.DISABLE_REGISTRATION = true;
        settings.actions.ENABLED = true;
        # Gitea does not send webhooks to private addresses by default.
        settings.security.ALLOWED_HOST_LIST = hostIP;
      };

      # Stub for the release feed and download server of the Gitea runner.
      services.nginx = {
        enable = true;
        virtualHosts.tools = {
          listen = [
            {
              addr = "0.0.0.0";
              port = 8080;
            }
          ];
          root = pkgs.runCommand "garm-tools-stub" { } ''
            mkdir $out
            ln -s ${lib.getExe pkgs.gitea-actions-runner} $out/gitea-runner
            ln -s ${
              pkgs.writers.writeJSON "releases.json" [
                {
                  tag_name = "v${pkgs.gitea-actions-runner.version}";
                  name = "v${pkgs.gitea-actions-runner.version}";
                  assets = [
                    {
                      name = "gitea-runner-v${pkgs.gitea-actions-runner.version}-linux-amd64";
                      browser_download_url = "http://${hostIP}:8080/gitea-runner";
                    }
                  ];
                }
              ]
            } $out/releases.json
          '';
        };
      };

      services.garm = {
        enable = true;
        package = garm;
        settings = {
          default.enable_webhook_management = true;
          apiserver.bind = "0.0.0.0";
          jwt_auth.secret._secret = "/etc/garm-jwt-secret";
          database.passphrase._secret = "/etc/garm-db-passphrase";
          # Two names for the same provider, to move a pool between them.
          provider =
            map
              (name: {
                inherit name;
                description = "Incus external provider";
                provider_type = "external";
                external = {
                  provider_executable = lib.getExe pkgs.garm-provider-incus;
                  config_file = pkgs.writers.writeTOML "incus-provider.toml" {
                    unix_socket_path = "/var/lib/incus/unix.socket";
                    project_name = "default";
                    instance_type = "container";
                    include_default_profile = false;
                    image_remotes.images = {
                      addr = "https://127.0.0.1";
                      public = true;
                      protocol = "simplestreams";
                    };
                  };
                };
              })
              [
                "incus"
                "incus-other"
              ];
        };

      };
      systemd.services.garm.serviceConfig.SupplementaryGroups = [ "incus-admin" ];
      environment.etc."garm-jwt-secret".text = "uhAVE3d5hiA4f93yPJcVo5xRbvUSbvVUSLGC8tG8WTzxtqtS";
      environment.etc."garm-db-passphrase".text = "6ahIZPHobW5SvKR92a8nrahCLsZvWBnD";
      environment.etc."garm-admin-password".text = adminPassword;

      environment.systemPackages = [
        garm-reconcile
        pkgs.gitea
      ];
    };

  testScript = ''
    import base64
    import json
    from datetime import timedelta

    states = json.loads('${builtins.toJSON states}')

    # A workflow that runs once, when it is pushed.
    def workflow(name, command):
        return base64.b64encode(f"""
    on:
      push:
        paths: [.gitea/workflows/{name}.yaml]
    jobs:
      test:
        runs-on: nixos-test
        steps:
          - run: {command}
    """.encode()).decode()

    def gitea_api(method, path, body=None):
        data = f" -H 'Content-Type: application/json' -d '{json.dumps(body)}'" if body is not None else ""
        out = machine.succeed(
            f"curl --fail --silent --show-error -X {method} http://localhost:3000/api/v1/{path}"
            f" -u test:totallysafe{data}"
        )
        return json.loads(out) if out.strip() else None

    def new_token(name):
        token = gitea_api("POST", "users/test/tokens", {"name": name, "scopes": ["all"]})["sha1"]
        machine.succeed(f"install -m 0600 /dev/stdin /run/gitea-token <<< '{token}'")

    machine.wait_for_unit("incus-preseed.service")
    machine.succeed("incus image import ${incusImageMeta} ${incusImageRoot} --alias nixos")

    machine.wait_for_unit("gitea.service")
    machine.wait_for_open_port(3000)
    machine.succeed(
        "su -l gitea -c 'GITEA_WORK_DIR=/var/lib/gitea gitea admin user create"
        " --username test --password totallysafe --email test@localhost'"
    )
    new_token("first")
    gitea_api("POST", "user/repos", {"name": "test", "auto_init": True})
    machine.wait_for_unit("garm.service")

    def reconcile(command, state, *flags):
        return machine.succeed(f"garm-reconcile {command} {' '.join(flags)} {states[state]}").splitlines()

    def expect(what, got, want):
        assert got == want, f"{what}: got {got}, want {want}"

    def garm(command):
        # An empty list is printed as null.
        return json.loads(machine.succeed(f"garm-cli {command} --format json")) or []

    def pools():
        return {p["runner_prefix"]: p for p in garm("pool list")}

    def pool(prefix):
        return garm(f"pool show {pools()[prefix]['id']}")

    def runners(prefix):
        return [r for r in garm("runner list") if r["name"].startswith(prefix + "-")]

    def pool_runners(pool_id):
        return [r["name"] for r in garm("runner list") if r.get("pool_id") == pool_id]

    def instances():
        return machine.succeed("incus list -c n --format csv").split()

    def push(name, command):
        gitea_api("POST", f"repos/test/test/contents/.gitea/workflows/{name}.yaml", {
            "content": workflow(name, command),
            "message": f"add {name}",
        })

    def job_status(name):
        tasks = gitea_api("GET", "repos/test/test/actions/tasks")["workflow_runs"]
        status = [t["status"] for t in tasks if t["workflow_id"] == f"{name}.yaml"]
        assert "failure" not in status, f"job {name} failed"
        return status

    def dump_diagnostics():
        machine.log(machine.execute("journalctl --no-pager -u garm -n 60")[1])
        machine.log(machine.execute("garm-cli runner list; garm-cli pool list; incus list")[1])
        machine.log(machine.execute("curl --silent -u test:totallysafe http://localhost:3000/api/v1/repos/test/test/actions/tasks")[1])

    def wait_until(description, condition):
        try:
            with machine.nested(description):
                retry(lambda _: condition(), timeout=timedelta(minutes=10))
        except Exception:
            dump_diagnostics()
            raise

    # Runners that took a job. Gitea does not tell which runner finished a
    # job, so garm reports them as idle until it next checks on its runners.
    used = set()

    def unused_runner(prefix):
        status = {r["name"]: r["runner_status"] for r in runners(prefix) if r["name"] not in used}
        assert "failed" not in status.values(), f"a runner of {prefix} failed"
        return [name for name in status if status[name] == "idle"]

    def start_job(name, prefix, command):
        wait_until(f"waiting for a runner of {prefix}", lambda: unused_runner(prefix))
        used.update(unused_runner(prefix))
        push(name, command)

    def run_job(name, prefix):
        start_job(name, prefix, "echo it works")
        wait_until(f"waiting for the job {name}", lambda: job_status(name) == ["success"])

    with subtest("a new controller is set up as declared"):
        expect("plan", reconcile("plan", "initial"), [
            "create admin user admin",
            "garm is not initialized, apply creates everything that is declared",
        ])
        expect("apply", reconcile("apply", "initial"), [
            "create admin user admin",
            "update controller (agent_url, callback_url, metadata_url, webhook_url)",
            "create gitea endpoint local",
            "create gitea credentials test",
            "create gitea template nixos",
            "create gitea repository test/test",
            "create gitea webhook test/test",
            "create gitea pool test/test/first",
        ])
        # The URL of the webhook ends in the ID of the controller.
        hooks = [h["config"]["url"] for h in gitea_api("GET", "repos/test/test/hooks")]
        assert len(hooks) == 1 and hooks[0].startswith("${garmURL}/webhooks/"), f"unexpected webhooks: {hooks}"
        machine.succeed(
            "garm-cli profile add --name garm --url http://127.0.0.1:9997"
            " --username admin --password ${adminPassword}"
        )
        expect("pools", list(pools()), ["first"])
        expect("template of the pool", pool("first")["template_name"], "nixos")

    with subtest("reconciling again changes nothing"):
        expect("plan", reconcile("plan", "initial", "-prune"), ["garm matches the desired state"])
        expect("apply", reconcile("apply", "initial", "-prune"), [])

    with subtest("the pool runs a job"):
        run_job("first-job", "first")

    with subtest("a pool is updated in place"):
        before = pools()["first"]
        change = "update gitea pool test/test/first (priority, runner_bootstrap_timeout, tags)"
        expect("plan", reconcile("plan", "updated"), [change])
        expect("pool after plan", pools()["first"], before)
        expect("apply", reconcile("apply", "updated"), [change])
        expect("plan after apply", reconcile("plan", "updated", "-prune"), ["garm matches the desired state"])
        after = pool("first")
        expect("ID of the pool", after["id"], before["id"])
        expect("tags", sorted(t["name"] for t in after["tags"]), ["nixos-test", "updated"])
        expect("priority", after["priority"], 5)
        expect("bootstrap timeout", after["runner_bootstrap_timeout"], 30)

        # What is no longer declared goes back to garm's default.
        expect("apply of the initial state", reconcile("apply", "initial"), [
            "update gitea pool test/test/first (priority, runner_bootstrap_timeout, tags)",
        ])
        expect("bootstrap timeout", pool("first")["runner_bootstrap_timeout"], 20)

    with subtest("a template is replaced"):
        expect("apply without prune", reconcile("apply", "retemplated"), [
            "create gitea template other",
            "update gitea pool test/test/first (template_id)",
        ])
        expect("template of the pool", pool("first")["template_name"], "other")
        expect("templates", sorted(t["name"] for t in garm("template list") if t["owner_id"] != "system"), ["nixos", "other"])
        expect("apply with prune", reconcile("apply", "retemplated", "-prune"), ["delete gitea template nixos"])
        expect("apply of the initial state", reconcile("apply", "initial", "-prune"), [
            "create gitea template nixos",
            "update gitea pool test/test/first (template_id)",
            "delete gitea template other",
        ])

    with subtest("without prune, nothing is deleted"):
        expect("plan", reconcile("plan", "empty"), ["garm matches the desired state"])
        expect("apply", reconcile("apply", "empty"), [])
        expect("pools", list(pools()), ["first"])
        expect("pool enabled", pools()["first"]["enabled"], True)

    with subtest("a rotated token is used"):
        gitea_api("DELETE", "users/test/tokens/first")
        new_token("second")
        # GARM keeps secrets to itself, so there is no change to report.
        expect("apply", reconcile("apply", "initial"), [])
        # Replacing a runner takes a token that works, to remove the old
        # one from Gitea and to register the new one.
        wait_until("waiting for an idle runner", lambda: unused_runner("first"))
        old = unused_runner("first")
        for name in old:
            # GARM refuses while the instance is not reported as running.
            wait_until(f"deleting {name}", lambda: machine.execute(f"garm-cli runner delete {name}")[0] == 0)
        wait_until("waiting for a new runner", lambda: set(unused_runner("first")) - set(old))

    with subtest("a replaced pool finishes its job before it is deleted"):
        # The job runs until the test releases it.
        start_job("slow", "first", "until [ -e /run/release ]; do sleep 1; done")
        wait_until("waiting for the job to start", lambda: job_status("slow") == ["running"])
        wait_until("waiting for garm to see the job", lambda: [r["runner_status"] for r in runners("first")] == ["active"])
        busy = [r["name"] for r in runners("first")]

        expect("plan", reconcile("plan", "replaced", "-prune"), [
            "create gitea pool test/test/second",
            "delete gitea pool test/test/first",
        ])
        expect("pools after plan", list(pools()), ["first"])
        expect("pool enabled after plan", pools()["first"]["enabled"], True)

        # The job outlasts the time garm-reconcile may wait for it. The
        # pool stays disabled, for another run to delete it.
        output = machine.fail(f"garm-reconcile apply -prune -drain-timeout 5s {states['replaced']} 2>&1")
        assert "waiting for the runners of test/test/first: context deadline exceeded" in output, output
        expect("pools", sorted(pools()), ["first", "second"])
        expect("old pool enabled", pools()["first"].get("enabled", False), False)
        expect("runners of the old pool", [r["name"] for r in runners("first")], busy)
        expect("job", job_status("slow"), ["running"])

        # As a service that notifies systemd, garm-reconcile counts as
        # started once everything declared exists. It keeps running until
        # the old pool finished its job and is deleted.
        machine.succeed(
            "systemd-run --unit reconcile --property Type=notify --property RemainAfterExit=yes"
            f" garm-reconcile apply -prune {states['replaced']}"
        )
        wait_until(
            "waiting for the status of the service",
            lambda: machine.succeed("systemctl show -p StatusText --value reconcile").strip()
            == "waiting for the runners of test/test/first",
        )
        expect("state of the service", machine.succeed("systemctl show -p SubState --value reconcile").strip(), "running")
        expect("job", job_status("slow"), ["running"])
        expect("runners of the old pool", [r["name"] for r in runners("first")], busy)

        machine.succeed(f"incus exec {busy[0]} -- touch /run/release")
        wait_until("waiting for the job", lambda: job_status("slow") == ["success"])
        wait_until("waiting for the old pool to be deleted", lambda: list(pools()) == ["second"])
        wait_until(
            "waiting for garm-reconcile to exit",
            lambda: machine.succeed("systemctl show -p SubState --value reconcile").strip() == "exited",
        )
        expect("result of the service", machine.succeed("systemctl show -p Result --value reconcile").strip(), "success")
        journal = machine.succeed("journalctl --no-pager -o cat -u reconcile").splitlines()
        assert "delete gitea pool test/test/first" in journal, f"unexpected journal: {journal}"
        run_job("on-second-pool", "second")
        assert all(i.startswith("second-") for i in instances()), f"unexpected instances: {instances()}"

    with subtest("a pool moves to another provider, its idle runner goes first"):
        expect("apply", reconcile("apply", "widened"), [
            "update gitea pool test/test/second (max_runners, min_idle_runners)",
        ])
        wait_until("waiting for two runners", lambda: len(unused_runner("second")) == 2)
        start_job("held", "second", "until [ -e /run/release ]; do sleep 1; done")
        wait_until(
            "waiting for garm to see the job",
            lambda: sorted(r["runner_status"] for r in runners("second")) == ["active", "idle"],
        )
        busy = [r["name"] for r in runners("second") if r["runner_status"] == "active"]
        old_pool = pools()["second"]["id"]

        # The provider is part of what identifies a pool.
        expect("plan", reconcile("plan", "moved", "-prune"), [
            "create gitea pool test/test/second",
            "delete gitea pool test/test/second",
        ])
        output = machine.fail(f"garm-reconcile apply -prune -drain-timeout 5s {states['moved']} 2>&1")
        assert "waiting for the runners of test/test/second: context deadline exceeded" in output, output
        wait_until("waiting for the idle runner to be removed", lambda: pool_runners(old_pool) == busy)
        expect("job", job_status("held"), ["running"])

        machine.succeed(f"incus exec {busy[0]} -- touch /run/release")
        wait_until("waiting for the job", lambda: job_status("held") == ["success"])
        expect("apply", reconcile("apply", "moved", "-prune"), ["delete gitea pool test/test/second"])
        expect("providers", [p["provider_name"] for p in garm("pool list")], ["incus-other"])
        run_job("on-other-provider", "second")

    with subtest("credentials are renamed"):
        changes = [
            "create gitea credentials renamed",
            "update gitea repository test/test (credentials_name)",
            "delete gitea credentials test",
        ]
        expect("plan", reconcile("plan", "renamed", "-prune"), changes)
        expect("apply", reconcile("apply", "renamed", "-prune"), changes)
        expect("credentials", [c["name"] for c in garm("gitea credentials list")], ["renamed"])
        expect("plan after apply", reconcile("plan", "renamed", "-prune"), ["garm matches the desired state"])
        run_job("with-renamed-credentials", "second")

    with subtest("an endpoint in use keeps its URLs"):
        output = machine.fail(f"garm-reconcile apply {states['relocated']} 2>&1")
        assert "cannot be changed while credentials use the endpoint" in output, output

    with subtest("a pool created by hand is adopted"):
        machine.succeed(
            "garm-cli pool add --repo test/test --provider-name incus --image nixos --flavor default"
            " --tags nixos-test --runner-prefix adopted --max-runners 1 --min-idle-runners 0"
        )
        before = pools()["adopted"]["id"]
        plan = reconcile("plan", "adopted", "-prune")
        assert len(plan) == 1 and plan[0].startswith("update gitea pool test/test/adopted ("), f"unexpected plan: {plan}"
        expect("apply", reconcile("apply", "adopted", "-prune"), plan)
        expect("ID of the pool", pools()["adopted"]["id"], before)
        expect("template of the pool", pool("adopted")["template_name"], "nixos")
        expect("plan after apply", reconcile("plan", "adopted", "-prune"), ["garm matches the desired state"])

    with subtest("everything is removed"):
        changes = reconcile("apply", "empty", "-prune")
        expect("deleted pools", sorted(changes[:2]), [
            "delete gitea pool test/test/adopted",
            "delete gitea pool test/test/second",
        ])
        expect("apply", changes[2:], [
            "delete gitea repository test/test",
            "delete gitea template nixos",
            "delete gitea credentials renamed",
            "delete gitea endpoint local",
        ])
        expect("plan", reconcile("plan", "empty", "-prune"), ["garm matches the desired state"])
        expect("instances", instances(), [])
        expect("repositories", garm("repository list"), [])
        expect("credentials", garm("gitea credentials list"), [])
        expect("endpoints", garm("gitea endpoint list"), [])
  '';
}
