//========= Team Frontress ====================================================//
//
// RmlUi's render interface, reduced to what every target can draw: coloured,
// textured triangles inside a scissor rectangle.
//
// Transforms and gradients are resolved here on the CPU, so the game's
// material system and the previewer's OpenGL draw exactly the same thing.
// Not supported (and not used by our stylesheets): filters, box-shadow,
// mask-image and inverse clip masks. Rounded/transformed overflow masks use
// conservative rectangular clipping on both backends.
//
//=============================================================================
#pragma once

#include <RmlUi/Core/RenderInterface.h>

#include <cstdint>
#include <string>
#include <vector>

namespace fui {

// Straight (not premultiplied) alpha, positions in window pixels.
struct DrawVertex {
	float x, y;
	float u, v;
	uint8_t r, g, b, a;
};

class DrawBackend {
public:
	virtual ~DrawBackend() {}

	// RGBA8, straight alpha, rows top to bottom. Returns 0 on failure.
	virtual uintptr_t CreateTexture( const uint8_t *pRGBA, int w, int h, bool bRepeat ) = 0;
	// A texture the backend knows by name, e.g. "tex:backpack/..." or
	// "avatar:7656...". Returns 0 if it does not know the scheme.
	virtual uintptr_t LoadNamedTexture( const std::string &source, int &w, int &h ) = 0;
	virtual void ReleaseTexture( uintptr_t texture ) = 0;

	// texture 0 means untextured (white).
	virtual void DrawTriangles( const DrawVertex *pVerts, int nVerts, const int *pIndices, int nIndices, uintptr_t texture ) = 0;
	virtual void SetScissor( bool bEnable, int x, int y, int w, int h ) = 0;
};

class Renderer final : public Rml::RenderInterface {
public:
	explicit Renderer( DrawBackend *pBackend ) : m_pBackend( pBackend ) {}

	Rml::CompiledGeometryHandle CompileGeometry( Rml::Span< const Rml::Vertex > vertices, Rml::Span< const int > indices ) override;
	void RenderGeometry( Rml::CompiledGeometryHandle geometry, Rml::Vector2f translation, Rml::TextureHandle texture ) override;
	void ReleaseGeometry( Rml::CompiledGeometryHandle geometry ) override;

	Rml::TextureHandle LoadTexture( Rml::Vector2i &dimensions, const Rml::String &source ) override;
	Rml::TextureHandle GenerateTexture( Rml::Span< const Rml::byte > source, Rml::Vector2i dimensions ) override;
	void ReleaseTexture( Rml::TextureHandle texture ) override;

	void EnableScissorRegion( bool enable ) override;
	void SetScissorRegion( Rml::Rectanglei region ) override;
	void EnableClipMask( bool enable ) override;
	void RenderToClipMask( Rml::ClipMaskOperation operation, Rml::CompiledGeometryHandle geometry, Rml::Vector2f translation ) override;

	void SetTransform( const Rml::Matrix4f *transform ) override;

	Rml::CompiledShaderHandle CompileShader( const Rml::String &name, const Rml::Dictionary &parameters ) override;
	void RenderShader( Rml::CompiledShaderHandle shader, Rml::CompiledGeometryHandle geometry, Rml::Vector2f translation, Rml::TextureHandle texture ) override;
	void ReleaseShader( Rml::CompiledShaderHandle shader ) override;

	// Called by the host at the start of each frame.
	void BeginFrame( int width, int height );

private:
	struct Geometry;
	struct Shader;
	struct Texture {
		uintptr_t id;
	};

	void Emit( const Geometry &geometry, Rml::Vector2f translation, uintptr_t texture, const float *pU, const float *pV );
	void ApplyScissor();

	DrawBackend *m_pBackend;
	bool m_bScissor = false;
	Rml::Rectanglei m_Scissor;
	bool m_bClipMask = false;
	Rml::Rectanglei m_ClipMask;
	bool m_bTransform = false;
	Rml::Matrix4f m_Transform;
	std::vector< DrawVertex > m_Scratch;
	int m_nWidth = 0;
	int m_nHeight = 0;
};

} // namespace fui
