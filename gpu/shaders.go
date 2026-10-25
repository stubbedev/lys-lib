package gpu

import _ "embed" // the compiled shaders below

//go:generate glslangValidator -V --quiet -o shaders/prim.vert.spv shaders/prim.vert
//go:generate glslangValidator -V --quiet -o shaders/prim.frag.spv shaders/prim.frag

//go:embed shaders/prim.vert.spv
var primVert []byte

//go:embed shaders/prim.frag.spv
var primFrag []byte
