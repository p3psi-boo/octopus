{
  description = "Octopus - AI Chat Hub Development Environment";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    flake-parts.url = "github:hercules-ci/flake-parts";
  };

  outputs = inputs@{ flake-parts, nixpkgs, ... }:
    flake-parts.lib.mkFlake { inherit inputs; } {
      systems = [ "x86_64-linux" "aarch64-linux" "x86_64-darwin" "aarch64-darwin" ];

      perSystem = { config, pkgs, system, ... }:
        let
          goVersion = pkgs.go_1_26;
        in
        {
          devShells.default = pkgs.mkShell {
            name = "octopus-dev";

            packages = with pkgs; [
              # Go toolchain
              goVersion
              gopls    # LSP
              gofumpt  # Stricter gofmt
              golines  # Long line fixer
              goimports-reviser        # Organize imports

              # Development tools
              air                      # Hot reload
              golangci-lint            # Linter
              go-task                  # Task runner

              # Git and misc
              git
              gnumake
              sqlite                 # For local SQLite development
            ];

            shellHook = ''
              echo "🐙 Octopus DevShell"
              echo "Go version: $(go version)"
              echo ""
              echo "Available commands:"
              echo "  go build        - Build the binary"
              echo "  go test ./...   - Run tests"
              echo "  air             - Start hot-reload dev server"
              echo "  golangci-lint   - Run linters"
              echo "  task            - Run task commands (see Taskfile.yml)"
            '';

            # Set up Go environment
            GOROOT = "${goVersion}";
            GOPATH = "${builtins.getEnv "HOME"}/go";
          };
        };
    };
}
