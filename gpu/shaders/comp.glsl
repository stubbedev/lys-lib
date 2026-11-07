layout(push_constant) uniform Draw {
    vec4 rect;
    vec4 target;
    vec4 extra[2];
    vec4 keep;
    vec4 cut;
    vec4 radii;
    vec4 merge;
} draw;

#include "mask.glsl"

#ifdef COLOR
layout(set = 1, binding = 0) uniform Color {
    vec4 cp[9];
} colorBlock;
#define CP colorBlock.cp
#include "color.glsl"
#define COLORED(c) colorApply(c)
#else
#define COLORED(c) (c)
#endif

#ifdef CUT
#ifdef COLOR
#define CUT_SET 2
#else
#define CUT_SET 1
#endif
layout(set = CUT_SET, binding = 0) uniform sampler2D cutTex;
layout(set = CUT_SET, binding = 1) uniform Cut {
    vec4 axes;
    vec4 base;
} cutBlock;
#ifndef SAMPLE
#define SAMPLE texture
#endif
#define CUT_SAMPLER sampler2D
#include "cut.glsl"
#define CUT_COVERAGE cutCoverage(cutTex, gl_FragCoord.xy, cutBlock.axes, cutBlock.base)
#else
#define CUT_COVERAGE 1.0
#endif
