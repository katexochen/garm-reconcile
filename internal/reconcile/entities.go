package reconcile

import (
	"context"
	"crypto/rand"
	"fmt"
	"slices"
	"strings"

	"github.com/cloudbase/garm/params"

	"github.com/katexochen/garm-reconcile/internal/config"
	"github.com/katexochen/garm-reconcile/internal/garm"
)

var entityKinds = []garm.EntityKind{garm.Repository, garm.Organization, garm.Enterprise}

// declaredEntities returns the entities of a kind that are declared for
// GitHub, keyed by their full name.
func (r *Reconciler) declaredEntities(kind garm.EntityKind) map[string]config.Entity {
	switch kind {
	case garm.Repository:
		return r.cfg.GitHub.Repositories
	case garm.Organization:
		return r.cfg.GitHub.Organizations
	case garm.Enterprise:
		return r.cfg.GitHub.Enterprises
	}
	panic("unknown entity kind " + string(kind))
}

// githubEntities lists the entities of a kind that live on GitHub
// endpoints. Entities of other forges are not managed.
func (r *Reconciler) githubEntities(ctx context.Context, kind garm.EntityKind) ([]garm.Entity, error) {
	entities, err := r.client.ListEntities(ctx, kind)
	if err != nil {
		return nil, fmt.Errorf("listing %s entities: %w", kind, err)
	}
	return slices.DeleteFunc(entities, func(e garm.Entity) bool {
		return e.Endpoint.EndpointType != params.GithubEndpointType
	}), nil
}

// isEntity reports whether have is the entity declared as name. An entity
// is identified by its name and the endpoint it lives on, which is the
// endpoint of its credentials. Garm compares both without regard to case.
func (r *Reconciler) isEntity(have garm.Entity, name string, want config.Entity) bool {
	endpoint := r.cfg.GitHub.Credentials[want.CredentialsName].Endpoint
	return strings.EqualFold(name, have.FullName()) && strings.EqualFold(endpoint, have.Endpoint.Name)
}

// declaration returns what is declared for an entity that exists in garm.
func (r *Reconciler) declaration(kind garm.EntityKind, have garm.Entity) (config.Entity, bool) {
	for name, want := range r.declaredEntities(kind) {
		if r.isEntity(have, name, want) {
			return want, true
		}
	}
	return config.Entity{}, false
}

func (r *Reconciler) ensureEntities(ctx context.Context) error {
	for _, kind := range entityKinds {
		existing, err := r.githubEntities(ctx, kind)
		if err != nil {
			return err
		}
		declared := r.declaredEntities(kind)
		for _, name := range sortedKeys(declared) {
			want := declared[name]
			id, err := r.ensureEntity(ctx, kind, name, want, existing)
			if err != nil {
				return err
			}
			if err := r.ensurePools(ctx, kind, name, id, want.Pools); err != nil {
				return err
			}
		}
	}
	return nil
}

// ensureEntity returns the ID of the entity, which is empty in a dry run
// that would create it.
func (r *Reconciler) ensureEntity(ctx context.Context, kind garm.EntityKind, name string, want config.Entity, existing []garm.Entity) (string, error) {
	idx := slices.IndexFunc(existing, func(have garm.Entity) bool { return r.isEntity(have, name, want) })
	if idx < 0 {
		return r.createEntity(ctx, kind, name, want)
	}
	return existing[idx].ID, r.updateEntity(ctx, kind, name, want, existing[idx])
}

func (r *Reconciler) createEntity(ctx context.Context, kind garm.EntityKind, name string, want config.Entity) (string, error) {
	create := garm.CreateEntityParams{
		Name:             name,
		CredentialsName:  want.CredentialsName,
		WebhookSecret:    want.WebhookSecret,
		PoolBalancerType: want.PoolBalancerType,
		ForgeType:        params.GithubEndpointType,
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
	err := r.apply(Change{Action: Create, Kind: Kind(kind), Name: name}, func() error {
		entity, err := r.client.CreateEntity(ctx, kind, create)
		id = entity.ID
		return err
	})
	return id, err
}

func (r *Reconciler) updateEntity(ctx context.Context, kind garm.EntityKind, name string, want config.Entity, have garm.Entity) error {
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
		r.opts.Report(Change{Action: Update, Kind: Kind(kind), Name: name, Fields: fields})
	}
	// Garm does not reveal whether the webhook secret changed, so a
	// declared one is always sent.
	if r.opts.DryRun || (len(fields) == 0 && want.WebhookSecret == "") {
		return nil
	}
	if err := r.client.UpdateEntity(ctx, kind, have.ID, update); err != nil {
		return fmt.Errorf("update %s %s: %w", kind, name, err)
	}
	return nil
}

func (r *Reconciler) pruneEntities(ctx context.Context) error {
	for _, kind := range entityKinds {
		existing, err := r.githubEntities(ctx, kind)
		if err != nil {
			return err
		}
		for _, have := range existing {
			if _, declared := r.declaration(kind, have); declared {
				continue
			}
			err := r.apply(Change{Action: Delete, Kind: Kind(kind), Name: have.FullName()}, func() error {
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
