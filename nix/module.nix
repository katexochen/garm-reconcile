{
  config,
  lib,
  pkgs,
  utils,
  ...
}:

let
  cfg = config.services.garm.provision;

  format = pkgs.formats.json { };

  isSecret = v: lib.isAttrs v && lib.attrNames v == [ "_secret" ] && lib.isString v._secret;

  # Applies f to the path of every { _secret = "/path"; } in v.
  mapSecrets =
    f: v:
    if isSecret v then
      f v._secret
    else if lib.isAttrs v then
      lib.mapAttrs (_: mapSecrets f) v
    else if lib.isList v then
      map (mapSecrets f) v
    else
      v;

  collectSecrets =
    v:
    if isSecret v then
      [ v._secret ]
    else if lib.isAttrs v then
      lib.concatMap collectSecrets (lib.attrValues v)
    else if lib.isList v then
      lib.concatMap collectSecrets v
    else
      [ ];

  secretPaths = lib.unique (collectSecrets cfg.settings);

  # The hash keeps files of the same name in different directories apart.
  credentialName =
    path: "${lib.substring 0 8 (builtins.hashString "sha256" path)}-${baseNameOf path}";

  # Secrets are passed as systemd credentials, which garm-reconcile finds by
  # their name. This way the files only need to be readable by root.
  stateFile = format.generate "garm-state.json" (
    mapSecrets (path: { _secret = credentialName path; }) cfg.settings
  );

  checkedStateFile = pkgs.runCommand "garm-state.json" { nativeBuildInputs = [ cfg.package ]; } ''
    garm-reconcile validate ${stateFile}
    cp ${stateFile} $out
  '';
in

{
  options.services.garm.provision = {
    enable = lib.mkEnableOption "declarative configuration of GARM with garm-reconcile";

    package = lib.mkOption {
      type = lib.types.package;
      description = "The garm-reconcile package to use.";
    };

    url = lib.mkOption {
      type = lib.types.str;
      default = "http://127.0.0.1:${toString config.services.garm.settings.apiserver.port}";
      defaultText = lib.literalExpression ''"http://127.0.0.1:''${toString config.services.garm.settings.apiserver.port}"'';
      description = "Base URL of the GARM API.";
    };

    prune = lib.mkOption {
      type = lib.types.bool;
      default = false;
      description = ''
        Whether to delete what is not declared in
        {option}`services.garm.provision.settings`. Pools and scale sets are
        deleted once the jobs on their runners finished.
      '';
    };

    drainTimeout = lib.mkOption {
      type = lib.types.nullOr lib.types.str;
      default = null;
      example = "6h";
      description = ''
        How long deleting pools and scale sets waits for the jobs on their
        runners, as a Go duration. The service fails when the time is up and
        continues when it is started again. By default it waits until the
        jobs finished.
      '';
    };

    settings = lib.mkOption {
      type = format.type;
      description = ''
        The desired state of GARM, see
        <https://github.com/katexochen/garm-reconcile>. It is checked when
        the system is built.

        Any string can be given as `{ _secret = "/path/to/file"; }`, and is
        then read from that file when the state is applied.
      '';
    };
  };

  config = lib.mkIf cfg.enable {
    systemd.services.garm-provision = {
      description = "Declarative configuration of GARM";
      wantedBy = [ "multi-user.target" ];
      requires = [ "garm.service" ];
      after = [ "garm.service" ];
      serviceConfig = {
        # The service is started once everything declared is in place.
        # Deleting the rest waits for running jobs and continues in the
        # background.
        Type = "notify";
        RemainAfterExit = true;
        ExecStart = utils.escapeSystemdExecArgs (
          [
            (lib.getExe cfg.package)
            "apply"
            "-url"
            cfg.url
          ]
          ++ lib.optional cfg.prune "-prune"
          ++ lib.optionals (cfg.drainTimeout != null) [
            "-drain-timeout"
            cfg.drainTimeout
          ]
          ++ [ "${checkedStateFile}" ]
        );
        LoadCredential = map (path: "${credentialName path}:${path}") secretPaths;
        DynamicUser = true;
        Restart = "on-failure";
        RestartSec = "10s";
      };
    };
  };
}
