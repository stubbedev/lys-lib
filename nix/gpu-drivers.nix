# What a dev shell needs to run package gpu (rendering, dmabuf export,
# Vulkan video and VA-API encode) on the host's GPU, for the lys
# family's shells (this flake's lib.gpuDriverEnv):
#
# - libraries: the loaders the package dlopens, for LD_LIBRARY_PATH.
# - shellHook: off NixOS there are no drivers in /run/opengl-driver,
#   and the host's own Mesa cannot load into processes linked against
#   nixpkgs' glibc: the Vulkan loader, sway's EGL and GBM, and libva
#   would find no driver. There it points them at nixpkgs' Mesa (and
#   Intel's VA-API driver): lavapipe, and the driver for each GPU the
#   kernel drives (AMD, Intel, nouveau, virtio and the SoC GPUs);
#   NVIDIA's proprietary driver needs nixGL. Only those: an ICD for
#   absent hardware can fault while it probes (CI's runners crashed so
#   with all of them). NixOS keeps its own.
pkgs: {
  libraries = [
    pkgs.vulkan-loader
    pkgs.libva
  ];
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
}
