// Package config defines the desired state file read by garm-reconcile.
package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"

	"github.com/cloudbase/garm/params"
	"sigs.k8s.io/yaml"
)

// Config is the desired state of a garm controller.
type Config struct {
	Admin      Admin      `json:"admin"`
	Controller Controller `json:"controller"`
	GitHub     Forge      `json:"github"`
}

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
	// WebhookURL is left untouched in garm when empty.
	WebhookURL string `json:"webhook_url,omitempty"`
}

// Forge is everything declared for one forge type.
type Forge struct {
	Credentials map[string]Credentials `json:"credentials,omitempty"`
	// Repositories are keyed by "owner/name".
	Repositories  map[string]Entity `json:"repositories,omitempty"`
	Organizations map[string]Entity `json:"organizations,omitempty"`
	Enterprises   map[string]Entity `json:"enterprises,omitempty"`
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

// Entity is a repository, organization or enterprise that garm provides
// runners for.
type Entity struct {
	CredentialsName string `json:"credentials_name"`
	// WebhookSecret is generated when the entity is created without one.
	WebhookSecret    string                  `json:"webhook_secret,omitempty"`
	PoolBalancerType params.PoolBalancerType `json:"pool_balancer_type,omitempty"`
	AgentMode        bool                    `json:"agent_mode,omitempty"`
	// Pools are keyed by their runner prefix, which identifies a pool
	// within its entity. Their settings are the ones garm takes to create
	// a pool, so the accepted fields follow the garm release this is
	// built against.
	Pools map[string]params.CreatePoolParams `json:"pools,omitempty"`
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
