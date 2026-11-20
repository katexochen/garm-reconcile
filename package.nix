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
      # Its example is checked by a test.
      ./README.md
      (lib.fileset.fileFilter (file: file.hasExt "go") ./.)
    ];
  };

  vendorHash = "sha256-ytECrYdCfDBXAs8726ITpWWYGJluJrh8irmOoUOGchM=";

  # The tests reconcile a garm controller.
  nativeCheckInputs = [ garm ];

  ldflags = [ "-s" ];

  meta = {
    description = "Declarative configuration for garm";
    license = lib.licenses.asl20;
    mainProgram = "garm-reconcile";
    platforms = lib.platforms.linux;
  };
}
