package config

import (
	"errors"
	"fmt"
	"maps"
	"net/url"
	"regexp"
	"slices"
	"strings"

	commonparams "github.com/cloudbase/garm-provider-common/params"
	"github.com/cloudbase/garm/params"
)

// runnerPrefix restricts pool names to what is valid in the runner names
// garm derives from them.
var runnerPrefix = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)

// problems collects everything that is wrong with a desired state, so that
// all of it can be reported at once.
type problems []error

func (p *problems) addf(format string, args ...any) {
	*p = append(*p, fmt.Errorf(format, args...))
}

// Validate reports everything that is wrong with the desired state.
func (c *Config) Validate() error {
	var p problems
	c.Admin.validate(&p)
	c.Controller.validate(&p)
	c.GitHub.validate("github", &p)
	return errors.Join(p...)
}

func (a Admin) validate(p *problems) {
	if a.Username == "" {
		p.addf("admin.username: must be set")
	}
	if a.Email == "" {
		p.addf("admin.email: must be set")
	}
	if a.Password == "" {
		p.addf("admin.password: must be set")
	}
}

func (c Controller) validate(p *problems) {
	validateURL(p, "controller.metadata_url", c.MetadataURL, true)
	validateURL(p, "controller.callback_url", c.CallbackURL, true)
	validateURL(p, "controller.agent_url", c.AgentURL, true)
	validateURL(p, "controller.webhook_url", c.WebhookURL, false)
}

func validateURL(p *problems, path, value string, required bool) {
	if value == "" {
		if required {
			p.addf("%s: must be set", path)
		}
		return
	}
	u, err := url.Parse(value)
	if err != nil || u.Scheme == "" || u.Host == "" {
		p.addf("%s: %q is not an absolute URL", path, value)
	}
}

func (f Forge) validate(path string, p *problems) {
	validateNames(p, path+".credentials", f.Credentials)
	validateNames(p, path+".templates", f.Templates)
	validateNames(p, path+".repositories", f.Repositories)
	validateNames(p, path+".organizations", f.Organizations)
	validateNames(p, path+".enterprises", f.Enterprises)

	for _, name := range sortedKeys(f.Credentials) {
		f.Credentials[name].validate(fmt.Sprintf("%s.credentials.%s", path, name), p)
	}
	for _, name := range sortedKeys(f.Templates) {
		f.Templates[name].validate(fmt.Sprintf("%s.templates.%s", path, name), p)
	}
	for _, name := range sortedKeys(f.Repositories) {
		entityPath := fmt.Sprintf("%s.repositories.%s", path, name)
		owner, repo, _ := strings.Cut(name, "/")
		if owner == "" || repo == "" || strings.Contains(repo, "/") {
			p.addf("%s: repositories must be named owner/name", entityPath)
		}
		f.validateEntity(entityPath, f.Repositories[name], p)
	}
	for _, name := range sortedKeys(f.Organizations) {
		f.validateEntity(fmt.Sprintf("%s.organizations.%s", path, name), f.Organizations[name], p)
	}
	for _, name := range sortedKeys(f.Enterprises) {
		f.validateEntity(fmt.Sprintf("%s.enterprises.%s", path, name), f.Enterprises[name], p)
	}
}

// validateNames rejects names garm cannot tell apart, as it compares them
// without regard to case.
func validateNames[V any](p *problems, path string, declared map[string]V) {
	seen := map[string]string{}
	for _, name := range sortedKeys(declared) {
		if name == "" {
			p.addf("%s: names must not be empty", path)
		}
		lower := strings.ToLower(name)
		if other, ok := seen[lower]; ok {
			p.addf("%s: %s and %s only differ in case", path, other, name)
		}
		seen[lower] = name
	}
}

func (c Credentials) validate(path string, p *problems) {
	if c.Endpoint == "" {
		p.addf("%s.endpoint: must be set", path)
	}
	switch {
	case c.PAT != nil && c.App != nil:
		p.addf("%s: pat and app are mutually exclusive", path)
	case c.PAT != nil:
		if c.PAT.OAuth2Token == "" {
			p.addf("%s.pat.oauth2_token: must be set", path)
		}
	case c.App != nil:
		if c.App.AppID == 0 {
			p.addf("%s.app.app_id: must be set", path)
		}
		if c.App.InstallationID == 0 {
			p.addf("%s.app.installation_id: must be set", path)
		}
		if c.App.PrivateKey == "" {
			p.addf("%s.app.private_key: must be set", path)
		}
	default:
		p.addf("%s: one of pat or app must be set", path)
	}
}

func (t Template) validate(path string, p *problems) {
	validateOSType(p, path, t.OSType)
	if t.Data == "" {
		p.addf("%s.data: must be set", path)
	}
}

func validateOSType(p *problems, path string, osType commonparams.OSType) {
	switch osType {
	case commonparams.Linux, commonparams.Windows:
	default:
		p.addf("%s.os_type: must be linux or windows", path)
	}
}

func (f Forge) validateEntity(path string, e Entity, p *problems) {
	if e.CredentialsName == "" {
		p.addf("%s.credentials_name: must be set", path)
	} else if _, ok := f.Credentials[e.CredentialsName]; !ok {
		p.addf("%s.credentials_name: credentials %q are not declared", path, e.CredentialsName)
	}
	switch e.PoolBalancerType {
	case params.PoolBalancerTypeNone, params.PoolBalancerTypeRoundRobin, params.PoolBalancerTypePack:
	default:
		p.addf("%s.pool_balancer_type: unknown type %q", path, e.PoolBalancerType)
	}
	for _, prefix := range sortedKeys(e.Pools) {
		pool := e.Pools[prefix]
		poolPath := fmt.Sprintf("%s.pools.%s", path, prefix)
		if !runnerPrefix.MatchString(prefix) {
			p.addf("%s: pool names must consist of lowercase letters, digits and dashes", poolPath)
		}
		if pool.Prefix != "" {
			p.addf("%s.runner_prefix: must not be set, the name of the pool is its runner prefix", poolPath)
		}
		if err := pool.Validate(); err != nil {
			p.addf("%s: %w", poolPath, err)
		}
		validateOSType(p, poolPath, pool.OSType)
		if pool.TemplateID != nil {
			p.addf("%s.template_id: must not be set, name the template in template instead", poolPath)
		}
		if tpl, ok := f.Templates[pool.Template]; ok && tpl.OSType != pool.OSType {
			p.addf("%s.template: template %s is for %s", poolPath, pool.Template, tpl.OSType)
		}
		switch pool.OSArch {
		case commonparams.Amd64, commonparams.Arm64, commonparams.Arm:
		default:
			p.addf("%s.os_arch: must be amd64, arm64 or arm", poolPath)
		}
	}
}

func sortedKeys[V any](m map[string]V) []string {
	return slices.Sorted(maps.Keys(m))
}
