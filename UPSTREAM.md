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
- **Some values cannot be reset.** An update cannot remove the CA bundle of
  an endpoint or the `webhook_url` of the controller, and cannot return
  `runner_bootstrap_timeout`, `minimum_job_age_backoff` or
  `tools_metadata_url` to garm's default other than by sending the default,
  which clients then have to know. `pool_balancer_type` of an entity cannot
  be set back to empty.
- **New credentials take effect a moment after the update returns.** A
  request that needs the forge right after it, like reading a webhook, fails
  with HTTP 500 if the previous token was revoked. Workaround: `getWebhook`
  retries.
- **Updating credentials always notifies.** `UpdateGithubCredentials` rewrites
  the payload and notifies watchers even if the token is the same. Entities
  compare the webhook secret first, credentials could do the same.

## Deleting

- **A pool with runners cannot be deleted**, and a disabled pool keeps its
  idle runners (`scaleDownOnePool` skips disabled pools). A way to ask garm to
  drain and delete a pool would replace `pruneRunners` and `deleteDrained`.
- **A scale set must be disabled before it can be deleted**, in addition to
  having no runners. The same request would cover it.

## Scale sets

- **Labels and `disable_update` cannot be updated**, although
  `UpdateScaleSetParams` exists. Changing them here is an error.
- **Deleting a scale set can report success without deleting it.** The
  check in `Runner.DeleteScaleSetByID` is inverted
  (`if !errors.Is(err, runnerErrors.ErrNotFound) { return nil }`), so any
  error from GitHub makes garm answer 200 and keep the scale set. Workaround:
  `deleteScaleSet` reads the scale set back.
- **Scale set names are not unique.** The model tags `Name` and
  `GitHubRunnerGroup` with `unique_index:idx_name`, which is gorm v1 syntax
  and creates no index.
- **Creating and deleting a scale set talks to GitHub**, as does moving it
  to another runner group. None of it can run against a controller without
  GitHub, so the integration test here does not cover scale sets. Deferring the GitHub side to the scale set worker,
  like the state `pending_create` suggests, would make the API testable.
- **The runner group is `github-runner-group` on create and `runner_group` on
  update.**

## Forges

- **Template names are not scoped by forge.** The unique index is on name
  and user, so a GitHub and a Gitea template cannot share a name, although
  a template only ever applies to one forge.
- **Without webhooks, garm does not know which runners are busy.** It does
  not ask the forge in between, so `runner_status` stays `idle` while a job
  runs. On Gitea that is fatal for the job when the runner is removed, as
  Gitea deletes a busy runner where GitHub refuses. Seen in the VM test here:
  with the `workflow_job` webhook delivered, garm marks the runner `active`
  and the job survives its pool being deleted.
- **Garm ignores when a Gitea job completes.** The `completed` event of
  Gitea carries no runner name, garm logs "job never got assigned to a
  runner, ignoring" and only replaces the runner when it next checks on all
  of them.
- **That check runs every five minutes and is not configurable**
  (`PoolReapTimeoutInterval` in `runner/common/pool.go`). The VM test here
  patches it to ten seconds. An option in `config.toml` would do, though
  matching the completed job to its runner removes the need.

## Templates

- **Admins can change and delete the templates garm ships.** Only users that
  are not admins are refused.
- **`owner_id` is ambiguous.** Shipped templates report the owner `system`,
  and so do the templates of an admin user named `system`, which garm accepts.
  Workaround: that username is rejected here.

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
