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
