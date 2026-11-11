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
          goreleaser # release builds: `goreleaser check`, `goreleaser build --snapshot`
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

      # Only what the Go build and tests read, so doc and script edits never
      # rebuild it. Tests load the sandbox manifests as fixtures.
      src =
        pkgs:
        pkgs.lib.fileset.toSource {
          root = ./.;
          fileset = pkgs.lib.fileset.unions [
            ./go.mod
            ./go.sum
            ./cmd
            ./internal
            ./scripts/sandbox/manifests
            ./docs/schema # TestCommittedSchemasAreCurrent compares against them
          ];
        };

      ade =
        pkgs:
        pkgs.buildGoModule {
          pname = "ade";
          version = self.shortRev or "dev";
          src = src pkgs;
          vendorHash = "sha256-7okVyePN9UDZhe3652hgwILSCpjbs05s7F/nYoP8xsE=";
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

      # Neovim for ADE (PLAN 6.7): NVIM_APPNAME=ade-nvim keeps it off any existing
      # config, its init comes from editors/ade-nvim, and every plugin is pinned
      # from nixpkgs (no runtime downloads). `ade` is on its PATH for :AdeItems.
      adeNvim =
        pkgs:
        let
          plugin = pkgs.vimUtils.buildVimPlugin {
            pname = "ade-nvim";
            version = "0";
            src = ./editors/ade-nvim;
            doCheck = false;
          };
          neovim = pkgs.neovim.override {
            configure = {
              customRC = "luafile ${./editors/ade-nvim/init.lua}";
              packages.ade.start = [
                plugin
                pkgs.vimPlugins.mini-nvim
                pkgs.vimPlugins.nvim-treesitter.withAllGrammars
                pkgs.vimPlugins.nvim-lspconfig
              ];
            };
          };
        in
        pkgs.writeShellApplication {
          name = "ade-nvim";
          runtimeInputs = [
            (ade pkgs)
            pkgs.git
          ];
          text = ''
            export NVIM_APPNAME=ade-nvim
            exec ${neovim}/bin/nvim "$@"
          '';
        };
    in
    {
      packages = forAllSystems (pkgs: {
        default = ade pkgs;
        ade-nvim = adeNvim pkgs;
      });

      apps = forAllSystems (pkgs: {
        default = {
          type = "app";
          program = pkgs.lib.getExe (ade pkgs);
          meta.description = "Run ADE";
        };
        ade-nvim = {
          type = "app";
          program = pkgs.lib.getExe (adeNvim pkgs);
          meta.description = "Neovim with ADE's plugin and keymap (NVIM_APPNAME=ade-nvim)";
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
        # scripts/gateway/ starts it (Prisma needs engine binaries it fetches).
        gateway = pkgs.mkShell (
          goEnv
          // {
            packages =
              runtimeTools pkgs
              ++ devTools pkgs
              ++ [
                pkgs.litellm
                pkgs.postgresql
                pkgs.openssl # LiteLLM's Prisma client probes for it at start-up
                pkgs.curl # scripts/gateway
              ];
          }
        );
      });

      formatter = forAllSystems (pkgs: pkgs.nixfmt);
    };
}
