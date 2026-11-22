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

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

// Profile is a controller garm-cli is logged in to.
type Profile struct {
	Name    string `toml:"name"`
	BaseURL string `toml:"base_url"`
	Token   string `toml:"bearer_token"`
}

// LoadProfile reads a profile of garm-cli from the configuration in the
// home directory of the user. Without a name, it is the active profile.
func LoadProfile(name string) (Profile, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return Profile{}, err
	}
	path := filepath.Join(home, ".local", "share", "garm-cli", "config.toml")
	var cfg struct {
		Profiles []Profile `toml:"manager"`
		Active   string    `toml:"active_manager"`
	}
	if _, err := toml.DecodeFile(path, &cfg); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Profile{}, fmt.Errorf("garm-cli has no profiles: %w", err)
		}
		return Profile{}, fmt.Errorf("%s: %w", path, err)
	}
	if name == "" {
		name = cfg.Active
	}
	for _, profile := range cfg.Profiles {
		if profile.Name == name {
			return profile, nil
		}
	}
	if name == "" {
		return Profile{}, fmt.Errorf("%s: garm-cli has no active profile", path)
	}
	return Profile{}, fmt.Errorf("%s: garm-cli has no profile %s", path, name)
}
