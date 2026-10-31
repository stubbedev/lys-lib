#version 450
#extension GL_GOOGLE_include_directive : require
#include "comp.glsl"

layout(set = 0, binding = 0) uniform sampler2D tex;

layout(location = 0) out vec4 color;

void main() {
    color = texture(tex, gradientUV(gl_FragCoord.xy, draw.extra[0], draw.extra[1].x)) * draw.extra[1].y * maskCoverage(gl_FragCoord.xy, draw.keep, draw.cut, draw.radii);
}
