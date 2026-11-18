package reconcile

import (
	"context"
	"fmt"
	"slices"

	commonparams "github.com/cloudbase/garm-provider-common/params"
	"github.com/cloudbase/garm/params"

	"github.com/katexochen/garm-reconcile/internal/config"
)

// isShipped reports whether a template comes with garm. Such templates are
// used, but not managed.
func isShipped(template params.Template) bool {
	return template.Owner == params.SystemUser && template.ForgeType == params.GithubEndpointType
}

// isManagedTemplate reports whether a template is subject to reconciling.
func isManagedTemplate(template params.Template) bool {
	return template.Owner != params.SystemUser && template.ForgeType == params.GithubEndpointType
}

// ensureTemplates creates and updates the declared templates and records
// their IDs, by which pools and scale sets refer to them.
func (r *Reconciler) ensureTemplates(ctx context.Context) error {
	existing, err := r.client.ListTemplates(ctx)
	if err != nil {
		return fmt.Errorf("listing templates: %w", err)
	}
	r.shippedTemplates = slices.DeleteFunc(slices.Clone(existing), func(t params.Template) bool { return !isShipped(t) })
	r.templateIDs = map[string]uint{}

	for _, name := range sortedKeys(r.cfg.GitHub.Templates) {
		want := r.cfg.GitHub.Templates[name]
		idx := slices.IndexFunc(existing, func(have params.Template) bool {
			return isManagedTemplate(have) && have.Name == name
		})
		if idx < 0 {
			// The ID stays 0 in a dry run, which differs from the
			// template of every existing pool.
			r.templateIDs[name], err = r.createTemplate(ctx, name, want)
		} else {
			r.templateIDs[name] = existing[idx].ID
			err = r.updateTemplate(ctx, name, want, existing[idx])
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func (r *Reconciler) createTemplate(ctx context.Context, name string, want config.Template) (uint, error) {
	create := params.CreateTemplateParams{
		Name:        name,
		Description: want.Description,
		Data:        []byte(want.Data),
		OSType:      want.OSType,
		ForgeType:   params.GithubEndpointType,
	}
	var id uint
	err := r.apply(Change{Action: Create, Kind: KindTemplate, Name: name}, func() error {
		template, err := r.client.CreateTemplate(ctx, create)
		id = template.ID
		return err
	})
	return id, err
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

// templateID returns the ID of the template a pool or scale set names. A
// declared template takes precedence over one that garm ships under the
// same name. Without a name, it is the template garm ships for the OS.
//
// Templates that exist in garm without being declared are not eligible:
// pruning deletes them, which garm refuses while they are in use.
func (r *Reconciler) templateID(name string, osType commonparams.OSType) (uint, error) {
	if id, declared := r.templateIDs[name]; declared {
		return id, nil
	}
	for _, template := range r.shippedTemplates {
		if template.Name == name || (name == "" && template.OSType == osType) {
			return template.ID, nil
		}
	}
	if name == "" {
		return 0, fmt.Errorf("garm ships no template for %s", osType)
	}
	return 0, fmt.Errorf("template %s is neither declared nor shipped by garm", name)
}
