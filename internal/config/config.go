// Package config defines the desired state file read by garm-reconcile.
package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"

	commonparams "github.com/cloudbase/garm-provider-common/params"
	"github.com/cloudbase/garm/params"
	"sigs.k8s.io/yaml"
)

// Config is the desired state of a garm controller.
type Config struct {
	Admin      Admin      `json:"admin"`
	Controller Controller `json:"controller"`
	GitHub     Forge      `json:"github"`
	Gitea      Forge      `json:"gitea"`
}

// DefaultGitHubEndpoint is the endpoint garm ships, which can be used
// without being declared.
const DefaultGitHubEndpoint = "github.com"

// Admin is the user garm-reconcile authenticates as. It is created when the
// controller is not initialized yet.
type Admin struct {
	Username string `json:"username"`
	Email    string `json:"email"`
	FullName string `json:"full_name,omitempty"`
	Password string `json:"password"`
}

// Controller holds the URLs under which runners and forges reach garm.
type Controller struct {
	MetadataURL string `json:"metadata_url"`
	CallbackURL string `json:"callback_url"`
	AgentURL    string `json:"agent_url"`
	WebhookURL  string `json:"webhook_url"`
	// MinimumJobAgeBackoff is the time in seconds a job has to be queued
	// before garm creates a runner for it. Garm's default applies if it
	// is not set.
	MinimumJobAgeBackoff *uint `json:"minimum_job_age_backoff,omitempty"`
}

// Forge is everything declared for one forge type.
type Forge struct {
	Endpoints   map[string]Endpoint    `json:"endpoints,omitempty"`
	Credentials map[string]Credentials `json:"credentials,omitempty"`
	Templates   map[string]Template    `json:"templates,omitempty"`
	// Repositories are keyed by "owner/name".
	Repositories  map[string]Entity `json:"repositories,omitempty"`
	Organizations map[string]Entity `json:"organizations,omitempty"`
	Enterprises   map[string]Entity `json:"enterprises,omitempty"`
}

// Endpoint is an instance of a forge.
type Endpoint struct {
	Description string `json:"description,omitempty"`
	APIBaseURL  string `json:"api_base_url"`
	BaseURL     string `json:"base_url"`
	// UploadBaseURL is required for GitHub and not used by Gitea.
	UploadBaseURL string `json:"upload_base_url,omitempty"`
	// CACertBundle holds PEM encoded certificates.
	CACertBundle string `json:"ca_cert_bundle,omitempty"`
	// ToolsMetadataURL is where garm finds the runner releases of a Gitea
	// endpoint. Garm has a default for it.
	ToolsMetadataURL string `json:"tools_metadata_url,omitempty"`
}

// Credentials authenticate garm against a forge endpoint, either with a
// personal access token or as an app.
type Credentials struct {
	Description string `json:"description,omitempty"`
	Endpoint    string `json:"endpoint"`
	PAT         *PAT   `json:"pat,omitempty"`
	App         *App   `json:"app,omitempty"`
}

// PAT is a personal access token.
type PAT struct {
	OAuth2Token string `json:"oauth2_token"`
}

// App authenticates as an installation of a GitHub app.
type App struct {
	AppID          int64 `json:"app_id"`
	InstallationID int64 `json:"installation_id"`
	// PrivateKey is the PEM encoded private key of the app.
	PrivateKey string `json:"private_key"`
}

// Template is a script that installs the runner on a new instance.
type Template struct {
	Description string              `json:"description,omitempty"`
	OSType      commonparams.OSType `json:"os_type"`
	Data        string              `json:"data"`
}

// Entity is a repository, organization or enterprise that garm provides
// runners for.
type Entity struct {
	CredentialsName string `json:"credentials_name"`
	// WebhookSecret is generated when the entity is created without one.
	WebhookSecret    string                  `json:"webhook_secret,omitempty"`
	PoolBalancerType params.PoolBalancerType `json:"pool_balancer_type,omitempty"`
	AgentMode        bool                    `json:"agent_mode,omitempty"`
	// InstallWebhook makes garm install its webhook in the forge, which
	// requires webhook management to be enabled in garm. Otherwise garm's
	// webhook is removed.
	InstallWebhook bool `json:"install_webhook,omitempty"`
	// Pools are keyed by their runner prefix, which identifies a pool
	// within its entity.
	Pools map[string]Pool `json:"pools,omitempty"`
	// ScaleSets are keyed by their name, which is also the label jobs
	// select them with.
	ScaleSets map[string]ScaleSet `json:"scale_sets,omitempty"`
}

// Pool is a pool of runners. Its settings are the ones garm takes to create
// a pool, so the accepted fields follow the garm release this is built
// against.
type Pool struct {
	params.CreatePoolParams
	// Template names the template that installs the runners, in place of
	// the template ID garm takes. It may be a template garm ships. Garm
	// picks one, and keeps the one a pool has, if it is empty.
	Template string `json:"template,omitempty"`
}

// ScaleSet is a set of runners that GitHub schedules jobs on. Like for
// pools, its settings are the ones garm takes to create one.
type ScaleSet struct {
	params.CreateScaleSetParams
	// Template names the template that installs the runners, see Pool.
	Template string `json:"template,omitempty"`
}

// Load reads and validates the desired state from a YAML or JSON file.
func Load(path string, secrets SecretReader) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	cfg, err := Parse(data, secrets)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return cfg, nil
}

// Parse decodes and validates the desired state. Secret references are
// replaced with the values secrets returns for them.
func Parse(data []byte, secrets SecretReader) (*Config, error) {
	data, err := yaml.YAMLToJSONStrict(data)
	if err != nil {
		return nil, err
	}

	// Numbers are kept verbatim, so that large IDs survive the round trip
	// through the generic representation.
	var tree any
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if err := dec.Decode(&tree); err != nil {
		return nil, err
	}
	tree, err = resolveSecrets(tree, "", secrets)
	if err != nil {
		return nil, err
	}
	data, err = json.Marshal(tree)
	if err != nil {
		return nil, err
	}

	var cfg Config
	dec = json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&cfg); err != nil {
		return nil, err
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}
