// Copyright 2026 Paul Meyer
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package reconcile_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/cloudbase/garm/params"

	"github.com/katexochen/garm-reconcile/internal/config"
	"github.com/katexochen/garm-reconcile/internal/garm"
	"github.com/katexochen/garm-reconcile/internal/garm/garmtest"
	"github.com/katexochen/garm-reconcile/internal/reconcile"
)

func export(t *testing.T, srv *garmtest.Server, admin config.Admin) (*config.Config, error) {
	t.Helper()
	client, err := garm.NewClient(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	client.ReadOnly()
	if err := reconcile.Login(context.Background(), client, admin.Username, admin.Password); err != nil {
		return nil, err
	}
	return reconcile.Export(context.Background(), client, admin.Username)
}

// exportedState exports the controller and reads the result back like a
// state file, with the secrets garm keeps to itself filled in.
func exportedState(t *testing.T, srv *garmtest.Server, admin config.Admin) *config.Config {
	t.Helper()
	cfg, err := export(t, srv, admin)
	if err != nil {
		t.Fatalf("Export(): %v", err)
	}
	data, err := config.Marshal(cfg)
	if err != nil {
		t.Fatalf("Marshal(): %v", err)
	}
	if strings.Contains(string(data), admin.Password) {
		t.Errorf("the state file has the password of the admin:\n%s", data)
	}
	if _, err := config.Parse(data, config.PlaceholderSecrets); err == nil || !strings.Contains(err.Error(), "admin.email: the placeholder") {
		t.Errorf("Parse() = %v, want it to reject the placeholder for the email", err)
	}
	data = bytes.ReplaceAll(data, []byte(config.Placeholder("the email of the admin user")), []byte(admin.Email))
	cfg, err = config.Parse(data, func(ref string) (string, error) {
		if ref == config.AdminPasswordSecret {
			return admin.Password, nil
		}
		return "secret of " + ref, nil
	})
	if err != nil {
		t.Fatalf("Parse(): %v\n%s", err, data)
	}
	return cfg
}

func TestExportedStateChangesNothing(t *testing.T) {
	everything := withGitea()
	everything.GitHub.Templates = map[string]config.Template{
		"custom": {Description: "for custom images", OSType: "linux", Data: "#!/bin/sh\necho install"},
	}
	org := everything.GitHub.Organizations["my-org"]
	org.InstallWebhook = true
	org.PoolBalancerType = params.PoolBalancerTypePack
	org.ScaleSets = map[string]config.ScaleSet{"coco-set": scaleSet(), "custom": scaleSet()}
	custom := org.ScaleSets["custom"]
	custom.Prefix = "custom"
	custom.Template = "custom"
	custom.ExtraSpecs = json.RawMessage(`{"disable_updates":true}`)
	org.ScaleSets["custom"] = custom
	withSpecs := pool()
	withSpecs.Template = "custom"
	withSpecs.RunnerBootstrapTimeout = 5
	withSpecs.ExtraSpecs = json.RawMessage(`{"extra_packages":["git"]}`)
	org.Pools["specs"] = withSpecs
	everything.GitHub.Organizations["my-org"] = org
	everything.Controller.MinimumJobAgeBackoff = new(uint)

	tests := map[string]*config.Config{
		"minimal":    desiredState(),
		"everything": everything,
	}
	for name, cfg := range tests {
		t.Run(name, func(t *testing.T) {
			srv := garmtest.NewServer(t)
			srv.State(func(st *garmtest.State) { st.ManageWebhooks = true })
			run{srv: srv, cfg: cfg}.ensure(t)
			takeMutations(srv)

			exported := exportedState(t, srv, cfg.Admin)
			r := run{srv: srv, cfg: exported, dryRun: true}
			assertEqual(t, "changes", r.ensure(t), nil)
			assertEqual(t, "pruned", r.prune(t), nil)
			assertEqual(t, "requests that change the controller", takeMutations(srv), nil)

			// Everything that was declared is in the exported state.
			assertEqual(t, "github pools", len(exported.GitHub.Organizations["my-org"].Pools), len(cfg.GitHub.Organizations["my-org"].Pools))
			assertEqual(t, "github scale sets", len(exported.GitHub.Organizations["my-org"].ScaleSets), len(cfg.GitHub.Organizations["my-org"].ScaleSets))
			assertEqual(t, "github templates", exported.GitHub.Templates, cfg.GitHub.Templates)
			assertEqual(t, "gitea endpoints", exported.Gitea.Endpoints, cfg.Gitea.Endpoints)
			assertEqual(t, "gitea repositories", len(exported.Gitea.Repositories), len(cfg.Gitea.Repositories))
			assertEqual(t, "webhook", exported.GitHub.Organizations["my-org"].InstallWebhook, cfg.GitHub.Organizations["my-org"].InstallWebhook)
		})
	}
}

func TestExportedStateFile(t *testing.T) {
	cfg := desiredState()
	delete(cfg.GitHub.Repositories, "owner/repo")
	delete(cfg.GitHub.Enterprises, "corp")
	srv := initialized(t, cfg)

	exported, err := export(t, srv, cfg.Admin)
	if err != nil {
		t.Fatalf("Export(): %v", err)
	}
	data, err := config.Marshal(exported)
	if err != nil {
		t.Fatalf("Marshal(): %v", err)
	}
	want := `admin:
  email: '<fill in: the email of the admin user>'
  password:
    _secret: garm-admin-password
  username: admin
controller:
  agent_url: http://garm/agent
  callback_url: http://garm/api/v1/callbacks
  metadata_url: http://garm/api/v1/metadata
  minimum_job_age_backoff: 30
  webhook_url: http://garm/webhooks
github:
  credentials:
    main:
      endpoint: github.com
      pat:
        oauth2_token:
          _secret: github-main-token
  organizations:
    my-org:
      credentials_name: main
      pools:
        coco:
          enable_shell: false
          enabled: true
          flavor: default
          image: ubuntu
          max_runners: 2
          os_arch: amd64
          os_type: linux
          provider_name: incus
          runner_bootstrap_timeout: 20
          tags:
          - coco
          - linux
`
	if string(data) != want {
		t.Errorf("state file:\n%s\nwant:\n%s", data, want)
	}
}

func TestExportKeepsWhatAStateFileCannotDeclare(t *testing.T) {
	cfg := desiredState()
	srv := initialized(t, cfg)
	srv.State(func(st *garmtest.State) { st.Pools[0].Prefix = "CoCo" })

	exported, err := export(t, srv, cfg.Admin)
	if err != nil {
		t.Fatalf("Export(): %v", err)
	}
	if _, ok := exported.GitHub.Organizations["my-org"].Pools["CoCo"]; !ok {
		t.Errorf("pools = %+v, want the pool under its prefix", exported.GitHub.Organizations["my-org"].Pools)
	}
	err = exported.Validate()
	want := "github.organizations.my-org.pools.CoCo"
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("Validate() = %v, want it to contain %q", err, want)
	}
}

func TestExportManualWebhook(t *testing.T) {
	cfg := withWebhook()
	srv := garmtest.NewServer(t)
	srv.State(func(st *garmtest.State) { st.ManageWebhooks = true })
	run{srv: srv, cfg: cfg}.ensure(t)
	exported := exportedState(t, srv, cfg.Admin)
	assertEqual(t, "webhook of garm", exported.GitHub.Organizations["my-org"].InstallWebhook, true)

	srv.State(func(st *garmtest.State) {
		st.Entities[garm.Organization][0].Webhook.URL = cfg.Controller.WebhookURL
	})
	exported = exportedState(t, srv, cfg.Admin)
	assertEqual(t, "webhook added by hand", exported.GitHub.Organizations["my-org"].InstallWebhook, false)
	assertEqual(t, "changes", run{srv: srv, cfg: exported, dryRun: true}.prune(t), nil)
}

func TestExportAppCredentials(t *testing.T) {
	cfg, _ := withAppCredentials(t)
	srv := initialized(t, cfg)

	exported, err := export(t, srv, cfg.Admin)
	if err != nil {
		t.Fatalf("Export(): %v", err)
	}
	data, err := config.Marshal(exported)
	if err != nil {
		t.Fatalf("Marshal(): %v", err)
	}
	// Garm does not tell which app the credentials are for.
	want := `
    main:
      app:
        app_id: 0
        installation_id: 0
        private_key:
          _secret: github-main-private-key
      endpoint: github.com
`
	if !strings.Contains(string(data), want) {
		t.Errorf("state file:\n%s\nwant it to contain:%s", data, want)
	}
}

func TestExportErrors(t *testing.T) {
	admin := desiredState().Admin

	t.Run("not initialized", func(t *testing.T) {
		_, err := export(t, garmtest.NewServer(t), admin)
		if !errors.Is(err, reconcile.ErrNotInitialized) {
			t.Errorf("Export() = %v, want %v", err, reconcile.ErrNotInitialized)
		}
	})
	t.Run("wrong password", func(t *testing.T) {
		srv := initialized(t, desiredState())
		wrong := admin
		wrong.Password = "changed"
		_, err := export(t, srv, wrong)
		if !errors.Is(err, reconcile.ErrLoginRejected) {
			t.Errorf("Export() = %v, want %v", err, reconcile.ErrLoginRejected)
		}
	})
	t.Run("pools with the same runner prefix", func(t *testing.T) {
		srv := initialized(t, desiredState())
		srv.State(func(st *garmtest.State) {
			twin := *st.Pools[0]
			twin.ID = "twin"
			twin.Prefix = "COCO"
			twin.ProviderName = "other"
			st.Pools = append(st.Pools, &twin)
		})
		_, err := export(t, srv, admin)
		want := "github pool my-org/COCO: several pools of the organization share this runner prefix"
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("Export() = %v, want it to contain %q", err, want)
		}
	})
	t.Run("template that is not text", func(t *testing.T) {
		for _, data := range []string{"echo \xff", "echo \u0085", "echo \x7f"} {
			srv := initialized(t, withTemplate())
			srv.State(func(st *garmtest.State) { findTemplate(t, st, "nixos").Data = []byte(data) })
			_, err := export(t, srv, admin)
			want := "github template nixos: its data is not text"
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Errorf("Export() with the data %q = %v, want it to contain %q", data, err, want)
			}
		}
	})
	t.Run("scale sets with the same name", func(t *testing.T) {
		srv := initialized(t, withScaleSet())
		srv.State(func(st *garmtest.State) {
			twin := *st.ScaleSets[0]
			twin.ID++
			st.ScaleSets = append(st.ScaleSets, &twin)
		})
		_, err := export(t, srv, admin)
		want := "github scale set my-org/coco-set: several scale sets of the organization share this name"
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("Export() = %v, want it to contain %q", err, want)
		}
	})
}
