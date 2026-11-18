// Package reconcile brings a garm controller to its desired state.
//
// Reconciling has two phases. Ensure creates and updates what is declared,
// after which garm serves the desired state. Prune then removes what is not
// declared, which can take as long as the jobs that still run on the runners
// of removed pools and scale sets.
package reconcile

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/cloudbase/garm/params"

	"github.com/katexochen/garm-reconcile/internal/config"
	"github.com/katexochen/garm-reconcile/internal/garm"
)

// ErrNotInitialized is returned by a dry run against a controller that
// lacks its admin user or URLs. Garm does not allow inspecting such a
// controller, so everything declared is going to be created.
var ErrNotInitialized = errors.New("garm is not initialized")

// Action is what reconciling does to a resource.
type Action string

const (
	Create Action = "create"
	Update Action = "update"
	Delete Action = "delete"
)

// Change is a difference between garm and the desired state.
type Change struct {
	Action Action
	Kind   Kind
	Name   string
	// Fields names the differing fields of an Update.
	Fields []string
}

func (c Change) String() string {
	s := fmt.Sprintf("%s %s", c.Action, c.Kind)
	if c.Name != "" {
		s += " " + c.Name
	}
	if len(c.Fields) > 0 {
		s += " (" + strings.Join(c.Fields, ", ") + ")"
	}
	return s
}

// Kind is the kind of resource a change affects. Kinds that exist per forge
// are prefixed with it, like "github credentials".
type Kind string

const (
	KindAdmin      Kind = "admin user"
	KindController Kind = "controller"
)

// forge is a type of forge together with what is declared for it.
type forge struct {
	typ params.EndpointType
	cfg config.Forge

	// Ensure records what pools and scale sets need to refer to
	// templates: the IDs of the declared ones by name, and the templates
	// garm ships.
	templateIDs      map[string]uint
	shippedTemplates []params.Template
}

func (f *forge) kind(resource string) Kind {
	return Kind(string(f.typ) + " " + resource)
}

func (f *forge) entityKind(kind garm.EntityKind) Kind {
	return f.kind(string(kind))
}

// Options configure a Reconciler.
type Options struct {
	// DryRun reports changes without applying them.
	DryRun bool
	// Report is called with every change before it is applied. Pools and
	// scale sets to delete are reported when Prune starts to drain them.
	Report func(Change)
	// Progress is called with what Prune is waiting for, whenever that
	// changes.
	Progress func(string)
	// PollInterval is the time between checks on the runners of a pool
	// that is being removed. It defaults to 5 seconds.
	PollInterval time.Duration
}

// Reconciler brings one garm controller to a desired state.
type Reconciler struct {
	client *garm.Client
	cfg    *config.Config
	forges []*forge
	opts   Options
}

// New returns a Reconciler that brings the controller behind client to the
// state declared in cfg.
func New(client *garm.Client, cfg *config.Config, opts Options) *Reconciler {
	if opts.Report == nil {
		opts.Report = func(Change) {}
	}
	if opts.Progress == nil {
		opts.Progress = func(string) {}
	}
	if opts.PollInterval == 0 {
		opts.PollInterval = 5 * time.Second
	}
	forges := []*forge{
		{typ: params.GithubEndpointType, cfg: cfg.GitHub},
		{typ: params.GiteaEndpointType, cfg: cfg.Gitea},
	}
	return &Reconciler{client: client, cfg: cfg, forges: forges, opts: opts}
}

// Ensure creates and updates resources until garm has everything that is
// declared. It initializes the controller if needed and never deletes
// anything.
func (r *Reconciler) Ensure(ctx context.Context) error {
	if err := r.authenticate(ctx); err != nil {
		return err
	}
	if err := r.ensureController(ctx); err != nil {
		return err
	}
	for _, f := range r.forges {
		if err := r.ensureEndpoints(ctx, f); err != nil {
			return err
		}
		if err := r.ensureCredentials(ctx, f); err != nil {
			return err
		}
		if err := r.ensureTemplates(ctx, f); err != nil {
			return err
		}
		if err := r.ensureEntities(ctx, f); err != nil {
			return err
		}
	}
	return nil
}

// Prune deletes the resources that are not declared. It must be called after
// Ensure.
//
// Pools and scale sets are disabled first, so that they take no new jobs.
// Their idle runners are removed, and Prune blocks until the remaining
// runners finished their jobs before it deletes them.
func (r *Reconciler) Prune(ctx context.Context) error {
	if err := r.pruneRunners(ctx); err != nil {
		return err
	}
	for _, f := range r.forges {
		if err := r.pruneEntities(ctx, f); err != nil {
			return err
		}
		if err := r.pruneTemplates(ctx, f); err != nil {
			return err
		}
		if err := r.pruneCredentials(ctx, f); err != nil {
			return err
		}
		if err := r.pruneEndpoints(ctx, f); err != nil {
			return err
		}
	}
	return nil
}

// apply reports a change and carries it out, unless this is a dry run.
func (r *Reconciler) apply(change Change, do func() error) error {
	r.opts.Report(change)
	if r.opts.DryRun {
		return nil
	}
	if err := do(); err != nil {
		return fmt.Errorf("%s: %w", change, err)
	}
	return nil
}

func (r *Reconciler) authenticate(ctx context.Context) error {
	admin := r.cfg.Admin
	err := r.client.Login(ctx, admin.Username, admin.Password)
	if err == nil {
		return nil
	}
	if !garm.IsNotInitialized(err) {
		return fmt.Errorf("logging in as %s: %w", admin.Username, err)
	}

	err = r.apply(Change{Action: Create, Kind: KindAdmin, Name: admin.Username}, func() error {
		return r.client.FirstRun(ctx, params.NewUserParams{
			Username: admin.Username,
			Email:    admin.Email,
			FullName: admin.FullName,
			Password: admin.Password,
		})
	})
	if err != nil {
		return err
	}
	if r.opts.DryRun {
		return ErrNotInitialized
	}
	if err := r.client.Login(ctx, admin.Username, admin.Password); err != nil {
		return fmt.Errorf("logging in as %s: %w", admin.Username, err)
	}
	return nil
}

func (r *Reconciler) ensureController(ctx context.Context) error {
	info, err := r.client.ControllerInfo(ctx)
	if err != nil {
		return fmt.Errorf("reading the controller settings: %w", err)
	}
	want := r.cfg.Controller
	update := params.UpdateControllerParams{
		MetadataURL: &want.MetadataURL,
		CallbackURL: &want.CallbackURL,
		AgentURL:    &want.AgentURL,
	}
	var fields []string
	if want.AgentURL != info.AgentURL {
		fields = append(fields, "agent_url")
	}
	if want.CallbackURL != info.CallbackURL {
		fields = append(fields, "callback_url")
	}
	if want.MetadataURL != info.MetadataURL {
		fields = append(fields, "metadata_url")
	}
	// Garm cannot unset the webhook URL, so it is only managed when
	// declared.
	if want.WebhookURL != "" {
		update.WebhookURL = &want.WebhookURL
		if want.WebhookURL != info.WebhookURL {
			fields = append(fields, "webhook_url")
		}
	}
	if len(fields) == 0 {
		return nil
	}
	err = r.apply(Change{Action: Update, Kind: KindController, Fields: fields}, func() error {
		return r.client.UpdateController(ctx, update)
	})
	if err != nil {
		return err
	}
	// Garm serves nothing but the controller settings without these URLs.
	if r.opts.DryRun && (info.MetadataURL == "" || info.CallbackURL == "" || info.AgentURL == "") {
		return ErrNotInitialized
	}
	return nil
}

func sortedKeys[V any](m map[string]V) []string {
	return slices.Sorted(maps.Keys(m))
}
