//========= Team Frontress ====================================================//
#include "fui_font.h"

#include <RmlUi/Core/CallbackTexture.h>
#include <RmlUi/Core/Core.h>
#include <RmlUi/Core/FileInterface.h>
#include <RmlUi/Core/FontEffect.h>
#include <RmlUi/Core/FontGlyph.h>
#include <RmlUi/Core/Log.h>
#include <RmlUi/Core/Math.h>
#include <RmlUi/Core/Mesh.h>
#include <RmlUi/Core/RenderManager.h>
#include <RmlUi/Core/StringUtilities.h>

#include <cmath>
#include <cstring>
#include <unordered_map>

#define STBTT_STATIC
#define STB_TRUETYPE_IMPLEMENTATION
#include "../thirdparty/stb/stb_truetype.h"

namespace fui {

using Rml::Character;

struct FontEngine::Face {
	Rml::String family;
	Rml::Style::FontStyle style;
	Rml::Style::FontWeight weight;
	bool fallback;
	std::vector< unsigned char > data;
	stbtt_fontinfo info;
};

// One texture's worth of glyph images. The base layer holds the plain glyphs;
// an effect that draws its own pixels (outline, glow, blur) gets a layer of
// its own, one that only moves or tints the glyph (shadow) reuses the base.
struct FontEngine::Layer {
	Rml::SharedPtr< const Rml::FontEffect > effect;
	bool unique = true;

	struct Rect {
		int x, y, w, h;
		Rml::Vector2i origin;
	};
	std::unordered_map< char32_t, Rect > rects;

	std::vector< Rml::byte > pixels;	// RGBA, premultiplied
	int width = 512;
	int height = 0;
	int penX = 0, penY = 0, rowHeight = 0;

	Rml::CallbackTextureSource texture;
	bool dirty = true;

	bool Place( int w, int h, int &x, int &y )
	{
		const int pad = 1;
		if ( w + pad * 2 > width )
			return false;
		if ( penX + w + pad * 2 > width )
		{
			penX = 0;
			penY += rowHeight;
			rowHeight = 0;
		}
		if ( penY + h + pad * 2 > height )
		{
			int newHeight = height ? height : 64;
			while ( penY + h + pad * 2 > newHeight )
				newHeight *= 2;
			if ( newHeight > 4096 )
				return false;
			pixels.resize( (size_t)width * newHeight * 4, 0 );
			height = newHeight;
		}
		x = penX + pad;
		y = penY + pad;
		penX += w + pad * 2;
		rowHeight = Rml::Math::Max( rowHeight, h + pad * 2 );
		return true;
	}
};

struct FontEngine::Handle {
	Face *face;
	int size;
	float scale;
	Rml::FontMetrics metrics;
	int version = 1;

	struct Glyph {
		Rml::FontGlyph glyph;		// bitmap is A8
		Face *source;
		int index;					// glyph index in source
	};
	std::unordered_map< char32_t, Glyph > glyphs;

	Layer base;
	struct Effects {
		Rml::FontEffectList list;
		std::vector< std::unique_ptr< Layer > > back, front;
	};
	std::vector< std::unique_ptr< Effects > > effects;
};

namespace {

Rml::String ReadFamilyName( const stbtt_fontinfo &info )
{
	// Prefer the typographic family (16), then the family (1); Windows/Unicode
	// strings are big-endian UTF-16.
	for ( int nameID : { 16, 1 } )
	{
		int len = 0;
		const char *pName = stbtt_GetFontNameString( &info, &len, STBTT_PLATFORM_ID_MICROSOFT, STBTT_MS_EID_UNICODE_BMP, STBTT_MS_LANG_ENGLISH, nameID );
		if ( pName && len > 0 )
		{
			Rml::String out;
			for ( int i = 0; i + 1 < len; i += 2 )
			{
				const unsigned cp = ( (unsigned char)pName[ i ] << 8 ) | (unsigned char)pName[ i + 1 ];
				if ( cp < 0x80 )
					out += (char)cp;
			}
			if ( !out.empty() )
				return out;
		}
		pName = stbtt_GetFontNameString( &info, &len, STBTT_PLATFORM_ID_MAC, STBTT_MAC_EID_ROMAN, STBTT_MAC_LANG_ENGLISH, nameID );
		if ( pName && len > 0 )
			return Rml::String( pName, len );
	}
	return Rml::String();
}

// What the menus use. Anything else is rasterised when first drawn.
void PreloadCharacters( std::vector< char32_t > &out )
{
	for ( char32_t c = 32; c < 127; ++c )
		out.push_back( c );
	for ( char32_t c = 0x410; c < 0x450; ++c )
		out.push_back( c );
	for ( char32_t c : { 0x401, 0x451, 0xAB, 0xBB, 0x2014, 0x2013, 0x2026, 0x2022, 0x2116, 0xB7, 0xD7 } )
		out.push_back( c );
}

} // namespace

FontEngine::FontEngine() = default;
FontEngine::~FontEngine() = default;

void FontEngine::Shutdown()
{
	// Glyph textures belong to RmlUi's render manager, which goes next.
	m_Handles.clear();
}

bool FontEngine::AddFace( std::vector< unsigned char > &&data, const Rml::String &family, Rml::Style::FontStyle style,
	Rml::Style::FontWeight weight, bool fallback_face )
{
	std::unique_ptr< Face > face( new Face );
	face->data = std::move( data );
	const int offset = stbtt_GetFontOffsetForIndex( face->data.data(), 0 );
	if ( offset < 0 || !stbtt_InitFont( &face->info, face->data.data(), offset ) )
		return false;

	face->family = family.empty() ? ReadFamilyName( face->info ) : family;
	face->fallback = fallback_face;
	face->style = style;
	face->weight = weight;
	if ( weight == Rml::Style::FontWeight::Auto )
	{
		// head.macStyle: bit 0 bold, bit 1 italic.
		const stbtt_uint8 *pHead = face->info.data + face->info.head;
		const int macStyle = ( pHead[ 44 ] << 8 ) | pHead[ 45 ];
		face->weight = ( macStyle & 1 ) ? Rml::Style::FontWeight::Bold : Rml::Style::FontWeight::Normal;
		if ( macStyle & 2 )
			face->style = Rml::Style::FontStyle::Italic;
	}
	Rml::Log::Message( Rml::Log::LT_INFO, "Font: %s (%d%s)%s", face->family.c_str(), (int)face->weight,
		face->style == Rml::Style::FontStyle::Italic ? " italic" : "", fallback_face ? ", fallback" : "" );
	m_Faces.push_back( std::move( face ) );
	return true;
}

bool FontEngine::LoadFontFile( const Rml::String &file_name, const Rml::String &family, Rml::Style::FontStyle style,
	Rml::Style::FontWeight weight, bool fallback_face )
{
	Rml::String data;
	if ( !Rml::GetFileInterface()->LoadFile( file_name, data ) || data.empty() )
	{
		Rml::Log::Message( Rml::Log::LT_ERROR, "Font not found: %s", file_name.c_str() );
		return false;
	}
	std::vector< unsigned char > bytes( data.begin(), data.end() );
	if ( !AddFace( std::move( bytes ), family, style, weight, fallback_face ) )
	{
		Rml::Log::Message( Rml::Log::LT_ERROR, "Not a font: %s", file_name.c_str() );
		return false;
	}
	return true;
}

bool FontEngine::LoadFontFace( const Rml::String &file_name, bool fallback_face, Rml::Style::FontWeight weight )
{
	return LoadFontFile( file_name, Rml::String(), Rml::Style::FontStyle::Normal, weight, fallback_face );
}

bool FontEngine::LoadFontFace( Rml::Span< const Rml::byte > data, const Rml::String &family, Rml::Style::FontStyle style,
	Rml::Style::FontWeight weight, bool fallback_face )
{
	std::vector< unsigned char > bytes( data.begin(), data.end() );
	return AddFace( std::move( bytes ), family, style, weight, fallback_face );
}

Rml::FontFaceHandle FontEngine::GetFontFaceHandle( const Rml::String &family, Rml::Style::FontStyle style, Rml::Style::FontWeight weight, int size )
{
	if ( size <= 0 )
		return 0;
	const int wanted = weight == Rml::Style::FontWeight::Auto ? 400 : (int)weight;

	Face *pBest = nullptr;
	int bestScore = 1 << 30;
	for ( const auto &face : m_Faces )
	{
		if ( Rml::StringUtilities::ToLower( face->family ) != Rml::StringUtilities::ToLower( family ) )
			continue;
		const int score = std::abs( (int)face->weight - wanted ) + ( face->style == style ? 0 : 10000 );
		if ( score < bestScore )
		{
			bestScore = score;
			pBest = face.get();
		}
	}
	if ( !pBest )
		return 0;

	for ( const auto &handle : m_Handles )
	{
		if ( handle->face == pBest && handle->size == size )
			return reinterpret_cast< Rml::FontFaceHandle >( handle.get() );
	}

	std::unique_ptr< Handle > handle( new Handle );
	handle->face = pBest;
	handle->size = size;
	handle->scale = stbtt_ScaleForMappingEmToPixels( &pBest->info, (float)size );

	int ascent, descent, lineGap;
	stbtt_GetFontVMetrics( &pBest->info, &ascent, &descent, &lineGap );
	Rml::FontMetrics &m = handle->metrics;
	m.size = size;
	m.ascent = ascent * handle->scale;
	m.descent = -descent * handle->scale;
	m.line_spacing = ( ascent - descent + lineGap ) * handle->scale;
	int x0, y0, x1, y1;
	if ( stbtt_GetCodepointBox( &pBest->info, 'x', &x0, &y0, &x1, &y1 ) )
		m.x_height = y1 * handle->scale;
	else
		m.x_height = size * 0.5f;
	m.underline_position = size * 0.12f;
	m.underline_thickness = Rml::Math::Max( 1.f, size / 16.f );

	handle->effects.emplace_back( new Handle::Effects );	// index 0: no effects
	m_Handles.push_back( std::move( handle ) );
	return reinterpret_cast< Rml::FontFaceHandle >( m_Handles.back().get() );
}

Rml::FontEffectsHandle FontEngine::PrepareFontEffects( Rml::FontFaceHandle face_handle, const Rml::FontEffectList &font_effects )
{
	Handle *pHandle = reinterpret_cast< Handle * >( face_handle );
	if ( !pHandle )
		return 0;
	for ( size_t i = 0; i < pHandle->effects.size(); ++i )
	{
		const Rml::FontEffectList &list = pHandle->effects[ i ]->list;
		if ( list.size() != font_effects.size() )
			continue;
		bool bSame = true;
		for ( size_t j = 0; j < list.size() && bSame; ++j )
			bSame = list[ j ]->GetFingerprint() == font_effects[ j ]->GetFingerprint();
		if ( bSame )
			return (Rml::FontEffectsHandle)i;
	}

	std::unique_ptr< Handle::Effects > effects( new Handle::Effects );
	effects->list = font_effects;
	for ( const auto &effect : font_effects )
	{
		std::unique_ptr< Layer > layer( new Layer );
		layer->effect = effect;
		layer->unique = effect->HasUniqueTexture();
		( effect->GetLayer() == Rml::FontEffect::Layer::Back ? effects->back : effects->front ).push_back( std::move( layer ) );
	}
	pHandle->effects.push_back( std::move( effects ) );
	return (Rml::FontEffectsHandle)( pHandle->effects.size() - 1 );
}

const Rml::FontMetrics &FontEngine::GetFontMetrics( Rml::FontFaceHandle face_handle )
{
	static Rml::FontMetrics s_Empty = {};
	Handle *pHandle = reinterpret_cast< Handle * >( face_handle );
	return pHandle ? pHandle->metrics : s_Empty;
}

//-----------------------------------------------------------------------------
// Glyphs
//-----------------------------------------------------------------------------
static FontEngine::Handle::Glyph *FindGlyph( FontEngine::Handle &handle, std::vector< std::unique_ptr< FontEngine::Face > > &faces, char32_t c );

static void PutInLayer( FontEngine::Layer &layer, char32_t c, const Rml::FontGlyph &glyph )
{
	if ( layer.rects.count( c ) )
		return;

	FontEngine::Layer::Rect rect = { 0, 0, 0, 0, Rml::Vector2i( 0, 0 ) };
	Rml::Vector2i dimensions = glyph.bitmap_dimensions;
	if ( layer.effect )
	{
		if ( !layer.effect->GetGlyphMetrics( rect.origin, dimensions, glyph ) )
		{
			layer.rects[ c ] = rect;	// the effect skips this glyph
			return;
		}
	}
	if ( !layer.unique )
	{
		layer.rects[ c ] = rect;		// drawn from the base layer
		return;
	}
	rect.w = dimensions.x;
	rect.h = dimensions.y;
	if ( rect.w > 0 && rect.h > 0 && layer.Place( rect.w, rect.h, rect.x, rect.y ) )
	{
		Rml::byte *pDest = &layer.pixels[ ( (size_t)rect.y * layer.width + rect.x ) * 4 ];
		const int stride = layer.width * 4;
		if ( layer.effect )
		{
			layer.effect->GenerateGlyphTexture( pDest, dimensions, stride, glyph );
		}
		else if ( glyph.bitmap_data )
		{
			for ( int y = 0; y < rect.h; ++y )
			{
				for ( int x = 0; x < rect.w; ++x )
				{
					const Rml::byte a = glyph.bitmap_data[ y * glyph.bitmap_dimensions.x + x ];
					Rml::byte *p = pDest + y * stride + x * 4;
					p[ 0 ] = p[ 1 ] = p[ 2 ] = p[ 3 ] = a;
				}
			}
		}
		layer.dirty = true;
	}
	else
	{
		rect.w = rect.h = 0;
	}
	layer.rects[ c ] = rect;
}

static FontEngine::Handle::Glyph *FindGlyph( FontEngine::Handle &handle, std::vector< std::unique_ptr< FontEngine::Face > > &faces, char32_t c )
{
	auto it = handle.glyphs.find( c );
	if ( it != handle.glyphs.end() )
		return &it->second;

	FontEngine::Face *pSource = nullptr;
	int index = stbtt_FindGlyphIndex( &handle.face->info, (int)c );
	if ( index )
		pSource = handle.face;
	else
	{
		for ( const auto &face : faces )
		{
			if ( !face->fallback )
				continue;
			index = stbtt_FindGlyphIndex( &face->info, (int)c );
			if ( index )
			{
				pSource = face.get();
				break;
			}
		}
	}
	if ( !pSource )
	{
		if ( c == 0xFFFD || c < 32 )
			return nullptr;
		// Draw the replacement character instead of nothing.
		FontEngine::Handle::Glyph *pReplacement = FindGlyph( handle, faces, '?' );
		return pReplacement;
	}

	const float scale = stbtt_ScaleForMappingEmToPixels( &pSource->info, (float)handle.size );
	int advance, lsb;
	stbtt_GetGlyphHMetrics( &pSource->info, index, &advance, &lsb );
	int x0, y0, x1, y1;
	stbtt_GetGlyphBitmapBox( &pSource->info, index, scale, scale, &x0, &y0, &x1, &y1 );

	FontEngine::Handle::Glyph &entry = handle.glyphs[ c ];
	entry.source = pSource;
	entry.index = index;
	Rml::FontGlyph &glyph = entry.glyph;
	glyph.advance = (int)std::lround( advance * scale );
	glyph.bearing = Rml::Vector2i( x0, -y0 );
	glyph.dimensions = Rml::Vector2i( x1 - x0, y1 - y0 );
	glyph.bitmap_dimensions = glyph.dimensions;
	glyph.color_format = Rml::ColorFormat::A8;
	if ( glyph.dimensions.x > 0 && glyph.dimensions.y > 0 )
	{
		glyph.bitmap_owned_data.reset( new Rml::byte[ glyph.dimensions.x * glyph.dimensions.y ] );
		stbtt_MakeGlyphBitmap( &pSource->info, glyph.bitmap_owned_data.get(), glyph.dimensions.x, glyph.dimensions.y, glyph.dimensions.x, scale, scale,
			index );
		glyph.bitmap_data = glyph.bitmap_owned_data.get();
	}
	PutInLayer( handle.base, c, glyph );
	return &entry;
}

static int Kerning( const FontEngine::Handle &handle, const FontEngine::Handle::Glyph *pPrev, const FontEngine::Handle::Glyph *pNext )
{
	if ( !pPrev || !pNext || pPrev->source != pNext->source )
		return 0;
	const float scale = stbtt_ScaleForMappingEmToPixels( &pPrev->source->info, (float)handle.size );
	return (int)std::lround( stbtt_GetGlyphKernAdvance( &pPrev->source->info, pPrev->index, pNext->index ) * scale );
}

int FontEngine::GetStringWidth( Rml::FontFaceHandle face_handle, Rml::StringView string, const Rml::TextShapingContext &context,
	Rml::Character prior_character )
{
	Handle *pHandle = reinterpret_cast< Handle * >( face_handle );
	if ( !pHandle )
		return 0;

	const Handle::Glyph *pPrev = prior_character != Character::Null ? FindGlyph( *pHandle, m_Faces, (char32_t)prior_character ) : nullptr;
	int width = 0;
	for ( auto it = Rml::StringIteratorU8( string.begin(), string.begin(), string.end() ); it; ++it )
	{
		const Handle::Glyph *pGlyph = FindGlyph( *pHandle, m_Faces, (char32_t)*it );
		if ( !pGlyph )
			continue;
		width += Kerning( *pHandle, pPrev, pGlyph ) + pGlyph->glyph.advance + (int)context.letter_spacing;
		pPrev = pGlyph;
	}
	return Rml::Math::Max( width, 0 );
}

static void RefreshTexture( FontEngine::Layer &layer, int &version )
{
	if ( !layer.dirty || layer.height == 0 )
		return;
	layer.dirty = false;
	FontEngine::Layer *pLayer = &layer;
	layer.texture = Rml::CallbackTextureSource( [ pLayer ]( const Rml::CallbackTextureInterface &texture_interface ) -> bool {
		return texture_interface.GenerateTexture( Rml::Span< const Rml::byte >( pLayer->pixels.data(), pLayer->pixels.size() ),
			Rml::Vector2i( pLayer->width, pLayer->height ) );
	} );
	++version;
}

int FontEngine::GenerateString( Rml::RenderManager &render_manager, Rml::FontFaceHandle face_handle, Rml::FontEffectsHandle font_effects_handle,
	Rml::StringView string, Rml::Vector2f position, Rml::ColourbPremultiplied colour, float opacity, const Rml::TextShapingContext &context,
	Rml::TexturedMeshList &mesh_list )
{
	Handle *pHandle = reinterpret_cast< Handle * >( face_handle );
	if ( !pHandle || font_effects_handle >= pHandle->effects.size() )
		return 0;
	Handle::Effects &effects = *pHandle->effects[ font_effects_handle ];

	// The layers to draw, back to front, and the glyphs each will need.
	std::vector< Layer * > layers;
	for ( auto &layer : effects.back )
		layers.push_back( layer.get() );
	layers.push_back( &pHandle->base );
	for ( auto &layer : effects.front )
		layers.push_back( layer.get() );

	if ( pHandle->glyphs.empty() )
	{
		std::vector< char32_t > preload;
		PreloadCharacters( preload );
		for ( char32_t c : preload )
			FindGlyph( *pHandle, m_Faces, c );
	}

	std::vector< std::pair< char32_t, const Handle::Glyph * > > glyphs;
	for ( auto it = Rml::StringIteratorU8( string.begin(), string.begin(), string.end() ); it; ++it )
	{
		const Handle::Glyph *pGlyph = FindGlyph( *pHandle, m_Faces, (char32_t)*it );
		if ( pGlyph )
			glyphs.emplace_back( (char32_t)*it, pGlyph );
	}
	for ( Layer *pLayer : layers )
	{
		if ( pLayer == &pHandle->base )
			continue;
		// Effect layers also need everything the base layer has, so that a
		// later string drawing a preloaded glyph finds it.
		for ( const auto &glyph : pHandle->glyphs )
			PutInLayer( *pLayer, glyph.first, glyph.second.glyph );
	}
	for ( Layer *pLayer : layers )
		RefreshTexture( *pLayer, pHandle->version );

	// RmlUi calls this once per line with the same list: one mesh per layer,
	// each line appended to it.
	if ( mesh_list.size() < layers.size() )
		mesh_list.resize( layers.size() );
	int width = 0;
	for ( size_t layerIndex = 0; layerIndex < layers.size(); ++layerIndex )
	{
		Layer *pLayer = layers[ layerIndex ];
		Layer &atlas = pLayer->unique ? *pLayer : pHandle->base;
		if ( atlas.height == 0 )
			continue;

		Rml::ColourbPremultiplied layerColour = colour;
		if ( pLayer->effect )
			layerColour = pLayer->effect->GetColour().ToPremultiplied( opacity );

		Rml::TexturedMesh &mesh = mesh_list[ layerIndex ];
		mesh.texture = atlas.texture.GetTexture( render_manager );
		const float invW = 1.f / atlas.width, invH = 1.f / atlas.height;

		int pen = 0;
		const Handle::Glyph *pPrev = nullptr;
		for ( const auto &entry : glyphs )
		{
			const Handle::Glyph *pGlyph = entry.second;
			pen += Kerning( *pHandle, pPrev, pGlyph );

			auto itRect = pLayer->rects.find( entry.first );
			auto itBase = pHandle->base.rects.find( entry.first );
			if ( itRect != pLayer->rects.end() && itBase != pHandle->base.rects.end() )
			{
				const Layer::Rect &own = itRect->second;
				const Layer::Rect &src = pLayer->unique ? own : itBase->second;
				if ( src.w > 0 && src.h > 0 )
				{
					const float x = position.x + pen + pGlyph->glyph.bearing.x + own.origin.x;
					const float y = position.y - pGlyph->glyph.bearing.y + own.origin.y;
					const int first = (int)mesh.mesh.vertices.size();
					const float u0 = src.x * invW, v0 = src.y * invH, u1 = ( src.x + src.w ) * invW, v1 = ( src.y + src.h ) * invH;
					const float px[ 4 ] = { x, x + src.w, x + src.w, x };
					const float py[ 4 ] = { y, y, y + src.h, y + src.h };
					const float pu[ 4 ] = { u0, u1, u1, u0 };
					const float pv[ 4 ] = { v0, v0, v1, v1 };
					for ( int k = 0; k < 4; ++k )
					{
						Rml::Vertex vertex;
						vertex.position = Rml::Vector2f( px[ k ], py[ k ] );
						vertex.tex_coord = Rml::Vector2f( pu[ k ], pv[ k ] );
						vertex.colour = layerColour;
						mesh.mesh.vertices.push_back( vertex );
					}
					for ( int k : { 0, 1, 2, 0, 2, 3 } )
						mesh.mesh.indices.push_back( first + k );
				}
			}
			pen += pGlyph->glyph.advance + (int)context.letter_spacing;
			pPrev = pGlyph;
		}
		width = pen;
	}
	return Rml::Math::Max( width, 0 );
}

int FontEngine::GetVersion( Rml::FontFaceHandle face_handle )
{
	Handle *pHandle = reinterpret_cast< Handle * >( face_handle );
	return pHandle ? pHandle->version : 0;
}

void FontEngine::ReleaseFontResources()
{
	m_Handles.clear();
}

} // namespace fui
