package reconcile_test

import (
	"context"
	"strings"
	"testing"

	"github.com/cloudbase/garm/params"

	"github.com/katexochen/garm-reconcile/internal/config"
	"github.com/katexochen/garm-reconcile/internal/garm"
	"github.com/katexochen/garm-reconcile/internal/garm/garmtest"
)

func withWebhook() *config.Config {
	cfg := desiredState()
	org := cfg.GitHub.Organizations["my-org"]
	org.InstallWebhook = true
	cfg.GitHub.Organizations["my-org"] = org
	return cfg
}

func TestWebhooks(t *testing.T) {
	cfg := withWebhook()
	srv := garmtest.NewServer(t)
	srv.State(func(st *garmtest.State) { st.ManageWebhooks = true })

	plan := run{srv: srv, cfg: cfg, dryRun: true}
	changes := run{srv: srv, cfg: cfg}.ensure(t)
	assertEqual(t, "changes", changes[4:7], []string{
		"create github organization my-org",
		"create github webhook my-org",
		"create github pool my-org/coco",
	})
	srv.State(func(st *garmtest.State) {
		hook := st.Entities[garm.Organization][0].Webhook
		if hook == nil || !strings.HasPrefix(hook.URL, cfg.Controller.WebhookURL+"/") {
			t.Errorf("webhook of the organization = %+v, want one for the controller", hook)
		}
		if st.Entities[garm.Repository][0].Webhook != nil {
			t.Error("the repository got a webhook that is not declared")
		}
	})
	assertEqual(t, "changes of the second run", run{srv: srv, cfg: cfg}.ensure(t), nil)

	// A webhook that is not declared is removed, one that is declared for
	// another entity is installed.
	cfg = desiredState()
	repo := cfg.GitHub.Repositories["owner/repo"]
	repo.InstallWebhook = true
	cfg.GitHub.Repositories["owner/repo"] = repo
	want := []string{"create github webhook owner/repo", "delete github webhook my-org"}
	assertEqual(t, "changes to plan", plan.with(cfg).ensure(t), want)
	assertEqual(t, "changes", run{srv: srv, cfg: cfg}.ensure(t), want)
	srv.State(func(st *garmtest.State) {
		if st.Entities[garm.Organization][0].Webhook != nil || st.Entities[garm.Repository][0].Webhook == nil {
			t.Error("the webhooks were not moved from the organization to the repository")
		}
	})
}

func TestDryRunWithNewEntityAndWebhook(t *testing.T) {
	srv := initialized(t, desiredState())
	cfg := desiredState()
	cfg.GitHub.Organizations["new-org"] = config.Entity{CredentialsName: "main", InstallWebhook: true}
	changes := run{srv: srv, cfg: cfg, dryRun: true}.ensure(t)
	assertEqual(t, "changes", changes, []string{"create github organization new-org", "create github webhook new-org"})
}

func TestWebhooksFollowTheWebhookURL(t *testing.T) {
	cfg := withWebhook()
	srv := garmtest.NewServer(t)
	srv.State(func(st *garmtest.State) { st.ManageWebhooks = true })
	run{srv: srv, cfg: cfg}.ensure(t)

	cfg.Controller.WebhookURL = "https://new.example.com/webhooks"
	want := []string{
		"delete github webhook my-org",
		"update controller (webhook_url)",
		"create github webhook my-org",
	}
	assertEqual(t, "changes to plan", run{srv: srv, cfg: cfg, dryRun: true}.ensure(t), want)
	assertEqual(t, "changes", run{srv: srv, cfg: cfg}.ensure(t), want)
	srv.State(func(st *garmtest.State) {
		hook := st.Entities[garm.Organization][0].Webhook
		if hook == nil || !strings.HasPrefix(hook.URL, "https://new.example.com/webhooks/") {
			t.Errorf("webhook of the organization = %+v, want one for the new URL", hook)
		}
	})
	assertEqual(t, "changes of the second run", run{srv: srv, cfg: cfg}.ensure(t), nil)
}

func TestManualWebhookIsLeftAlone(t *testing.T) {
	cfg := desiredState()
	srv := garmtest.NewServer(t)
	srv.State(func(st *garmtest.State) { st.ManageWebhooks = true })
	run{srv: srv, cfg: cfg}.ensure(t)
	srv.State(func(st *garmtest.State) {
		st.Entities[garm.Organization][0].Webhook = &params.HookInfo{ID: 1, URL: cfg.Controller.WebhookURL}
	})

	// Garm refuses to remove it, and it serves where a webhook is declared.
	assertEqual(t, "changes without a declared webhook", run{srv: srv, cfg: cfg}.ensure(t), nil)
	assertEqual(t, "changes with a declared webhook", run{srv: srv, cfg: withWebhook()}.ensure(t), nil)

	cfg.Controller.WebhookURL = "https://new.example.com/webhooks"
	assertEqual(t, "changes", run{srv: srv, cfg: cfg}.ensure(t), []string{"update controller (webhook_url)"})
}

func TestWebhooksOutOfReach(t *testing.T) {
	// The token of the credentials may not read webhooks.
	srv := garmtest.NewServer(t)
	srv.State(func(st *garmtest.State) { st.ManageWebhooks, st.UnreadableWebhooks = true, true })
	cfg := desiredState()
	run{srv: srv, cfg: cfg}.ensure(t)
	assertEqual(t, "changes of the second run", run{srv: srv, cfg: cfg}.ensure(t), nil)

	rec, _ := run{srv: srv, cfg: withWebhook()}.reconciler(t)
	err := rec.Ensure(context.Background())
	if err == nil || !strings.Contains(err.Error(), "reading the github webhook of my-org: garm: Bad Request") {
		t.Errorf("Ensure() = %v, want it to fail reading the declared webhook", err)
	}
}

func TestWebhookWithoutWebhookManagement(t *testing.T) {
	// Without webhooks declared, it does not matter that garm does not
	// manage them.
	srv := initialized(t, desiredState())

	rec, _ := run{srv: srv, cfg: withWebhook()}.reconciler(t)
	err := rec.Ensure(context.Background())
	if err == nil || !strings.Contains(err.Error(), "github webhook of my-org: garm is not configured to manage webhooks") {
		t.Errorf("Ensure() = %v, want it to report that garm does not manage webhooks", err)
	}
}

func TestWebhookReadIsRetried(t *testing.T) {
	cfg := withWebhook()
	srv := garmtest.NewServer(t)
	srv.State(func(st *garmtest.State) { st.ManageWebhooks = true })
	run{srv: srv, cfg: cfg}.ensure(t)

	// GARM fails to reach the forge until it uses the new credentials.
	srv.State(func(st *garmtest.State) { st.FailingWebhookReads = 3 })
	assertEqual(t, "changes", run{srv: srv, cfg: cfg}.ensure(t), nil)

	srv.State(func(st *garmtest.State) { st.FailingWebhookReads = 100 })
	rec, _ := run{srv: srv, cfg: cfg}.reconciler(t)
	err := rec.Ensure(context.Background())
	if err == nil || !strings.Contains(err.Error(), "reading the github webhook of owner/repo: garm: Server error") {
		t.Errorf("Ensure() = %v, want it to give up reading the webhook", err)
	}
}
