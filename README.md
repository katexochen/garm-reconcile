# garm-reconcile

Declarative configuration for [garm](https://github.com/cloudbase/garm).

garm keeps credentials, repositories, organizations and runner pools in its
database and is configured through its API. garm-reconcile reads the desired
state from a file and makes the necessary API calls until the controller
matches it.

## Usage

```
garm-reconcile validate <file>
garm-reconcile plan  [-url URL] [-wait DURATION] [-prune] <file>
garm-reconcile apply [-url URL] [-wait DURATION] [-prune] [-drain-timeout DURATION] <file>
garm-reconcile export [-url URL] [-wait DURATION] -username NAME -password-file FILE
```

- `validate` checks the file without contacting garm. It does not read
  secrets, so it can run at build time.
- `plan` prints what `apply` would change.
- `apply` creates and updates what is declared. With `-prune` it also deletes
  what is not declared.
- `export` prints what garm has as a desired state file, see
  [Adopting a controller](#adopting-a-controller).

`-url` defaults to `http://127.0.0.1:9997`. All commands but `validate` wait
for the API to come up, for one minute unless `-wait` says otherwise.

## Desired state

The file is YAML or JSON. Field names follow the garm API, except that
resources are keyed by their name and that keys and certificates are given as
PEM text: the private key of an app in `private_key`, CA certificates in
`ca_cert_bundle`.

```yaml
admin:
  username: admin
  email: admin@example.com
  password: { _secret: /run/secrets/garm-admin-password }

controller:
  metadata_url: http://10.0.10.1:9997/api/v1/metadata
  callback_url: http://10.0.10.1:9997/api/v1/callbacks
  agent_url: http://10.0.10.1:9997/agent
  webhook_url: https://garm.example.com/webhooks
  minimum_job_age_backoff: 30 # optional, 0 for no backoff

github:
  credentials:
    cred1:
      endpoint: github.com
      pat:
        oauth2_token: { _secret: /run/secrets/github-token }
      # Alternatively, authenticate as an app:
      # app:
      #   app_id: 1
      #   installation_id: 2
      #   private_key: { _secret: /run/secrets/github-app.pem }

  repositories:
    owner/name:
      credentials_name: cred1
      pools:
        linux:
          provider_name: incus
          image: ubuntu:24.04
          flavor: default
          os_type: linux
          os_arch: amd64
          tags: [self-hosted, linux]
          max_runners: 4
          min_idle_runners: 1
          enabled: true

  organizations:
    my-org:
      credentials_name: cred1
      install_webhook: true
      pools:
        large:
          provider_name: incus
          image: ubuntu:24.04
          flavor: large
          os_type: linux
          os_arch: amd64
          tags: [self-hosted, large]
          max_runners: 2
          enabled: true
          template: custom
      scale_sets:
        linux-set:
          provider_name: incus
          image: ubuntu:24.04
          flavor: default
          os_type: linux
          os_arch: amd64
          max_runners: 4
          enabled: true

  templates:
    custom:
      os_type: linux
      data: { _secret: /etc/garm/runner-install.tmpl }

gitea:
  endpoints:
    local:
      api_base_url: https://gitea.example.com/api/v1
      base_url: https://gitea.example.com
  credentials:
    cred1:
      endpoint: local
      pat:
        oauth2_token: { _secret: /run/secrets/gitea-token }
  repositories:
    owner/name:
      credentials_name: cred1
```

Repositories, organizations and enterprises are the levels at which runners
can be registered. They can be mixed freely: the runners of a repository only
take jobs of that repository, those of an organization take jobs of all its
repositories.

`github` and `gitea` take the same sections. Gitea has no apps, enterprises and
scale sets. Endpoints and templates cannot have the same name on both forges,
as garm keeps each of them in one namespace.

Entities take `credentials_name`, `webhook_secret`, `pool_balancer_type`,
`agent_mode`, `install_webhook`, `pools` and `scale_sets`. A webhook secret is
generated when an entity is created without one.

Pools take the fields of garm's request to create a pool, except for
`runner_prefix` and `template_id`.

### Webhooks

With `install_webhook`, garm installs its webhook in the repository or
organization, and without it, garm's webhook is removed from there. Both
require `enable_webhook_management` in the configuration of garm. Without that
setting, `install_webhook` is an error and existing webhooks are left alone.
garm has no webhooks for enterprises.

A webhook that was added by hand for the `webhook_url` of the controller
counts as installed and is never removed. Where the token of the credentials
may not read webhooks, `install_webhook` is an error, and nothing is removed.

When `webhook_url` changes, garm's webhooks are removed before and installed
again after the change. A changed `webhook_secret` does not reach a webhook
that is installed, as garm never updates one: apply once without
`install_webhook` to have it installed anew.

### Endpoints

An endpoint is an instance of a forge. Credentials name the endpoint they are
for, which has to be declared, except for the `github.com` endpoint that garm
ships. Endpoints take `description`, `api_base_url`, `base_url` and
`ca_cert_bundle`, GitHub endpoints also `upload_base_url`, and Gitea endpoints
`tools_metadata_url`.

garm does not change the URLs of an endpoint that has credentials and cannot
remove a CA bundle, so both are errors. Declare a new endpoint instead.

### Pools

garm does not name pools. garm-reconcile identifies a pool by its runner
prefix, which is the name it is declared under: runners of the pool above are
called `linux-<id>`. To manage an existing pool, set its runner prefix to a name
that is unique within its entity and declare it under that name.

The provider of a pool cannot be changed. Declaring another provider creates a
new pool, and the existing one is deleted by `-prune`.

### Scale sets

Scale sets take the fields of garm's request to create a scale set, except for
`name`, which is the name they are declared under, and `scale_set_id`. Like
pools, they name their template in `template`.

garm cannot change `provider_name`, `labels` and `disable_update` of a scale
set. Changing them is an error. Declare the scale set under another name
instead, and let `-prune` delete the existing one.

### Templates

A template is the script that installs the runner on a new instance. Pools and
scale sets name their template in `template`. That is either a declared
template or one that garm ships, like `github_linux`. A declared template takes
precedence over a shipped one of the same name. Without `template`, the
template garm ships for the OS is used.

Templates that garm ships are never changed or deleted. Templates that exist
in garm without being declared cannot be used, as `-prune` deletes them. The OS
type of a template cannot be changed.

### Secrets

Any string can be replaced with `{ _secret: <path> }`, and is then read from
that file. This also works for values that are not secret but kept in a file,
like the data of a template. Trailing newlines are removed. Relative paths
are resolved against `$CREDENTIALS_DIRECTORY`, which systemd sets for services
that use `LoadCredential=`.

garm never returns secrets, so garm-reconcile cannot tell whether one changed.
It sends the secrets of credentials and declared webhook secrets on every
`apply`. The admin password is only used to create the admin user of a new
controller and to log in. Changing it afterwards is not supported.

### What is not declared

Everything garm-reconcile knows about is managed. A setting that is not
declared is set to garm's default, for example `runner_bootstrap_timeout` of a
pool or `minimum_job_age_backoff` of the controller. Where garm cannot go back
to having no value, like for the CA bundle of an endpoint, removing it from the
desired state is an error.

## Reconciliation

`apply` works in two phases:

1. It creates and updates everything that is declared. On a new controller it
   creates the admin user first.
2. With `-prune`, it deletes pools, scale sets, entities, templates,
   credentials and endpoints that are not declared.

A pool or scale set that has runners cannot be deleted. garm-reconcile disables
it, removes its idle runners and waits for the remaining ones to finish their
jobs. It does not remove a runner that executes a job. On Gitea this depends on
garm knowing that the runner is busy, which it learns from the webhook of the
repository or organization (see [Webhooks](#webhooks)): without it, garm takes
a busy runner for idle, and Gitea does not refuse to delete it. GitHub does.

`-drain-timeout` limits how long the second phase may take. When it expires,
`apply` fails and the pools and scale sets stay disabled until the next run
deletes them. Waiting cannot outlast the lifetime of garm's API tokens
(`jwt_auth.time_to_live`) either. An interrupted `apply` continues where it
left off when run again.

## Adopting a controller

`export` prints the state of a controller that was set up by hand, so that
`plan -prune` with the result reports no changes:

```
garm-reconcile export -username admin -password-file admin-password > state.yaml
```

It logs in as the admin user and changes nothing. garm keeps some things to
itself, which have to be filled in:

- Secrets. The state file references them by name: `garm-admin-password`, and
  for credentials `<forge>-<name>-token` or `<forge>-<name>-private-key`.
  Provide these files in `$CREDENTIALS_DIRECTORY`, or replace the references.
- The email of the admin user, which is exported as the placeholder
  `<fill in: the email of the admin user>`. `validate`, `plan` and `apply`
  reject a state file that still has a placeholder of this form.
- The full name of the admin user, which is left out.
- `app_id` and `installation_id` of app credentials, which are exported as 0
  and rejected likewise.
- Webhook secrets. They are not part of the export, and garm keeps the ones
  it has.

garm also accepts settings that a state file cannot declare, for example a
runner prefix in upper case or a pool with `max_runners` of 0. They are
exported as they are. `export` lists on stderr what `validate` would reject,
placeholders included, so that it can be changed in garm or in the file.

Pools are identified by their runner prefix, so `export` fails when a
repository, organization or enterprise has two pools with the same one, which
is the case for pools that were created without a prefix. Give them distinct
prefixes first, with `garm-cli pool update --runner-prefix`.

## systemd

garm-reconcile notifies systemd when the first phase is done. A unit of
`Type=notify` is therefore started as soon as garm serves the desired state,
while deleting the rest continues in the background:

```ini
[Unit]
After=garm.service
Requires=garm.service

[Service]
Type=notify
RemainAfterExit=yes
ExecStart=garm-reconcile apply -prune /etc/garm/state.yaml
LoadCredential=garm-admin-password:/var/lib/secrets/garm-admin-password
LoadCredential=github-token:/var/lib/secrets/github-token
DynamicUser=yes
```

With this unit, the state file references the secrets as
`{ _secret: garm-admin-password }` and `{ _secret: github-token }`.

garm-reconcile exits with status 3 when garm has an admin user already and
rejects the declared one. Running it again does not help then, so a unit that
restarts on failure should have `RestartPreventExitStatus=3`.

## Development

```
go test ./...
```

`TestAgainstGarm` reconciles a real controller and is skipped unless `garm` is
in `$PATH`. `nix flake check` runs all tests against garm from nixpkgs, and
`nix develop` provides it for `go test`, along with `golangci-lint` and
`govulncheck`. `nix fmt` formats everything, which `nix flake check` requires
as it does `golangci-lint run` to pass.

garm-reconcile is built against the request types of a garm release. After
updating that dependency, `TestEnsureCoversAllPoolFields` and
`TestEnsureCoversAllScaleSetFields` fail if garm added a setting that is not
reconciled yet.

garm talks to GitHub when a scale set is created or deleted, so scale sets are
only tested against the fake of the unit tests.
