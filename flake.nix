# Copyright 2026 Paul Meyer
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

{
  description = "Declarative configuration for garm";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    treefmt-nix = {
      url = "github:numtide/treefmt-nix";
      inputs.nixpkgs.follows = "nixpkgs";
    };
  };

  outputs =
    {
      self,
      nixpkgs,
      treefmt-nix,
    }:
    let
      forAllSystems =
        f:
        nixpkgs.lib.genAttrs [
          "x86_64-linux"
          "aarch64-linux"
        ] (system: f nixpkgs.legacyPackages.${system});

      treefmt =
        pkgs:
        treefmt-nix.lib.evalModule pkgs {
          projectRootFile = "flake.nix";
          programs.nixfmt.enable = true;
          programs.gofmt.enable = true;
          programs.mdformat = {
            enable = true;
            settings.number = true;
          };
          programs.yamlfmt = {
            enable = true;
            settings.formatter.retain_line_breaks_single = true;
          };
        };
    in
    {
      packages = forAllSystems (pkgs: {
        default = pkgs.callPackage ./package.nix { };
      });

      formatter = forAllSystems (pkgs: (treefmt pkgs).config.build.wrapper);

      nixosModules.default =
        { lib, pkgs, ... }:
        {
          imports = [ ./nix/module.nix ];
          services.garm.provision.package =
            lib.mkDefault
              self.packages.${pkgs.stdenv.hostPlatform.system}.default;
        };

      checks = forAllSystems (
        pkgs:
        {
          package = self.packages.${pkgs.stdenv.hostPlatform.system}.default;
          formatting = (treefmt pkgs).config.build.check self;
        }
        // nixpkgs.lib.optionalAttrs pkgs.stdenv.hostPlatform.isx86_64 {
          vm-test = pkgs.testers.runNixOSTest {
            imports = [ ./nix/test.nix ];
            _module.args.garm-reconcile = self.packages.${pkgs.stdenv.hostPlatform.system}.default;
          };
          module-test = pkgs.testers.runNixOSTest {
            imports = [ ./nix/module-test.nix ];
            _module.args.module = self.nixosModules.default;
          };
          # A desired state that is not valid fails the build of the system.
          module-validation =
            let
              system = nixpkgs.lib.nixosSystem {
                inherit (pkgs.stdenv.hostPlatform) system;
                modules = [
                  self.nixosModules.default
                  {
                    services.garm.provision = {
                      enable = true;
                      settings.controller.webhook_url = "not a URL";
                    };
                  }
                ];
              };
              failed = pkgs.testers.testBuildFailure system.config.services.garm.provision.stateFile;
            in
            pkgs.runCommand "garm-reconcile-module-validation" { } ''
              grep -F "controller.webhook_url" ${failed}/testBuildFailure.log
              touch $out
            '';
        }
      );

      devShells = forAllSystems (pkgs: {
        default = pkgs.mkShell {
          inputsFrom = [ self.packages.${pkgs.stdenv.hostPlatform.system}.default ];
        };
      });
    };
}
