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
	"strings"
	"time"

	commonparams "github.com/cloudbase/garm-provider-common/params"
	"github.com/cloudbase/garm/params"

	"github.com/katexochen/garm-reconcile/internal/garm"
)

// runnerSet is a pool or scale set that is not declared. Garm only deletes
// one that is disabled and has no runners.
type runnerSet struct {
	kind      Kind
	name      string
	enabled   bool
	disable   func(context.Context) error
	instances func(context.Context) ([]params.Instance, error)
	delete    func(context.Context) error
}

func (r *Reconciler) pruneRunners(ctx context.Context) error {
	var stale []runnerSet
	for _, f := range r.forges {
		pools, err := r.stalePools(ctx, f)
		if err != nil {
			return err
		}
		scaleSets, err := r.staleScaleSets(ctx, f)
		if err != nil {
			return err
		}
		stale = append(append(stale, pools...), scaleSets...)
	}

	for _, set := range stale {
		r.opts.Report(Change{Action: Delete, Kind: set.kind, Name: set.name})
	}
	if r.opts.DryRun {
		return nil
	}

	// All of them stop taking jobs before the first one is waited for.
	for _, set := range stale {
		if !set.enabled {
			continue
		}
		if err := set.disable(ctx); err != nil {
			return fmt.Errorf("disabling %s %s: %w", set.kind, set.name, err)
		}
	}

	var reported string
	for {
		var err error
		stale, err = r.deleteDrained(ctx, stale)
		if err != nil || len(stale) == 0 {
			return err
		}
		names := make([]string, len(stale))
		for i, set := range stale {
			names[i] = set.name
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

// deleteDrained deletes the sets that have no runners left and removes the
// idle runners of the others, which it returns.
func (r *Reconciler) deleteDrained(ctx context.Context, sets []runnerSet) ([]runnerSet, error) {
	var remaining []runnerSet
	for _, set := range sets {
		instances, err := set.instances(ctx)
		if err != nil {
			return nil, fmt.Errorf("listing the runners of %s %s: %w", set.kind, set.name, err)
		}
		if len(instances) == 0 {
			err := set.delete(ctx)
			if err == nil {
				continue
			}
			// A runner that was being created when the set got
			// disabled may have appeared in the meantime.
			if !garm.IsBadRequest(err) {
				return nil, fmt.Errorf("delete %s %s: %w", set.kind, set.name, err)
			}
		}
		for _, instance := range instances {
			if !isIdle(instance) {
				continue
			}
			// A runner that picked up a job in the meantime is
			// protected by the forge, which refuses to remove it. That
			// is the only protection for runners of a scale set: garm
			// stops tracking their jobs once the scale set is disabled.
			// A runner that finished its job may be gone already.
			err := r.client.DeleteInstance(ctx, instance.Name)
			if err != nil && !garm.IsBadRequest(err) && !garm.IsNotFound(err) {
				return nil, fmt.Errorf("removing runner %s of %s %s: %w", instance.Name, set.kind, set.name, err)
			}
		}
		remaining = append(remaining, set)
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
