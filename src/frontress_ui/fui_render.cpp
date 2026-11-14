//========= Team Frontress ====================================================//
#include "fui_render.h"

#include <RmlUi/Core/DecorationTypes.h>
#include <RmlUi/Core/FileInterface.h>
#include <RmlUi/Core/Core.h>
#include <RmlUi/Core/Log.h>
#include <RmlUi/Core/Math.h>

#include <algorithm>
#include <cmath>

#define STB_IMAGE_STATIC
#define STB_IMAGE_IMPLEMENTATION
#define STBI_ONLY_PNG
#define STBI_ONLY_TGA
#define STBI_ONLY_JPEG
#define STBI_NO_STDIO
#include "../thirdparty/stb/stb_image.h"

namespace fui {

struct Renderer::Geometry {
	std::vector< Rml::Vertex > vertices;
	std::vector< int > indices;
};

struct Renderer::Shader {
	enum class Kind { Linear, Radial, Conic } kind = Kind::Linear;
	bool repeating = false;
	Rml::Vector2f p;			// linear: start; radial/conic: centre
	Rml::Vector2f v;			// linear: start->end; radial: 1/radius; conic: angle direction
	std::vector< std::pair< float, Rml::ColourbPremultiplied > > stops;

	uintptr_t texture = 0;		// linear: 256x1 ramp; radial/conic: baked over bbox
	Rml::Vector2f bakedMin, bakedMax;
};

static uint8_t Unpremultiply( uint8_t c, uint8_t a )
{
	if ( a == 0 )
		return 0;
	const int v = ( c * 255 + a / 2 ) / a;
	return (uint8_t)( v > 255 ? 255 : v );
}

void Renderer::BeginFrame( int width, int height )
{
	m_nWidth = width;
	m_nHeight = height;
	m_bScissor = false;
	m_bClipMask = false;
	m_bTransform = false;
	m_pBackend->SetScissor( false, 0, 0, width, height );
}

Rml::CompiledGeometryHandle Renderer::CompileGeometry( Rml::Span< const Rml::Vertex > vertices, Rml::Span< const int > indices )
{
	Geometry *pGeometry = new Geometry;
	pGeometry->vertices.assign( vertices.begin(), vertices.end() );
	pGeometry->indices.assign( indices.begin(), indices.end() );
	return reinterpret_cast< Rml::CompiledGeometryHandle >( pGeometry );
}

void Renderer::ReleaseGeometry( Rml::CompiledGeometryHandle geometry )
{
	delete reinterpret_cast< Geometry * >( geometry );
}

void Renderer::Emit( const Geometry &geometry, Rml::Vector2f translation, uintptr_t texture, const float *pU, const float *pV )
{
	const size_t n = geometry.vertices.size();
	m_Scratch.resize( n );
	for ( size_t i = 0; i < n; ++i )
	{
		const Rml::Vertex &in = geometry.vertices[ i ];
		DrawVertex &out = m_Scratch[ i ];
		Rml::Vector2f pos = in.position + translation;
		if ( m_bTransform )
		{
			const Rml::Vector4f p = m_Transform * Rml::Vector4f( pos.x, pos.y, 0.f, 1.f );
			const float w = ( p.w != 0.f ) ? p.w : 1.f;
			pos = Rml::Vector2f( p.x / w, p.y / w );
		}
		out.x = pos.x;
		out.y = pos.y;
		out.u = pU ? pU[ i ] : in.tex_coord.x;
		out.v = pV ? pV[ i ] : in.tex_coord.y;
		out.a = in.colour.alpha;
		out.r = Unpremultiply( in.colour.red, in.colour.alpha );
		out.g = Unpremultiply( in.colour.green, in.colour.alpha );
		out.b = Unpremultiply( in.colour.blue, in.colour.alpha );
	}
	m_pBackend->DrawTriangles( m_Scratch.data(), (int)n, geometry.indices.data(), (int)geometry.indices.size(), texture );
}

void Renderer::RenderGeometry( Rml::CompiledGeometryHandle geometry, Rml::Vector2f translation, Rml::TextureHandle texture )
{
	const Geometry *pGeometry = reinterpret_cast< const Geometry * >( geometry );
	if ( pGeometry && !pGeometry->indices.empty() )
		Emit( *pGeometry, translation, (uintptr_t)texture, nullptr, nullptr );
}

//-----------------------------------------------------------------------------
// Textures
//-----------------------------------------------------------------------------
Rml::TextureHandle Renderer::LoadTexture( Rml::Vector2i &dimensions, const Rml::String &source )
{
	// "scheme:name" is the backend's (game textures, avatars). A drive letter
	// is not a scheme.
	const size_t colon = source.find( ':' );
	if ( colon != Rml::String::npos && colon > 1 )
	{
		if ( colon + 1 == source.size() )
		{
			// Bound to a value that is still empty: draw nothing, quietly.
			const uint8_t clear[ 4 ] = { 0, 0, 0, 0 };
			dimensions = Rml::Vector2i( 1, 1 );
			return (Rml::TextureHandle)m_pBackend->CreateTexture( clear, 1, 1, false );
		}
		int w = 0, h = 0;
		const uintptr_t id = m_pBackend->LoadNamedTexture( source, w, h );
		dimensions = Rml::Vector2i( w, h );
		return (Rml::TextureHandle)id;
	}

	Rml::String data;
	if ( !Rml::GetFileInterface()->LoadFile( source, data ) || data.empty() )
	{
		Rml::Log::Message( Rml::Log::LT_WARNING, "Image not found: %s", source.c_str() );
		return 0;
	}

	int w = 0, h = 0, channels = 0;
	stbi_uc *pPixels = stbi_load_from_memory( (const stbi_uc *)data.data(), (int)data.size(), &w, &h, &channels, 4 );
	if ( !pPixels )
	{
		Rml::Log::Message( Rml::Log::LT_WARNING, "Could not decode %s: %s", source.c_str(), stbi_failure_reason() );
		return 0;
	}
	const uintptr_t id = m_pBackend->CreateTexture( pPixels, w, h, false );
	stbi_image_free( pPixels );
	dimensions = Rml::Vector2i( w, h );
	return (Rml::TextureHandle)id;
}

Rml::TextureHandle Renderer::GenerateTexture( Rml::Span< const Rml::byte > source, Rml::Vector2i dimensions )
{
	// RmlUi hands out premultiplied pixels; the backends blend straight alpha.
	std::vector< uint8_t > straight( source.begin(), source.end() );
	for ( size_t i = 0; i + 3 < straight.size(); i += 4 )
	{
		const uint8_t a = straight[ i + 3 ];
		straight[ i + 0 ] = Unpremultiply( straight[ i + 0 ], a );
		straight[ i + 1 ] = Unpremultiply( straight[ i + 1 ], a );
		straight[ i + 2 ] = Unpremultiply( straight[ i + 2 ], a );
	}
	return (Rml::TextureHandle)m_pBackend->CreateTexture( straight.data(), dimensions.x, dimensions.y, false );
}

void Renderer::ReleaseTexture( Rml::TextureHandle texture )
{
	if ( texture )
		m_pBackend->ReleaseTexture( (uintptr_t)texture );
}

//-----------------------------------------------------------------------------
// Scissor and transform
//-----------------------------------------------------------------------------
void Renderer::EnableScissorRegion( bool enable )
{
	m_bScissor = enable;
	ApplyScissor();
}

void Renderer::SetScissorRegion( Rml::Rectanglei region )
{
	m_Scissor = region;
	ApplyScissor();
}

void Renderer::ApplyScissor()
{
	int left = 0, top = 0, right = m_nWidth, bottom = m_nHeight;
	auto intersect = [&]( const Rml::Rectanglei &rect ) {
		left = std::max( left, rect.Left() );
		top = std::max( top, rect.Top() );
		right = std::min( right, rect.Right() );
		bottom = std::min( bottom, rect.Bottom() );
	};
	if ( m_bScissor ) intersect( m_Scissor );
	if ( m_bClipMask ) intersect( m_ClipMask );
	m_pBackend->SetScissor( m_bScissor || m_bClipMask, left, top, std::max( 0, right - left ), std::max( 0, bottom - top ) );
}

void Renderer::EnableClipMask( bool enable )
{
	m_bClipMask = enable;
	m_ClipMask = Rml::Rectanglei::FromPositionSize( { 0, 0 }, { m_nWidth, m_nHeight } );
	ApplyScissor();
}

void Renderer::RenderToClipMask( Rml::ClipMaskOperation operation, Rml::CompiledGeometryHandle handle, Rml::Vector2f translation )
{
	// RmlUi disables its normal scissor for transformed overflow containers.
	// Keep their projected bounds clipped even on backends without stencil
	// masks. Our UI uses Set/Intersect for rectangular and rounded panels.
	if ( operation == Rml::ClipMaskOperation::SetInverse )
		return;
	const Geometry *geometry = reinterpret_cast< const Geometry * >( handle );
	Rml::Vector2f minimum( (float)m_nWidth, (float)m_nHeight ), maximum( 0.f, 0.f );
	if ( geometry )
	{
		for ( const auto &vertex : geometry->vertices )
		{
			Rml::Vector2f position = vertex.position + translation;
			if ( m_bTransform )
			{
				const Rml::Vector4f point = m_Transform * Rml::Vector4f( position.x, position.y, 0.f, 1.f );
				const float w = point.w != 0.f ? point.w : 1.f;
				position = { point.x / w, point.y / w };
			}
			minimum.x = std::min( minimum.x, position.x );
			minimum.y = std::min( minimum.y, position.y );
			maximum.x = std::max( maximum.x, position.x );
			maximum.y = std::max( maximum.y, position.y );
		}
	}
	int left = (int)std::floor( minimum.x ), top = (int)std::floor( minimum.y );
	int right = (int)std::ceil( maximum.x ), bottom = (int)std::ceil( maximum.y );
	if ( operation == Rml::ClipMaskOperation::Intersect )
	{
		left = std::max( left, m_ClipMask.Left() );
		top = std::max( top, m_ClipMask.Top() );
		right = std::min( right, m_ClipMask.Right() );
		bottom = std::min( bottom, m_ClipMask.Bottom() );
	}
	m_ClipMask = Rml::Rectanglei::FromPositionSize( { left, top }, { std::max( 0, right - left ), std::max( 0, bottom - top ) } );
	ApplyScissor();
}

void Renderer::SetTransform( const Rml::Matrix4f *transform )
{
	m_bTransform = transform != nullptr;
	if ( transform )
		m_Transform = *transform;
}

//-----------------------------------------------------------------------------
// Gradients, baked into textures
//-----------------------------------------------------------------------------
static void SampleStops( const std::vector< std::pair< float, Rml::ColourbPremultiplied > > &stops, float t, uint8_t *pOut )
{
	if ( stops.empty() )
	{
		pOut[ 0 ] = pOut[ 1 ] = pOut[ 2 ] = pOut[ 3 ] = 0;
		return;
	}
	Rml::ColourbPremultiplied c = stops.front().second;
	if ( t >= stops.back().first )
		c = stops.back().second;
	else if ( t > stops.front().first )
	{
		for ( size_t i = 1; i < stops.size(); ++i )
		{
			if ( t <= stops[ i ].first )
			{
				const float span = stops[ i ].first - stops[ i - 1 ].first;
				const float f = span > 0.f ? ( t - stops[ i - 1 ].first ) / span : 1.f;
				const Rml::ColourbPremultiplied &a = stops[ i - 1 ].second;
				const Rml::ColourbPremultiplied &b = stops[ i ].second;
				auto lerp = [ f ]( uint8_t x, uint8_t y ) { return (uint8_t)std::lround( x + ( y - x ) * f ); };
				c = Rml::ColourbPremultiplied( lerp( a.red, b.red ), lerp( a.green, b.green ), lerp( a.blue, b.blue ), lerp( a.alpha, b.alpha ) );
				break;
			}
		}
	}
	pOut[ 3 ] = c.alpha;
	pOut[ 0 ] = Unpremultiply( c.red, c.alpha );
	pOut[ 1 ] = Unpremultiply( c.green, c.alpha );
	pOut[ 2 ] = Unpremultiply( c.blue, c.alpha );
}

Rml::CompiledShaderHandle Renderer::CompileShader( const Rml::String &name, const Rml::Dictionary &parameters )
{
	Shader *pShader = new Shader;
	pShader->repeating = Rml::Get( parameters, "repeating", false );
	if ( name == "linear-gradient" )
	{
		pShader->kind = Shader::Kind::Linear;
		pShader->p = Rml::Get( parameters, "p0", Rml::Vector2f( 0.f ) );
		pShader->v = Rml::Get( parameters, "p1", Rml::Vector2f( 0.f ) ) - pShader->p;
	}
	else if ( name == "radial-gradient" )
	{
		pShader->kind = Shader::Kind::Radial;
		pShader->p = Rml::Get( parameters, "center", Rml::Vector2f( 0.f ) );
		pShader->v = Rml::Vector2f( 1.f ) / Rml::Get( parameters, "radius", Rml::Vector2f( 1.f ) );
	}
	else if ( name == "conic-gradient" )
	{
		pShader->kind = Shader::Kind::Conic;
		pShader->p = Rml::Get( parameters, "center", Rml::Vector2f( 0.f ) );
		const float angle = Rml::Get( parameters, "angle", 0.f );
		pShader->v = Rml::Vector2f( std::cos( angle ), std::sin( angle ) );
	}
	else
	{
		Rml::Log::Message( Rml::Log::LT_WARNING, "Unsupported shader '%s'.", name.c_str() );
		delete pShader;
		return 0;
	}

	auto it = parameters.find( "color_stop_list" );
	if ( it != parameters.end() && it->second.GetType() == Rml::Variant::COLORSTOPLIST )
	{
		for ( const Rml::ColorStop &stop : it->second.GetReference< Rml::ColorStopList >() )
			pShader->stops.emplace_back( stop.position.number, stop.color );
	}

	if ( pShader->kind == Shader::Kind::Linear )
	{
		uint8_t ramp[ 256 * 4 ];
		for ( int i = 0; i < 256; ++i )
			SampleStops( pShader->stops, i / 255.f, &ramp[ i * 4 ] );
		pShader->texture = m_pBackend->CreateTexture( ramp, 256, 1, pShader->repeating );
	}
	return reinterpret_cast< Rml::CompiledShaderHandle >( pShader );
}

void Renderer::RenderShader( Rml::CompiledShaderHandle shader, Rml::CompiledGeometryHandle geometry, Rml::Vector2f translation, Rml::TextureHandle )
{
	Shader *pShader = reinterpret_cast< Shader * >( shader );
	const Geometry *pGeometry = reinterpret_cast< const Geometry * >( geometry );
	if ( !pShader || !pGeometry || pGeometry->vertices.empty() )
		return;

	const size_t n = pGeometry->vertices.size();
	std::vector< float > u( n ), v( n, 0.5f );

	if ( pShader->kind == Shader::Kind::Linear )
	{
		const float len2 = pShader->v.x * pShader->v.x + pShader->v.y * pShader->v.y;
		for ( size_t i = 0; i < n; ++i )
		{
			const Rml::Vector2f d = pGeometry->vertices[ i ].tex_coord - pShader->p;
			float t = len2 > 0.f ? ( d.x * pShader->v.x + d.y * pShader->v.y ) / len2 : 0.f;
			if ( !pShader->repeating )
				t = Rml::Math::Clamp( t, 0.f, 1.f );
			u[ i ] = ( 0.5f + t * 255.f ) / 256.f;
		}
		Emit( *pGeometry, translation, pShader->texture, u.data(), v.data() );
		return;
	}

	// Radial and conic gradients are not linear across a triangle, so bake
	// them over the geometry's box and map the box onto the texture.
	Rml::Vector2f lo = pGeometry->vertices[ 0 ].tex_coord, hi = lo;
	for ( const Rml::Vertex &vertex : pGeometry->vertices )
	{
		lo = Rml::Vector2f( std::min( lo.x, vertex.tex_coord.x ), std::min( lo.y, vertex.tex_coord.y ) );
		hi = Rml::Vector2f( std::max( hi.x, vertex.tex_coord.x ), std::max( hi.y, vertex.tex_coord.y ) );
	}
	if ( !pShader->texture || lo != pShader->bakedMin || hi != pShader->bakedMax )
	{
		if ( pShader->texture )
			m_pBackend->ReleaseTexture( pShader->texture );
		// Bucket sizes so a resized decorator can reuse its procedural texture.
		const auto textureSize = []( float size ) { return size <= 32.f ? 32 : size <= 64.f ? 64 : 128; };
		const int w = textureSize( hi.x - lo.x );
		const int h = textureSize( hi.y - lo.y );
		std::vector< uint8_t > pixels( w * h * 4 );
		for ( int y = 0; y < h; ++y )
		{
			for ( int x = 0; x < w; ++x )
			{
				const Rml::Vector2f pos( lo.x + ( x + 0.5f ) / w * ( hi.x - lo.x ), lo.y + ( y + 0.5f ) / h * ( hi.y - lo.y ) );
				const Rml::Vector2f d = pos - pShader->p;
				float t;
				if ( pShader->kind == Shader::Kind::Radial )
				{
					const Rml::Vector2f s( d.x * pShader->v.x, d.y * pShader->v.y );
					t = std::sqrt( s.x * s.x + s.y * s.y );
				}
				else
				{
					// Clockwise from the start angle, as CSS draws it.
					const float a = std::atan2( d.y, d.x ) - std::atan2( pShader->v.y, pShader->v.x ) + 3.14159265f * 0.5f;
					t = a / ( 2.f * 3.14159265f );
					t -= std::floor( t );
				}
				if ( pShader->repeating && !pShader->stops.empty() )
				{
					const float first = pShader->stops.front().first, last = pShader->stops.back().first;
					const float span = last - first;
					if ( span > 0.f )
						t = first + std::fmod( std::fmod( t - first, span ) + span, span );
				}
				SampleStops( pShader->stops, t, &pixels[ ( y * w + x ) * 4 ] );
			}
		}
		pShader->texture = m_pBackend->CreateTexture( pixels.data(), w, h, false );
		pShader->bakedMin = lo;
		pShader->bakedMax = hi;
	}

	const Rml::Vector2f size( std::max( hi.x - lo.x, 1e-3f ), std::max( hi.y - lo.y, 1e-3f ) );
	for ( size_t i = 0; i < n; ++i )
	{
		u[ i ] = ( pGeometry->vertices[ i ].tex_coord.x - lo.x ) / size.x;
		v[ i ] = ( pGeometry->vertices[ i ].tex_coord.y - lo.y ) / size.y;
	}
	Emit( *pGeometry, translation, pShader->texture, u.data(), v.data() );
}

void Renderer::ReleaseShader( Rml::CompiledShaderHandle shader )
{
	Shader *pShader = reinterpret_cast< Shader * >( shader );
	if ( !pShader )
		return;
	if ( pShader->texture )
		m_pBackend->ReleaseTexture( pShader->texture );
	delete pShader;
}

} // namespace fui
