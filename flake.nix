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
    in
    {
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
