#version 450
#extension GL_GOOGLE_include_directive : require
#include "comp.glsl"

layout(location = 0) out vec4 color;

void main() {
    color = draw.extra[0];
}
