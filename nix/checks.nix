{
  perSystem = {
    buildGoModule,
    lib,
    pkgs,
    ...
  }: {
    checks = lib.optionalAttrs pkgs.stdenv.hostPlatform.isLinux {
      # The KMS tests on a real card: a NixOS VM whose only DRM device is
      # vkms runs the drm package's tests as root, so master handling and
      # CLOSEFB are exercised; on a host without a card they skip. vkms has
      # no render node, so the timeline syncobj test is left out.
      vm-kms = let
        drmTests = buildGoModule {
          pname = "lys-lib-drm-tests";
          version = "0";
          src = lib.fileset.toSource {
            root = ../.;
            fileset = lib.fileset.difference (lib.fileset.gitTracked ../.) (
              lib.fileset.unions [../flake.nix ../flake.lock ../nix]
            );
          };
          # Pins the module dependencies in go.sum. When they change, set this
          # to lib.fakeHash, build the check and put the reported got: hash here.
          vendorHash = "sha256-CPqUW2QfU4dYI+zN/oTtWbF5rOanRDLJqnOcze34hm8=";
          buildPhase = ''
            runHook preBuild
            go test -c -o drm.test ./drm
            runHook postBuild
          '';
          doCheck = false;
          installPhase = ''
            runHook preInstall
            install -Dm755 drm.test $out/bin/drm.test
            runHook postInstall
          '';
        };
      in
        pkgs.testers.runNixOSTest {
          name = "lys-lib-kms";
          nodes.machine = {
            boot.kernelModules = ["vkms"];
            virtualisation.graphics = false;
            virtualisation.qemu.options = ["-vga none"];
          };
          testScript = ''
            machine.wait_for_unit("multi-user.target")
            machine.succeed("udevadm settle", "test \"$(readlink -f /sys/class/drm/card0/device)\" = /sys/devices/faux/vkms", "test ! -e /sys/class/drm/card1")
            out = machine.succeed("${drmTests}/bin/drm.test -test.v -test.count=1 -test.skip TestTimelinePointsSignalEventFDs 2>&1")
            print(out)
            assert "--- SKIP" not in out, "a KMS test skipped on the vkms card"
          '';
        };
    };
  };
}
