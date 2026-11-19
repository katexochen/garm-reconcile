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

package garm_test

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"testing"

	commonparams "github.com/cloudbase/garm-provider-common/params"
	"github.com/cloudbase/garm/params"

	"github.com/katexochen/garm-reconcile/internal/garm"
	"github.com/katexochen/garm-reconcile/internal/garm/garmtest"
)

var admin = params.NewUserParams{Username: "admin", Email: "admin@example.com", Password: "hunter2"}

func ptr[T any](v T) *T { return &v }

func newClient(t *testing.T, srv *garmtest.Server) *garm.Client {
	t.Helper()
	client, err := garm.NewClient(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	return client
}

// configuredClient returns a client that is logged in to an initialized
// controller with its URLs set.
func configuredClient(t *testing.T) (*garm.Client, *garmtest.Server) {
	t.Helper()
	ctx := context.Background()
	srv := garmtest.NewServer(t)
	client := newClient(t, srv)
	if err := client.FirstRun(ctx, admin); err != nil {
		t.Fatal(err)
	}
	if err := client.Login(ctx, admin.Username, admin.Password); err != nil {
		t.Fatal(err)
	}
	err := client.UpdateController(ctx, params.UpdateControllerParams{
		MetadataURL: ptr("http://garm/api/v1/metadata"),
		CallbackURL: ptr("http://garm/api/v1/callbacks"),
		AgentURL:    ptr("http://garm/agent"),
	})
	if err != nil {
		t.Fatal(err)
	}
	return client, srv
}

func TestNewClientRejectsRelativeURL(t *testing.T) {
	for _, url := range []string{"", "localhost:9997", "/api"} {
		if _, err := garm.NewClient(url); err == nil {
			t.Errorf("NewClient(%q) succeeded, want an error", url)
		}
	}
}

func TestInitialization(t *testing.T) {
	ctx := context.Background()
	srv := garmtest.NewServer(t)
	client := newClient(t, srv)

	err := client.Login(ctx, admin.Username, admin.Password)
	if !garm.IsNotInitialized(err) {
		t.Fatalf("Login() before the first run: error = %v, want not initialized", err)
	}
	if err := client.FirstRun(ctx, admin); err != nil {
		t.Fatal(err)
	}
	if err := client.FirstRun(ctx, admin); err == nil || garm.IsNotInitialized(err) {
		t.Errorf("second FirstRun(): error = %v, want a conflict", err)
	}

	if _, err := client.ControllerInfo(ctx); err == nil {
		t.Error("ControllerInfo() succeeded without logging in")
	}
	err = client.Login(ctx, admin.Username, "wrong")
	var apiErr *garm.Error
	if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusUnauthorized {
		t.Errorf("Login() with a wrong password: error = %v, want HTTP 401", err)
	}
	if err := client.Login(ctx, admin.Username, admin.Password); err != nil {
		t.Fatal(err)
	}

	// Everything but the controller settings is unavailable until the
	// controller URLs are set.
	if _, err := client.ListCredentials(ctx, params.GithubEndpointType); err == nil {
		t.Error("ListCredentials() succeeded without controller URLs")
	}
	update := params.UpdateControllerParams{
		MetadataURL: ptr("http://garm/api/v1/metadata"),
		CallbackURL: ptr("http://garm/api/v1/callbacks"),
		AgentURL:    ptr("http://garm/agent"),
	}
	if err := client.UpdateController(ctx, update); err != nil {
		t.Fatal(err)
	}
	info, err := client.ControllerInfo(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if info.MetadataURL != *update.MetadataURL || info.CallbackURL != *update.CallbackURL || info.AgentURL != *update.AgentURL {
		t.Errorf("ControllerInfo() = %+v, want the URLs of %+v", info, update)
	}
	if _, err := client.ListCredentials(ctx, params.GithubEndpointType); err != nil {
		t.Errorf("ListCredentials() with controller URLs: %v", err)
	}
}

func TestCredentials(t *testing.T) {
	ctx := context.Background()
	client, srv := configuredClient(t)

	create := params.CreateGithubCredentialsParams{
		Name:     "main",
		Endpoint: "github.com",
		AuthType: params.ForgeAuthTypePAT,
		PAT:      params.GithubPAT{OAuth2Token: "ghp_old"},
	}
	if err := client.CreateCredentials(ctx, params.GithubEndpointType, create); err != nil {
		t.Fatal(err)
	}
	creds, err := client.ListCredentials(ctx, params.GithubEndpointType)
	if err != nil {
		t.Fatal(err)
	}
	if len(creds) != 1 || creds[0].Name != "main" || creds[0].Endpoint.Name != "github.com" || creds[0].AuthType != params.ForgeAuthTypePAT {
		t.Fatalf("ListCredentials() = %+v, want the created credentials", creds)
	}

	update := params.UpdateGithubCredentialsParams{
		Description: ptr("rotated"),
		PAT:         &params.GithubPAT{OAuth2Token: "ghp_new"},
	}
	if err := client.UpdateCredentials(ctx, params.GithubEndpointType, creds[0].ID, update); err != nil {
		t.Fatal(err)
	}
	srv.State(func(st *garmtest.State) {
		if got := st.Credentials[0]; got.Description != "rotated" || got.PAT.OAuth2Token != "ghp_new" {
			t.Errorf("credentials after update = %+v, want the new description and token", got)
		}
	})

	if err := client.DeleteCredentials(ctx, params.GithubEndpointType, creds[0].ID); err != nil {
		t.Fatal(err)
	}
	srv.State(func(st *garmtest.State) {
		if len(st.Credentials) != 0 {
			t.Errorf("credentials after delete = %+v, want none", st.Credentials)
		}
	})
}

func TestEntitiesAndPools(t *testing.T) {
	ctx := context.Background()
	client, srv := configuredClient(t)
	err := client.CreateCredentials(ctx, params.GithubEndpointType, params.CreateGithubCredentialsParams{
		Name: "main", Endpoint: "github.com", AuthType: params.ForgeAuthTypePAT,
		PAT: params.GithubPAT{OAuth2Token: "ghp_token"},
	})
	if err != nil {
		t.Fatal(err)
	}

	names := map[garm.EntityKind]garm.CreateEntityParams{
		garm.Repository:   {Owner: "owner", Name: "repo"},
		garm.Organization: {Name: "my-org"},
		garm.Enterprise:   {Name: "corp"},
	}
	for kind, create := range names {
		t.Run(string(kind), func(t *testing.T) {
			create.CredentialsName = "main"
			create.WebhookSecret = "secret"
			create.ForgeType = params.GithubEndpointType
			created, err := client.CreateEntity(ctx, kind, create)
			if err != nil {
				t.Fatal(err)
			}

			entities, err := client.ListEntities(ctx, kind)
			if err != nil {
				t.Fatal(err)
			}
			want := garm.Entity{
				ID: created.ID, Owner: create.Owner, Name: create.Name, CredentialsName: "main",
			}
			want.Endpoint = entities[0].Endpoint
			if len(entities) != 1 || !reflect.DeepEqual(entities[0], want) || want.Endpoint.Name != "github.com" {
				t.Fatalf("ListEntities() = %+v, want [%+v]", entities, want)
			}

			update := params.UpdateEntityParams{PoolBalancerType: params.PoolBalancerTypePack, AgentMode: ptr(true)}
			if err := client.UpdateEntity(ctx, kind, created.ID, update); err != nil {
				t.Fatal(err)
			}
			entities, err = client.ListEntities(ctx, kind)
			if err != nil {
				t.Fatal(err)
			}
			if got := entities[0]; got.PoolBalancerType != params.PoolBalancerTypePack || !got.AgentMode {
				t.Errorf("entity after update = %+v, want pack balancing and agent mode", got)
			}

			testPools(t, client, srv, kind, created.ID)

			if err := client.DeleteEntity(ctx, kind, created.ID); err != nil {
				t.Fatal(err)
			}
			entities, err = client.ListEntities(ctx, kind)
			if err != nil {
				t.Fatal(err)
			}
			if len(entities) != 0 {
				t.Errorf("ListEntities() after delete = %+v, want none", entities)
			}
		})
	}
}

func testPools(t *testing.T, client *garm.Client, srv *garmtest.Server, kind garm.EntityKind, entityID string) {
	t.Helper()
	ctx := context.Background()
	create := params.CreatePoolParams{
		RunnerPrefix: params.RunnerPrefix{Prefix: "coco"},
		ProviderName: "incus",
		Image:        "ubuntu",
		Flavor:       "default",
		OSType:       commonparams.Linux,
		OSArch:       commonparams.Amd64,
		Tags:         []string{"coco"},
		MaxRunners:   2,
		ExtraSpecs:   []byte(`{"disable_updates":true}`),
	}
	if err := client.CreateEntityPool(ctx, kind, entityID, create); err != nil {
		t.Fatal(err)
	}
	pools, err := client.ListEntityPools(ctx, kind, entityID)
	if err != nil {
		t.Fatal(err)
	}
	if len(pools) != 1 || pools[0].Prefix != "coco" || pools[0].ProviderName != "incus" {
		t.Fatalf("ListEntityPools() = %+v, want the created pool", pools)
	}
	if pools[0].ExtraSpecs != nil {
		t.Errorf("listed pool has extra specs %s, which garm leaves out", pools[0].ExtraSpecs)
	}
	pool, err := client.GetPool(ctx, pools[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if string(pool.ExtraSpecs) != `{"disable_updates":true}` {
		t.Errorf("GetPool() returned the extra specs %s, want the created ones", pool.ExtraSpecs)
	}

	if err := client.DeleteEntity(ctx, kind, entityID); !garm.IsBadRequest(err) {
		t.Errorf("DeleteEntity() with a pool: error = %v, want a bad request", err)
	}

	if err := client.UpdatePool(ctx, pool.ID, params.UpdatePoolParams{MaxRunners: ptr(uint(4))}); err != nil {
		t.Fatal(err)
	}
	pool, err = client.GetPool(ctx, pool.ID)
	if err != nil {
		t.Fatal(err)
	}
	if pool.MaxRunners != 4 {
		t.Errorf("max_runners after update = %d, want 4", pool.MaxRunners)
	}

	srv.State(func(st *garmtest.State) {
		st.Instances = append(st.Instances,
			&params.Instance{Name: "coco-idle", PoolID: pool.ID, Status: commonparams.InstanceRunning, RunnerStatus: params.RunnerIdle},
			&params.Instance{Name: "coco-busy", PoolID: pool.ID, Status: commonparams.InstanceRunning, RunnerStatus: params.RunnerActive},
		)
	})
	instances, err := client.ListPoolInstances(ctx, pool.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(instances) != 2 {
		t.Fatalf("ListPoolInstances() = %+v, want two instances", instances)
	}
	if err := client.DeletePool(ctx, pool.ID); !garm.IsBadRequest(err) {
		t.Errorf("DeletePool() with runners: error = %v, want a bad request", err)
	}
	if err := client.DeleteInstance(ctx, "coco-busy"); !garm.IsBadRequest(err) {
		t.Errorf("DeleteInstance() of a busy runner: error = %v, want a bad request", err)
	}
	if err := client.DeleteInstance(ctx, "coco-idle"); err != nil {
		t.Fatal(err)
	}
	srv.State(func(st *garmtest.State) { st.Instances = nil })
	if err := client.DeletePool(ctx, pool.ID); err != nil {
		t.Fatal(err)
	}
}

func TestErrorMessage(t *testing.T) {
	ctx := context.Background()
	client := newClient(t, garmtest.NewServer(t))
	err := client.Login(ctx, admin.Username, admin.Password)
	if err == nil || !strings.Contains(err.Error(), "init_required: Missing superuser (HTTP 409)") {
		t.Errorf("error = %v, want it to describe the API error", err)
	}
}

func TestReadOnly(t *testing.T) {
	ctx := context.Background()
	client, srv := configuredClient(t)
	srv.State(func(st *garmtest.State) { st.Mutations = nil })

	client.ReadOnly()
	if err := client.Login(ctx, admin.Username, admin.Password); err != nil {
		t.Errorf("Login() with a read-only client: %v", err)
	}
	if _, err := client.ListCredentials(ctx, params.GithubEndpointType); err != nil {
		t.Errorf("ListCredentials() with a read-only client: %v", err)
	}
	err := client.UpdateController(ctx, params.UpdateControllerParams{WebhookURL: ptr("http://garm/webhooks")})
	if err == nil || !strings.Contains(err.Error(), "refusing PUT controller with a read-only client") {
		t.Errorf("UpdateController() with a read-only client = %v, want it to be refused", err)
	}
	srv.State(func(st *garmtest.State) {
		if len(st.Mutations) != 0 {
			t.Errorf("mutations = %v, want none", st.Mutations)
		}
	})
}

func TestNotFound(t *testing.T) {
	client, _ := configuredClient(t)
	err := client.DeleteInstance(context.Background(), "missing")
	if !garm.IsNotFound(err) || garm.IsBadRequest(err) {
		t.Errorf("DeleteInstance() of a missing instance: error = %v, want not found", err)
	}
}

func TestTemplates(t *testing.T) {
	ctx := context.Background()
	client, _ := configuredClient(t)

	created, err := client.CreateTemplate(ctx, params.CreateTemplateParams{
		Name: "nixos", OSType: commonparams.Linux, ForgeType: params.GithubEndpointType, Data: []byte("#!/bin/sh"),
	})
	if err != nil {
		t.Fatal(err)
	}
	templates, err := client.ListTemplates(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// Garm ships templates of its own.
	last := templates[len(templates)-1]
	if len(templates) != 5 || last.Name != "nixos" || last.Data != nil {
		t.Fatalf("ListTemplates() = %+v, want the created template without data", templates)
	}

	update := params.UpdateTemplateParams{Description: ptr("changed"), Data: []byte("#!/bin/bash")}
	if err := client.UpdateTemplate(ctx, created.ID, update); err != nil {
		t.Fatal(err)
	}
	template, err := client.GetTemplate(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if template.Description != "changed" || string(template.Data) != "#!/bin/bash" {
		t.Errorf("GetTemplate() = %+v, want the updated template", template)
	}

	if err := client.DeleteTemplate(ctx, created.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := client.GetTemplate(ctx, created.ID); !garm.IsNotFound(err) {
		t.Errorf("GetTemplate() after delete: error = %v, want not found", err)
	}
}

func TestScaleSets(t *testing.T) {
	ctx := context.Background()
	client, srv := configuredClient(t)
	err := client.CreateCredentials(ctx, params.GithubEndpointType, params.CreateGithubCredentialsParams{
		Name: "main", Endpoint: "github.com", AuthType: params.ForgeAuthTypePAT,
		PAT: params.GithubPAT{OAuth2Token: "ghp_token"},
	})
	if err != nil {
		t.Fatal(err)
	}
	org, err := client.CreateEntity(ctx, garm.Organization, garm.CreateEntityParams{
		Name: "my-org", CredentialsName: "main", WebhookSecret: "secret",
	})
	if err != nil {
		t.Fatal(err)
	}

	create := params.CreateScaleSetParams{
		Name: "coco-set", ProviderName: "incus", Image: "ubuntu", Flavor: "default",
		OSType: commonparams.Linux, OSArch: commonparams.Amd64, MaxRunners: 2, Enabled: true,
		ExtraSpecs: []byte(`{"disable_updates":true}`),
	}
	if err := client.CreateEntityScaleSet(ctx, garm.Organization, org.ID, create); err != nil {
		t.Fatal(err)
	}
	sets, err := client.ListEntityScaleSets(ctx, garm.Organization, org.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(sets) != 1 || sets[0].Name != "coco-set" || sets[0].ExtraSpecs != nil {
		t.Fatalf("ListEntityScaleSets() = %+v, want the created scale set without extra specs", sets)
	}
	id := sets[0].ID
	set, err := client.GetScaleSet(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if string(set.ExtraSpecs) != `{"disable_updates":true}` {
		t.Errorf("GetScaleSet() returned the extra specs %s, want the created ones", set.ExtraSpecs)
	}

	srv.State(func(st *garmtest.State) {
		st.Instances = []*params.Instance{{Name: "set-idle", ScaleSetID: id, Status: commonparams.InstanceRunning, RunnerStatus: params.RunnerIdle}}
	})
	instances, err := client.ListScaleSetInstances(ctx, id)
	if err != nil || len(instances) != 1 {
		t.Fatalf("ListScaleSetInstances() = %+v, %v, want one instance", instances, err)
	}
	if err := client.DeleteScaleSet(ctx, id); !garm.IsBadRequest(err) {
		t.Errorf("DeleteScaleSet() with runners: error = %v, want a bad request", err)
	}
	if err := client.DeleteInstance(ctx, "set-idle"); err != nil {
		t.Fatal(err)
	}
	if err := client.DeleteScaleSet(ctx, id); !garm.IsBadRequest(err) {
		t.Errorf("DeleteScaleSet() of an enabled scale set: error = %v, want a bad request", err)
	}
	if err := client.DeleteEntity(ctx, garm.Organization, org.ID); !garm.IsBadRequest(err) {
		t.Errorf("DeleteEntity() with a scale set: error = %v, want a bad request", err)
	}
	if err := client.UpdateScaleSet(ctx, id, params.UpdateScaleSetParams{Enabled: ptr(false)}); err != nil {
		t.Fatal(err)
	}
	if err := client.DeleteScaleSet(ctx, id); err != nil {
		t.Fatal(err)
	}
}

func TestGiteaEndpointsAndCredentials(t *testing.T) {
	ctx := context.Background()
	client, _ := configuredClient(t)
	gitea := params.GiteaEndpointType

	create := garm.CreateEndpointParams{
		Name: "local", APIBaseURL: "http://gitea/api/v1", BaseURL: "http://gitea",
		ToolsMetadataURL: "http://gitea/releases.json",
	}
	if err := client.CreateEndpoint(ctx, gitea, create); err != nil {
		t.Fatal(err)
	}
	endpoints, err := client.ListEndpoints(ctx, gitea)
	if err != nil {
		t.Fatal(err)
	}
	if len(endpoints) != 1 || endpoints[0].Name != "local" || endpoints[0].ToolsMetadataURL != create.ToolsMetadataURL {
		t.Fatalf("ListEndpoints() = %+v, want the created endpoint", endpoints)
	}
	github, err := client.ListEndpoints(ctx, params.GithubEndpointType)
	if err != nil || len(github) != 1 || github[0].Name != "github.com" {
		t.Fatalf("ListEndpoints() for GitHub = %+v, %v, want the endpoint garm ships", github, err)
	}

	err = client.CreateCredentials(ctx, gitea, params.CreateGithubCredentialsParams{
		Name: "main", Endpoint: "local", AuthType: params.ForgeAuthTypePAT, PAT: params.GithubPAT{OAuth2Token: "token"},
	})
	if err != nil {
		t.Fatal(err)
	}
	creds, err := client.ListCredentials(ctx, gitea)
	if err != nil || len(creds) != 1 || creds[0].ForgeType != gitea {
		t.Fatalf("ListCredentials() = %+v, %v, want the Gitea credentials", creds, err)
	}
	if creds, err := client.ListCredentials(ctx, params.GithubEndpointType); err != nil || len(creds) != 0 {
		t.Errorf("ListCredentials() for GitHub = %+v, %v, want none", creds, err)
	}

	moved := garm.UpdateEndpointParams{BaseURL: ptr("http://other")}
	if err := client.UpdateEndpoint(ctx, gitea, "local", moved); !garm.IsBadRequest(err) {
		t.Errorf("UpdateEndpoint() of the URL of a used endpoint: error = %v, want a bad request", err)
	}
	if err := client.UpdateEndpoint(ctx, gitea, "local", garm.UpdateEndpointParams{Description: ptr("changed")}); err != nil {
		t.Fatal(err)
	}
	if err := client.DeleteEndpoint(ctx, gitea, "local"); !garm.IsBadRequest(err) {
		t.Errorf("DeleteEndpoint() of a used endpoint: error = %v, want a bad request", err)
	}
	if err := client.DeleteCredentials(ctx, gitea, creds[0].ID); err != nil {
		t.Fatal(err)
	}
	if err := client.DeleteEndpoint(ctx, gitea, "local"); err != nil {
		t.Fatal(err)
	}
}
