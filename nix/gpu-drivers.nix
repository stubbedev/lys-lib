# What a dev shell needs to run package gpu (rendering, dmabuf export,
# Vulkan video and VA-API encode) on the host's GPU, for the lys
# family's shells (this flake's lib.gpuDriverEnv):
#
# - libraries: the loaders the package dlopens, for LD_LIBRARY_PATH.
# - driverEnv: lys-gpu-driver-env MESA, the exports for one Mesa's
#   drivers, which the shellHook and `just gpu-host` share.
# - shellHook: off NixOS there are no drivers in /run/opengl-driver,
#   and the host's own Mesa cannot load into processes linked against
#   nixpkgs' glibc: the Vulkan loader, sway's EGL and GBM, and libva
#   would find no driver. There it points them at nixpkgs' Mesa (and
#   Intel's VA-API driver): lavapipe, and the driver for each GPU the
#   kernel drives (AMD, Intel, nouveau, virtio and the SoC GPUs);
#   NVIDIA's proprietary driver needs nixGL. Only those: an ICD for
#   absent hardware can fault while it probes (CI's runners crashed so
#   with all of them). NixOS keeps its own.
pkgs: let
  # driverEnv MESA prints the exports that point the Vulkan loader, EGL,
  # GBM and VA at MESA's drivers for the GPUs present: the shellHook
  # below with nixpkgs' Mesa, and `just gpu-host` with each Mesa it runs
  # against.
  driverEnv = pkgs.writeShellScriptBin "lys-gpu-driver-env" ''
    set -euo pipefail
    mesa=''${1:?usage: lys-gpu-driver-env MESA}
    icds=$mesa/share/vulkan/icd.d
    files=$(printf '%s:' "$icds"/lvp_icd.*.json)
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
          case ":$files" in
            *":$f:"*) ;;
            *) [ -e "$f" ] && files="$f:$files" ;;
          esac
        done
      done
    done
    printf 'export VK_DRIVER_FILES=%q\n' "''${files%:}"
    printf 'export __EGL_VENDOR_LIBRARY_FILENAMES=%q\n' "$mesa/share/glvnd/egl_vendor.d/50_mesa.json"
    printf 'export GBM_BACKENDS_PATH=%q\n' "$mesa/lib/gbm"
    printf 'export LIBGL_DRIVERS_PATH=%q\n' "$mesa/lib/dri"
    printf 'export LIBVA_DRIVERS_PATH=%q\n' "${pkgs.lib.makeSearchPath "lib/dri" vaDrivers}:$mesa/lib/dri"
  '';
  vaDrivers = pkgs.lib.optional pkgs.stdenv.hostPlatform.isx86 pkgs.intel-media-driver;
in {
  inherit driverEnv;
  libraries = [
    pkgs.vulkan-loader
    pkgs.libva
  ];
  shellHook = ''
    if ! ls /run/opengl-driver/share/vulkan/icd.d/*.json >/dev/null 2>&1; then
      eval "$(${driverEnv}/bin/lys-gpu-driver-env ${pkgs.mesa})"
    fi
  '';
}
