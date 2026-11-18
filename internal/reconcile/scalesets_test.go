package reconcile_test

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"

	commonparams "github.com/cloudbase/garm-provider-common/params"
	"github.com/cloudbase/garm/params"

	"github.com/katexochen/garm-reconcile/internal/config"
	"github.com/katexochen/garm-reconcile/internal/garm"
	"github.com/katexochen/garm-reconcile/internal/garm/garmtest"
)

func scaleSet() config.ScaleSet {
	return config.ScaleSet{CreateScaleSetParams: params.CreateScaleSetParams{
		ProviderName: "incus",
		Image:        "ubuntu",
		Flavor:       "default",
		OSType:       "linux",
		OSArch:       "amd64",
		MaxRunners:   2,
		Enabled:      true,
		Labels:       []string{"coco"},
	}}
}

func withScaleSet() *config.Config {
	cfg := desiredState()
	org := cfg.GitHub.Organizations["my-org"]
	org.ScaleSets = map[string]config.ScaleSet{"coco-set": scaleSet()}
	cfg.GitHub.Organizations["my-org"] = org
	return cfg
}

func TestScaleSets(t *testing.T) {
	cfg := withScaleSet()
	srv := garmtest.NewServer(t)

	changes := run{srv: srv, cfg: cfg}.ensure(t)
	assertEqual(t, "changes", changes[4:], []string{
		"create organization my-org",
		"create pool my-org/coco",
		"create scale set my-org/coco-set",
		"create enterprise corp",
	})
	srv.State(func(st *garmtest.State) {
		set := st.ScaleSets[0]
		assertEqual(t, "name", set.Name, "coco-set")
		assertEqual(t, "organization", set.OrgID, st.Entities[garm.Organization][0].ID)
		assertEqual(t, "labels", set.Tags, []params.Tag{{ID: "coco", Name: "coco"}})
		assertEqual(t, "runner group", set.GitHubRunnerGroup, "Default")
	})
	assertEqual(t, "changes of the second run", run{srv: srv, cfg: cfg}.ensure(t), nil)

	changed := scaleSet()
	changed.MaxRunners = 4
	changed.Enabled = false
	changed.Prefix = "runner"
	changed.ExtraSpecs = json.RawMessage(`{"disable_updates":true}`)
	cfg.GitHub.Organizations["my-org"].ScaleSets["coco-set"] = changed
	want := []string{"update scale set my-org/coco-set (enabled, extra_specs, max_runners, runner_prefix)"}
	assertEqual(t, "changes to plan", run{srv: srv, cfg: cfg, dryRun: true}.ensure(t), want)
	assertEqual(t, "changes", run{srv: srv, cfg: cfg}.ensure(t), want)
	srv.State(func(st *garmtest.State) {
		set := st.ScaleSets[0]
		assertEqual(t, "max runners", set.MaxRunners, 4)
		assertEqual(t, "enabled", set.Enabled, false)
		assertEqual(t, "prefix", set.Prefix, "runner")
		// Extra specs that only differ in formatting are not sent.
		set.ExtraSpecs = json.RawMessage(`{ "disable_updates": true }`)
	})
	assertEqual(t, "changes of the second run", run{srv: srv, cfg: cfg}.ensure(t), nil)
}

func TestScaleSetImmutableSettings(t *testing.T) {
	tests := map[string]func(*config.ScaleSet){
		"provider_name":  func(s *config.ScaleSet) { s.ProviderName = "lxd" },
		"labels":         func(s *config.ScaleSet) { s.Labels = []string{"other"} },
		"disable_update": func(s *config.ScaleSet) { s.DisableUpdate = true },
	}
	for field, change := range tests {
		t.Run(field, func(t *testing.T) {
			cfg := withScaleSet()
			srv := initialized(t, cfg)
			set := scaleSet()
			change(&set)
			cfg.GitHub.Organizations["my-org"].ScaleSets["coco-set"] = set

			rec, _ := run{srv: srv, cfg: cfg}.reconciler(t)
			err := rec.Ensure(context.Background())
			want := "scale set my-org/coco-set: " + field + " cannot be changed"
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Errorf("Ensure() = %v, want it to contain %q", err, want)
			}
			assertEqual(t, "mutations after the credentials", len(takeMutations(srv)), 1)
		})
	}
}

func TestPruneScaleSets(t *testing.T) {
	cfg := withScaleSet()
	srv := initialized(t, cfg)
	srv.State(func(st *garmtest.State) {
		st.Instances = []*params.Instance{
			{Name: "set-idle", ScaleSetID: st.ScaleSets[0].ID, Status: commonparams.InstanceRunning, RunnerStatus: params.RunnerIdle},
			{Name: "set-busy", ScaleSetID: st.ScaleSets[0].ID, Status: commonparams.InstanceRunning, RunnerStatus: params.RunnerActive},
		}
	})
	// The organization goes away with its pool and scale set.
	delete(cfg.GitHub.Organizations, "my-org")

	var progress []string
	onProgress := func(msg string) {
		progress = append(progress, msg)
		srv.State(func(st *garmtest.State) {
			assertEqual(t, "scale set enabled while waiting", st.ScaleSets[0].Enabled, false)
			assertEqual(t, "runners after the first pass", len(st.Instances), 1)
			st.Instances[0].RunnerStatus = params.RunnerTerminated
		})
	}
	changes := run{srv: srv, cfg: cfg, progress: onProgress}.prune(t)
	assertEqual(t, "changes", changes, []string{
		"delete pool my-org/coco",
		"delete scale set my-org/coco-set",
		"delete organization my-org",
	})
	assertEqual(t, "progress", progress, []string{"waiting for the runners of my-org/coco-set"})
	srv.State(func(st *garmtest.State) {
		assertEqual(t, "scale sets", len(st.ScaleSets), 0)
		assertEqual(t, "organizations", len(st.Entities[garm.Organization]), 0)
	})
}

func TestPruneDisabledScaleSetOfDeclaredEntity(t *testing.T) {
	cfg := withScaleSet()
	disabled := scaleSet()
	disabled.Enabled = false
	cfg.GitHub.Organizations["my-org"].ScaleSets["old"] = disabled
	srv := initialized(t, cfg)

	delete(cfg.GitHub.Organizations["my-org"].ScaleSets, "old")
	r := run{srv: srv, cfg: cfg}
	assertEqual(t, "changes to plan", run{srv: srv, cfg: cfg, dryRun: true}.prune(t), []string{"delete scale set my-org/old"})
	assertEqual(t, "changes", r.prune(t), []string{"delete scale set my-org/old"})
	srv.State(func(st *garmtest.State) {
		assertEqual(t, "scale sets", len(st.ScaleSets), 1)
		assertEqual(t, "scale set", st.ScaleSets[0].Name, "coco-set")
	})
}

// populatedScaleSet returns a scale set with every field set, see
// populatedPool. Seeds of a different parity only differ in what garm can
// update.
func populatedScaleSet(t *testing.T, seed uint) params.CreateScaleSetParams {
	t.Helper()
	var spec params.CreateScaleSetParams
	v := reflect.ValueOf(&spec).Elem()
	for i := range v.NumField() {
		field := v.Field(i)
		switch {
		case field.Type() == reflect.TypeFor[params.RunnerPrefix]():
			field.Set(reflect.ValueOf(params.RunnerPrefix{Prefix: fmt.Sprint("prefix-", seed)}))
		case field.Type() == reflect.TypeFor[json.RawMessage]():
			field.SetBytes(fmt.Appendf(nil, `{"seed":%d}`, seed))
		case field.Type() == reflect.TypeFor[[]string]():
			field.Set(reflect.ValueOf([]string{"label"}))
		case field.Type() == reflect.TypeFor[*uint]():
			field.Set(reflect.ValueOf(&seed))
		case field.Kind() == reflect.String:
			field.SetString(fmt.Sprint("value-", seed))
		case field.Kind() == reflect.Uint:
			field.SetUint(uint64(10 + seed))
		case field.Kind() == reflect.Bool:
			field.SetBool(seed%2 == 1)
		case field.Kind() == reflect.Int:
			// The ID GitHub assigns is not declared.
		default:
			t.Fatalf("populatedScaleSet does not handle field %s of type %s", v.Type().Field(i).Name, field.Type())
		}
	}
	spec.Name = ""
	spec.ProviderName = "incus"
	spec.DisableUpdate = true
	spec.OSType = []commonparams.OSType{commonparams.Linux, commonparams.Windows}[seed%2]
	spec.OSArch = []commonparams.OSArch{commonparams.Amd64, commonparams.Arm64}[seed%2]
	return spec
}

// TestEnsureCoversAllScaleSetFields fails when garm adds a field to scale
// sets that reconciling does not read back or does not update.
func TestEnsureCoversAllScaleSetFields(t *testing.T) {
	cfg := withScaleSet()
	cfg.GitHub.Organizations["my-org"].ScaleSets["coco-set"] = config.ScaleSet{CreateScaleSetParams: populatedScaleSet(t, 1)}
	srv := initialized(t, cfg)
	assertEqual(t, "changes after creating the scale set", run{srv: srv, cfg: cfg}.ensure(t), nil)

	cfg.GitHub.Organizations["my-org"].ScaleSets["coco-set"] = config.ScaleSet{CreateScaleSetParams: populatedScaleSet(t, 2)}
	fields := jsonNames(reflect.TypeFor[params.CreateScaleSetParams]())
	fields = slices.DeleteFunc(fields, func(name string) bool {
		return slices.Contains([]string{"name", "scale_set_id", "provider_name", "labels", "disable_update"}, name)
	})
	slices.Sort(fields)
	changes := run{srv: srv, cfg: cfg}.ensure(t)
	assertEqual(t, "changes", changes, []string{"update scale set my-org/coco-set (" + strings.Join(fields, ", ") + ")"})
	assertEqual(t, "changes after updating the scale set", run{srv: srv, cfg: cfg}.ensure(t), nil)
}

func TestScaleSetTemplate(t *testing.T) {
	cfg := withScaleSet()
	srv := initialized(t, cfg)

	cfg.GitHub.Templates = map[string]config.Template{"nixos": {OSType: "linux", Data: "#!/bin/sh"}}
	set := scaleSet()
	set.Template = "nixos"
	cfg.GitHub.Organizations["my-org"].ScaleSets["coco-set"] = set
	changes := run{srv: srv, cfg: cfg}.ensure(t)
	assertEqual(t, "changes", changes, []string{
		"create github template nixos",
		"update scale set my-org/coco-set (template_id)",
	})
	srv.State(func(st *garmtest.State) {
		assertEqual(t, "template", st.ScaleSets[0].TemplateName, "nixos")
	})

	set.Template = "missing"
	cfg.GitHub.Organizations["my-org"].ScaleSets["coco-set"] = set
	rec, _ := run{srv: srv, cfg: cfg}.reconciler(t)
	err := rec.Ensure(context.Background())
	if err == nil || !strings.Contains(err.Error(), "scale set my-org/coco-set: template missing is neither declared") {
		t.Errorf("Ensure() = %v, want it to report the unknown template", err)
	}
}

func TestScaleSetExtraSpecsAndLabels(t *testing.T) {
	cfg := withScaleSet()
	set := scaleSet()
	set.Labels = []string{"coco", "linux"}
	set.ExtraSpecs = json.RawMessage(`{"disable_updates":true}`)
	cfg.GitHub.Organizations["my-org"].ScaleSets["coco-set"] = set
	srv := initialized(t, cfg)
	// Garm keeps the spelling a label was first created with.
	srv.State(func(st *garmtest.State) {
		st.ScaleSets[0].Tags = []params.Tag{{Name: "Linux"}, {Name: "coco"}}
	})
	assertEqual(t, "changes", run{srv: srv, cfg: cfg}.ensure(t), nil)

	set.ExtraSpecs = nil
	cfg.GitHub.Organizations["my-org"].ScaleSets["coco-set"] = set
	assertEqual(t, "changes", run{srv: srv, cfg: cfg}.ensure(t), []string{"update scale set my-org/coco-set (extra_specs)"})
	srv.State(func(st *garmtest.State) {
		assertEqual(t, "extra specs", string(st.ScaleSets[0].ExtraSpecs), "{}")
	})
	assertEqual(t, "changes of the second run", run{srv: srv, cfg: cfg}.ensure(t), nil)
}

func TestDryRunWithNewEntityAndScaleSet(t *testing.T) {
	srv := initialized(t, desiredState())
	cfg := desiredState()
	cfg.GitHub.Organizations["new-org"] = config.Entity{
		CredentialsName: "main",
		ScaleSets:       map[string]config.ScaleSet{"coco-set": scaleSet()},
	}
	changes := run{srv: srv, cfg: cfg, dryRun: true}.ensure(t)
	assertEqual(t, "changes", changes, []string{"create organization new-org", "create scale set new-org/coco-set"})
}

func TestEnsureRejectsAmbiguousScaleSets(t *testing.T) {
	cfg := withScaleSet()
	srv := initialized(t, cfg)
	srv.State(func(st *garmtest.State) {
		duplicate := *st.ScaleSets[0]
		duplicate.ID = 1000
		duplicate.GitHubRunnerGroup = "other"
		st.ScaleSets = append(st.ScaleSets, &duplicate)
	})

	rec, _ := run{srv: srv, cfg: cfg}.reconciler(t)
	err := rec.Ensure(context.Background())
	if err == nil || !strings.Contains(err.Error(), "scale set my-org/coco-set: 2 scale sets share this name") {
		t.Errorf("Ensure() = %v, want it to reject the duplicate scale sets", err)
	}
}

func TestPruneReportsScaleSetThatGarmKeeps(t *testing.T) {
	cfg := withScaleSet()
	srv := initialized(t, cfg)
	srv.State(func(st *garmtest.State) { st.KeepDeletedScaleSets = true })
	delete(cfg.GitHub.Organizations["my-org"].ScaleSets, "coco-set")

	rec, _ := run{srv: srv, cfg: cfg}.reconciler(t)
	if err := rec.Ensure(context.Background()); err != nil {
		t.Fatal(err)
	}
	err := rec.Prune(context.Background())
	if err == nil || !strings.Contains(err.Error(), "delete scale set my-org/coco-set: garm did not delete the scale set") {
		t.Errorf("Prune() = %v, want it to notice that the scale set still exists", err)
	}
}
