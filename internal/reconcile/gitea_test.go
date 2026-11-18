package reconcile_test

import (
	"context"
	"strings"
	"testing"

	"github.com/cloudbase/garm/params"
	"github.com/cloudbase/garm/util/appdefaults"

	"github.com/katexochen/garm-reconcile/internal/config"
	"github.com/katexochen/garm-reconcile/internal/garm"
	"github.com/katexochen/garm-reconcile/internal/garm/garmtest"
)

func withGitea() *config.Config {
	cfg := desiredState()
	cfg.Gitea = config.Forge{
		Endpoints: map[string]config.Endpoint{
			"local": {
				APIBaseURL:       "http://gitea/api/v1",
				BaseURL:          "http://gitea",
				ToolsMetadataURL: "http://gitea/releases.json",
			},
		},
		Credentials: map[string]config.Credentials{
			"main": {Endpoint: "local", PAT: &config.PAT{OAuth2Token: "gitea_token"}},
		},
		Templates: map[string]config.Template{
			"nixos": {OSType: "linux", Data: "#!/bin/sh"},
		},
		Repositories: map[string]config.Entity{
			"owner/repo": {
				CredentialsName: "main",
				Pools:           map[string]config.Pool{"coco": pool(), "nixos": pool()},
			},
		},
	}
	nixos := cfg.Gitea.Repositories["owner/repo"].Pools["nixos"]
	nixos.Template = "nixos"
	cfg.Gitea.Repositories["owner/repo"].Pools["nixos"] = nixos
	return cfg
}

// TestGitea reconciles resources that have namesakes on GitHub.
func TestGitea(t *testing.T) {
	cfg := withGitea()
	srv := garmtest.NewServer(t)

	changes := run{srv: srv, cfg: cfg}.ensure(t)
	assertEqual(t, "changes", changes[len(changes)-6:], []string{
		"create gitea endpoint local",
		"create gitea credentials main",
		"create gitea template nixos",
		"create gitea repository owner/repo",
		"create gitea pool owner/repo/coco",
		"create gitea pool owner/repo/nixos",
	})
	srv.State(func(st *garmtest.State) {
		assertEqual(t, "endpoints", len(st.Endpoints), 2)
		assertEqual(t, "tools metadata URL", st.Endpoints[1].ToolsMetadataURL, "http://gitea/releases.json")
		assertEqual(t, "credentials", len(st.Credentials), 2)
		assertEqual(t, "token", st.Credentials[1].PAT.OAuth2Token, "gitea_token")
		assertEqual(t, "repositories", len(st.Entities[garm.Repository]), 2)
		repo := st.Entities[garm.Repository][1]
		assertEqual(t, "forge of the repository", repo.Endpoint.EndpointType, params.GiteaEndpointType)
		assertEqual(t, "endpoint of the repository", repo.Endpoint.Name, "local")
		for _, p := range st.Pools {
			if p.RepoID != repo.ID {
				continue
			}
			want := map[string]string{"coco": "gitea_linux", "nixos": "nixos"}[p.Prefix]
			assertEqual(t, "template of pool "+p.Prefix, p.TemplateName, want)
		}
	})
	assertEqual(t, "changes of the second run", run{srv: srv, cfg: cfg}.prune(t), nil)

	endpoint := cfg.Gitea.Endpoints["local"]
	endpoint.Description = "changed"
	endpoint.ToolsMetadataURL = "http://gitea/other.json"
	cfg.Gitea.Endpoints["local"] = endpoint
	want := []string{"update gitea endpoint local (description, tools_metadata_url)"}
	assertEqual(t, "changes to plan", run{srv: srv, cfg: cfg, dryRun: true}.ensure(t), want)
	assertEqual(t, "changes", run{srv: srv, cfg: cfg}.ensure(t), want)
	srv.State(func(st *garmtest.State) {
		assertEqual(t, "description", st.Endpoints[1].Description, "changed")
		assertEqual(t, "tools metadata URL", st.Endpoints[1].ToolsMetadataURL, "http://gitea/other.json")
	})

	// Everything on Gitea goes away, the namesakes on GitHub stay.
	cfg.Gitea = config.Forge{}
	assertEqual(t, "changes to prune", run{srv: srv, cfg: cfg}.prune(t), []string{
		"delete gitea pool owner/repo/coco",
		"delete gitea pool owner/repo/nixos",
		"delete gitea repository owner/repo",
		"delete gitea template nixos",
		"delete gitea credentials main",
		"delete gitea endpoint local",
	})
	srv.State(func(st *garmtest.State) {
		assertEqual(t, "endpoints", len(st.Endpoints), 1)
		assertEqual(t, "endpoint", st.Endpoints[0].Name, "github.com")
		assertEqual(t, "credentials", len(st.Credentials), 1)
		assertEqual(t, "repositories", len(st.Entities[garm.Repository]), 1)
		assertEqual(t, "pools", len(st.Pools), 1)
	})
}

func TestEndpointURLOfUsedEndpointCannotChange(t *testing.T) {
	cfg := withGitea()
	srv := initialized(t, cfg)
	endpoint := cfg.Gitea.Endpoints["local"]
	endpoint.BaseURL = "http://moved"
	cfg.Gitea.Endpoints["local"] = endpoint

	// The plan fails as well, instead of showing a change apply cannot make.
	for _, dryRun := range []bool{true, false} {
		rec, _ := run{srv: srv, cfg: cfg, dryRun: dryRun}.reconciler(t)
		err := rec.Ensure(context.Background())
		if err == nil || !strings.Contains(err.Error(), "gitea endpoint local: base_url cannot be changed while credentials use the endpoint") {
			t.Errorf("Ensure() = %v, want it to refuse the new URL", err)
		}
	}
}

func TestEndpointUpdates(t *testing.T) {
	cfg := desiredState()
	cfg.GitHub.Endpoints = map[string]config.Endpoint{
		"ghes": {
			APIBaseURL:    "https://ghes.example.com/api/v3",
			BaseURL:       "https://ghes.example.com",
			UploadBaseURL: "https://ghes.example.com/api/uploads",
			CACertBundle:  "first bundle",
		},
	}
	srv := initialized(t, cfg)
	// Garm compares the names of endpoints without regard to case.
	srv.State(func(st *garmtest.State) { st.Endpoints[1].Name = "GHES" })

	cfg.GitHub.Endpoints["ghes"] = config.Endpoint{
		Description:   "moved",
		APIBaseURL:    "https://moved.example.com/api/v3",
		BaseURL:       "https://moved.example.com",
		UploadBaseURL: "https://moved.example.com/api/uploads",
		CACertBundle:  "second bundle",
	}
	changes := run{srv: srv, cfg: cfg}.ensure(t)
	assertEqual(t, "changes", changes, []string{
		"update github endpoint ghes (api_base_url, base_url, description, ca_cert_bundle, upload_base_url)",
	})
	srv.State(func(st *garmtest.State) {
		assertEqual(t, "endpoints", len(st.Endpoints), 2)
		assertEqual(t, "endpoint", *st.Endpoints[1], params.ForgeEndpoint{
			Name:          "GHES",
			Description:   "moved",
			EndpointType:  params.GithubEndpointType,
			APIBaseURL:    "https://moved.example.com/api/v3",
			BaseURL:       "https://moved.example.com",
			UploadBaseURL: "https://moved.example.com/api/uploads",
			CACertBundle:  []byte("second bundle"),
		})
	})

	// Garm has no way to remove a CA bundle.
	undeclared := cfg.GitHub.Endpoints["ghes"]
	undeclared.CACertBundle = ""
	cfg.GitHub.Endpoints["ghes"] = undeclared
	rec, _ := run{srv: srv, cfg: cfg}.reconciler(t)
	err := rec.Ensure(context.Background())
	if err == nil || !strings.Contains(err.Error(), "github endpoint ghes: garm cannot remove the CA bundle") {
		t.Errorf("Ensure() = %v, want it to refuse removing the CA bundle", err)
	}
}

func TestUndeclaredToolsMetadataURLIsGarmsDefault(t *testing.T) {
	cfg := withGitea()
	srv := initialized(t, cfg)

	endpoint := cfg.Gitea.Endpoints["local"]
	endpoint.ToolsMetadataURL = ""
	cfg.Gitea.Endpoints["local"] = endpoint
	changes := run{srv: srv, cfg: cfg}.ensure(t)
	assertEqual(t, "changes", changes, []string{"update gitea endpoint local (tools_metadata_url)"})
	srv.State(func(st *garmtest.State) {
		assertEqual(t, "tools metadata URL", st.Endpoints[1].ToolsMetadataURL, appdefaults.GiteaRunnerReleasesURL)
	})
	assertEqual(t, "changes of the second run", run{srv: srv, cfg: cfg}.ensure(t), nil)
}

func TestCustomGitHubEndpoint(t *testing.T) {
	cfg := desiredState()
	cfg.GitHub.Endpoints = map[string]config.Endpoint{
		"ghes": {
			APIBaseURL:    "https://ghes.example.com/api/v3",
			BaseURL:       "https://ghes.example.com",
			UploadBaseURL: "https://ghes.example.com/api/uploads",
			CACertBundle:  "-----BEGIN CERTIFICATE-----",
		},
	}
	cfg.GitHub.Credentials["main"] = config.Credentials{Endpoint: "ghes", PAT: &config.PAT{OAuth2Token: "t"}}
	srv := garmtest.NewServer(t)

	changes := run{srv: srv, cfg: cfg}.ensure(t)
	assertEqual(t, "changes", changes[2:4], []string{"create github endpoint ghes", "create github credentials main"})
	srv.State(func(st *garmtest.State) {
		assertEqual(t, "CA bundle", string(st.Endpoints[1].CACertBundle), "-----BEGIN CERTIFICATE-----")
		assertEqual(t, "endpoint of the organization", st.Entities[garm.Organization][0].Endpoint.Name, "ghes")
	})
	assertEqual(t, "changes of the second run", run{srv: srv, cfg: cfg}.prune(t), nil)
}
