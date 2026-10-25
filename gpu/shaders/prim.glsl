// One drawn primitive: a device-pixel quad and what fills it. Colors
// are premultiplied ARGB8888 words, render.Color's encoding.
struct Prim {
    vec4 quad;
    vec4 box;
    vec4 radii;
    vec4 inner;
    vec4 iradii;
    vec4 widths;
    vec4 m0;
    vec4 m1;
    vec4 m2;
    uvec4 cols;
    uvec4 info;
};

layout(std430, set = 0, binding = 0) readonly buffer Prims {
    Prim prims[];
};

layout(push_constant) uniform Frame {
    vec2 size;
} frame;
