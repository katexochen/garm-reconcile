# Upstream candidates

Things in garm that garm-reconcile works around, collected while building it
against garm v0.2.1. Each could be proposed upstream. Where a change lands,
the workaround named with it can go.

## Identity

- **Pools have no name.** They are identified here by runner prefix and
  provider. The maintainer wrote in
  [discussion #417](https://github.com/cloudbase/garm/discussions/417) that he
  is "thinking of adding unique names to pools". A unique `name` per entity
  would replace `isPool` in `internal/reconcile/pools.go` and the rule that
  prefixes must be unique.

## Reading state

- **Listed pools are incomplete.** `GET /pools` and `GET /{entity}/{id}/pools`
  leave out `extra_specs` (`Omit("extra_specs")` in `database/sql/pools.go`).
  Against a real controller they also returned no `runner_bootstrap_timeout`
  and no `template_name`. Reconciling needs one `GET /pools/{id}` per pool
  because of it. A query parameter to list complete pools would do.
- **Secrets cannot be compared.** Tokens, app keys and webhook secrets are
  never returned, which is right, but nothing tells whether a stored secret is
  the declared one. A fingerprint in the response would allow sending secrets
  only when they change, and showing a rotation in `plan`.

## Updating state

- **Extra specs are compared byte by byte.** `updatePool` bumps the generation
  of a pool, which recreates its runners, when the JSON it receives is not
  byte-identical to the stored one. Comparing the content would make updates
  safe to repeat. Workaround: `poolUpdate` only sends extra specs that differ
  in content.
- **Some values cannot be reset.** An update cannot return
  `runner_bootstrap_timeout` or `template_id` of a pool to garm's default,
  cannot clear the `webhook_url` of the controller, and cannot set
  `pool_balancer_type` of an entity back to empty. Removing these from the
  desired state therefore leaves them as they are.
- **Updating credentials always notifies.** `UpdateGithubCredentials` rewrites
  the payload and notifies watchers even if the token is the same. Entities
  compare the webhook secret first, credentials could do the same.

## Deleting

- **A pool with runners cannot be deleted**, and a disabled pool keeps its
  idle runners (`scaleDownOnePool` skips disabled pools). A way to ask garm to
  drain and delete a pool would replace `prunePools` and `deleteDrained`.
- **A scale set must be disabled before it can be deleted**, in addition to
  having no runners. The same request would cover it.

## Scale sets

- **Labels and `disable_update` cannot be updated**, although
  `UpdateScaleSetParams` exists. Changing them here is an error.
- **Every scale set request talks to GitHub.** Creating one looks up the
  runner group, updating and deleting call the scale set API. None of it can
  run against a controller without GitHub, so the integration test here does
  not cover scale sets. Deferring the GitHub side to the scale set worker,
  like the state `pending_create` suggests, would make the API testable.
- **The runner group is `github-runner-group` on create and `runner_group` on
  update.**

## Validation

- **`CreatePoolParams.Validate` is incomplete.** The checks of the OS type,
  the architecture and the provider live in `runner.appendTagsToCreatePoolParams`.
  Moving the first two into `Validate` would let clients reuse them.
  Workaround: `validateEntity` in `internal/config/validate.go` repeats them.
- **Request and response names differ.** `pool_balancer_type` is returned as
  `pool_balancing_type`, `auth_type` as `auth-type`, and
  `github-runner-group` is the only field with dashes. Not fixable without
  breaking the API, but worth knowing for a v2.
- **The swagger spec has no schemas.** Every model is an `x-go-type` reference
  to a Go struct, so typed clients can only be written in Go against the
  `params` package.

## Operations

- **No readiness signal.** garm does not notify systemd and has no health
  endpoint. `Client.WaitReady` polls `controller-info` until it answers.
  `Type=notify` support in garm would let units order after it properly.
- **Authentication is password to JWT only.** There are no service tokens, no
  API to change the admin password, and a token cannot be renewed. Waiting for
  jobs is limited by `jwt_auth.time_to_live` because of it.

## garm-cli

- `--password-file` for `init` and `profile` is merged
  ([#853](https://github.com/cloudbase/garm/pull/853)) but not released.
- `--skip-if-initialized` for `init` exists as a local branch and was never
  submitted. garm-reconcile initializes the controller itself, so it is only
  needed for setting up a `garm-cli` profile on an initialized controller.

## This tool

The maintainer's answer to
[issue #861](https://github.com/cloudbase/garm/issues/861) was that
declarative configuration should live outside of garm. Since garm-reconcile
uses garm's own request types, it could still move to the cloudbase
organization, or become `garm-cli apply`, with little change.
