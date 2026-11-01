---
layout: home

hero:
  name: lys-lib
  text: Building blocks in pure Go
  tagline: What lys-ui draws with and what lys-shell talks to the system through. Rendering, Vulkan, Wayland capture, media muxing, PipeWire and D-Bus, with CGO_ENABLED=0.
  image:
    src: /lys-lib.svg
    alt: lys-lib
  actions:
    - theme: brand
      text: Package map
      link: /overview
    - theme: alt
      text: Patch policy
      link: /third-party/
    - theme: alt
      text: GitHub
      link: https://github.com/stubbedev/lys-lib

features:
  - title: Rendering and GPU
    details: A 2D canvas with CPU and Vulkan renderers, frames shared as dmabufs, and DRM ioctls without libdrm.
    link: /rendering
  - title: Wayland
    details: Screen and window capture, virtual input, and generated bindings for protocols beyond the core and xdg-shell.
    link: /wayland
  - title: Media
    details: H.264 and H.265 hardware encoding, MP4 and Matroska muxing, a pure-Go Opus encoder, PCM mixing and V4L2 webcams.
    link: /media
  - title: System services
    details: A PipeWire video producer without libpipewire, an EIS server, and D-Bus services on a patched godbus.
    link: /system
---
