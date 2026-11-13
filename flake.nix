{
  description = "lys-lib - the lys family's non-UI building blocks";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    # A second, newer Mesa that `just gpu-host` also runs against, so a
    # driver release breaks a gate before it reaches a desktop. Locked on
    # its own: `nix flake update nixpkgs-mesa` moves it to nixos-unstable's
    # head without moving the rest of the shell.
    nixpkgs-mesa.url = "github:NixOS/nixpkgs/nixos-unstable";
    lys-ci.url = "github:stubbedev/lys-ci";
    flake-parts = {
      url = "github:hercules-ci/flake-parts";
      inputs.nixpkgs-lib.follows = "nixpkgs";
    };
    devtools = {
      url = "github:stubbedev/flakes.nix";
      inputs.flake-parts.follows = "flake-parts";
    };
  };

  outputs = inputs @ {flake-parts, ...}:
    flake-parts.lib.mkFlake {inherit inputs;} {
      systems = [
        "x86_64-linux"
        "aarch64-linux"
      ];
      imports = [
        ./nix/checks.nix
        ./nix/shell.nix
        ./nix/toolchain.nix
      ];
      perSystem = {pkgs, ...}: {
        formatter = pkgs.alejandra;
      };
      # The GPU driver environment for the lys family's dev shells:
      # gpuDriverEnv pkgs is { libraries; shellHook; driverEnv; }.
      flake.lib.gpuDriverEnv = import ./nix/gpu-drivers.nix;
    };
}
