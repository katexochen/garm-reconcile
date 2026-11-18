package reconcile

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/cloudbase/garm/params"

	"github.com/katexochen/garm-reconcile/internal/config"
	"github.com/katexochen/garm-reconcile/internal/garm"
)

// defaultRunnerGroup is the runner group garm puts scale sets in that do
// not name one.
const defaultRunnerGroup = "Default"

// ensureScaleSets creates and updates the scale sets of an entity, which
// are identified by their name. entityID is empty in a dry run that would
// create the entity.
func (r *Reconciler) ensureScaleSets(ctx context.Context, kind garm.EntityKind, entityName, entityID string, declared map[string]config.ScaleSet) error {
	var existing []params.ScaleSet
	if entityID != "" && len(declared) > 0 {
		var err error
		existing, err = r.client.ListEntityScaleSets(ctx, kind, entityID)
		if err != nil {
			return fmt.Errorf("listing the scale sets of %s %s: %w", kind, entityName, err)
		}
	}

	for _, setName := range sortedKeys(declared) {
		want := declared[setName].CreateScaleSetParams
		want.Name = setName
		name := entityName + "/" + setName
		if template := declared[setName].Template; template != "" {
			id, err := r.templateID(template)
			if err != nil {
				return fmt.Errorf("%s %s: %w", KindScaleSet, name, err)
			}
			want.TemplateID = &id
		}

		idx := slices.IndexFunc(existing, func(have params.ScaleSet) bool { return have.Name == setName })
		var err error
		if idx < 0 {
			err = r.apply(Change{Action: Create, Kind: KindScaleSet, Name: name}, func() error {
				return r.client.CreateEntityScaleSet(ctx, kind, entityID, want)
			})
		} else {
			err = r.updateScaleSet(ctx, name, existing[idx].ID, want)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func (r *Reconciler) updateScaleSet(ctx context.Context, name string, id uint, want params.CreateScaleSetParams) error {
	// Listed scale sets lack their extra specs.
	have, err := r.client.GetScaleSet(ctx, id)
	if err != nil {
		return fmt.Errorf("reading %s %s: %w", KindScaleSet, name, err)
	}
	fields, err := scaleSetDiff(want, have)
	if err != nil || len(fields) == 0 {
		return err
	}
	// Garm cannot change these, and GitHub has no room for a replacement
	// under the same name.
	for _, immutable := range []string{"provider_name", "labels", "disable_update"} {
		if slices.Contains(fields, immutable) {
			return fmt.Errorf("%s %s: %s cannot be changed, declare a scale set with another name instead",
				KindScaleSet, name, immutable)
		}
	}
	return r.apply(Change{Action: Update, Kind: KindScaleSet, Name: name, Fields: fields}, func() error {
		return r.client.UpdateScaleSet(ctx, id, scaleSetUpdate(want, fields))
	})
}

// scaleSetDiff returns the fields in which a scale set differs from what is
// declared for it.
func scaleSetDiff(want params.CreateScaleSetParams, set params.ScaleSet) ([]string, error) {
	have := params.CreateScaleSetParams{
		RunnerPrefix:           params.RunnerPrefix{Prefix: set.GetRunnerPrefix()},
		Name:                   set.Name,
		DisableUpdate:          set.DisableUpdate,
		ProviderName:           set.ProviderName,
		MaxRunners:             set.MaxRunners,
		MinIdleRunners:         set.MinIdleRunners,
		Image:                  set.Image,
		Flavor:                 set.Flavor,
		OSType:                 set.OSType,
		OSArch:                 set.OSArch,
		Enabled:                set.Enabled,
		RunnerBootstrapTimeout: set.RunnerBootstrapTimeout,
		ExtraSpecs:             set.ExtraSpecs,
		EnableShell:            set.EnableShell,
		GitHubRunnerGroup:      set.GitHubRunnerGroup,
		TemplateID:             &set.TemplateID,
	}
	for _, tag := range set.Tags {
		have.Labels = append(have.Labels, tag.Name)
	}

	// Garm picks these when a scale set is created without them, see
	// poolDiff.
	if want.RunnerBootstrapTimeout == 0 {
		have.RunnerBootstrapTimeout = 0
	}
	if want.TemplateID == nil {
		have.TemplateID = nil
	}
	if want.GitHubRunnerGroup == "" {
		want.GitHubRunnerGroup = defaultRunnerGroup
	}
	want.Prefix = want.GetRunnerPrefix()
	want.Labels, have.Labels = tagSet(want.Labels), tagSet(have.Labels)
	want.ExtraSpecs, have.ExtraSpecs = extraSpecs(want.ExtraSpecs), extraSpecs(have.ExtraSpecs)

	return changedFields(want, have)
}

// scaleSetUpdate returns the request that brings a scale set to its
// declared state. fields are the fields in which it differs from it.
func scaleSetUpdate(want params.CreateScaleSetParams, fields []string) params.UpdateScaleSetParams {
	runnerGroup := want.GitHubRunnerGroup
	if runnerGroup == "" {
		runnerGroup = defaultRunnerGroup
	}
	update := params.UpdateScaleSetParams{
		RunnerPrefix:      params.RunnerPrefix{Prefix: want.GetRunnerPrefix()},
		Enabled:           &want.Enabled,
		MaxRunners:        &want.MaxRunners,
		MinIdleRunners:    &want.MinIdleRunners,
		Image:             want.Image,
		Flavor:            want.Flavor,
		OSType:            want.OSType,
		OSArch:            want.OSArch,
		EnableShell:       &want.EnableShell,
		GitHubRunnerGroup: &runnerGroup,
		TemplateID:        want.TemplateID,
	}
	if want.RunnerBootstrapTimeout != 0 {
		update.RunnerBootstrapTimeout = &want.RunnerBootstrapTimeout
	}
	// See poolUpdate.
	if slices.Contains(fields, "extra_specs") {
		update.ExtraSpecs = want.ExtraSpecs
		if extraSpecs(want.ExtraSpecs) == nil {
			update.ExtraSpecs = json.RawMessage("{}")
		}
	}
	return update
}

// staleScaleSets returns the scale sets that are not declared.
func (r *Reconciler) staleScaleSets(ctx context.Context) ([]runnerSet, error) {
	var stale []runnerSet
	for _, kind := range entityKinds {
		entities, err := r.githubEntities(ctx, kind)
		if err != nil {
			return nil, err
		}
		for _, entity := range entities {
			// Without a declaration, all scale sets of the entity are
			// stale.
			want, _ := r.declaration(kind, entity)
			sets, err := r.client.ListEntityScaleSets(ctx, kind, entity.ID)
			if err != nil {
				return nil, fmt.Errorf("listing the scale sets of %s %s: %w", kind, entity.FullName(), err)
			}
			for _, set := range sets {
				if _, declared := want.ScaleSets[set.Name]; declared {
					continue
				}
				stale = append(stale, runnerSet{
					kind:    KindScaleSet,
					name:    entity.FullName() + "/" + set.Name,
					enabled: set.Enabled,
					disable: func(ctx context.Context) error {
						disabled := false
						return r.client.UpdateScaleSet(ctx, set.ID, params.UpdateScaleSetParams{Enabled: &disabled})
					},
					instances: func(ctx context.Context) ([]params.Instance, error) {
						return r.client.ListScaleSetInstances(ctx, set.ID)
					},
					delete: func(ctx context.Context) error { return r.client.DeleteScaleSet(ctx, set.ID) },
				})
			}
		}
	}
	return stale, nil
}
