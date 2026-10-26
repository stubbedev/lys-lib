{
  perSystem = { pkgs, ... }:
    let
      # The dev shell's Go toolchain. go.mod declares `go 1.27.1` and the
      # shell holds GOTOOLCHAIN=local, so the version is load-bearing:
      # nixpkgs' default `go` (1.26) cannot compile this module, so the
      # pin is go_1_27 - no toolchain is ever downloaded at dev time.
      go = pkgs.go_1_27;
    in
    {
      _module.args = {
        inherit go;
        buildGoModule = pkgs.buildGoModule.override { inherit go; };
      };
    };
}
