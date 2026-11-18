package reconcile_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	commonparams "github.com/cloudbase/garm-provider-common/params"
	"github.com/cloudbase/garm/params"

	"github.com/katexochen/garm-reconcile/internal/config"
	"github.com/katexochen/garm-reconcile/internal/garm"
	"github.com/katexochen/garm-reconcile/internal/garm/garmtest"
)

func runner(name, poolID string, status params.RunnerStatus) *params.Instance {
	return &params.Instance{Name: name, PoolID: poolID, Status: commonparams.InstanceRunning, RunnerStatus: status}
}

func TestEnsureDeletesNothing(t *testing.T) {
	cfg := desiredState()
	srv := initialized(t, cfg)

	delete(cfg.GitHub.Organizations, "my-org")
	delete(cfg.GitHub.Enterprises, "corp")
	changes := run{srv: srv, cfg: cfg}.ensure(t)
	assertEqual(t, "changes", changes, nil)
	srv.State(func(st *garmtest.State) {
		assertEqual(t, "organizations", len(st.Entities[garm.Organization]), 1)
		assertEqual(t, "enterprises", len(st.Entities[garm.Enterprise]), 1)
		assertEqual(t, "pools", len(st.Pools), 1)
	})
}

func TestPruneDeletesUndeclaredResources(t *testing.T) {
	cfg := desiredState()
	cfg.GitHub.Credentials["legacy"] = config.Credentials{Endpoint: "github.com", PAT: &config.PAT{OAuth2Token: "t"}}
	cfg.GitHub.Organizations["legacy-org"] = config.Entity{
		CredentialsName: "legacy",
		Pools:           map[string]config.Pool{"old": pool()},
	}
	cfg.GitHub.Organizations["my-org"].Pools["old"] = pool()
	srv := initialized(t, cfg)
	delete(cfg.GitHub.Credentials, "legacy")
	delete(cfg.GitHub.Organizations, "legacy-org")
	delete(cfg.GitHub.Organizations["my-org"].Pools, "old")
	delete(cfg.GitHub.Enterprises, "corp")

	changes := run{srv: srv, cfg: cfg}.prune(t)
	assertEqual(t, "changes", changes, []string{
		"delete github pool legacy-org/old",
		"delete github pool my-org/old",
		"delete github organization legacy-org",
		"delete github enterprise corp",
		"delete github credentials legacy",
	})

	srv.State(func(st *garmtest.State) {
		assertEqual(t, "credentials", len(st.Credentials), 1)
		assertEqual(t, "credentials name", st.Credentials[0].Name, "main")
		assertEqual(t, "repositories", len(st.Entities[garm.Repository]), 1)
		assertEqual(t, "organizations", len(st.Entities[garm.Organization]), 1)
		assertEqual(t, "organization", st.Entities[garm.Organization][0].Name, "my-org")
		assertEqual(t, "enterprises", len(st.Entities[garm.Enterprise]), 0)
		assertEqual(t, "pools", len(st.Pools), 1)
		assertEqual(t, "pool", st.Pools[0].Prefix, "coco")
	})

	changes = run{srv: srv, cfg: cfg}.prune(t)
	assertEqual(t, "changes of the second run", changes, nil)
}

func TestPruneWaitsForBusyRunners(t *testing.T) {
	cfg := desiredState()
	cfg.GitHub.Organizations["my-org"].Pools["old"] = pool()
	srv := initialized(t, cfg)
	var poolID string
	srv.State(func(st *garmtest.State) {
		poolID = st.Pools[1].ID
		st.Instances = []*params.Instance{
			runner("old-idle", poolID, params.RunnerIdle),
			runner("old-busy", poolID, params.RunnerActive),
			{Name: "old-creating", PoolID: poolID, Status: commonparams.InstanceCreating, RunnerStatus: params.RunnerPending},
			runner("coco-idle", st.Pools[0].ID, params.RunnerIdle),
		}
	})
	delete(cfg.GitHub.Organizations["my-org"].Pools, "old")

	// The remaining runners become removable while Prune waits for them:
	// one finishes its job, the other one is fully created and idle.
	var progress []string
	onProgress := func(msg string) {
		progress = append(progress, msg)
		srv.State(func(st *garmtest.State) {
			assertEqual(t, "pool enabled while waiting", st.Pools[1].Enabled, false)
			assertEqual(t, "runners after the first pass", len(st.Instances), 3)
			for _, instance := range st.Instances {
				switch instance.Name {
				case "old-busy":
					instance.RunnerStatus = params.RunnerTerminated
				case "old-creating":
					instance.Status = commonparams.InstanceRunning
					instance.RunnerStatus = params.RunnerIdle
				}
			}
		})
	}

	changes := run{srv: srv, cfg: cfg, progress: onProgress}.prune(t)
	assertEqual(t, "changes", changes, []string{"delete github pool my-org/old"})
	assertEqual(t, "progress", progress, []string{"waiting for the runners of my-org/old"})
	// The first mutation sends the secret of the credentials again.
	assertEqual(t, "mutations", takeMutations(srv)[1:], []string{
		"PUT /api/v1/pools/" + poolID,
		"DELETE /api/v1/instances/old-idle",
		"DELETE /api/v1/instances/old-busy",
		"DELETE /api/v1/instances/old-creating",
		"DELETE /api/v1/pools/" + poolID,
	})
	srv.State(func(st *garmtest.State) {
		assertEqual(t, "pools", len(st.Pools), 1)
		assertEqual(t, "runners", len(st.Instances), 1)
		assertEqual(t, "runner of the declared pool", st.Instances[0].Name, "coco-idle")
	})
}

func TestPruneDisablesAllPoolsBeforeWaiting(t *testing.T) {
	cfg := desiredState()
	cfg.GitHub.Repositories["owner/repo"] = config.Entity{
		CredentialsName: "main",
		Pools:           map[string]config.Pool{"other": pool()},
	}
	srv := initialized(t, cfg)
	srv.State(func(st *garmtest.State) {
		for _, p := range st.Pools {
			st.Instances = append(st.Instances, runner(p.Prefix+"-busy", p.ID, params.RunnerActive))
		}
	})
	delete(cfg.GitHub.Repositories["owner/repo"].Pools, "other")
	delete(cfg.GitHub.Organizations["my-org"].Pools, "coco")

	var progress []string
	onProgress := func(msg string) {
		progress = append(progress, msg)
		srv.State(func(st *garmtest.State) {
			for _, p := range st.Pools {
				assertEqual(t, "pool "+p.Prefix+" enabled while waiting", p.Enabled, false)
			}
			// Both jobs finish. Garm already removed one of the
			// runners on its own.
			if len(progress) == 1 {
				st.Instances = st.Instances[:1]
				st.Instances[0].RunnerStatus = params.RunnerTerminated
			}
		})
	}
	changes := run{srv: srv, cfg: cfg, progress: onProgress}.prune(t)
	assertEqual(t, "changes", changes, []string{"delete github pool owner/repo/other", "delete github pool my-org/coco"})
	assertEqual(t, "first progress", progress[0], "waiting for the runners of owner/repo/other, my-org/coco")
	srv.State(func(st *garmtest.State) {
		assertEqual(t, "pools", len(st.Pools), 0)
	})
}

func TestPruneStopsWaitingWhenCanceled(t *testing.T) {
	cfg := desiredState()
	srv := initialized(t, cfg)
	srv.State(func(st *garmtest.State) {
		st.Instances = []*params.Instance{runner("coco-busy", st.Pools[0].ID, params.RunnerActive)}
	})
	delete(cfg.GitHub.Organizations, "my-org")

	// The deadline passes while Prune sleeps between two checks.
	rec, _ := run{srv: srv, cfg: cfg, pollInterval: time.Minute}.reconciler(t)
	if err := rec.Ensure(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	err := rec.Prune(ctx)
	if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "waiting for the runners of my-org/coco") {
		t.Errorf("Prune() = %v, want it to give up waiting for my-org/coco", err)
	}
	srv.State(func(st *garmtest.State) {
		assertEqual(t, "pools", len(st.Pools), 1)
		assertEqual(t, "pool enabled", st.Pools[0].Enabled, false)
		assertEqual(t, "runners", len(st.Instances), 1)
		assertEqual(t, "organizations", len(st.Entities[garm.Organization]), 1)
	})
}

func TestReplacedPoolIsCreatedBeforeTheOldOneIsDeleted(t *testing.T) {
	cfg := desiredState()
	srv := initialized(t, cfg)

	// The provider of a pool cannot be changed, so this declares a new
	// pool under the name of the existing one.
	replacement := pool()
	replacement.ProviderName = "lxd"
	cfg.GitHub.Organizations["my-org"].Pools["coco"] = replacement

	r := run{srv: srv, cfg: cfg}
	assertEqual(t, "changes to ensure", r.ensure(t), []string{"create github pool my-org/coco"})
	srv.State(func(st *garmtest.State) {
		assertEqual(t, "pools", len(st.Pools), 2)
		assertEqual(t, "old pool enabled", st.Pools[0].Enabled, true)
		assertEqual(t, "new pool provider", st.Pools[1].ProviderName, "lxd")
	})

	assertEqual(t, "changes to prune", r.prune(t), []string{"delete github pool my-org/coco"})
	srv.State(func(st *garmtest.State) {
		assertEqual(t, "pools", len(st.Pools), 1)
		assertEqual(t, "pool provider", st.Pools[0].ProviderName, "lxd")
	})
}

func TestEnsureRejectsAmbiguousPools(t *testing.T) {
	cfg := desiredState()
	srv := initialized(t, cfg)
	srv.State(func(st *garmtest.State) {
		duplicate := *st.Pools[0]
		duplicate.ID = "duplicate"
		st.Pools = append(st.Pools, &duplicate)
	})

	rec, _ := run{srv: srv, cfg: cfg}.reconciler(t)
	err := rec.Ensure(context.Background())
	if err == nil || !strings.Contains(err.Error(), "github pool my-org/coco: 2 pools of provider incus share this runner prefix") {
		t.Errorf("Ensure() = %v, want it to reject the duplicate pools", err)
	}
}

func TestEnsureIgnoresHowGarmStoresPools(t *testing.T) {
	cfg := desiredState()
	declared := pool()
	declared.ExtraSpecs = []byte(`{"disable_updates":true,"extra_packages":["jq"]}`)
	cfg.GitHub.Organizations["my-org"].Pools["coco"] = declared
	cfg.GitHub.Organizations["my-org"].Pools["plain"] = pool()
	srv := initialized(t, cfg)
	srv.State(func(st *garmtest.State) {
		coco, plain := st.Pools[0], st.Pools[1]
		// Garm picked a bootstrap timeout and a template on its own.
		if coco.RunnerBootstrapTimeout == 0 || coco.TemplateID == 0 {
			t.Fatalf("pool = %+v, want defaults for the bootstrap timeout and template", coco)
		}
		coco.ExtraSpecs = []byte(`{"extra_packages": ["jq"], "disable_updates": true}`)
		// Garm keeps the spelling a tag was first created with.
		coco.Tags = []params.Tag{{Name: "Linux"}, {Name: "coco"}}
		coco.Prefix = "CoCo"
		plain.ExtraSpecs = []byte(`{}`)
	})

	changes := run{srv: srv, cfg: cfg}.ensure(t)
	assertEqual(t, "changes", changes, []string{"update github pool my-org/coco (runner_prefix)"})
	srv.State(func(st *garmtest.State) {
		// Sending extra specs that only differ in formatting would make
		// garm recreate the runners of the pool.
		assertEqual(t, "extra specs", string(st.Pools[0].ExtraSpecs), `{"extra_packages": ["jq"], "disable_updates": true}`)
	})
}

func TestEnsureClearsExtraSpecs(t *testing.T) {
	cfg := desiredState()
	declared := pool()
	declared.ExtraSpecs = []byte(`{"disable_updates":true}`)
	cfg.GitHub.Organizations["my-org"].Pools["coco"] = declared
	srv := initialized(t, cfg)

	cfg.GitHub.Organizations["my-org"].Pools["coco"] = pool()
	changes := run{srv: srv, cfg: cfg}.ensure(t)
	assertEqual(t, "changes", changes, []string{"update github pool my-org/coco (extra_specs)"})
	srv.State(func(st *garmtest.State) {
		assertEqual(t, "extra specs", string(st.Pools[0].ExtraSpecs), "{}")
	})

	changes = run{srv: srv, cfg: cfg}.ensure(t)
	assertEqual(t, "changes of the second run", changes, nil)
}

func TestUndeclaredBootstrapTimeoutIsGarmsDefault(t *testing.T) {
	cfg := desiredState()
	declared := pool()
	declared.RunnerBootstrapTimeout = 30
	cfg.GitHub.Organizations["my-org"].Pools["coco"] = declared
	srv := initialized(t, cfg)

	cfg.GitHub.Organizations["my-org"].Pools["coco"] = pool()
	changes := run{srv: srv, cfg: cfg}.ensure(t)
	assertEqual(t, "changes", changes, []string{"update github pool my-org/coco (runner_bootstrap_timeout)"})
	srv.State(func(st *garmtest.State) {
		assertEqual(t, "bootstrap timeout", st.Pools[0].RunnerBootstrapTimeout, 20)
	})
	assertEqual(t, "changes of the second run", run{srv: srv, cfg: cfg}.ensure(t), nil)
}
