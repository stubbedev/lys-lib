#version 450
layout(push_constant) uniform Draw {
    layout(offset = 64) vec4 keep;
} draw;
layout(set = 0, binding = 0) uniform sampler2D tex;
layout(location = 0) in vec2 vUV;
layout(location = 0) out vec4 color;
void main() {
    vec4 c = texture(tex, vUV);
    color = vec4(1.0 - c.r, vUV.x, draw.keep.x, 0.0);
}
