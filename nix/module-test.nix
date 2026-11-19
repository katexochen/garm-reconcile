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

# Checks that the NixOS module applies the desired state with a service that
# gets its secrets as systemd credentials. What garm-reconcile does with a
# controller is covered by test.nix.

{ module, ... }:

{
  name = "garm-reconcile-module";

  nodes.machine =
    { pkgs, ... }:
    {
      imports = [ module ];

      services.garm = {
        enable = true;
        settings = {
          jwt_auth.secret._secret = "/etc/garm-jwt-secret";
          database.passphrase._secret = "/etc/garm-db-passphrase";
        };
        provision = {
          enable = true;
          prune = true;
          settings = {
            admin = {
              username = "admin";
              email = "admin@example.com";
              password._secret = "/etc/garm-admin-password";
            };
            controller = {
              metadata_url = "http://127.0.0.1:9997/api/v1/metadata";
              callback_url = "http://127.0.0.1:9997/api/v1/callbacks";
              agent_url = "http://127.0.0.1:9997/agent";
              webhook_url = "http://127.0.0.1:9997/webhooks";
            };
            gitea.endpoints.local = {
              api_base_url = "http://127.0.0.1:3000";
              base_url = "http://127.0.0.1:3000";
              # A file from the store works like any other.
              description._secret = pkgs.writeText "description" "read from the store";
            };
          };
        };
      };

      environment.etc."garm-jwt-secret".text = "uhAVE3d5hiA4f93yPJcVo5xRbvUSbvVUSLGC8tG8WTzxtqtS";
      environment.etc."garm-db-passphrase".text = "6ahIZPHobW5SvKR92a8nrahCLsZvWBnD";
      # Only root can read the password, the service runs as another user.
      environment.etc."garm-admin-password" = {
        text = "CbIhvLQTXCCfVROvLcJ2B7A85BGWbasV";
        mode = "0400";
      };
    };

  testScript = ''
    machine.wait_for_unit("garm-provision.service")
    journal = machine.succeed("journalctl --no-pager -o cat -u garm-provision")
    assert "create gitea endpoint local" in journal, f"unexpected journal: {journal}"
    machine.succeed(
        "garm-cli profile add --name garm --url http://127.0.0.1:9997"
        " --username admin --password CbIhvLQTXCCfVROvLcJ2B7A85BGWbasV"
    )
    endpoint = machine.succeed("garm-cli gitea endpoint show local")
    assert "read from the store" in endpoint, f"unexpected endpoint: {endpoint}"
  '';
}
