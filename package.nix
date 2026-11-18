{
  lib,
  buildGoModule,
  garm,
}:

buildGoModule {
  pname = "garm-reconcile";
  version = "0.1.0-dev";

  src = lib.fileset.toSource {
    root = ./.;
    fileset = lib.fileset.unions [
      ./go.mod
      ./go.sum
      (lib.fileset.fileFilter (file: file.hasExt "go") ./.)
    ];
  };

  vendorHash = "sha256-zWLOOlmfVI1tMngRVTR4NEC8UAG5Y8fL87liDCUyKfI=";

  # The tests reconcile a garm controller.
  nativeCheckInputs = [ garm ];

  ldflags = [ "-s" ];

  meta = {
    description = "Declarative configuration for garm";
    mainProgram = "garm-reconcile";
    platforms = lib.platforms.linux;
  };
}
