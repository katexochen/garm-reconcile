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

// runnerPrefix restricts runner prefixes, and thereby the names of pools, to
// what is valid in the runner names garm derives from them.
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
	c.Gitea.validate("gitea", &p)
	c.Gitea.validateGitea(&p)
	c.validateSharedNames(&p)
	return errors.Join(p...)
}

func (a Admin) validate(p *problems) {
	switch a.Username {
	case "":
		p.addf("admin.username: must be set")
	case params.SystemUser:
		// Garm reports this name as the owner of what it ships.
		p.addf("admin.username: %s is reserved by garm", params.SystemUser)
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
	validateURL(p, "controller.webhook_url", c.WebhookURL, true)
}

// validateSharedNames rejects names that are declared for both forges, where
// garm has one namespace for them.
func (c *Config) validateSharedNames(p *problems) {
	for _, name := range sortedKeys(c.GitHub.Templates) {
		if _, ok := c.Gitea.Templates[name]; ok {
			p.addf("gitea.templates.%s: garm has no room for a GitHub and a Gitea template of the same name", name)
		}
	}
	// Endpoint names are also compared without regard to case, and garm
	// ships one.
	github := map[string]string{strings.ToLower(DefaultGitHubEndpoint): DefaultGitHubEndpoint}
	for name := range c.GitHub.Endpoints {
		github[strings.ToLower(name)] = name
	}
	for _, name := range sortedKeys(c.Gitea.Endpoints) {
		if other, ok := github[strings.ToLower(name)]; ok {
			p.addf("gitea.endpoints.%s: garm has no room for this endpoint next to the GitHub endpoint %s", name, other)
		}
	}
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
	validateNames(p, path+".endpoints", f.Endpoints)
	for _, name := range sortedKeys(f.Endpoints) {
		f.Endpoints[name].validate(fmt.Sprintf("%s.endpoints.%s", path, name), path == "github", p)
	}
	validateNames(p, path+".credentials", f.Credentials)
	validateNames(p, path+".repositories", f.Repositories)
	validateNames(p, path+".organizations", f.Organizations)
	validateNames(p, path+".enterprises", f.Enterprises)

	for _, name := range sortedKeys(f.Credentials) {
		credsPath := fmt.Sprintf("%s.credentials.%s", path, name)
		creds := f.Credentials[name]
		creds.validate(credsPath, p)
		_, declared := f.Endpoints[creds.Endpoint]
		shipped := path == "github" && creds.Endpoint == DefaultGitHubEndpoint
		if creds.Endpoint != "" && !declared && !shipped {
			p.addf("%s.endpoint: endpoint %q is not declared", credsPath, creds.Endpoint)
		}
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

// validateGitea reports what is declared for Gitea, but only exists on
// GitHub.
func (f Forge) validateGitea(p *problems) {
	for _, name := range sortedKeys(f.Credentials) {
		if f.Credentials[name].App != nil {
			p.addf("gitea.credentials.%s.app: Gitea has no apps, use pat", name)
		}
	}
	if len(f.Enterprises) > 0 {
		p.addf("gitea.enterprises: Gitea has no enterprises")
	}
	for _, name := range sortedKeys(f.Repositories) {
		if len(f.Repositories[name].ScaleSets) > 0 {
			p.addf("gitea.repositories.%s.scale_sets: Gitea has no scale sets", name)
		}
	}
	for _, name := range sortedKeys(f.Organizations) {
		if len(f.Organizations[name].ScaleSets) > 0 {
			p.addf("gitea.organizations.%s.scale_sets: Gitea has no scale sets", name)
		}
	}
}

func (e Endpoint) validate(path string, github bool, p *problems) {
	validateURL(p, path+".api_base_url", e.APIBaseURL, true)
	validateURL(p, path+".base_url", e.BaseURL, true)
	validateURL(p, path+".upload_base_url", e.UploadBaseURL, github)
	validateURL(p, path+".tools_metadata_url", e.ToolsMetadataURL, false)
	if !github && e.UploadBaseURL != "" {
		p.addf("%s.upload_base_url: Gitea endpoints have no upload URL", path)
	}
	if github && e.ToolsMetadataURL != "" {
		p.addf("%s.tools_metadata_url: only Gitea endpoints have a tools metadata URL", path)
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
		f.validateTemplateRef(p, poolPath, pool.Template, pool.TemplateID, pool.OSType)
		validateOSArch(p, poolPath, pool.OSArch)
	}
	f.validateScaleSets(path, e, p)
}

func (f Forge) validateScaleSets(path string, e Entity, p *problems) {
	for _, name := range sortedKeys(e.ScaleSets) {
		set := e.ScaleSets[name]
		setPath := fmt.Sprintf("%s.scale_sets.%s", path, name)
		if name == "" {
			p.addf("%s.scale_sets: names must not be empty", path)
		}
		if set.Name != "" {
			p.addf("%s.name: must not be set, the scale set is named by its key", setPath)
		}
		if set.ScaleSetID != 0 {
			p.addf("%s.scale_set_id: must not be set, GitHub assigns it", setPath)
		}
		if set.Prefix != "" && !runnerPrefix.MatchString(set.Prefix) {
			p.addf("%s.runner_prefix: must consist of lowercase letters, digits and dashes", setPath)
		}
		set.Name = name
		if err := set.Validate(); err != nil {
			p.addf("%s: %w", setPath, err)
		}
		validateOSType(p, setPath, set.OSType)
		validateOSArch(p, setPath, set.OSArch)
		f.validateTemplateRef(p, setPath, set.Template, set.TemplateID, set.OSType)
	}
}

func validateOSArch(p *problems, path string, arch commonparams.OSArch) {
	switch arch {
	case commonparams.Amd64, commonparams.Arm64, commonparams.Arm:
	default:
		p.addf("%s.os_arch: must be amd64, arm64 or arm", path)
	}
}

// validateTemplateRef checks the template a pool or scale set at path names.
func (f Forge) validateTemplateRef(p *problems, path, template string, id *uint, osType commonparams.OSType) {
	if id != nil {
		p.addf("%s.template_id: must not be set, name the template in template instead", path)
	}
	if tpl, ok := f.Templates[template]; ok && tpl.OSType != osType {
		p.addf("%s.template: template %s is for %s", path, template, tpl.OSType)
	}
}

func sortedKeys[V any](m map[string]V) []string {
	return slices.Sorted(maps.Keys(m))
}
