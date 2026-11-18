{
  description = "Declarative configuration for garm";

  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";

  outputs =
    { self, nixpkgs }:
    let
      forAllSystems =
        f:
        nixpkgs.lib.genAttrs [
          "x86_64-linux"
          "aarch64-linux"
        ] (system: f nixpkgs.legacyPackages.${system});
    in
    {
      packages = forAllSystems (pkgs: {
        default = pkgs.callPackage ./package.nix { };
      });

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
        }
      );

      devShells = forAllSystems (pkgs: {
        default = pkgs.mkShell {
          inputsFrom = [ self.packages.${pkgs.stdenv.hostPlatform.system}.default ];
        };
      });
    };
}
