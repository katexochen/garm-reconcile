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
