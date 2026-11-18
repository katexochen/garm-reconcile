package reconcile

import (
	"context"
	"fmt"
	"slices"

	commonparams "github.com/cloudbase/garm-provider-common/params"
	"github.com/cloudbase/garm/params"

	"github.com/katexochen/garm-reconcile/internal/config"
)

// isShipped reports whether a template of the forge comes with garm. Such
// templates are used, but not managed.
func (f *forge) isShipped(template params.Template) bool {
	return template.Owner == params.SystemUser && template.ForgeType == f.typ
}

// isManaged reports whether a template of the forge is subject to
// reconciling.
func (f *forge) isManaged(template params.Template) bool {
	return template.Owner != params.SystemUser && template.ForgeType == f.typ
}

// ensureTemplates creates and updates the declared templates and records
// their IDs, by which pools and scale sets refer to them.
func (r *Reconciler) ensureTemplates(ctx context.Context, f *forge) error {
	existing, err := r.client.ListTemplates(ctx)
	if err != nil {
		return fmt.Errorf("listing templates: %w", err)
	}
	f.shippedTemplates = slices.DeleteFunc(slices.Clone(existing), func(t params.Template) bool { return !f.isShipped(t) })
	f.templateIDs = map[string]uint{}

	for _, name := range sortedKeys(f.cfg.Templates) {
		want := f.cfg.Templates[name]
		idx := slices.IndexFunc(existing, func(have params.Template) bool {
			return f.isManaged(have) && have.Name == name
		})
		if idx < 0 {
			// The ID stays 0 in a dry run, which differs from the
			// template of every existing pool.
			f.templateIDs[name], err = r.createTemplate(ctx, f, name, want)
		} else {
			f.templateIDs[name] = existing[idx].ID
			err = r.updateTemplate(ctx, f, name, want, existing[idx])
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func (r *Reconciler) createTemplate(ctx context.Context, f *forge, name string, want config.Template) (uint, error) {
	create := params.CreateTemplateParams{
		Name:        name,
		Description: want.Description,
		Data:        []byte(want.Data),
		OSType:      want.OSType,
		ForgeType:   f.typ,
	}
	var id uint
	err := r.apply(Change{Action: Create, Kind: f.kind("template"), Name: name}, func() error {
		template, err := r.client.CreateTemplate(ctx, create)
		id = template.ID
		return err
	})
	return id, err
}

func (r *Reconciler) updateTemplate(ctx context.Context, f *forge, name string, want config.Template, listed params.Template) error {
	if want.OSType != listed.OSType {
		return fmt.Errorf("%s %s: the OS type cannot be changed from %s to %s, declare a new template instead",
			f.kind("template"), name, listed.OSType, want.OSType)
	}
	// Listed templates lack their data.
	have, err := r.client.GetTemplate(ctx, listed.ID)
	if err != nil {
		return fmt.Errorf("reading %s %s: %w", f.kind("template"), name, err)
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
	return r.apply(Change{Action: Update, Kind: f.kind("template"), Name: name, Fields: fields}, func() error {
		return r.client.UpdateTemplate(ctx, have.ID, update)
	})
}

func (r *Reconciler) pruneTemplates(ctx context.Context, f *forge) error {
	existing, err := r.client.ListTemplates(ctx)
	if err != nil {
		return fmt.Errorf("listing templates: %w", err)
	}
	for _, have := range existing {
		if _, declared := f.cfg.Templates[have.Name]; declared || !f.isManaged(have) {
			continue
		}
		err := r.apply(Change{Action: Delete, Kind: f.kind("template"), Name: have.Name}, func() error {
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
func (f *forge) templateID(name string, osType commonparams.OSType) (uint, error) {
	if id, declared := f.templateIDs[name]; declared {
		return id, nil
	}
	for _, template := range f.shippedTemplates {
		if template.Name == name || (name == "" && template.OSType == osType) {
			return template.ID, nil
		}
	}
	if name == "" {
		return 0, fmt.Errorf("garm ships no template for %s", osType)
	}
	return 0, fmt.Errorf("template %s is neither declared nor shipped by garm", name)
}
