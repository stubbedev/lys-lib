{
  perSystem = {
    inputs',
    pkgs,
    ...
  }: let
    go = inputs'.devtools.packages.go;
  in {
    _module.args = {
      inherit go;
      buildGoModule = pkgs.buildGoModule.override {inherit go;};
    };
  };
}
