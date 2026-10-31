package gpu

import _ "embed" // the compiled shaders below

//go:generate glslangValidator -V --quiet -o shaders/prim.vert.spv shaders/prim.vert
//go:generate glslangValidator -V --quiet -o shaders/prim.frag.spv shaders/prim.frag
//go:generate glslangValidator -V --quiet -o shaders/nv12.comp.spv shaders/nv12.comp
//go:generate glslangValidator -V --quiet -o shaders/comp.vert.spv shaders/comp.vert
//go:generate glslangValidator -V --quiet -o shaders/comp.frag.spv shaders/comp.frag
//go:generate glslangValidator -V --quiet -o shaders/fill.frag.spv shaders/fill.frag
//go:generate glslangValidator -V --quiet -o shaders/grad.frag.spv shaders/grad.frag
//go:generate glslangValidator -V --quiet -o shaders/kawase.frag.spv shaders/kawase.frag
//go:generate glslangValidator -V --quiet -o shaders/goo.frag.spv shaders/goo.frag

//go:embed shaders/prim.vert.spv
var primVert []byte

//go:embed shaders/prim.frag.spv
var primFrag []byte

//go:embed shaders/nv12.comp.spv
var nv12Comp []byte

//go:embed shaders/comp.vert.spv
var compVert []byte

//go:embed shaders/comp.frag.spv
var compFrag []byte

//go:embed shaders/fill.frag.spv
var fillFrag []byte

//go:embed shaders/grad.frag.spv
var gradFrag []byte

//go:embed shaders/kawase.frag.spv
var kawaseFrag []byte

//go:embed shaders/goo.frag.spv
var gooFrag []byte
