package reconcile

import (
	"context"
	"fmt"
	"slices"

	"github.com/cloudbase/garm/params"

	"github.com/katexochen/garm-reconcile/internal/config"
)

// isManagedTemplate reports whether a template is subject to reconciling.
// The templates garm ships are not, nor are those of other forges.
func isManagedTemplate(template params.Template) bool {
	return template.Owner != params.SystemUser && template.ForgeType == params.GithubEndpointType
}

func (r *Reconciler) ensureTemplates(ctx context.Context) error {
	existing, err := r.client.ListTemplates(ctx)
	if err != nil {
		return fmt.Errorf("listing templates: %w", err)
	}
	for _, name := range sortedKeys(r.cfg.GitHub.Templates) {
		want := r.cfg.GitHub.Templates[name]
		idx := slices.IndexFunc(existing, func(have params.Template) bool {
			return isManagedTemplate(have) && have.Name == name
		})
		if idx < 0 {
			err = r.createTemplate(ctx, name, want)
		} else {
			err = r.updateTemplate(ctx, name, want, existing[idx])
		}
		if err != nil {
			return err
		}
	}

	// Pools refer to templates by ID, including the ones just created.
	if r.opts.DryRun {
		r.templates = existing
		return nil
	}
	r.templates, err = r.client.ListTemplates(ctx)
	if err != nil {
		return fmt.Errorf("listing templates: %w", err)
	}
	return nil
}

func (r *Reconciler) createTemplate(ctx context.Context, name string, want config.Template) error {
	create := params.CreateTemplateParams{
		Name:        name,
		Description: want.Description,
		Data:        []byte(want.Data),
		OSType:      want.OSType,
		ForgeType:   params.GithubEndpointType,
	}
	return r.apply(Change{Action: Create, Kind: KindTemplate, Name: name}, func() error {
		_, err := r.client.CreateTemplate(ctx, create)
		return err
	})
}

func (r *Reconciler) updateTemplate(ctx context.Context, name string, want config.Template, listed params.Template) error {
	if want.OSType != listed.OSType {
		return fmt.Errorf("%s %s: the OS type cannot be changed from %s to %s, declare a new template instead",
			KindTemplate, name, listed.OSType, want.OSType)
	}
	// Listed templates lack their data.
	have, err := r.client.GetTemplate(ctx, listed.ID)
	if err != nil {
		return fmt.Errorf("reading %s %s: %w", KindTemplate, name, err)
	}
	var fields []string
	if want.Data != string(have.Data) {
		fields = append(fields, "data")
	}
	if want.Description != have.Description {
		fields = append(fields, "description")
	}
	if len(fields) == 0 {
		return nil
	}
	update := params.UpdateTemplateParams{Description: &want.Description, Data: []byte(want.Data)}
	return r.apply(Change{Action: Update, Kind: KindTemplate, Name: name, Fields: fields}, func() error {
		return r.client.UpdateTemplate(ctx, have.ID, update)
	})
}

func (r *Reconciler) pruneTemplates(ctx context.Context) error {
	existing, err := r.client.ListTemplates(ctx)
	if err != nil {
		return fmt.Errorf("listing templates: %w", err)
	}
	for _, have := range existing {
		if _, declared := r.cfg.GitHub.Templates[have.Name]; declared || !isManagedTemplate(have) {
			continue
		}
		err := r.apply(Change{Action: Delete, Kind: KindTemplate, Name: have.Name}, func() error {
			return r.client.DeleteTemplate(ctx, have.ID)
		})
		if err != nil {
			return err
		}
	}
	return nil
}

// templateID returns the ID of the template a pool names. Declared templates
// take precedence over the ones garm ships. In a dry run, the ID of a
// template that is yet to be created is 0.
func (r *Reconciler) templateID(name string) (uint, error) {
	for _, managed := range []bool{true, false} {
		for _, template := range r.templates {
			if template.Name == name && isManagedTemplate(template) == managed &&
				template.ForgeType == params.GithubEndpointType {
				return template.ID, nil
			}
		}
	}
	if _, declared := r.cfg.GitHub.Templates[name]; declared && r.opts.DryRun {
		return 0, nil
	}
	return 0, fmt.Errorf("template %s does not exist", name)
}
