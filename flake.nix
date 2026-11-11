{
  description = "ADE: agentic development environment (Go + Bubble Tea + zellij)";

  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";

  outputs =
    { self, nixpkgs }:
    let
      systems = [
        "x86_64-linux"
        "aarch64-linux"
        "x86_64-darwin"
        "aarch64-darwin"
      ];
      forAllSystems = f: nixpkgs.lib.genAttrs systems (system: f nixpkgs.legacyPackages.${system});

      # Tools ADE drives at runtime. The dashboard discovers them on PATH,
      # so these also belong in any environment image that runs ADE.
      runtimeTools =
        pkgs: with pkgs; [
          git
          zellij
          lazygit
          delta
          neovim
          glab
          gh
        ];

      # Toolchain for working on ADE itself.
      devTools =
        pkgs: with pkgs; [
          go
          gopls
          gotools
          golangci-lint
          gofumpt
          delve
          sqlite # inspect the local cache (ADE itself uses pure-Go SQLite)
          vhs # scripted TUI screenshots for visual review
          jq
          ripgrep
          fd
          nixfmt
        ];

      goEnv = {
        CGO_ENABLED = "0"; # single static binary
        GOTOOLCHAIN = "local"; # never download a toolchain behind Nix's back
      };

      # Only what the Go build reads, so doc and script edits never rebuild it.
      src =
        pkgs:
        pkgs.lib.fileset.toSource {
          root = ./.;
          fileset = pkgs.lib.fileset.unions [
            ./go.mod
            ./go.sum
            ./cmd
            ./internal
          ];
        };

      ade =
        pkgs:
        pkgs.buildGoModule {
          pname = "ade";
          version = self.shortRev or "dev";
          src = src pkgs;
          vendorHash = "sha256-MUvpFA+53BzhXvl7hfZPTciCeyUE3h+3qMQav7lXkhc=";
          proxyVendor = true; # hash tracks go.mod/go.sum only, not which packages are imported
          subPackages = [ "cmd/ade" ];
          env = goEnv;
          ldflags = [
            "-s"
            "-w"
            "-X github.com/coredev-uk/ade/internal/cli.Version=${self.shortRev or "dev"}"
          ];
          # The package build stays lean; `checks.default` runs the suite.
          doCheck = false;
          meta = {
            description = "Agentic development environment (Go + Bubble Tea + zellij)";
            mainProgram = "ade";
          };
        };
    in
    {
      packages = forAllSystems (pkgs: {
        default = ade pkgs;
      });

      apps = forAllSystems (pkgs: {
        default = {
          type = "app";
          program = pkgs.lib.getExe (ade pkgs);
          meta.description = "Run ADE";
        };
      });

      # Runs the whole offline test suite (`go test ./...`) in the Nix sandbox.
      checks = forAllSystems (pkgs: {
        default = (ade pkgs).overrideAttrs (old: {
          pname = "ade-tests";
          doCheck = true;
          nativeCheckInputs = [ pkgs.git ];
          checkPhase = ''
            runHook preCheck
            export HOME=$TMPDIR
            go test ./...
            runHook postCheck
          '';
        });
      });

      devShells = forAllSystems (pkgs: {
        default = pkgs.mkShell (
          goEnv
          // {
            packages = runtimeTools pkgs ++ devTools pkgs;
          }
        );

        # Adds a local LiteLLM proxy (+ Postgres, which LiteLLM budgets need)
        # for exercising the gateway driver, key budgets, and spend reporting.
        gateway = pkgs.mkShell (
          goEnv
          // {
            packages =
              runtimeTools pkgs
              ++ devTools pkgs
              ++ [
                pkgs.litellm
                pkgs.postgresql
              ];
          }
        );
      });

      formatter = forAllSystems (pkgs: pkgs.nixfmt);
    };
}
