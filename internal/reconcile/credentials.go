package reconcile

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/cloudbase/garm/params"

	"github.com/katexochen/garm-reconcile/internal/config"
)

func (r *Reconciler) ensureCredentials(ctx context.Context) error {
	existing, err := r.client.ListGithubCredentials(ctx)
	if err != nil {
		return fmt.Errorf("listing %s: %w", KindCredentials, err)
	}
	for _, name := range sortedKeys(r.cfg.GitHub.Credentials) {
		want := r.cfg.GitHub.Credentials[name]
		idx := slices.IndexFunc(existing, func(c params.ForgeCredentials) bool {
			return strings.EqualFold(c.Name, name)
		})
		if idx < 0 {
			err = r.createCredentials(ctx, name, want)
		} else {
			err = r.updateCredentials(ctx, name, want, existing[idx])
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func (r *Reconciler) createCredentials(ctx context.Context, name string, want config.Credentials) error {
	create := params.CreateGithubCredentialsParams{
		Name:        name,
		Description: want.Description,
		Endpoint:    want.Endpoint,
		AuthType:    authType(want),
	}
	if want.PAT != nil {
		create.PAT = githubPAT(want.PAT)
	} else {
		create.App = githubApp(want.App)
	}
	return r.apply(Change{Action: Create, Kind: KindCredentials, Name: name}, func() error {
		return r.client.CreateGithubCredentials(ctx, create)
	})
}

func (r *Reconciler) updateCredentials(ctx context.Context, name string, want config.Credentials, have params.ForgeCredentials) error {
	// Garm cannot change these, and the credentials cannot be replaced
	// under the same name without taking the runners of their entities
	// down.
	if !strings.EqualFold(want.Endpoint, have.Endpoint.Name) {
		return fmt.Errorf("%s %s: the endpoint cannot be changed from %s to %s, declare new credentials instead",
			KindCredentials, name, have.Endpoint.Name, want.Endpoint)
	}
	if authType(want) != have.AuthType {
		return fmt.Errorf("%s %s: the authentication cannot be changed from %s to %s, declare new credentials instead",
			KindCredentials, name, have.AuthType, authType(want))
	}

	update := params.UpdateGithubCredentialsParams{Description: &want.Description}
	if want.PAT != nil {
		pat := githubPAT(want.PAT)
		update.PAT = &pat
	} else {
		app := githubApp(want.App)
		update.App = &app
	}
	if want.Description != have.Description {
		r.opts.Report(Change{Action: Update, Kind: KindCredentials, Name: name, Fields: []string{"description"}})
	}
	if r.opts.DryRun {
		return nil
	}
	// Garm does not reveal whether the secret changed, so it is always
	// sent.
	if err := r.client.UpdateGithubCredentials(ctx, have.ID, update); err != nil {
		return fmt.Errorf("update %s %s: %w", KindCredentials, name, err)
	}
	return nil
}

func (r *Reconciler) pruneCredentials(ctx context.Context) error {
	existing, err := r.client.ListGithubCredentials(ctx)
	if err != nil {
		return fmt.Errorf("listing %s: %w", KindCredentials, err)
	}
	for _, have := range existing {
		if r.declaresCredentials(have.Name) {
			continue
		}
		err := r.apply(Change{Action: Delete, Kind: KindCredentials, Name: have.Name}, func() error {
			return r.client.DeleteGithubCredentials(ctx, have.ID)
		})
		if err != nil {
			return err
		}
	}
	return nil
}

func (r *Reconciler) declaresCredentials(name string) bool {
	for declared := range r.cfg.GitHub.Credentials {
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

func githubPAT(pat *config.PAT) params.GithubPAT {
	return params.GithubPAT{OAuth2Token: pat.OAuth2Token}
}

func githubApp(app *config.App) params.GithubApp {
	return params.GithubApp{
		AppID:           app.AppID,
		InstallationID:  app.InstallationID,
		PrivateKeyBytes: []byte(app.PrivateKey),
	}
}
