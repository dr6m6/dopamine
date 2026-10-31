{
  description = "dopamine: minimal Codex workspaces and project servers";
  inputs.nixpkgs.url = "github:NixOS/nixpkgs/21ea275a7c46aef9d4d6ddc962e6d562e9d94183";
  outputs = { self, nixpkgs }: let
    system = "x86_64-linux";
    pkgs = import nixpkgs { inherit system; };
    hub = pkgs.buildGoModule {
      pname = "dopamine";
      version = "0.1.0";
      src = pkgs.lib.cleanSourceWith {
        src = ./.;
        filter = path: type: pkgs.lib.cleanSourceFilter path type
          && !(builtins.elem (builtins.baseNameOf path) [ "bin" "result" "node_modules" "__pycache__" ])
          && !(pkgs.lib.hasPrefix "result-" (builtins.baseNameOf path));
      };
      vendorHash = "sha256-N1XjCz+5I3ge+QUsOGqFbj1s9lw1gpgcgtBmSDg//oc=";
      subPackages = [ "cmd/dopamine" ];
      ldflags = [ "-s" "-w" "-X main.Version=0.1.0" ];
      nativeBuildInputs = [ pkgs.makeWrapper ];
      nativeCheckInputs = [ pkgs.git pkgs.tmux pkgs.python3 ];
      # Package checks are deterministic. The devShell also runs the
      # isolated tmux interaction scenario without model API calls.
      checkPhase = ''
        go test ./...
        python3 -m unittest discover -s scripts -p 'test_project_hub_source.py'
        python3 -m unittest discover -s scripts -p 'test_dopamine_install.py'
      '';
      postInstall = ''
        wrapProgram "$out/bin/dopamine" --set PROJECT_HUB_EXECUTABLE "$out/bin/dopamine" --prefix PATH : ${pkgs.lib.makeBinPath [ pkgs.tmux pkgs.git pkgs.xdg-utils pkgs.libnotify pkgs.python3 pkgs.nodejs pkgs.coreutils pkgs.bash ]}
        ln -s dopamine "$out/bin/project-hub"
        install -Dm644 LICENSE "$out/share/licenses/dopamine/LICENSE"
        install -Dm644 internal/projecthub/assets/dopamine.svg "$out/share/icons/hicolor/scalable/apps/dopamine.svg"
      '';
      meta = { license = pkgs.lib.licenses.mit; platforms = [ system ]; mainProgram = "dopamine"; };
    };
  in {
    packages.${system} = { default = hub; dopamine = hub; project-hub = hub; };
    apps.${system}.default = { type = "app"; program = "${hub}/bin/dopamine"; meta.description = "dopamine terminal workspaces"; };
    devShells.${system}.default = pkgs.mkShell { packages = [ pkgs.go_1_26 pkgs.tmux pkgs.git pkgs.libnotify pkgs.nodejs pkgs.python3 pkgs.gnumake ]; };
    checks.${system}.package = hub;
    nixosModules.default = { config, lib, pkgs, ... }: let cfg = config.programs.dopamine; in {
      imports = [ (lib.mkAliasOptionModule [ "programs" "project-hub" "enable" ] [ "programs" "dopamine" "enable" ]) ];
      options.programs.dopamine.enable = lib.mkEnableOption "dopamine (uses existing codex-vpn)";
      config = lib.mkIf cfg.enable { environment.systemPackages = [ self.packages.${pkgs.stdenv.hostPlatform.system}.default ]; };
    };
  };
}
