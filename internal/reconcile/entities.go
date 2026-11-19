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
	"crypto/rand"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/cloudbase/garm/params"

	"github.com/katexochen/garm-reconcile/internal/config"
	"github.com/katexochen/garm-reconcile/internal/garm"
)

// entityKinds returns the kinds of entities the forge has.
func (f *forge) entityKinds() []garm.EntityKind {
	if f.typ == params.GiteaEndpointType {
		return []garm.EntityKind{garm.Repository, garm.Organization}
	}
	return []garm.EntityKind{garm.Repository, garm.Organization, garm.Enterprise}
}

// declaredEntities returns the entities of a kind that are declared for the
// forge, keyed by their full name.
func (f *forge) declaredEntities(kind garm.EntityKind) map[string]config.Entity {
	switch kind {
	case garm.Repository:
		return f.cfg.Repositories
	case garm.Organization:
		return f.cfg.Organizations
	case garm.Enterprise:
		return f.cfg.Enterprises
	}
	panic("unknown entity kind " + string(kind))
}

// forgeEntities lists the entities of a kind that live on endpoints of the
// forge.
func (r *Reconciler) forgeEntities(ctx context.Context, f *forge, kind garm.EntityKind) ([]garm.Entity, error) {
	entities, err := r.client.ListEntities(ctx, kind)
	if err != nil {
		return nil, fmt.Errorf("listing %s entities: %w", kind, err)
	}
	return slices.DeleteFunc(entities, func(e garm.Entity) bool {
		return e.Endpoint.EndpointType != f.typ
	}), nil
}

// isEntity reports whether have is the entity declared as name. An entity
// is identified by its name and the endpoint it lives on, which is the
// endpoint of its credentials. Garm compares both without regard to case.
func (f *forge) isEntity(have garm.Entity, name string, want config.Entity) bool {
	endpoint := f.cfg.Credentials[want.CredentialsName].Endpoint
	return strings.EqualFold(name, have.FullName()) && strings.EqualFold(endpoint, have.Endpoint.Name)
}

// declaration returns what is declared for an entity that exists in garm.
func (f *forge) declaration(kind garm.EntityKind, have garm.Entity) (config.Entity, bool) {
	for name, want := range f.declaredEntities(kind) {
		if f.isEntity(have, name, want) {
			return want, true
		}
	}
	return config.Entity{}, false
}

func (r *Reconciler) ensureEntities(ctx context.Context, f *forge) error {
	for _, kind := range f.entityKinds() {
		existing, err := r.forgeEntities(ctx, f, kind)
		if err != nil {
			return err
		}
		declared := f.declaredEntities(kind)
		for _, name := range sortedKeys(declared) {
			want := declared[name]
			id, err := r.ensureEntity(ctx, f, kind, name, want, existing)
			if err != nil {
				return err
			}
			if err := r.ensureWebhook(ctx, f, kind, name, id, want); err != nil {
				return err
			}
			if err := r.ensurePools(ctx, f, kind, name, id, want.Pools); err != nil {
				return err
			}
			if err := r.ensureScaleSets(ctx, f, kind, name, id, want.ScaleSets); err != nil {
				return err
			}
		}
	}
	return nil
}

// ensureEntity returns the ID of the entity, which is empty in a dry run
// that would create it.
func (r *Reconciler) ensureEntity(ctx context.Context, f *forge, kind garm.EntityKind, name string, want config.Entity, existing []garm.Entity) (string, error) {
	idx := slices.IndexFunc(existing, func(have garm.Entity) bool { return f.isEntity(have, name, want) })
	if idx < 0 {
		return r.createEntity(ctx, f, kind, name, want)
	}
	return existing[idx].ID, r.updateEntity(ctx, f, kind, name, want, existing[idx])
}

func (r *Reconciler) createEntity(ctx context.Context, f *forge, kind garm.EntityKind, name string, want config.Entity) (string, error) {
	create := garm.CreateEntityParams{
		Name:             name,
		CredentialsName:  want.CredentialsName,
		WebhookSecret:    want.WebhookSecret,
		PoolBalancerType: want.PoolBalancerType,
		ForgeType:        f.typ,
		AgentMode:        want.AgentMode,
	}
	if kind == garm.Repository {
		create.Owner, create.Name, _ = strings.Cut(name, "/")
	}
	// Garm requires a secret, even if no webhook is going to use it.
	if create.WebhookSecret == "" {
		create.WebhookSecret = rand.Text()
	}

	var id string
	err := r.apply(Change{Action: Create, Kind: f.entityKind(kind), Name: name}, func() error {
		entity, err := r.client.CreateEntity(ctx, kind, create)
		id = entity.ID
		return err
	})
	return id, err
}

func (r *Reconciler) updateEntity(ctx context.Context, f *forge, kind garm.EntityKind, name string, want config.Entity, have garm.Entity) error {
	update := params.UpdateEntityParams{WebhookSecret: want.WebhookSecret}
	var fields []string
	if !strings.EqualFold(want.CredentialsName, have.CredentialsName) {
		update.CredentialsName = want.CredentialsName
		fields = append(fields, "credentials_name")
	}
	if balancer := poolBalancer(want.PoolBalancerType); balancer != poolBalancer(have.PoolBalancerType) {
		update.PoolBalancerType = balancer
		fields = append(fields, "pool_balancer_type")
	}
	if want.AgentMode != have.AgentMode {
		update.AgentMode = &want.AgentMode
		fields = append(fields, "agent_mode")
	}

	if len(fields) > 0 {
		r.opts.Report(Change{Action: Update, Kind: f.entityKind(kind), Name: name, Fields: fields})
	}
	// Garm does not reveal whether the webhook secret changed, so a
	// declared one is always sent.
	if r.opts.DryRun || (len(fields) == 0 && want.WebhookSecret == "") {
		return nil
	}
	if err := r.client.UpdateEntity(ctx, kind, have.ID, update); err != nil {
		return fmt.Errorf("update %s %s: %w", f.entityKind(kind), name, err)
	}
	return nil
}

func (r *Reconciler) pruneEntities(ctx context.Context, f *forge) error {
	for _, kind := range f.entityKinds() {
		existing, err := r.forgeEntities(ctx, f, kind)
		if err != nil {
			return err
		}
		for _, have := range existing {
			if _, declared := f.declaration(kind, have); declared {
				continue
			}
			err := r.apply(Change{Action: Delete, Kind: f.entityKind(kind), Name: have.FullName()}, func() error {
				return r.client.DeleteEntity(ctx, kind, have.ID)
			})
			if err != nil {
				return err
			}
		}
	}
	return nil
}

// poolBalancer returns the balancer garm uses for a type, which is round
// robin when none is set.
func poolBalancer(t params.PoolBalancerType) params.PoolBalancerType {
	if t == params.PoolBalancerTypeNone {
		return params.PoolBalancerTypeRoundRobin
	}
	return t
}

// ensureWebhook installs or removes the webhook of an entity. entityID is
// empty in a dry run that would create the entity.
func (r *Reconciler) ensureWebhook(ctx context.Context, f *forge, kind garm.EntityKind, name, entityID string, want config.Entity) error {
	// installed is whether the forge delivers to this controller, own
	// whether it does so through the webhook garm installs and removes.
	var installed, own bool
	if entityID != "" && !r.releasedWebhooks[entityID] {
		hook, err := r.getWebhook(ctx, kind, entityID)
		switch {
		case err == nil:
			installed, own = true, !isManualWebhook(hook, r.cfg.Controller.WebhookURL)
		case garm.IsNotFound(err):
		case !want.InstallWebhook && (errors.Is(err, garm.ErrNoWebhooks) || garm.IsBadRequest(err)):
			// Garm cannot manage webhooks at all or not with the
			// credentials of this entity, so none of its own is there.
			return nil
		default:
			return fmt.Errorf("reading the %s of %s: %w", f.kind("webhook"), name, err)
		}
	}

	switch {
	case want.InstallWebhook && !installed:
		return r.apply(Change{Action: Create, Kind: f.kind("webhook"), Name: name}, func() error {
			return r.client.InstallWebhook(ctx, kind, entityID, params.InstallWebhookParams{})
		})
	case !want.InstallWebhook && own:
		return r.apply(Change{Action: Delete, Kind: f.kind("webhook"), Name: name}, func() error {
			return r.client.UninstallWebhook(ctx, kind, entityID)
		})
	}
	return nil
}

// isManualWebhook reports whether a webhook was added to the forge by hand.
// Such a webhook delivers to the webhook URL of the controller, while garm
// installs its own for a URL below it, and refuses to remove any other.
func isManualWebhook(hook params.HookInfo, webhookURL string) bool {
	return strings.EqualFold(strings.TrimRight(hook.URL, "/"), strings.TrimRight(webhookURL, "/"))
}

// releaseWebhooks removes the webhooks garm installed while the controller
// had the given webhook URL. Garm finds its webhooks by the current URL, so
// after a change it could neither remove nor replace them. ensureWebhook
// installs them again where they are declared.
func (r *Reconciler) releaseWebhooks(ctx context.Context, webhookURL string) error {
	for _, f := range r.forges {
		for _, kind := range f.entityKinds() {
			entities, err := r.forgeEntities(ctx, f, kind)
			if err != nil {
				return err
			}
			for _, entity := range entities {
				hook, err := r.getWebhook(ctx, kind, entity.ID)
				switch {
				case errors.Is(err, garm.ErrNoWebhooks), garm.IsNotFound(err), garm.IsBadRequest(err):
					continue
				case err != nil:
					return fmt.Errorf("reading the %s of %s: %w", f.kind("webhook"), entity.FullName(), err)
				case isManualWebhook(hook, webhookURL):
					continue
				}
				err = r.apply(Change{Action: Delete, Kind: f.kind("webhook"), Name: entity.FullName()}, func() error {
					return r.client.UninstallWebhook(ctx, kind, entity.ID)
				})
				if err != nil {
					return err
				}
				r.releasedWebhooks[entity.ID] = true
			}
		}
	}
	return nil
}

// getWebhook reads the webhook of an entity from its forge. Garm starts
// using credentials a moment after they were updated, and fails to reach the
// forge with the previous ones if those were revoked, so failures get a few
// more attempts.
func (r *Reconciler) getWebhook(ctx context.Context, kind garm.EntityKind, entityID string) (params.HookInfo, error) {
	const attempts = 10
	for attempt := 1; ; attempt++ {
		hook, err := r.client.GetWebhook(ctx, kind, entityID)
		if !garm.IsServerError(err) || attempt == attempts {
			return hook, err
		}
		select {
		case <-ctx.Done():
			return hook, ctx.Err()
		case <-time.After(r.opts.RetryInterval):
		}
	}
}
