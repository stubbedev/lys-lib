#version 450
#extension GL_GOOGLE_include_directive : require
#define SAMPLE texture
#include "comp.glsl"

layout(set = 0, binding = 0) uniform sampler2D tex;

layout(location = 0) in vec2 vUV;
layout(location = 0) out vec4 color;

void main() {
    color = draw.keep.z > 0.5 ? kawaseUp(tex, vUV, draw.keep.xy) : kawaseDown(tex, vUV, draw.keep.xy);
}
