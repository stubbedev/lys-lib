{
  perSystem = { buildGoModule, go, lib, pkgs, ... }:
    let
      # Rebuild a nixpkgs Go tool against the pinned toolchain: every
      # buildGo*Module argument its override takes (gopls asks for
      # buildGoLatestModule, the rest for buildGoModule) gets ours, so
      # gopls, delve and the editor tools understand go 1.27 sources.
      withPinnedGo = pkg:
        pkg.override (lib.mapAttrs (_: _: buildGoModule)
          (lib.filterAttrs (name: _: lib.match "buildGo.*Module" name != null)
            (lib.functionArgs pkg.override)));
    in
    {
      devShells.default = pkgs.mkShellNoCC ({
        packages = [
          go
          pkgs.git

          # Development tools
          pkgs.golangci-lint # `just lint`, and `just fmt` through its gofumpt + gci formatters; config in .golangci.yml
          pkgs.just # task runner
        ]
        # Headless test compositor: sway on WLR_BACKENDS=headless gives
        # the capture and virtual input tests a private Wayland session
        # without touching the developer's real desktop. Linux only.
        ++ lib.optionals pkgs.stdenv.hostPlatform.isLinux [ pkgs.sway ]
        ++ [
          # The test compositor's private session bus.
          pkgs.dbus
          # `just svg-refs` rasterizes the SVG references render.LoadSVG
          # is compared against with rsvg-convert.
          pkgs.librsvg
          # `just shaders` compiles the GPU renderer's GLSL to the
          # SPIR-V package gpu embeds.
          pkgs.glslang
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
      } // lib.optionalAttrs pkgs.stdenv.hostPlatform.isLinux {
        # The GPU renderer (package gpu) loads libvulkan at runtime, and
        # its VA-API encoder libva; both find the host's drivers
        # (/run/opengl-driver on NixOS).
        LD_LIBRARY_PATH = "${pkgs.vulkan-loader}/lib:${lib.getLib pkgs.libva}/lib";
        # Mesa's lavapipe, a CPU Vulkan driver: the GPU tests force it
        # (VK_DRIVER_FILES) to run without a GPU, as CI does.
        LYS_TEST_LAVAPIPE = "${pkgs.mesa}/share/vulkan/icd.d/lvp_icd.x86_64.json";
        # The Khronos validation layer: the GPU tests run under it, and
        # any error it finds stops the test.
        LYS_TEST_VULKAN_LAYERS = "${pkgs.vulkan-validation-layers}/share/vulkan/explicit_layer.d";
        # Off NixOS there are no drivers in /run/opengl-driver, and the
        # host's own Mesa cannot load into processes linked against
        # nixpkgs' glibc: the Vulkan loader, and sway's EGL and GBM,
        # would find no driver. There the shell points them at nixpkgs'
        # Mesa (and Intel's VA-API driver): lavapipe, and the driver for
        # each GPU the kernel drives
        # (AMD, Intel, nouveau, virtio and the SoC GPUs); NVIDIA's
        # proprietary driver needs nixGL. Only those: an ICD for absent
        # hardware can fault while it probes (CI's runners crashed so
        # with all of them). NixOS keeps its own.
        shellHook = ''
          if ! ls /run/opengl-driver/share/vulkan/icd.d/*.json >/dev/null 2>&1; then
            icds=${pkgs.mesa}/share/vulkan/icd.d
            VK_DRIVER_FILES=$(printf '%s:' "$icds"/lvp_icd.*.json)
            for drv in /sys/class/drm/*/device/driver; do
              case $(basename "$(readlink -f "$drv")") in
                i915 | xe) want="intel intel_hasvk" ;;
                amdgpu | radeon) want=radeon ;;
                nouveau) want=nouveau ;;
                virtio_gpu) want=virtio ;;
                msm) want=freedreno ;;
                panfrost | panthor) want=panfrost ;;
                v3d | vc4) want=broadcom ;;
                asahi) want=asahi ;;
                powervr) want=powervr_mesa ;;
                *) continue ;;
              esac
              for w in $want; do
                for f in "$icds"/"$w"_icd.*.json; do
                  case ":$VK_DRIVER_FILES" in
                    *":$f:"*) ;;
                    *) [ -e "$f" ] && VK_DRIVER_FILES="$f:$VK_DRIVER_FILES" ;;
                  esac
                done
              done
            done
            export VK_DRIVER_FILES="''${VK_DRIVER_FILES%:}"
            export __EGL_VENDOR_LIBRARY_FILENAMES=${pkgs.mesa}/share/glvnd/egl_vendor.d/50_mesa.json
            export GBM_BACKENDS_PATH=${pkgs.mesa}/lib/gbm
            export LIBGL_DRIVERS_PATH=${pkgs.mesa}/lib/dri
            export LIBVA_DRIVERS_PATH=${pkgs.intel-media-driver}/lib/dri:${pkgs.mesa}/lib/dri
          fi
        '';
      });
    };
}
