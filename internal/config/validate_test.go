package config

import (
	"strings"
	"testing"

	"github.com/cloudbase/garm/params"
)

func validConfig() *Config {
	return &Config{
		Admin: Admin{Username: "admin", Email: "admin@example.com", Password: "hunter2"},
		Controller: Controller{
			MetadataURL: "http://garm/api/v1/metadata",
			CallbackURL: "http://garm/api/v1/callbacks",
			AgentURL:    "http://garm/agent",
			WebhookURL:  "http://garm/webhooks",
		},
		GitHub: Forge{
			Credentials: map[string]Credentials{
				"main": {Endpoint: "github.com", PAT: &PAT{OAuth2Token: "ghp_token"}},
			},
			Repositories: map[string]Entity{
				"owner/repo": {CredentialsName: "main"},
			},
			Organizations: map[string]Entity{
				"my-org": {
					CredentialsName: "main",
					Pools:           map[string]Pool{"coco": validPool()},
				},
			},
		},
	}
}

func validPool() Pool {
	return Pool{CreatePoolParams: params.CreatePoolParams{
		ProviderName: "incus",
		Image:        "ubuntu",
		Flavor:       "default",
		OSType:       "linux",
		OSArch:       "amd64",
		Tags:         []string{"coco"},
		MaxRunners:   2,
	}}
}

func TestValidate(t *testing.T) {
	if err := validConfig().Validate(); err != nil {
		t.Fatalf("valid desired state rejected: %v", err)
	}

	withPool := func(name string, change func(*Pool)) func(*Config) {
		return func(c *Config) {
			pool := validPool()
			change(&pool)
			c.GitHub.Organizations["my-org"].Pools[name] = pool
		}
	}
	withScaleSet := func(change func(*ScaleSet)) func(*Config) {
		return func(c *Config) {
			set := ScaleSet{CreateScaleSetParams: params.CreateScaleSetParams{
				ProviderName: "incus", Image: "ubuntu", Flavor: "default",
				OSType: "linux", OSArch: "amd64", MaxRunners: 2,
			}}
			change(&set)
			org := c.GitHub.Organizations["my-org"]
			org.ScaleSets = map[string]ScaleSet{"coco-set": set}
			c.GitHub.Organizations["my-org"] = org
		}
	}
	if cfg := validConfig(); true {
		withScaleSet(func(*ScaleSet) {})(cfg)
		if err := cfg.Validate(); err != nil {
			t.Fatalf("valid scale set rejected: %v", err)
		}
	}
	withCredentials := func(creds Credentials) func(*Config) {
		return func(c *Config) { c.GitHub.Credentials["main"] = creds }
	}

	tests := map[string]struct {
		change  func(*Config)
		wantErr string
	}{
		"missing admin username": {
			change:  func(c *Config) { c.Admin.Username = "" },
			wantErr: "admin.username: must be set",
		},
		"admin named like garm itself": {
			change:  func(c *Config) { c.Admin.Username = "system" },
			wantErr: "admin.username: system is reserved by garm",
		},
		"missing admin email": {
			change:  func(c *Config) { c.Admin.Email = "" },
			wantErr: "admin.email: must be set",
		},
		"missing admin password": {
			change:  func(c *Config) { c.Admin.Password = "" },
			wantErr: "admin.password: must be set",
		},
		"missing metadata URL": {
			change:  func(c *Config) { c.Controller.MetadataURL = "" },
			wantErr: "controller.metadata_url: must be set",
		},
		"relative callback URL": {
			change:  func(c *Config) { c.Controller.CallbackURL = "/api/v1/callbacks" },
			wantErr: `controller.callback_url: "/api/v1/callbacks" is not an absolute URL`,
		},
		"missing webhook URL": {
			change:  func(c *Config) { c.Controller.WebhookURL = "" },
			wantErr: "controller.webhook_url: must be set",
		},
		"invalid webhook URL": {
			change:  func(c *Config) { c.Controller.WebhookURL = "garm" },
			wantErr: `controller.webhook_url: "garm" is not an absolute URL`,
		},
		"credentials without endpoint": {
			change:  withCredentials(Credentials{PAT: &PAT{OAuth2Token: "ghp_token"}}),
			wantErr: "github.credentials.main.endpoint: must be set",
		},
		"credentials without authentication": {
			change:  withCredentials(Credentials{Endpoint: "github.com"}),
			wantErr: "github.credentials.main: one of pat or app must be set",
		},
		"credentials with both authentication types": {
			change:  withCredentials(Credentials{Endpoint: "github.com", PAT: &PAT{OAuth2Token: "t"}, App: &App{}}),
			wantErr: "github.credentials.main: pat and app are mutually exclusive",
		},
		"empty token": {
			change:  withCredentials(Credentials{Endpoint: "github.com", PAT: &PAT{}}),
			wantErr: "github.credentials.main.pat.oauth2_token: must be set",
		},
		"incomplete app": {
			change:  withCredentials(Credentials{Endpoint: "github.com", App: &App{AppID: 1}}),
			wantErr: "github.credentials.main.app.installation_id: must be set",
		},
		"repository without owner": {
			change:  func(c *Config) { c.GitHub.Repositories["repo"] = Entity{CredentialsName: "main"} },
			wantErr: "github.repositories.repo: repositories must be named owner/name",
		},
		"repository with nested name": {
			change:  func(c *Config) { c.GitHub.Repositories["a/b/c"] = Entity{CredentialsName: "main"} },
			wantErr: "github.repositories.a/b/c: repositories must be named owner/name",
		},
		"entity without credentials": {
			change:  func(c *Config) { c.GitHub.Enterprises = map[string]Entity{"corp": {}} },
			wantErr: "github.enterprises.corp.credentials_name: must be set",
		},
		"undeclared credentials": {
			change:  func(c *Config) { c.GitHub.Repositories["owner/repo"] = Entity{CredentialsName: "other"} },
			wantErr: `github.repositories.owner/repo.credentials_name: credentials "other" are not declared`,
		},
		"unknown pool balancer": {
			change: func(c *Config) {
				c.GitHub.Repositories["owner/repo"] = Entity{CredentialsName: "main", PoolBalancerType: "random"}
			},
			wantErr: `github.repositories.owner/repo.pool_balancer_type: unknown type "random"`,
		},
		"pool name with invalid characters": {
			change:  withPool("Co_Co", func(*Pool) {}),
			wantErr: "github.organizations.my-org.pools.Co_Co: pool names must consist of",
		},
		"pool with runner prefix": {
			change:  withPool("coco", func(p *Pool) { p.Prefix = "other" }),
			wantErr: "github.organizations.my-org.pools.coco.runner_prefix: must not be set",
		},
		"pool without OS type": {
			change:  withPool("coco", func(p *Pool) { p.OSType = "" }),
			wantErr: "github.organizations.my-org.pools.coco.os_type: must be linux or windows",
		},
		"pool with unknown architecture": {
			change:  withPool("coco", func(p *Pool) { p.OSArch = "riscv64" }),
			wantErr: "github.organizations.my-org.pools.coco.os_arch: must be amd64, arm64 or arm",
		},
		"names that only differ in case": {
			change:  func(c *Config) { c.GitHub.Organizations["My-Org"] = Entity{CredentialsName: "main"} },
			wantErr: "github.organizations: My-Org and my-org only differ in case",
		},
		"empty name": {
			change: func(c *Config) {
				c.GitHub.Credentials[""] = Credentials{Endpoint: "github.com", PAT: &PAT{OAuth2Token: "t"}}
			},
			wantErr: "github.credentials: names must not be empty",
		},
		"pool with template ID": {
			change:  withPool("coco", func(p *Pool) { id := uint(1); p.TemplateID = &id }),
			wantErr: "github.organizations.my-org.pools.coco.template_id: must not be set",
		},
		"pool with template for another OS": {
			change: func(c *Config) {
				c.GitHub.Templates = map[string]Template{"win": {OSType: "windows", Data: "#ps1"}}
				withPool("coco", func(p *Pool) { p.Template = "win" })(c)
			},
			wantErr: "github.organizations.my-org.pools.coco.template: template win is for windows",
		},
		"template without data": {
			change:  func(c *Config) { c.GitHub.Templates = map[string]Template{"empty": {OSType: "linux"}} },
			wantErr: "github.templates.empty.data: must be set",
		},
		"template without OS type": {
			change:  func(c *Config) { c.GitHub.Templates = map[string]Template{"tpl": {Data: "#!/bin/sh"}} },
			wantErr: "github.templates.tpl.os_type: must be linux or windows",
		},
		"scale set with name": {
			change:  withScaleSet(func(s *ScaleSet) { s.Name = "other" }),
			wantErr: "github.organizations.my-org.scale_sets.coco-set.name: must not be set",
		},
		"scale set with GitHub ID": {
			change:  withScaleSet(func(s *ScaleSet) { s.ScaleSetID = 3 }),
			wantErr: "github.organizations.my-org.scale_sets.coco-set.scale_set_id: must not be set",
		},
		"scale set with invalid runner prefix": {
			change:  withScaleSet(func(s *ScaleSet) { s.Prefix = "Co_Co" }),
			wantErr: "github.organizations.my-org.scale_sets.coco-set.runner_prefix: must consist of",
		},
		"scale set with template ID": {
			change:  withScaleSet(func(s *ScaleSet) { id := uint(1); s.TemplateID = &id }),
			wantErr: "github.organizations.my-org.scale_sets.coco-set.template_id: must not be set",
		},
		"scale set without architecture": {
			change:  withScaleSet(func(s *ScaleSet) { s.OSArch = "" }),
			wantErr: "github.organizations.my-org.scale_sets.coco-set.os_arch: must be amd64, arm64 or arm",
		},
		"scale set without OS type": {
			change:  withScaleSet(func(s *ScaleSet) { s.OSType = "" }),
			wantErr: "github.organizations.my-org.scale_sets.coco-set.os_type: must be linux or windows",
		},
		"scale set rejected by garm": {
			change:  withScaleSet(func(s *ScaleSet) { s.Image = "" }),
			wantErr: "github.organizations.my-org.scale_sets.coco-set: missing image",
		},
		"credentials for undeclared endpoint": {
			change:  withCredentials(Credentials{Endpoint: "ghes", PAT: &PAT{OAuth2Token: "t"}}),
			wantErr: `github.credentials.main.endpoint: endpoint "ghes" is not declared`,
		},
		"GitHub endpoint without upload URL": {
			change: func(c *Config) {
				c.GitHub.Endpoints = map[string]Endpoint{"ghes": {APIBaseURL: "https://ghes/api/v3", BaseURL: "https://ghes"}}
			},
			wantErr: "github.endpoints.ghes.upload_base_url: must be set",
		},
		"GitHub endpoint with tools metadata URL": {
			change: func(c *Config) {
				c.GitHub.Endpoints = map[string]Endpoint{"ghes": {
					APIBaseURL: "https://ghes/api/v3", BaseURL: "https://ghes", UploadBaseURL: "https://ghes/up",
					ToolsMetadataURL: "https://ghes/tools",
				}}
			},
			wantErr: "github.endpoints.ghes.tools_metadata_url: only Gitea endpoints have",
		},
		"Gitea endpoint with upload URL": {
			change: func(c *Config) {
				c.Gitea.Endpoints = map[string]Endpoint{"local": {
					APIBaseURL: "http://gitea/api/v1", BaseURL: "http://gitea", UploadBaseURL: "http://gitea/up",
				}}
			},
			wantErr: "gitea.endpoints.local.upload_base_url: Gitea endpoints have no upload URL",
		},
		"Gitea endpoint without URL": {
			change:  func(c *Config) { c.Gitea.Endpoints = map[string]Endpoint{"local": {BaseURL: "http://gitea"}} },
			wantErr: "gitea.endpoints.local.api_base_url: must be set",
		},
		"Gitea credentials on the GitHub endpoint": {
			change: func(c *Config) {
				c.Gitea.Credentials = map[string]Credentials{"main": {Endpoint: "github.com", PAT: &PAT{OAuth2Token: "t"}}}
			},
			wantErr: `gitea.credentials.main.endpoint: endpoint "github.com" is not declared`,
		},
		"Gitea app": {
			change: func(c *Config) {
				c.Gitea.Endpoints = map[string]Endpoint{"local": {APIBaseURL: "http://gitea/api/v1", BaseURL: "http://gitea"}}
				c.Gitea.Credentials = map[string]Credentials{"main": {Endpoint: "local", App: &App{AppID: 1, InstallationID: 2, PrivateKey: "k"}}}
			},
			wantErr: "gitea.credentials.main.app: Gitea has no apps",
		},
		"Gitea enterprise": {
			change:  func(c *Config) { c.Gitea.Enterprises = map[string]Entity{"corp": {}} },
			wantErr: "gitea.enterprises: Gitea has no enterprises",
		},
		"Gitea scale set": {
			change: func(c *Config) {
				c.Gitea.Organizations = map[string]Entity{"org": {ScaleSets: map[string]ScaleSet{"set": {}}}}
			},
			wantErr: "gitea.organizations.org.scale_sets: Gitea has no scale sets",
		},
		"template declared for both forges": {
			change: func(c *Config) {
				c.GitHub.Templates = map[string]Template{"nixos": {OSType: "linux", Data: "#!/bin/sh"}}
				c.Gitea.Templates = map[string]Template{"nixos": {OSType: "linux", Data: "#!/bin/sh"}}
			},
			wantErr: "gitea.templates.nixos: garm has no room for a GitHub and a Gitea template of the same name",
		},
		"endpoint declared for both forges": {
			change: func(c *Config) {
				c.GitHub.Endpoints = map[string]Endpoint{"Local": {APIBaseURL: "https://ghes/api/v3", BaseURL: "https://ghes", UploadBaseURL: "https://ghes/up"}}
				c.Gitea.Endpoints = map[string]Endpoint{"local": {APIBaseURL: "http://gitea/api/v1", BaseURL: "http://gitea"}}
			},
			wantErr: "gitea.endpoints.local: garm has no room for this endpoint next to the GitHub endpoint Local",
		},
		"Gitea endpoint named like the one garm ships": {
			change: func(c *Config) {
				c.Gitea.Endpoints = map[string]Endpoint{"GitHub.com": {APIBaseURL: "http://gitea/api/v1", BaseURL: "http://gitea"}}
			},
			wantErr: "gitea.endpoints.GitHub.com: garm has no room for this endpoint next to the GitHub endpoint github.com",
		},
		"endpoints that only differ in case": {
			change: func(c *Config) {
				c.Gitea.Endpoints = map[string]Endpoint{
					"local": {APIBaseURL: "http://gitea/api/v1", BaseURL: "http://gitea"},
					"Local": {APIBaseURL: "http://gitea/api/v1", BaseURL: "http://gitea"},
				}
			},
			wantErr: "gitea.endpoints: Local and local only differ in case",
		},
		"Gitea endpoint with invalid tools metadata URL": {
			change: func(c *Config) {
				c.Gitea.Endpoints = map[string]Endpoint{"local": {
					APIBaseURL: "http://gitea/api/v1", BaseURL: "http://gitea", ToolsMetadataURL: "releases.json",
				}}
			},
			wantErr: `gitea.endpoints.local.tools_metadata_url: "releases.json" is not an absolute URL`,
		},
		"Gitea repository with scale set": {
			change: func(c *Config) {
				c.Gitea.Repositories = map[string]Entity{"owner/repo": {ScaleSets: map[string]ScaleSet{"set": {}}}}
			},
			wantErr: "gitea.repositories.owner/repo.scale_sets: Gitea has no scale sets",
		},
		"enterprise with webhook": {
			change: func(c *Config) {
				c.GitHub.Enterprises = map[string]Entity{"corp": {CredentialsName: "main", InstallWebhook: true}}
			},
			wantErr: "github.enterprises.corp.install_webhook: garm cannot install webhooks for enterprises",
		},
		"pool rejected by garm": {
			change:  withPool("coco", func(p *Pool) { p.MinIdleRunners = 3 }),
			wantErr: "github.organizations.my-org.pools.coco: min_idle_runners cannot be larger than max_runners",
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			cfg := validConfig()
			tt.change(cfg)
			err := cfg.Validate()
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("Validate() = %v, want it to contain %q", err, tt.wantErr)
			}
		})
	}
}

func TestValidateReportsAllProblems(t *testing.T) {
	cfg := validConfig()
	cfg.Admin.Username = ""
	cfg.Controller.AgentURL = ""
	err := cfg.Validate()
	for _, want := range []string{"admin.username", "controller.agent_url"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("Validate() = %v, want it to mention %s", err, want)
		}
	}
}
