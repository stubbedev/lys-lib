layout(push_constant) uniform Draw {
    vec4 rect;
    vec4 target;
    vec4 extra[2];
    vec4 keep;
    vec4 cut;
    vec4 radii;
} draw;

#include "mask.glsl"
