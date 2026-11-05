#version 440
// Unsharp mask at the source pixel scale, run on the bilinear-upscaled region.
layout(location = 0) in vec2 qt_TexCoord0;
layout(location = 0) out vec4 fragColor;
layout(std140, binding = 0) uniform buf {
    mat4 qt_Matrix;
    float qt_Opacity;
    vec2 texel;
    float amount;
};
layout(binding = 1) uniform sampler2D source;

void main() {
    vec2 uv = qt_TexCoord0;
    vec4 c = texture(source, uv);
    vec3 n = texture(source, uv + vec2(0.0, -texel.y)).rgb
           + texture(source, uv + vec2(0.0, texel.y)).rgb
           + texture(source, uv + vec2(-texel.x, 0.0)).rgb
           + texture(source, uv + vec2(texel.x, 0.0)).rgb;
    vec3 r = c.rgb + (c.rgb * 4.0 - n) * amount;
    fragColor = vec4(clamp(r, 0.0, 1.0), c.a) * qt_Opacity;
}
