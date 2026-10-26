{
  description = "lys-lib - the lys family's non-UI building blocks";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    flake-parts = {
      url = "github:hercules-ci/flake-parts";
      inputs.nixpkgs-lib.follows = "nixpkgs";
    };
  };

  outputs = inputs @ {flake-parts, ...}:
    flake-parts.lib.mkFlake {inherit inputs;} {
      systems = [
        "x86_64-linux"
        "aarch64-linux"
        "aarch64-darwin"
      ];
      imports = [
        ./nix/shell.nix
        ./nix/toolchain.nix
      ];
      perSystem = {pkgs, ...}: {
        formatter = pkgs.alejandra;
      };
      # The GPU driver environment for the lys family's dev shells:
      # gpuDriverEnv pkgs is { libraries; shellHook; }.
      flake.lib.gpuDriverEnv = import ./nix/gpu-drivers.nix;
    };
}
