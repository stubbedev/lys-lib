#version 450
#extension GL_GOOGLE_include_directive : require
#define SAMPLE texture
#include "comp.glsl"
#include "glass.glsl"

layout(set = 0, binding = 0) uniform sampler2D tex;

layout(location = 0) in vec2 vUV;
layout(location = 0) out vec4 color;

void main() {
    vec2 perPixel = vec2(draw.extra[0].z - draw.extra[0].x, draw.extra[1].y - draw.extra[0].y) / draw.rect.zw;
    vec4 c = glassBackdrop(tex, vUV, gl_FragCoord.xy, perPixel, draw.keep, draw.radii.x, draw.radii.z, draw.merge.w);
    color = COLORED(glassAdjust(c, draw.merge.xyz)) * (draw.target.z * maskCoverage(gl_FragCoord.xy, draw.keep, draw.cut, vec4(draw.radii.xy, 0.0, 0.0), vec4(0.0), 0.0));
}
