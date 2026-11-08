---
layout: home

hero:
  name: lys-lib
  text: Building blocks in pure Go
  tagline: Rendering, Vulkan, Wayland capture, media, PipeWire, EIS and D-Bus with CGO_ENABLED=0.
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
    details: 2D canvas with CPU and Vulkan renderers, dmabufs, DRM ioctls.
    link: /rendering
  - title: Wayland
    details: Screen and window capture, virtual input, protocol bindings.
    link: /wayland
  - title: Media
    details: Hardware H.264/H.265, MP4 and Matroska, Opus, Vorbis, PCM mixing, V4L2.
    link: /media
  - title: System services
    details: PipeWire video producer, EIS server, D-Bus, PAM.
    link: /system
---
