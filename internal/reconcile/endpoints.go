package reconcile

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/cloudbase/garm/params"

	"github.com/katexochen/garm-reconcile/internal/config"
	"github.com/katexochen/garm-reconcile/internal/garm"
)

func (r *Reconciler) ensureEndpoints(ctx context.Context, f *forge) error {
	existing, err := r.client.ListEndpoints(ctx, f.typ)
	if err != nil {
		return fmt.Errorf("listing %s: %w", f.kind("endpoints"), err)
	}
	for _, name := range sortedKeys(f.cfg.Endpoints) {
		want := f.cfg.Endpoints[name]
		idx := slices.IndexFunc(existing, func(have params.ForgeEndpoint) bool {
			return strings.EqualFold(have.Name, name)
		})
		if idx < 0 {
			err = r.apply(Change{Action: Create, Kind: f.kind("endpoint"), Name: name}, func() error {
				return r.client.CreateEndpoint(ctx, f.typ, garm.CreateEndpointParams{
					Name:             name,
					Description:      want.Description,
					APIBaseURL:       want.APIBaseURL,
					BaseURL:          want.BaseURL,
					UploadBaseURL:    want.UploadBaseURL,
					CACertBundle:     []byte(want.CACertBundle),
					ToolsMetadataURL: want.ToolsMetadataURL,
				})
			})
		} else {
			err = r.updateEndpoint(ctx, f, name, want, existing[idx])
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func (r *Reconciler) updateEndpoint(ctx context.Context, f *forge, name string, want config.Endpoint, have params.ForgeEndpoint) error {
	update := garm.UpdateEndpointParams{Description: &want.Description}
	var fields []string
	// Garm refuses to change the URLs of an endpoint that has credentials.
	if want.APIBaseURL != have.APIBaseURL {
		update.APIBaseURL = &want.APIBaseURL
		fields = append(fields, "api_base_url")
	}
	if want.BaseURL != have.BaseURL {
		update.BaseURL = &want.BaseURL
		fields = append(fields, "base_url")
	}
	if want.Description != have.Description {
		fields = append(fields, "description")
	}
	// Garm cannot unset the CA bundle and the tools metadata URL, so they
	// are only managed when declared. Gitea endpoints have no upload URL.
	if want.CACertBundle != "" && want.CACertBundle != string(have.CACertBundle) {
		update.CACertBundle = []byte(want.CACertBundle)
		fields = append(fields, "ca_cert_bundle")
	}
	if want.ToolsMetadataURL != "" && want.ToolsMetadataURL != have.ToolsMetadataURL {
		update.ToolsMetadataURL = want.ToolsMetadataURL
		fields = append(fields, "tools_metadata_url")
	}
	if want.UploadBaseURL != "" && want.UploadBaseURL != have.UploadBaseURL {
		update.UploadBaseURL = &want.UploadBaseURL
		fields = append(fields, "upload_base_url")
	}
	if len(fields) == 0 {
		return nil
	}
	return r.apply(Change{Action: Update, Kind: f.kind("endpoint"), Name: name, Fields: fields}, func() error {
		return r.client.UpdateEndpoint(ctx, f.typ, have.Name, update)
	})
}

func (r *Reconciler) pruneEndpoints(ctx context.Context, f *forge) error {
	existing, err := r.client.ListEndpoints(ctx, f.typ)
	if err != nil {
		return fmt.Errorf("listing %s: %w", f.kind("endpoints"), err)
	}
	for _, have := range existing {
		declared := slices.ContainsFunc(sortedKeys(f.cfg.Endpoints), func(name string) bool {
			return strings.EqualFold(name, have.Name)
		})
		// The endpoint garm ships is used without being declared.
		if declared || (f.typ == params.GithubEndpointType && have.Name == config.DefaultGitHubEndpoint) {
			continue
		}
		err := r.apply(Change{Action: Delete, Kind: f.kind("endpoint"), Name: have.Name}, func() error {
			return r.client.DeleteEndpoint(ctx, f.typ, have.Name)
		})
		if err != nil {
			return err
		}
	}
	return nil
}
