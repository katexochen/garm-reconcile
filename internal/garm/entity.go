package garm

import "github.com/cloudbase/garm/params"

// EntityKind distinguishes the kinds of forge entities garm provides
// runners for. Their APIs only differ in the path.
type EntityKind string

const (
	Repository   EntityKind = "repository"
	Organization EntityKind = "organization"
	Enterprise   EntityKind = "enterprise"
)

func (k EntityKind) collection() string {
	switch k {
	case Repository:
		return "repositories"
	case Organization:
		return "organizations"
	case Enterprise:
		return "enterprises"
	}
	panic("unknown entity kind " + string(k))
}

// Entity is the part of a repository, organization or enterprise that all
// kinds have in common.
type Entity struct {
	ID string `json:"id"`
	// Owner is only set for repositories.
	Owner            string                  `json:"owner"`
	Name             string                  `json:"name"`
	CredentialsName  string                  `json:"credentials_name"`
	PoolBalancerType params.PoolBalancerType `json:"pool_balancing_type"`
	AgentMode        bool                    `json:"agent_mode"`
	Endpoint         params.ForgeEndpoint    `json:"endpoint"`
}

// FullName returns the name that identifies the entity on its endpoint,
// which is owner/name for repositories.
func (e Entity) FullName() string {
	if e.Owner != "" {
		return e.Owner + "/" + e.Name
	}
	return e.Name
}

// CreateEntityParams is the request to create an entity of any kind.
type CreateEntityParams struct {
	// Owner must only be set for repositories.
	Owner            string                  `json:"owner,omitempty"`
	Name             string                  `json:"name"`
	CredentialsName  string                  `json:"credentials_name"`
	WebhookSecret    string                  `json:"webhook_secret"`
	PoolBalancerType params.PoolBalancerType `json:"pool_balancer_type,omitempty"`
	ForgeType        params.EndpointType     `json:"forge_type,omitempty"`
	AgentMode        bool                    `json:"agent_mode,omitempty"`
}

// CreateEndpointParams is the request to create an endpoint of any forge.
type CreateEndpointParams struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	APIBaseURL  string `json:"api_base_url"`
	BaseURL     string `json:"base_url"`
	// UploadBaseURL must only be set for GitHub.
	UploadBaseURL string `json:"upload_base_url,omitempty"`
	CACertBundle  []byte `json:"ca_cert_bundle,omitempty"`
	// ToolsMetadataURL must only be set for Gitea.
	ToolsMetadataURL string `json:"tools_metadata_url,omitempty"`
}

// UpdateEndpointParams is the request to update an endpoint of any forge.
// Fields that are nil or empty are left as they are.
type UpdateEndpointParams struct {
	Description      *string `json:"description,omitempty"`
	APIBaseURL       *string `json:"api_base_url,omitempty"`
	BaseURL          *string `json:"base_url,omitempty"`
	UploadBaseURL    *string `json:"upload_base_url,omitempty"`
	CACertBundle     []byte  `json:"ca_cert_bundle,omitempty"`
	ToolsMetadataURL string  `json:"tools_metadata_url,omitempty"`
}
