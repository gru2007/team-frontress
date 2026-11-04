//========= Team Frontress ====================================================//
//
// A font engine for RmlUi on stb_truetype: no FreeType to build or ship on
// three platforms. Glyphs are rasterised into one atlas per face and size;
// RmlUi's own font effects (shadow, outline, glow, blur) draw from it.
//
//=============================================================================
#pragma once

#include <RmlUi/Core/FontEngineInterface.h>

#include <memory>
#include <vector>

namespace fui {

class FontEngine final : public Rml::FontEngineInterface {
public:
	FontEngine();
	~FontEngine() override;

	void Shutdown() override;

	bool LoadFontFace( const Rml::String &file_name, bool fallback_face, Rml::Style::FontWeight weight ) override;
	bool LoadFontFace( Rml::Span< const Rml::byte > data, const Rml::String &family, Rml::Style::FontStyle style, Rml::Style::FontWeight weight,
		bool fallback_face ) override;

	Rml::FontFaceHandle GetFontFaceHandle( const Rml::String &family, Rml::Style::FontStyle style, Rml::Style::FontWeight weight, int size ) override;
	Rml::FontEffectsHandle PrepareFontEffects( Rml::FontFaceHandle handle, const Rml::FontEffectList &font_effects ) override;
	const Rml::FontMetrics &GetFontMetrics( Rml::FontFaceHandle handle ) override;
	int GetStringWidth( Rml::FontFaceHandle handle, Rml::StringView string, const Rml::TextShapingContext &text_shaping_context,
		Rml::Character prior_character ) override;
	int GenerateString( Rml::RenderManager &render_manager, Rml::FontFaceHandle face_handle, Rml::FontEffectsHandle font_effects_handle,
		Rml::StringView string, Rml::Vector2f position, Rml::ColourbPremultiplied colour, float opacity,
		const Rml::TextShapingContext &text_shaping_context, Rml::TexturedMeshList &mesh_list ) override;
	int GetVersion( Rml::FontFaceHandle handle ) override;
	void ReleaseFontResources() override;

	// Like LoadFontFace(file), with the family name given rather than read
	// from the file.
	bool LoadFontFile( const Rml::String &file_name, const Rml::String &family, Rml::Style::FontStyle style, Rml::Style::FontWeight weight,
		bool fallback_face );

	struct Face;
	struct Handle;
	struct Layer;

private:
	bool AddFace( std::vector< unsigned char > &&data, const Rml::String &family, Rml::Style::FontStyle style, Rml::Style::FontWeight weight,
		bool fallback_face );

	std::vector< std::unique_ptr< Face > > m_Faces;
	std::vector< std::unique_ptr< Handle > > m_Handles;
};

} // namespace fui
