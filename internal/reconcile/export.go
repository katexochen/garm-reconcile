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

package reconcile

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	commonparams "github.com/cloudbase/garm-provider-common/params"
	"github.com/cloudbase/garm/params"

	"github.com/katexochen/garm-reconcile/internal/config"
	"github.com/katexochen/garm-reconcile/internal/garm"
)

// Export returns the state of the controller behind client in the form it
// is declared, so that reconciling the controller with it changes nothing.
// It logs in as the admin user with the given name. Secrets are left empty,
// as garm does not reveal them, and so are the IDs of apps. The email of the
// admin user is a placeholder for the same reason.
//
// Garm accepts settings that a state file cannot declare, like a runner
// prefix in upper case. They are exported as they are, which leaves it to
// the validation of the state to point them out.
func Export(ctx context.Context, client *garm.Client, username, password string) (*config.Config, error) {
	cfg := &config.Config{Admin: config.Admin{Username: username, Email: config.Placeholder("the email of the admin user")}}
	r := New(client, cfg, Options{})
	if err := client.Login(ctx, username, password); err != nil {
		if garm.IsNotInitialized(err) {
			return nil, ErrNotInitialized
		}
		return nil, loginError(username, err)
	}

	info, err := client.ControllerInfo(ctx)
	if err != nil {
		return nil, fmt.Errorf("reading the controller settings: %w", err)
	}
	cfg.Controller = config.Controller{
		MetadataURL:          info.MetadataURL,
		CallbackURL:          info.CallbackURL,
		AgentURL:             info.AgentURL,
		WebhookURL:           info.WebhookURL,
		MinimumJobAgeBackoff: &info.MinimumJobAgeBackoff,
	}

	templates, err := client.ListTemplates(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing templates: %w", err)
	}
	for _, f := range r.forges {
		f.shippedTemplates = slices.DeleteFunc(slices.Clone(templates), func(t params.Template) bool { return !f.isShipped(t) })
		if err := r.exportForge(ctx, f, templates); err != nil {
			return nil, err
		}
		switch f.typ {
		case params.GithubEndpointType:
			cfg.GitHub = f.cfg
		case params.GiteaEndpointType:
			cfg.Gitea = f.cfg
		}
	}
	return cfg, nil
}

// exportForge fills in what garm has for the forge.
func (r *Reconciler) exportForge(ctx context.Context, f *forge, templates []params.Template) error {
	endpoints, err := r.client.ListEndpoints(ctx, f.typ)
	if err != nil {
		return fmt.Errorf("listing %s: %w", f.kind("endpoints"), err)
	}
	f.cfg.Endpoints = map[string]config.Endpoint{}
	for _, endpoint := range endpoints {
		// The endpoint garm ships is used without being declared.
		if f.typ == params.GithubEndpointType && endpoint.Name == config.DefaultGitHubEndpoint {
			continue
		}
		f.cfg.Endpoints[endpoint.Name] = config.Endpoint{
			Description:      endpoint.Description,
			APIBaseURL:       endpoint.APIBaseURL,
			BaseURL:          endpoint.BaseURL,
			UploadBaseURL:    endpoint.UploadBaseURL,
			CACertBundle:     string(endpoint.CACertBundle),
			ToolsMetadataURL: endpoint.ToolsMetadataURL,
		}
	}

	credentials, err := r.client.ListCredentials(ctx, f.typ)
	if err != nil {
		return fmt.Errorf("listing %s: %w", f.kind("credentials"), err)
	}
	f.cfg.Credentials = map[string]config.Credentials{}
	for _, have := range credentials {
		declared := config.Credentials{Description: have.Description, Endpoint: have.Endpoint.Name}
		if have.AuthType == params.ForgeAuthTypeApp {
			declared.App = &config.App{}
		} else {
			declared.PAT = &config.PAT{}
		}
		f.cfg.Credentials[have.Name] = declared
	}

	f.cfg.Templates = map[string]config.Template{}
	for _, listed := range templates {
		if !f.isManaged(listed) {
			continue
		}
		if _, exists := f.cfg.Templates[listed.Name]; exists {
			return fmt.Errorf("%s %s: several templates share this name, which has to be unique",
				f.kind("template"), listed.Name)
		}
		// Listed templates lack their data.
		template, err := r.client.GetTemplate(ctx, listed.ID)
		if err != nil {
			return fmt.Errorf("reading %s %s: %w", f.kind("template"), listed.Name, err)
		}
		f.cfg.Templates[template.Name] = config.Template{
			Description: template.Description,
			OSType:      template.OSType,
			Data:        string(template.Data),
		}
	}

	for _, kind := range f.entityKinds() {
		entities, err := r.forgeEntities(ctx, f, kind)
		if err != nil {
			return err
		}
		declared := map[string]config.Entity{}
		for _, have := range entities {
			name := have.FullName()
			if _, exists := declared[name]; exists {
				return fmt.Errorf("%s %s: exists on several endpoints, which a state file cannot hold",
					f.entityKind(kind), name)
			}
			declared[name], err = r.exportEntity(ctx, f, kind, have, templates)
			if err != nil {
				return err
			}
		}
		switch kind {
		case garm.Repository:
			f.cfg.Repositories = declared
		case garm.Organization:
			f.cfg.Organizations = declared
		case garm.Enterprise:
			f.cfg.Enterprises = declared
		}
	}
	return nil
}

func (r *Reconciler) exportEntity(ctx context.Context, f *forge, kind garm.EntityKind, have garm.Entity, templates []params.Template) (config.Entity, error) {
	name := have.FullName()
	entity := config.Entity{
		CredentialsName:  have.CredentialsName,
		PoolBalancerType: have.PoolBalancerType,
		AgentMode:        have.AgentMode,
		Pools:            map[string]config.Pool{},
		ScaleSets:        map[string]config.ScaleSet{},
	}

	hook, err := r.getWebhook(ctx, kind, have.ID)
	switch {
	case err == nil:
		entity.InstallWebhook = !isManualWebhook(hook, r.cfg.Controller.WebhookURL)
	case garm.IsNotFound(err), errors.Is(err, garm.ErrNoWebhooks), garm.IsBadRequest(err):
	default:
		return entity, fmt.Errorf("reading the %s of %s: %w", f.kind("webhook"), name, err)
	}

	pools, err := r.client.ListEntityPools(ctx, kind, have.ID)
	if err != nil {
		return entity, fmt.Errorf("listing the pools of %s %s: %w", kind, name, err)
	}
	for _, listed := range pools {
		prefix := listed.GetRunnerPrefix()
		// Garm does not tell runner prefixes apart by case.
		taken := slices.ContainsFunc(sortedKeys(entity.Pools), func(other string) bool { return strings.EqualFold(other, prefix) })
		if taken {
			return entity, fmt.Errorf("%s %s/%s: several pools of the %s share this runner prefix, which identifies a pool",
				f.kind("pool"), name, prefix, kind)
		}
		// Listed pools lack some of their settings.
		pool, err := r.client.GetPool(ctx, listed.ID)
		if err != nil {
			return entity, fmt.Errorf("reading %s %s/%s: %w", f.kind("pool"), name, prefix, err)
		}
		declared := poolParams(pool)
		declared.Prefix = ""
		declared.TemplateID = nil
		declared.ExtraSpecs = extraSpecs(declared.ExtraSpecs)
		entity.Pools[prefix] = config.Pool{
			CreatePoolParams: declared,
			Template:         f.templateName(templates, pool.TemplateID, pool.OSType),
		}
	}

	if f.typ != params.GithubEndpointType {
		return entity, nil
	}
	sets, err := r.client.ListEntityScaleSets(ctx, kind, have.ID)
	if err != nil {
		return entity, fmt.Errorf("listing the scale sets of %s %s: %w", kind, name, err)
	}
	for _, listed := range sets {
		if _, exists := entity.ScaleSets[listed.Name]; exists {
			return entity, fmt.Errorf("%s %s/%s: several scale sets of the %s share this name, which has to be unique",
				f.kind("scale set"), name, listed.Name, kind)
		}
		// Listed scale sets lack their extra specs.
		set, err := r.client.GetScaleSet(ctx, listed.ID)
		if err != nil {
			return entity, fmt.Errorf("reading %s %s/%s: %w", f.kind("scale set"), name, listed.Name, err)
		}
		declared := scaleSetParams(set)
		declared.Name = ""
		declared.Prefix = set.Prefix
		declared.TemplateID = nil
		declared.ExtraSpecs = extraSpecs(declared.ExtraSpecs)
		entity.ScaleSets[set.Name] = config.ScaleSet{
			CreateScaleSetParams: declared,
			Template:             f.templateName(templates, set.TemplateID, set.OSType),
		}
	}
	return entity, nil
}

// templateName returns the name by which a pool or scale set declares the
// template with the given ID. It is empty for the template that garm picks
// when none is declared.
func (f *forge) templateName(templates []params.Template, id uint, osType commonparams.OSType) string {
	for _, shipped := range f.shippedTemplates {
		if shipped.OSType == osType {
			if shipped.ID == id {
				return ""
			}
			break
		}
	}
	for _, template := range templates {
		if template.ID == id {
			return template.Name
		}
	}
	return ""
}
