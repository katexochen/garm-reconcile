package reconcile_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	commonparams "github.com/cloudbase/garm-provider-common/params"
	"github.com/cloudbase/garm/params"

	"github.com/katexochen/garm-reconcile/internal/config"
	"github.com/katexochen/garm-reconcile/internal/garm"
	"github.com/katexochen/garm-reconcile/internal/garm/garmtest"
	"github.com/katexochen/garm-reconcile/internal/reconcile"
)

func desiredState() *config.Config {
	return &config.Config{
		Admin: config.Admin{Username: "admin", Email: "admin@example.com", Password: "hunter2"},
		Controller: config.Controller{
			MetadataURL: "http://garm/api/v1/metadata",
			CallbackURL: "http://garm/api/v1/callbacks",
			AgentURL:    "http://garm/agent",
			WebhookURL:  "http://garm/webhooks",
		},
		GitHub: config.Forge{
			Credentials: map[string]config.Credentials{
				"main": {Endpoint: "github.com", PAT: &config.PAT{OAuth2Token: "ghp_token"}},
			},
			Repositories: map[string]config.Entity{
				"owner/repo": {CredentialsName: "main"},
			},
			Organizations: map[string]config.Entity{
				"my-org": {
					CredentialsName: "main",
					Pools:           map[string]config.Pool{"coco": pool()},
				},
			},
			Enterprises: map[string]config.Entity{
				"corp": {CredentialsName: "main"},
			},
		},
	}
}

func pool() config.Pool {
	return config.Pool{CreatePoolParams: params.CreatePoolParams{
		ProviderName: "incus",
		Image:        "ubuntu",
		Flavor:       "default",
		OSType:       "linux",
		OSArch:       "amd64",
		Tags:         []string{"coco", "linux"},
		MaxRunners:   2,
		Enabled:      true,
	}}
}

// run describes how to reconcile the controller behind srv.
type run struct {
	srv      *garmtest.Server
	cfg      *config.Config
	dryRun   bool
	progress func(string)
	// pollInterval defaults to a millisecond.
	pollInterval time.Duration
}

func (r run) reconciler(t *testing.T) (*reconcile.Reconciler, *[]string) {
	t.Helper()
	client, err := garm.NewClient(r.srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if r.pollInterval == 0 {
		r.pollInterval = time.Millisecond
	}
	if r.dryRun {
		client.ReadOnly()
	}
	var changes []string
	return reconcile.New(client, r.cfg, reconcile.Options{
		DryRun:       r.dryRun,
		Report:       func(c reconcile.Change) { changes = append(changes, c.String()) },
		Progress:     r.progress,
		PollInterval: r.pollInterval,
	}), &changes
}

// ensure runs the first phase and returns the changes it reported.
func (r run) ensure(t *testing.T) []string {
	t.Helper()
	rec, changes := r.reconciler(t)
	if err := rec.Ensure(context.Background()); err != nil {
		t.Fatalf("Ensure(): %v", err)
	}
	return *changes
}

// prune runs both phases and returns the changes the second one reported.
func (r run) prune(t *testing.T) []string {
	t.Helper()
	rec, changes := r.reconciler(t)
	if err := rec.Ensure(context.Background()); err != nil {
		t.Fatalf("Ensure(): %v", err)
	}
	*changes = nil
	if err := rec.Prune(context.Background()); err != nil {
		t.Fatalf("Prune(): %v", err)
	}
	return *changes
}

// initialized returns a fake controller that was reconciled with cfg.
func initialized(t *testing.T, cfg *config.Config) *garmtest.Server {
	t.Helper()
	srv := garmtest.NewServer(t)
	run{srv: srv, cfg: cfg}.ensure(t)
	takeMutations(srv)
	return srv
}

// takeMutations returns the requests that changed the controller since the
// last call.
func takeMutations(srv *garmtest.Server) []string {
	var mutations []string
	srv.State(func(st *garmtest.State) {
		mutations, st.Mutations = st.Mutations, nil
	})
	return mutations
}

func assertEqual[T any](t *testing.T, what string, got, want T) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("%s:\n got: %+v\nwant: %+v", what, got, want)
	}
}

func TestEnsureCreatesDeclaredResources(t *testing.T) {
	srv := garmtest.NewServer(t)
	cfg := desiredState()

	changes := run{srv: srv, cfg: cfg}.ensure(t)
	assertEqual(t, "changes", changes, []string{
		"create admin user admin",
		"update controller (agent_url, callback_url, metadata_url, webhook_url)",
		"create github credentials main",
		"create github repository owner/repo",
		"create github organization my-org",
		"create github pool my-org/coco",
		"create github enterprise corp",
	})

	srv.State(func(st *garmtest.State) {
		assertEqual(t, "admin", *st.Admin, params.NewUserParams{
			Username: "admin", Email: "admin@example.com", Password: "hunter2",
		})
		assertEqual(t, "metadata URL", st.Controller.MetadataURL, cfg.Controller.MetadataURL)
		assertEqual(t, "webhook URL", st.Controller.WebhookURL, cfg.Controller.WebhookURL)
		assertEqual(t, "job age backoff", st.Controller.MinimumJobAgeBackoff, 30)

		creds := st.Credentials[0]
		assertEqual(t, "credentials name", creds.Name, "main")
		assertEqual(t, "credentials endpoint", creds.Endpoint.Name, "github.com")
		assertEqual(t, "credentials auth type", creds.AuthType, params.ForgeAuthTypePAT)
		assertEqual(t, "token", creds.PAT.OAuth2Token, "ghp_token")

		repo := st.Entities[garm.Repository][0]
		assertEqual(t, "repository", repo.FullName(), "owner/repo")
		assertEqual(t, "repository credentials", repo.CredentialsName, "main")
		if repo.WebhookSecret == "" {
			t.Error("repository was created without a webhook secret")
		}
		assertEqual(t, "organization", st.Entities[garm.Organization][0].FullName(), "my-org")
		assertEqual(t, "enterprise", st.Entities[garm.Enterprise][0].FullName(), "corp")

		created := st.Pools[0]
		assertEqual(t, "pool organization", created.OrgID, st.Entities[garm.Organization][0].ID)
		assertEqual(t, "pool prefix", created.Prefix, "coco")
		assertEqual(t, "pool provider", created.ProviderName, "incus")
		assertEqual(t, "pool max runners", created.MaxRunners, 2)
		assertEqual(t, "pool enabled", created.Enabled, true)
	})
}

func TestEnsureIsIdempotent(t *testing.T) {
	cfg := desiredState()
	srv := initialized(t, cfg)

	changes := run{srv: srv, cfg: cfg}.ensure(t)
	assertEqual(t, "changes", changes, nil)
	// Only the secret of the credentials is sent again.
	mutations := takeMutations(srv)
	if len(mutations) != 1 || !strings.HasPrefix(mutations[0], "PUT /api/v1/github/credentials/") {
		t.Errorf("mutations = %v, want one update of the credentials", mutations)
	}
}

func TestEnsureUpdates(t *testing.T) {
	cfg := desiredState()
	srv := initialized(t, cfg)

	cfg.Controller.WebhookURL = "http://garm/moved"
	cfg.GitHub.Credentials["main"] = config.Credentials{
		Description: "rotated",
		Endpoint:    "github.com",
		PAT:         &config.PAT{OAuth2Token: "ghp_new"},
	}
	cfg.GitHub.Credentials["other"] = config.Credentials{
		Endpoint: "github.com",
		PAT:      &config.PAT{OAuth2Token: "ghp_other"},
	}
	cfg.GitHub.Repositories["owner/repo"] = config.Entity{
		CredentialsName:  "other",
		WebhookSecret:    "declared",
		PoolBalancerType: params.PoolBalancerTypePack,
		AgentMode:        true,
	}
	org := cfg.GitHub.Organizations["my-org"]
	changed := pool()
	changed.MaxRunners = 4
	changed.Enabled = false
	changed.Tags = []string{"coco"}
	org.Pools["coco"] = changed

	changes := run{srv: srv, cfg: cfg}.ensure(t)
	assertEqual(t, "changes", changes, []string{
		"update controller (webhook_url)",
		"update github credentials main (description)",
		"create github credentials other",
		"update github repository owner/repo (credentials_name, pool_balancer_type, agent_mode)",
		"update github pool my-org/coco (enabled, max_runners, tags)",
	})

	srv.State(func(st *garmtest.State) {
		assertEqual(t, "webhook URL", st.Controller.WebhookURL, "http://garm/moved")
		assertEqual(t, "description", st.Credentials[0].Description, "rotated")
		assertEqual(t, "token", st.Credentials[0].PAT.OAuth2Token, "ghp_new")

		repo := st.Entities[garm.Repository][0]
		assertEqual(t, "repository credentials", repo.CredentialsName, "other")
		assertEqual(t, "webhook secret", repo.WebhookSecret, "declared")
		assertEqual(t, "pool balancer", repo.PoolBalancerType, params.PoolBalancerTypePack)
		assertEqual(t, "agent mode", repo.AgentMode, true)

		updated := st.Pools[0]
		assertEqual(t, "pool max runners", updated.MaxRunners, 4)
		assertEqual(t, "pool enabled", updated.Enabled, false)
		assertEqual(t, "pool tags", updated.Tags, []params.Tag{{ID: "coco", Name: "coco"}})
	})

	changes = run{srv: srv, cfg: cfg}.ensure(t)
	assertEqual(t, "changes of the second run", changes, nil)
}

func TestEnsureSendsDeclaredWebhookSecret(t *testing.T) {
	cfg := desiredState()
	cfg.GitHub.Repositories["owner/repo"] = config.Entity{CredentialsName: "main", WebhookSecret: "first"}
	srv := initialized(t, cfg)
	srv.State(func(st *garmtest.State) {
		assertEqual(t, "webhook secret", st.Entities[garm.Repository][0].WebhookSecret, "first")
	})

	cfg.GitHub.Repositories["owner/repo"] = config.Entity{CredentialsName: "main", WebhookSecret: "second"}
	changes := run{srv: srv, cfg: cfg}.ensure(t)
	// Garm keeps the secret to itself, so a rotation is not a visible change.
	assertEqual(t, "changes", changes, nil)
	srv.State(func(st *garmtest.State) {
		assertEqual(t, "webhook secret", st.Entities[garm.Repository][0].WebhookSecret, "second")
	})
}

func TestEnsureAppCredentials(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	privateKey := string(pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(key),
	}))

	cfg := desiredState()
	cfg.GitHub.Credentials["main"] = config.Credentials{
		Endpoint: "github.com",
		App:      &config.App{AppID: 1, InstallationID: 2, PrivateKey: privateKey},
	}
	srv := initialized(t, cfg)
	srv.State(func(st *garmtest.State) {
		creds := st.Credentials[0]
		assertEqual(t, "auth type", creds.AuthType, params.ForgeAuthTypeApp)
		assertEqual(t, "app", creds.App, params.GithubApp{
			AppID: 1, InstallationID: 2, PrivateKeyBytes: []byte(privateKey),
		})
	})

	changes := run{srv: srv, cfg: cfg}.ensure(t)
	assertEqual(t, "changes of the second run", changes, nil)
}

func TestEnsureRejectsImmutableCredentialChanges(t *testing.T) {
	tests := map[string]struct {
		creds   config.Credentials
		wantErr string
	}{
		"endpoint": {
			creds:   config.Credentials{Endpoint: "ghes.example.com", PAT: &config.PAT{OAuth2Token: "t"}},
			wantErr: "the endpoint cannot be changed from github.com to ghes.example.com",
		},
		"authentication": {
			creds:   config.Credentials{Endpoint: "github.com", App: &config.App{AppID: 1, InstallationID: 2, PrivateKey: "k"}},
			wantErr: "the authentication cannot be changed from pat to app",
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			cfg := desiredState()
			srv := initialized(t, cfg)
			cfg.GitHub.Credentials["main"] = tt.creds

			rec, _ := run{srv: srv, cfg: cfg}.reconciler(t)
			err := rec.Ensure(context.Background())
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("Ensure() = %v, want it to contain %q", err, tt.wantErr)
			}
		})
	}
}

func TestEnsureWrongPassword(t *testing.T) {
	cfg := desiredState()
	srv := initialized(t, cfg)
	cfg.Admin.Password = "changed"

	rec, _ := run{srv: srv, cfg: cfg}.reconciler(t)
	err := rec.Ensure(context.Background())
	if err == nil || !strings.Contains(err.Error(), "logging in as admin") {
		t.Errorf("Ensure() = %v, want a login error", err)
	}
}

func TestEnsureReportsFailedChange(t *testing.T) {
	cfg := desiredState()
	srv := initialized(t, cfg)
	// The fake accepts any provider, but like garm it requires an image.
	invalid := pool()
	invalid.Image = ""
	cfg.GitHub.Organizations["my-org"].Pools["broken"] = invalid

	rec, _ := run{srv: srv, cfg: cfg}.reconciler(t)
	err := rec.Ensure(context.Background())
	if err == nil || !strings.Contains(err.Error(), "create github pool my-org/broken: garm: Bad Request") {
		t.Errorf("Ensure() = %v, want it to name the failed change", err)
	}
}

func TestDryRun(t *testing.T) {
	cfg := desiredState()
	srv := initialized(t, cfg)

	cfg.GitHub.Credentials["main"] = config.Credentials{
		Description: "rotated", Endpoint: "github.com", PAT: &config.PAT{OAuth2Token: "ghp_token"},
	}
	cfg.GitHub.Repositories["owner/repo"] = config.Entity{CredentialsName: "main", WebhookSecret: "declared", AgentMode: true}
	delete(cfg.GitHub.Enterprises, "corp")
	cfg.GitHub.Organizations["new-org"] = config.Entity{
		CredentialsName: "main",
		Pools:           map[string]config.Pool{"coco": pool()},
	}
	larger := pool()
	larger.MaxRunners = 4
	cfg.GitHub.Organizations["my-org"].Pools["coco"] = larger

	r := run{srv: srv, cfg: cfg, dryRun: true}
	assertEqual(t, "changes to ensure", r.ensure(t), []string{
		"update github credentials main (description)",
		"update github repository owner/repo (agent_mode)",
		"update github pool my-org/coco (max_runners)",
		"create github organization new-org",
		"create github pool new-org/coco",
	})
	assertEqual(t, "changes to prune", r.prune(t), []string{"delete github enterprise corp"})
	assertEqual(t, "mutations", takeMutations(srv), nil)
}

func TestDryRunOnUninitializedController(t *testing.T) {
	cfg := desiredState()

	t.Run("without admin user", func(t *testing.T) {
		srv := garmtest.NewServer(t)
		rec, changes := run{srv: srv, cfg: cfg, dryRun: true}.reconciler(t)
		err := rec.Ensure(context.Background())
		if !errors.Is(err, reconcile.ErrNotInitialized) {
			t.Fatalf("Ensure() = %v, want ErrNotInitialized", err)
		}
		assertEqual(t, "changes", *changes, []string{"create admin user admin"})
		assertEqual(t, "mutations", takeMutations(srv), nil)
	})

	t.Run("without controller URLs", func(t *testing.T) {
		srv := garmtest.NewServer(t)
		srv.State(func(st *garmtest.State) {
			st.Admin = &params.NewUserParams{Username: "admin", Email: "admin@example.com", Password: "hunter2"}
		})
		rec, changes := run{srv: srv, cfg: cfg, dryRun: true}.reconciler(t)
		err := rec.Ensure(context.Background())
		if !errors.Is(err, reconcile.ErrNotInitialized) {
			t.Fatalf("Ensure() = %v, want ErrNotInitialized", err)
		}
		assertEqual(t, "changes", *changes, []string{"update controller (agent_url, callback_url, metadata_url, webhook_url)"})
		assertEqual(t, "mutations", takeMutations(srv), nil)
	})
}

func TestEnsureResetsUndeclaredJobAgeBackoff(t *testing.T) {
	cfg := desiredState()
	cfg.Controller.MinimumJobAgeBackoff = 5
	srv := initialized(t, cfg)
	srv.State(func(st *garmtest.State) {
		assertEqual(t, "job age backoff", st.Controller.MinimumJobAgeBackoff, 5)
	})

	cfg.Controller.MinimumJobAgeBackoff = 0
	changes := run{srv: srv, cfg: cfg}.ensure(t)
	assertEqual(t, "changes", changes, []string{"update controller (minimum_job_age_backoff)"})
	srv.State(func(st *garmtest.State) {
		assertEqual(t, "job age backoff", st.Controller.MinimumJobAgeBackoff, 30)
	})
	assertEqual(t, "changes of the second run", run{srv: srv, cfg: cfg}.ensure(t), nil)
}

func TestEnsureMatchesEntitiesByEndpoint(t *testing.T) {
	cfg := desiredState()
	srv := initialized(t, cfg)
	// The same organization on another endpoint is a different entity.
	srv.State(func(st *garmtest.State) {
		org := st.Entities[garm.Organization][0]
		org.Endpoint.Name = "ghes.example.com"
	})

	changes := run{srv: srv, cfg: cfg}.ensure(t)
	assertEqual(t, "changes", changes, []string{"create github organization my-org", "create github pool my-org/coco"})
}

func jsonNames(t reflect.Type) []string {
	var names []string
	for i := range t.NumField() {
		field := t.Field(i)
		if field.Anonymous {
			names = append(names, jsonNames(field.Type)...)
			continue
		}
		name, _, _ := strings.Cut(field.Tag.Get("json"), ",")
		names = append(names, name)
	}
	return names
}

// populatedPool returns a pool with every field set. Pools of seeds with a
// different parity differ in every field, except for what identifies a pool.
func populatedPool(t *testing.T, seed uint) params.CreatePoolParams {
	t.Helper()
	var spec params.CreatePoolParams
	v := reflect.ValueOf(&spec).Elem()
	for i := range v.NumField() {
		field := v.Field(i)
		switch {
		case field.Type() == reflect.TypeFor[params.RunnerPrefix]():
			// Set from the name the pool is declared under.
		case field.Type() == reflect.TypeFor[json.RawMessage]():
			field.SetBytes(fmt.Appendf(nil, `{"seed":%d}`, seed))
		case field.Type() == reflect.TypeFor[[]string]():
			field.Set(reflect.ValueOf([]string{fmt.Sprint("tag-", seed)}))
		case field.Type() == reflect.TypeFor[*uint]():
			field.Set(reflect.ValueOf(&seed))
		case field.Kind() == reflect.String:
			field.SetString(fmt.Sprint("value-", seed))
		case field.Kind() == reflect.Uint:
			field.SetUint(uint64(10 + seed))
		case field.Kind() == reflect.Bool:
			field.SetBool(seed%2 == 1)
		default:
			t.Fatalf("populatedPool does not handle field %s of type %s", v.Type().Field(i).Name, field.Type())
		}
	}
	spec.ProviderName = "incus"
	spec.OSType = []commonparams.OSType{commonparams.Linux, commonparams.Windows}[seed%2]
	spec.OSArch = []commonparams.OSArch{commonparams.Amd64, commonparams.Arm64}[seed%2]
	return spec
}

// TestEnsureCoversAllPoolFields fails when garm adds a field to pools that
// reconciling does not read back or does not update.
func TestEnsureCoversAllPoolFields(t *testing.T) {
	cfg := desiredState()
	cfg.GitHub.Organizations["my-org"].Pools["coco"] = config.Pool{CreatePoolParams: populatedPool(t, 1)}
	srv := initialized(t, cfg)

	changes := run{srv: srv, cfg: cfg}.ensure(t)
	assertEqual(t, "changes after creating the pool", changes, nil)

	cfg.GitHub.Organizations["my-org"].Pools["coco"] = config.Pool{CreatePoolParams: populatedPool(t, 2)}
	fields := jsonNames(reflect.TypeFor[params.CreatePoolParams]())
	fields = slices.DeleteFunc(fields, func(name string) bool {
		return name == "runner_prefix" || name == "provider_name"
	})
	slices.Sort(fields)
	changes = run{srv: srv, cfg: cfg}.ensure(t)
	assertEqual(t, "changes", changes, []string{"update github pool my-org/coco (" + strings.Join(fields, ", ") + ")"})

	changes = run{srv: srv, cfg: cfg}.ensure(t)
	assertEqual(t, "changes after updating the pool", changes, nil)
}

// TestCreateEntityParamsCoverGarm fails when garm adds a field to the
// requests that create entities.
func TestCreateEntityParamsCoverGarm(t *testing.T) {
	ours := jsonNames(reflect.TypeFor[garm.CreateEntityParams]())
	for _, upstream := range []reflect.Type{
		reflect.TypeFor[params.CreateRepoParams](),
		reflect.TypeFor[params.CreateOrgParams](),
		reflect.TypeFor[params.CreateEnterpriseParams](),
	} {
		for _, name := range jsonNames(upstream) {
			if !slices.Contains(ours, name) {
				t.Errorf("garm.CreateEntityParams lacks the field %s of %s", name, upstream.Name())
			}
		}
	}
}

func TestEnsureMatchesNamesWithoutCase(t *testing.T) {
	cfg := desiredState()
	srv := initialized(t, cfg)
	srv.State(func(st *garmtest.State) {
		st.Credentials[0].Name = "Main"
		for _, kind := range []garm.EntityKind{garm.Repository, garm.Organization, garm.Enterprise} {
			entity := st.Entities[kind][0]
			entity.Owner, entity.Name = strings.ToUpper(entity.Owner), strings.ToUpper(entity.Name)
			entity.CredentialsName = "Main"
			entity.Endpoint.Name = "GitHub.com"
		}
		st.Credentials[0].Endpoint.Name = "GitHub.com"
	})

	changes := run{srv: srv, cfg: cfg}.prune(t)
	assertEqual(t, "changes to prune", changes, nil)
	changes = run{srv: srv, cfg: cfg}.ensure(t)
	assertEqual(t, "changes to ensure", changes, nil)
}

func TestEnsureReportsRejectedAdmin(t *testing.T) {
	cfg := desiredState()
	cfg.Admin.Email = ""
	srv := garmtest.NewServer(t)

	rec, _ := run{srv: srv, cfg: cfg}.reconciler(t)
	err := rec.Ensure(context.Background())
	if err == nil || !strings.Contains(err.Error(), "create admin user admin: garm: Bad Request") {
		t.Errorf("Ensure() = %v, want it to report the rejected admin user", err)
	}
}
