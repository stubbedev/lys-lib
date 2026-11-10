package gpu

import _ "embed" // the compiled shaders below

//go:generate glslangValidator -V --quiet -o shaders/prim.vert.spv shaders/prim.vert
//go:generate glslangValidator -V --quiet -o shaders/prim.frag.spv shaders/prim.frag
//go:generate glslangValidator -V --quiet -o shaders/nv12.comp.spv shaders/nv12.comp
//go:generate glslangValidator -V --quiet -o shaders/grain.comp.spv shaders/grain.comp
//go:generate glslangValidator -V --quiet -o shaders/comp.vert.spv shaders/comp.vert
//go:generate glslangValidator -V --quiet -o shaders/comp.frag.spv shaders/comp.frag
//go:generate glslangValidator -V --quiet -o shaders/fill.frag.spv shaders/fill.frag
//go:generate glslangValidator -V --quiet -o shaders/grad.frag.spv shaders/grad.frag
//go:generate glslangValidator -V --quiet -o shaders/kawase.frag.spv shaders/kawase.frag
//go:generate glslangValidator -V --quiet -o shaders/goo.frag.spv shaders/goo.frag
//go:generate glslangValidator -V --quiet -o shaders/glass.frag.spv shaders/glass.frag
//go:generate glslangValidator -V --quiet -o shaders/ycbcr444.frag.spv shaders/ycbcr444.frag
//go:generate glslangValidator -V --quiet -o shaders/ycbcrplanes.frag.spv shaders/ycbcrplanes.frag
//go:generate glslangValidator -V --quiet -o testdata/fragment_test.frag.spv testdata/fragment_test.frag
//go:generate glslangValidator -V --quiet -DCOLOR -o shaders/comp.color.frag.spv shaders/comp.frag
//go:generate glslangValidator -V --quiet -DCOLOR -o shaders/glass.color.frag.spv shaders/glass.frag
//go:generate glslangValidator -V --quiet -DCOLOR -o shaders/grad.color.frag.spv shaders/grad.frag
//go:generate glslangValidator -V --quiet -DCOLOR -o shaders/ycbcr444.color.frag.spv shaders/ycbcr444.frag
//go:generate glslangValidator -V --quiet -DCOLOR -o shaders/ycbcrplanes.color.frag.spv shaders/ycbcrplanes.frag
//go:generate glslangValidator -V --quiet -DCUT -o shaders/comp.cut.frag.spv shaders/comp.frag
//go:generate glslangValidator -V --quiet -DCUT -DCOLOR -o shaders/comp.cut.color.frag.spv shaders/comp.frag
//go:generate glslangValidator -V --quiet -DCUT -o shaders/glass.cut.frag.spv shaders/glass.frag
//go:generate glslangValidator -V --quiet -DCUT -DCOLOR -o shaders/glass.cut.color.frag.spv shaders/glass.frag
//go:generate glslangValidator -V --quiet -DCUT -o shaders/fill.cut.frag.spv shaders/fill.frag

//go:embed shaders/prim.vert.spv
var primVert []byte

//go:embed shaders/prim.frag.spv
var primFrag []byte

//go:embed shaders/nv12.comp.spv
var nv12Comp []byte

//go:embed shaders/grain.comp.spv
var grainComp []byte

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

//go:embed shaders/glass.frag.spv
var glassFrag []byte

//go:embed shaders/ycbcr444.frag.spv
var ycbcr444Frag []byte

//go:embed shaders/ycbcrplanes.frag.spv
var ycbcrPlanesFrag []byte

//go:embed shaders/comp.color.frag.spv
var compColorFrag []byte

//go:embed shaders/glass.color.frag.spv
var glassColorFrag []byte

//go:embed shaders/grad.color.frag.spv
var gradColorFrag []byte

//go:embed shaders/ycbcr444.color.frag.spv
var ycbcr444ColorFrag []byte

//go:embed shaders/ycbcrplanes.color.frag.spv
var ycbcrPlanesColorFrag []byte

//go:embed shaders/comp.cut.frag.spv
var compCutFrag []byte

//go:embed shaders/comp.cut.color.frag.spv
var compCutColorFrag []byte

//go:embed shaders/glass.cut.frag.spv
var glassCutFrag []byte

//go:embed shaders/glass.cut.color.frag.spv
var glassCutColorFrag []byte

//go:embed shaders/fill.cut.frag.spv
var fillCutFrag []byte
