package reconcile

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	commonparams "github.com/cloudbase/garm-provider-common/params"
	"github.com/cloudbase/garm/params"

	"github.com/katexochen/garm-reconcile/internal/garm"
)

// isPool reports whether pool is the one declared under prefix. Garm has no
// names for pools, so they are identified by their runner prefix. The
// provider is part of the identity as it cannot be changed: declaring
// another one leaves the existing pool undeclared and creates a new one.
func isPool(pool params.Pool, prefix string, want params.CreatePoolParams) bool {
	return strings.EqualFold(prefix, pool.GetRunnerPrefix()) && want.ProviderName == pool.ProviderName
}

// ensurePools creates and updates the pools of an entity. entityID is empty
// in a dry run that would create the entity.
func (r *Reconciler) ensurePools(ctx context.Context, kind garm.EntityKind, entityName, entityID string, declared map[string]params.CreatePoolParams) error {
	var existing []params.Pool
	if entityID != "" {
		var err error
		existing, err = r.client.ListEntityPools(ctx, kind, entityID)
		if err != nil {
			return fmt.Errorf("listing the pools of %s %s: %w", kind, entityName, err)
		}
	}

	for _, prefix := range sortedKeys(declared) {
		want := declared[prefix]
		want.Prefix = prefix
		name := entityName + "/" + prefix

		var matches []params.Pool
		for _, pool := range existing {
			if isPool(pool, prefix, want) {
				matches = append(matches, pool)
			}
		}
		var err error
		switch len(matches) {
		case 0:
			err = r.apply(Change{Action: Create, Kind: KindPool, Name: name}, func() error {
				return r.client.CreateEntityPool(ctx, kind, entityID, want)
			})
		case 1:
			err = r.updatePool(ctx, name, matches[0].ID, want)
		default:
			err = fmt.Errorf("%s %s: %d pools of provider %s share this runner prefix, which has to be unique",
				KindPool, name, len(matches), want.ProviderName)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// updatePool brings the pool with the given ID to its declared state.
func (r *Reconciler) updatePool(ctx context.Context, name, id string, want params.CreatePoolParams) error {
	// Listed pools lack some of their settings.
	have, err := r.client.GetPool(ctx, id)
	if err != nil {
		return fmt.Errorf("reading %s %s: %w", KindPool, name, err)
	}
	fields, err := poolDiff(want, have)
	if err != nil || len(fields) == 0 {
		return err
	}
	return r.apply(Change{Action: Update, Kind: KindPool, Name: name, Fields: fields}, func() error {
		return r.client.UpdatePool(ctx, id, poolUpdate(want, fields))
	})
}

// poolDiff returns the fields in which a pool differs from what is declared
// for it.
func poolDiff(want params.CreatePoolParams, pool params.Pool) ([]string, error) {
	have := params.CreatePoolParams{
		RunnerPrefix:           params.RunnerPrefix{Prefix: pool.GetRunnerPrefix()},
		ProviderName:           pool.ProviderName,
		MaxRunners:             pool.MaxRunners,
		MinIdleRunners:         pool.MinIdleRunners,
		Image:                  pool.Image,
		Flavor:                 pool.Flavor,
		OSType:                 pool.OSType,
		OSArch:                 pool.OSArch,
		Enabled:                pool.Enabled,
		RunnerBootstrapTimeout: pool.RunnerBootstrapTimeout,
		ExtraSpecs:             pool.ExtraSpecs,
		EnableShell:            pool.EnableShell,
		GitHubRunnerGroup:      pool.GitHubRunnerGroup,
		Priority:               pool.Priority,
		TemplateID:             &pool.TemplateID,
	}
	for _, tag := range pool.Tags {
		have.Tags = append(have.Tags, tag.Name)
	}

	// Garm picks these when a pool is created without them, and offers
	// no way to go back to its choice. Not declaring them therefore leaves
	// them as they are.
	if want.RunnerBootstrapTimeout == 0 {
		have.RunnerBootstrapTimeout = 0
	}
	if want.TemplateID == nil {
		have.TemplateID = nil
	}
	want.Tags, have.Tags = tagSet(want.Tags), tagSet(have.Tags)
	want.ExtraSpecs, have.ExtraSpecs = extraSpecs(want.ExtraSpecs), extraSpecs(have.ExtraSpecs)

	return changedFields(want, have)
}

// poolUpdate returns the request that brings a pool to its declared state.
// fields are the fields in which the pool differs from it.
func poolUpdate(want params.CreatePoolParams, fields []string) params.UpdatePoolParams {
	update := params.UpdatePoolParams{
		RunnerPrefix:      want.RunnerPrefix,
		Tags:              want.Tags,
		Enabled:           &want.Enabled,
		MaxRunners:        &want.MaxRunners,
		MinIdleRunners:    &want.MinIdleRunners,
		Image:             want.Image,
		Flavor:            want.Flavor,
		OSType:            want.OSType,
		OSArch:            want.OSArch,
		EnableShell:       &want.EnableShell,
		GitHubRunnerGroup: &want.GitHubRunnerGroup,
		Priority:          &want.Priority,
		TemplateID:        want.TemplateID,
	}
	if want.RunnerBootstrapTimeout != 0 {
		update.RunnerBootstrapTimeout = &want.RunnerBootstrapTimeout
	}
	// Garm compares extra specs byte by byte and recreates the runners of
	// the pool when they differ, so they are only sent on a real change.
	if slices.Contains(fields, "extra_specs") {
		update.ExtraSpecs = want.ExtraSpecs
		if extraSpecs(want.ExtraSpecs) == nil {
			update.ExtraSpecs = json.RawMessage("{}")
		}
	}
	return update
}

// tagSet returns tags in the form garm compares them: as a set, without
// regard to case. Garm keeps the spelling a tag was first created with, for
// all pools.
func tagSet(tags []string) []string {
	var set []string
	for _, tag := range tags {
		set = append(set, strings.ToLower(tag))
	}
	slices.Sort(set)
	return slices.Compact(set)
}

// extraSpecs returns nil for extra specs that carry no settings.
func extraSpecs(specs json.RawMessage) json.RawMessage {
	switch string(bytes.TrimSpace(specs)) {
	case "", "null", "{}":
		return nil
	}
	return specs
}

// stalePool is a pool that is not declared.
type stalePool struct {
	params.Pool
	name string
}

func (r *Reconciler) stalePools(ctx context.Context) ([]stalePool, error) {
	var stale []stalePool
	for _, kind := range entityKinds {
		entities, err := r.githubEntities(ctx, kind)
		if err != nil {
			return nil, err
		}
		for _, entity := range entities {
			// Without a declaration, all pools of the entity are stale.
			want, _ := r.declaration(kind, entity)
			pools, err := r.client.ListEntityPools(ctx, kind, entity.ID)
			if err != nil {
				return nil, fmt.Errorf("listing the pools of %s %s: %w", kind, entity.FullName(), err)
			}
			for _, pool := range pools {
				prefix := strings.ToLower(pool.GetRunnerPrefix())
				if declared, ok := want.Pools[prefix]; ok && isPool(pool, prefix, declared) {
					continue
				}
				stale = append(stale, stalePool{Pool: pool, name: entity.FullName() + "/" + pool.GetRunnerPrefix()})
			}
		}
	}
	return stale, nil
}

func (r *Reconciler) prunePools(ctx context.Context) error {
	stale, err := r.stalePools(ctx)
	if err != nil {
		return err
	}
	for _, pool := range stale {
		r.opts.Report(Change{Action: Delete, Kind: KindPool, Name: pool.name})
	}
	if r.opts.DryRun {
		return nil
	}

	// All pools stop taking jobs before the first one is waited for.
	for _, pool := range stale {
		if !pool.Enabled {
			continue
		}
		disabled := false
		if err := r.client.UpdatePool(ctx, pool.ID, params.UpdatePoolParams{Enabled: &disabled}); err != nil {
			return fmt.Errorf("disabling %s %s: %w", KindPool, pool.name, err)
		}
	}

	var reported string
	for {
		stale, err = r.deleteDrained(ctx, stale)
		if err != nil || len(stale) == 0 {
			return err
		}
		names := make([]string, len(stale))
		for i, pool := range stale {
			names[i] = pool.name
		}
		waiting := "waiting for the runners of " + strings.Join(names, ", ")
		if waiting != reported {
			r.opts.Progress(waiting)
			reported = waiting
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("%s: %w", waiting, ctx.Err())
		case <-time.After(r.opts.PollInterval):
		}
	}
}

// deleteDrained deletes the pools that have no runners left and removes the
// idle runners of the others, which it returns.
func (r *Reconciler) deleteDrained(ctx context.Context, pools []stalePool) ([]stalePool, error) {
	var remaining []stalePool
	for _, pool := range pools {
		instances, err := r.client.ListPoolInstances(ctx, pool.ID)
		if err != nil {
			return nil, fmt.Errorf("listing the runners of %s %s: %w", KindPool, pool.name, err)
		}
		if len(instances) == 0 {
			err := r.client.DeletePool(ctx, pool.ID)
			if err == nil {
				continue
			}
			// A runner that was being created when the pool got
			// disabled may have appeared in the meantime.
			if !garm.IsBadRequest(err) {
				return nil, fmt.Errorf("delete %s %s: %w", KindPool, pool.name, err)
			}
		}
		for _, instance := range instances {
			if !isIdle(instance) {
				continue
			}
			// A runner that picked up a job in the meantime is
			// protected by the forge, which refuses to remove it. One
			// that finished its job may be gone already.
			err := r.client.DeleteInstance(ctx, instance.Name)
			if err != nil && !garm.IsBadRequest(err) && !garm.IsNotFound(err) {
				return nil, fmt.Errorf("removing runner %s of %s %s: %w", instance.Name, KindPool, pool.name, err)
			}
		}
		remaining = append(remaining, pool)
	}
	return remaining, nil
}

// isIdle reports whether an instance can be removed without interrupting a
// job. That includes instances that failed to come up. Instances that are
// still being created or deleted are left alone, as garm only accepts
// removing settled ones.
func isIdle(instance params.Instance) bool {
	settled := instance.Status == commonparams.InstanceRunning || instance.Status == commonparams.InstanceError
	return settled && instance.RunnerStatus != params.RunnerActive
}
