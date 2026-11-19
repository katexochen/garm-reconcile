// Copyright 2026 Paul Meyer
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

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
