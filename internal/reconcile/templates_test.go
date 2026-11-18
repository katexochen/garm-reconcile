package reconcile_test

import (
	"context"
	"strings"
	"testing"

	"github.com/cloudbase/garm/params"

	"github.com/katexochen/garm-reconcile/internal/config"
	"github.com/katexochen/garm-reconcile/internal/garm/garmtest"
)

// findTemplate returns the template that is not shipped by garm.
func findTemplate(t *testing.T, st *garmtest.State, name string) *params.Template {
	t.Helper()
	for _, template := range st.Templates {
		if template.Name == name && template.Owner != params.SystemUser {
			return template
		}
	}
	t.Fatalf("template %s does not exist", name)
	return nil
}

func withTemplate() *config.Config {
	cfg := desiredState()
	cfg.GitHub.Templates = map[string]config.Template{
		"nixos": {Description: "for NixOS images", OSType: "linux", Data: "#!/bin/sh\necho install"},
	}
	declared := pool()
	declared.Template = "nixos"
	cfg.GitHub.Organizations["my-org"].Pools["coco"] = declared
	return cfg
}

func TestTemplates(t *testing.T) {
	cfg := withTemplate()
	srv := garmtest.NewServer(t)

	changes := run{srv: srv, cfg: cfg}.ensure(t)
	assertEqual(t, "changes", changes[2:], []string{
		"create github credentials main",
		"create github template nixos",
		"create repository owner/repo",
		"create organization my-org",
		"create pool my-org/coco",
		"create enterprise corp",
	})
	srv.State(func(st *garmtest.State) {
		template := findTemplate(t, st, "nixos")
		assertEqual(t, "data", string(template.Data), "#!/bin/sh\necho install")
		assertEqual(t, "forge type", template.ForgeType, params.GithubEndpointType)
		assertEqual(t, "template of the pool", st.Pools[0].TemplateID, template.ID)
	})
	assertEqual(t, "changes of the second run", run{srv: srv, cfg: cfg}.ensure(t), nil)

	cfg.GitHub.Templates["nixos"] = config.Template{OSType: "linux", Data: "#!/bin/sh\necho changed"}
	assertEqual(t, "changes to plan", run{srv: srv, cfg: cfg, dryRun: true}.ensure(t), []string{
		"update github template nixos (data, description)",
	})
	assertEqual(t, "changes", run{srv: srv, cfg: cfg}.ensure(t), []string{
		"update github template nixos (data, description)",
	})
	srv.State(func(st *garmtest.State) {
		template := findTemplate(t, st, "nixos")
		assertEqual(t, "data", string(template.Data), "#!/bin/sh\necho changed")
		assertEqual(t, "description", template.Description, "")
	})

	// The pool goes back to a template garm ships before the declared one
	// is deleted.
	delete(cfg.GitHub.Templates, "nixos")
	shipped := pool()
	shipped.Template = "github_linux"
	cfg.GitHub.Organizations["my-org"].Pools["coco"] = shipped
	r := run{srv: srv, cfg: cfg}
	assertEqual(t, "changes to ensure", r.ensure(t), []string{"update pool my-org/coco (template_id)"})
	assertEqual(t, "changes to prune", r.prune(t), []string{"delete github template nixos"})
	srv.State(func(st *garmtest.State) {
		assertEqual(t, "templates", len(st.Templates), 2)
		assertEqual(t, "template of the pool", st.Pools[0].TemplateName, "github_linux")
	})
}

func TestTemplateOfPoolIsKeptWhenNotDeclared(t *testing.T) {
	cfg := withTemplate()
	srv := initialized(t, cfg)

	cfg.GitHub.Organizations["my-org"].Pools["coco"] = pool()
	assertEqual(t, "changes", run{srv: srv, cfg: cfg}.ensure(t), nil)
	srv.State(func(st *garmtest.State) {
		assertEqual(t, "template of the pool", st.Pools[0].TemplateName, "nixos")
	})
}

func TestDryRunWithNewTemplate(t *testing.T) {
	cfg := desiredState()
	srv := initialized(t, cfg)

	cfg = withTemplate()
	changes := run{srv: srv, cfg: cfg, dryRun: true}.ensure(t)
	assertEqual(t, "changes", changes, []string{
		"create github template nixos",
		"update pool my-org/coco (template_id)",
	})
	assertEqual(t, "mutations", takeMutations(srv), nil)
}

func TestTemplateErrors(t *testing.T) {
	t.Run("unknown template", func(t *testing.T) {
		cfg := desiredState()
		declared := pool()
		declared.Template = "missing"
		cfg.GitHub.Organizations["my-org"].Pools["coco"] = declared

		rec, _ := run{srv: garmtest.NewServer(t), cfg: cfg}.reconciler(t)
		err := rec.Ensure(context.Background())
		if err == nil || !strings.Contains(err.Error(), "pool my-org/coco: template missing does not exist") {
			t.Errorf("Ensure() = %v, want it to report the unknown template", err)
		}
	})

	t.Run("changed OS type", func(t *testing.T) {
		cfg := withTemplate()
		srv := initialized(t, cfg)
		cfg.GitHub.Templates["nixos"] = config.Template{OSType: "windows", Data: "#ps1"}

		rec, _ := run{srv: srv, cfg: cfg}.reconciler(t)
		err := rec.Ensure(context.Background())
		if err == nil || !strings.Contains(err.Error(), "the OS type cannot be changed from linux to windows") {
			t.Errorf("Ensure() = %v, want it to reject the changed OS type", err)
		}
	})
}

func TestPruneLeavesTemplatesOfOtherOwners(t *testing.T) {
	cfg := desiredState()
	srv := initialized(t, cfg)
	srv.State(func(st *garmtest.State) {
		st.Templates = append(st.Templates, &params.Template{
			ID: 100, Name: "gitea", OSType: "linux", ForgeType: params.GiteaEndpointType, Owner: "admin",
		})
	})

	assertEqual(t, "changes", run{srv: srv, cfg: cfg}.prune(t), nil)
	srv.State(func(st *garmtest.State) {
		assertEqual(t, "templates", len(st.Templates), 3)
	})
}
