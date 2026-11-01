import { lysConfig } from 'lys-docs-theme/config'

export default lysConfig({
  name: 'lys-lib',
  description: "The lys family's building blocks that are not widgets, in pure Go.",
  nav: [
    { text: 'Overview', link: '/overview' },
    { text: 'Third party', link: '/third-party/' },
  ],
  sidebar: [
    {
      text: 'Guide',
      items: [
        { text: 'Overview and package map', link: '/overview' },
        { text: 'Development', link: '/development' },
      ],
    },
    {
      text: 'Packages',
      items: [
        { text: 'Rendering and GPU', link: '/rendering' },
        { text: 'Wayland', link: '/wayland' },
        { text: 'Media', link: '/media' },
        { text: 'System services', link: '/system' },
        { text: 'xkb', link: '/xkb' },
      ],
    },
    {
      text: 'Third party',
      items: [
        { text: 'Patch policy', link: '/third-party/' },
        { text: 'godbus', link: '/third-party/godbus' },
        { text: 'gomath', link: '/third-party/gomath' },
        { text: 'oksvg', link: '/third-party/oksvg' },
        { text: 'rasterx', link: '/third-party/rasterx' },
        { text: 'typesetting', link: '/third-party/typesetting' },
        { text: 'vector', link: '/third-party/vector' },
        { text: 'vk', link: '/third-party/vk' },
        { text: 'wayland', link: '/third-party/wayland' },
      ],
    },
  ],
})
