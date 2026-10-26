package gpu

import _ "embed" // the compiled shaders below

//go:generate glslangValidator -V --quiet -o shaders/prim.vert.spv shaders/prim.vert
//go:generate glslangValidator -V --quiet -o shaders/prim.frag.spv shaders/prim.frag
//go:generate glslangValidator -V --quiet -o shaders/nv12.comp.spv shaders/nv12.comp

//go:embed shaders/prim.vert.spv
var primVert []byte

//go:embed shaders/prim.frag.spv
var primFrag []byte

//go:embed shaders/nv12.comp.spv
var nv12Comp []byte
