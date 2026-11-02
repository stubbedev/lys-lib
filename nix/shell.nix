{inputs, ...}: {
  perSystem = {
    buildGoModule,
    go,
    lib,
    pkgs,
    ...
  }: let
    # Rebuild a nixpkgs Go tool against the pinned toolchain: every
    # buildGo*Module argument its override takes (gopls asks for
    # buildGoLatestModule, the rest for buildGoModule) gets ours, so
    # gopls, delve and the editor tools understand go 1.27 sources.
    withPinnedGo = pkg:
      pkg.override (lib.mapAttrs (_: _: buildGoModule)
        (lib.filterAttrs (name: _: lib.match "buildGo.*Module" name != null)
          (lib.functionArgs pkg.override)));
    gpuDrivers = import ./gpu-drivers.nix pkgs;
  in {
    devShells.default = pkgs.mkShellNoCC ({
        packages =
          [
            go
            "${inputs.lys-ci}" # no-fused
            pkgs.git
            pkgs.gh

            # Development tools
            pkgs.golangci-lint # `just lint`, and `just fmt` through its gofumpt + gci formatters; config in .golangci.yml
            pkgs.just # task runner
          ]
          # Headless test compositor: sway on WLR_BACKENDS=headless gives
          # the capture and virtual input tests a private Wayland session
          # without touching the developer's real desktop.
          ++ [
            pkgs.sway
            # pwtest's private PipeWire daemon, pw-link, and libpipewire,
            # the reference consumer the producer in pipewire is checked
            # against.
            pkgs.pipewire
            # libei, the C client the EIS server tests drive.
            pkgs.libei
            # libxkbcommon, the reference the xkb package's differential
            # tests and keysym table generator hold it to.
            pkgs.libxkbcommon
            # A private X server, the keymap compiler that uploads to it and
            # libxcb, for the X11 keymap import tests against
            # libxkbcommon-x11.
            pkgs.xvfb
            pkgs.xkbcomp
            pkgs.setxkbmap
          ]
          ++ [
            # The test compositor's private session bus.
            pkgs.dbus
            # `just svg-refs` rasterizes the SVG references render.LoadSVG
            # is compared against with rsvg-convert.
            pkgs.librsvg
            # `just shaders` compiles the GPU renderer's GLSL to the
            # SPIR-V package gpu embeds.
            pkgs.glslang
            # The nal and gpu encoder tests read the H.264 and H.265
            # headers they write back through ffmpeg's trace_headers.
            pkgs.ffmpeg-headless
          ]
          # Editor tooling (gopls, delve, and the tools vscode-go and
          # vim-go install), built against the pinned Go: vscode-go
          # expects every tool compiled with the Go it drives.
          ++ map withPinnedGo [
            pkgs.gopls
            pkgs.delve
            pkgs.gotools
            pkgs.gomodifytags
            pkgs.impl
            pkgs.go-tools
            pkgs.gotests
            pkgs.iferr
          ];

        # lys-lib is pure Go by design; keep accidental cgo out.
        CGO_ENABLED = "0";

        # nixpkgs' go defaults to GOTOOLCHAIN=auto, which would fetch
        # whatever toolchain a go.mod line asks for; the shell holds
        # to the pinned one.
        GOTOOLCHAIN = "local";

        # `just race` runs the suite under the race detector, which needs
        # cgo: only that recipe points CC here and turns cgo on, so the
        # shell keeps no C compiler on PATH.
        LYS_RACE_CC = "${pkgs.gcc}/bin/gcc";

        # third_party/typesetting's heap-budget and lazy-table tests
        # parse a real font of thousands of glyphs.
        LYS_TEST_DEJAVU_SANS = "${pkgs.dejavu_fonts.minimal}/share/fonts/truetype/DejaVuSans.ttf";
      }
      // {
        # The GPU renderer (package gpu) loads libvulkan at runtime, and
        # its VA-API encoder libva; both find the host's drivers
        # (/run/opengl-driver on NixOS). The pipewire and eis tests load
        # libpipewire and libei, the reference clients they are held to.
        LD_LIBRARY_PATH = lib.makeLibraryPath (gpuDrivers.libraries
          ++ [
            pkgs.pipewire
            pkgs.libei
            pkgs.libxkbcommon
            pkgs.libxcb
          ]);
        # The xkb package's tests: the data libxkbcommon and the package
        # both compile from, and the header its keysym tables come from.
        XKB_CONFIG_ROOT = "${pkgs.xkeyboard-config}/share/X11/xkb";
        XLOCALEDIR = "${pkgs.libX11}/share/X11/locale";
        LYS_XKB_KEYSYMS_H = "${pkgs.libxkbcommon.dev}/include/xkbcommon/xkbcommon-keysyms.h";
        # Mesa's lavapipe, a CPU Vulkan driver: the GPU tests force it
        # (VK_DRIVER_FILES) to run without a GPU, as CI does.
        LYS_TEST_LAVAPIPE = "${pkgs.mesa}/share/vulkan/icd.d/lvp_icd.${pkgs.stdenv.hostPlatform.parsed.cpu.name}.json";
        # The Khronos validation layer: the GPU tests run under it, and
        # any error it finds stops the test.
        LYS_TEST_VULKAN_LAYERS = "${pkgs.vulkan-validation-layers}/share/vulkan/explicit_layer.d";
        # Off NixOS the shell points the Vulkan loader, EGL, GBM and VA
        # at nixpkgs' drivers for the GPUs present (nix/gpu-drivers.nix,
        # which the lys family's other shells take from this flake).
        inherit (gpuDrivers) shellHook;
      });
  };
}
