#version 450
#extension GL_GOOGLE_include_directive : require
#include "comp.glsl"

layout(location = 0) out vec2 vUV;

void main() {
    vec2 corner = vec2(float(gl_VertexIndex & 1), float(gl_VertexIndex >> 1));
    vec2 pos = draw.rect.xy + corner * draw.rect.zw;
    gl_Position = vec4(pos / draw.target.xy * 2.0 - 1.0, 0.0, 1.0);
    vec4 pair = draw.extra[gl_VertexIndex >> 1];
    vUV = (gl_VertexIndex & 1) == 0 ? pair.xy : pair.zw;
}
