#version 450
#extension GL_GOOGLE_include_directive : require
#include "prim.glsl"

layout(location = 0) flat out uint vPrim;

void main() {
    Prim p = prims[gl_InstanceIndex];
    vec2 corner = vec2(float(gl_VertexIndex & 1), float(gl_VertexIndex >> 1));
    vec2 pos = mix(p.quad.xy, p.quad.zw, corner);
    gl_Position = vec4(pos / frame.size * 2.0 - 1.0, 0.0, 1.0);
    vPrim = uint(gl_InstanceIndex);
}
