# garm-reconcile

Declarative configuration for [garm](https://github.com/cloudbase/garm).

garm keeps credentials, repositories, organizations and runner pools in its
database and is configured through its API. garm-reconcile reads the desired
state from a file and makes the necessary API calls until the controller
matches it.

## Usage

```
garm-reconcile validate <file>
garm-reconcile plan  [-url URL] [-prune] <file>
garm-reconcile apply [-url URL] [-prune] [-drain-timeout DURATION] <file>
```

- `validate` checks the file without contacting garm. It does not read
  secrets, so it can run at build time. What only garm can judge, like the
  strength of the admin password, is reported by `apply`.
- `plan` prints what `apply` would change.
- `apply` creates and updates what is declared. With `-prune` it also deletes
  what is not declared.

`-url` defaults to `http://127.0.0.1:9997`. Both `plan` and `apply` wait for
the API to come up, for one minute unless `-wait` says otherwise.

## Desired state

The file is YAML or JSON. Field names follow the garm API, except that
resources are keyed by their name and that the private key of an app is given
as PEM text in `private_key`.

```yaml
admin:
  username: admin
  email: admin@example.com
  password: { _secret: /run/secrets/garm-admin-password }

controller:
  metadata_url: http://10.0.10.1:9997/api/v1/metadata
  callback_url: http://10.0.10.1:9997/api/v1/callbacks
  agent_url: http://10.0.10.1:9997/agent
  webhook_url: https://garm.example.com/webhooks # optional

github:
  credentials:
    main:
      endpoint: github.com
      pat:
        oauth2_token: { _secret: /run/secrets/github-token }
    # Alternatively, authenticate as an app:
    # app:
    #   app_id: 1
    #   installation_id: 2
    #   private_key: { _secret: /run/secrets/github-app.pem }

  templates:
    nixos:
      os_type: linux
      data: { _secret: /etc/garm/runner-install.tmpl }

  organizations:
    my-org:
      credentials_name: main
      pools:
        coco:
          provider_name: incus
          image: ubuntu:24.04
          flavor: default
          template: nixos # optional
      scale_sets:
        coco-set:
          provider_name: incus
          image: ubuntu:24.04
          flavor: default
          os_type: linux
          os_arch: amd64
          max_runners: 4
          enabled: true
          os_type: linux
          os_arch: amd64
          tags: [coco]
          max_runners: 4
          min_idle_runners: 1
          enabled: true

  repositories:
    owner/name:
      credentials_name: main

  enterprises: {}
```

Entities take `credentials_name`, `webhook_secret`, `pool_balancer_type`,
`agent_mode`, `pools` and `scale_sets`. A webhook secret is generated when an entity is
created without one. Pools take the fields of garm's request to create a pool,
except for `runner_prefix`.

### Pools

garm does not name pools. garm-reconcile identifies a pool by its runner
prefix, which is the name it is declared under: runners of the pool above are
called `coco-<id>`. To manage an existing pool, set its runner prefix to a name
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

A template is the script that installs the runner on a new instance. Pools name
their template in `template`, which can be a declared template or one that garm
ships, like `github_linux`. `template_id` of the garm API is not accepted.
Templates that garm ships are never changed or deleted. The OS type of a
template cannot be changed.

garm chooses `runner_bootstrap_timeout` and the template for pools that are
created without them. Removing them from a declared pool leaves the values the
pool has.

### Secrets

Any string can be replaced with `{ _secret: <path> }`, and is then read from
that file. This also works for values that are not secret but kept in a file,
like the data of a template. Trailing newlines are removed. Relative paths are resolved against
`$CREDENTIALS_DIRECTORY`, which systemd sets for services that use
`LoadCredential=`.

garm never returns secrets, so garm-reconcile cannot tell whether one changed.
It sends the secrets of credentials and declared webhook secrets on every
`apply`. The admin password is only used to create the admin user of a new
controller and to log in. Changing it afterwards is not supported.

## Reconciliation

`apply` works in two phases:

1. It creates and updates everything that is declared. On a new controller it
   creates the admin user first.
2. With `-prune`, it deletes pools, scale sets, entities, templates and
   credentials that are not declared.

A pool or scale set that has runners cannot be deleted. garm-reconcile disables
it, removes its idle runners and waits for the remaining ones to finish their jobs.
It never removes a runner that executes a job. `-drain-timeout` limits how long
the second phase may take. When it expires, `apply` fails and the pools stay
disabled until the next run deletes them. Waiting cannot outlast the lifetime
of garm's API tokens (`jwt_auth.time_to_live`) either. An interrupted `apply`
continues where it left off when run again.

Only resources of GitHub endpoints are managed. Entities of other forges are
left alone.

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

## Not supported yet

- Custom GitHub endpoints and Gitea
- Installing webhooks

## Development

```
go test ./...
```

`TestAgainstGarm` reconciles a real controller and is skipped unless `garm` is
in `$PATH`. `nix flake check` runs all tests against garm from nixpkgs, and
`nix develop` provides it for `go test`.

garm-reconcile is built against the request types of a garm release. After
updating that dependency, `TestEnsureCoversAllPoolFields` and
`TestEnsureCoversAllScaleSetFields` fail if garm added a setting that is not
reconciled yet.

garm talks to GitHub whenever a scale set is created, updated or deleted, so
scale sets are only tested against the fake of the unit tests.
