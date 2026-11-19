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
	"fmt"
	"slices"
	"strings"

	"github.com/cloudbase/garm/params"

	"github.com/katexochen/garm-reconcile/internal/config"
)

func (r *Reconciler) ensureCredentials(ctx context.Context, f *forge) error {
	existing, err := r.client.ListCredentials(ctx, f.typ)
	if err != nil {
		return fmt.Errorf("listing %s: %w", f.kind("credentials"), err)
	}
	for _, name := range sortedKeys(f.cfg.Credentials) {
		want := f.cfg.Credentials[name]
		idx := slices.IndexFunc(existing, func(c params.ForgeCredentials) bool {
			return strings.EqualFold(c.Name, name)
		})
		if idx < 0 {
			err = r.createCredentials(ctx, f, name, want)
		} else {
			err = r.updateCredentials(ctx, f, name, want, existing[idx])
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func (r *Reconciler) createCredentials(ctx context.Context, f *forge, name string, want config.Credentials) error {
	create := params.CreateGithubCredentialsParams{
		Name:        name,
		Description: want.Description,
		Endpoint:    want.Endpoint,
		AuthType:    authType(want),
	}
	if want.PAT != nil {
		create.PAT = tokenParams(want.PAT)
	} else {
		create.App = appParams(want.App)
	}
	return r.apply(Change{Action: Create, Kind: f.kind("credentials"), Name: name}, func() error {
		return r.client.CreateCredentials(ctx, f.typ, create)
	})
}

func (r *Reconciler) updateCredentials(ctx context.Context, f *forge, name string, want config.Credentials, have params.ForgeCredentials) error {
	// Garm cannot change these, and the credentials cannot be replaced
	// under the same name without taking the runners of their entities
	// down.
	if !strings.EqualFold(want.Endpoint, have.Endpoint.Name) {
		return fmt.Errorf("%s %s: the endpoint cannot be changed from %s to %s, declare new credentials instead",
			f.kind("credentials"), name, have.Endpoint.Name, want.Endpoint)
	}
	if authType(want) != have.AuthType {
		return fmt.Errorf("%s %s: the authentication cannot be changed from %s to %s, declare new credentials instead",
			f.kind("credentials"), name, have.AuthType, authType(want))
	}

	update := params.UpdateGithubCredentialsParams{Description: &want.Description}
	if want.PAT != nil {
		pat := tokenParams(want.PAT)
		update.PAT = &pat
	} else {
		app := appParams(want.App)
		update.App = &app
	}
	if want.Description != have.Description {
		r.opts.Report(Change{Action: Update, Kind: f.kind("credentials"), Name: name, Fields: []string{"description"}})
	}
	if r.opts.DryRun {
		return nil
	}
	// Garm does not reveal whether the secret changed, so it is always
	// sent.
	if err := r.client.UpdateCredentials(ctx, f.typ, have.ID, update); err != nil {
		return fmt.Errorf("update %s %s: %w", f.kind("credentials"), name, err)
	}
	return nil
}

func (r *Reconciler) pruneCredentials(ctx context.Context, f *forge) error {
	existing, err := r.client.ListCredentials(ctx, f.typ)
	if err != nil {
		return fmt.Errorf("listing %s: %w", f.kind("credentials"), err)
	}
	for _, have := range existing {
		if f.declaresCredentials(have.Name) {
			continue
		}
		err := r.apply(Change{Action: Delete, Kind: f.kind("credentials"), Name: have.Name}, func() error {
			return r.client.DeleteCredentials(ctx, f.typ, have.ID)
		})
		if err != nil {
			return err
		}
	}
	return nil
}

func (f *forge) declaresCredentials(name string) bool {
	for declared := range f.cfg.Credentials {
		if strings.EqualFold(declared, name) {
			return true
		}
	}
	return false
}

func authType(c config.Credentials) params.ForgeAuthType {
	if c.App != nil {
		return params.ForgeAuthTypeApp
	}
	return params.ForgeAuthTypePAT
}

func tokenParams(pat *config.PAT) params.GithubPAT {
	return params.GithubPAT{OAuth2Token: pat.OAuth2Token}
}

func appParams(app *config.App) params.GithubApp {
	return params.GithubApp{
		AppID:           app.AppID,
		InstallationID:  app.InstallationID,
		PrivateKeyBytes: []byte(app.PrivateKey),
	}
}
