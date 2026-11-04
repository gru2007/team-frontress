//========= Team Frontress ====================================================//
//
// <warmap>: the war, drawn as a planet.
//
//     <warmap source="war" select="ui.front" hover="ui.war_hover">
//         <div class="sector" data-for="f : war.fronts" data-attr-node="f.id">...</div>
//     </warmap>
//
// The theater (sectors in a 1140 x 640 plane, as the coordinator has them) is
// wrapped onto the front of a globe: a generated planet (or the equirect map
// texture= names), lit from one side, with clouds, an atmosphere and stars.
// Drag turns it any way (it keeps spinning a little when let go), the wheel
// zooms, and it turns by itself to the sector you pick.
//
// Territory is not authored, it is derived (the idea comes from the demo
// branch's theater): the plane is tiled with hexes and each hex belongs to
// the nearest sector, so the ground, the borders between sectors and the
// front line all follow the owners. A sector changes hands and the front
// moves with it; the map cannot disagree with the war.
//
// On the ground: roads between sectors, and an arrow for every operation,
// from the sector it pushes from to its target, thicker at each stage, with
// chevrons running along it. Targets pulse in the attacker's colour.
//
// Children with a node="<id>" attribute are the sector markers: the element
// puts them on their sector's point and hides them on the far side of the
// planet, so the document only styles them. Clicking the ground selects the
// sector under the cursor (select=), hovering names it (hover=).
//
// Reads, under source: fronts [{ id, x, y, owner, attacker, from, stage, hq }]
// and links [{ a, b, side }].
//
//=============================================================================
#include "fui_warmap.h"
#include "fui.h"

#include <RmlUi/Core/Core.h>
#include <RmlUi/Core/Element.h>
#include <RmlUi/Core/ElementInstancer.h>
#include <RmlUi/Core/Event.h>
#include <RmlUi/Core/Geometry.h>
#include <RmlUi/Core/Mesh.h>
#include <RmlUi/Core/RenderManager.h>
#include <RmlUi/Core/SystemInterface.h>
#include <RmlUi/Core/Texture.h>

#include <algorithm>
#include <cmath>
#include <map>
#include <string>
#include <vector>

namespace fui {
namespace {

const float k_Pi = 3.14159265f;
const float k_Deg = k_Pi / 180.f;
const float k_Sqrt3 = 1.7320508f;

// How much of the globe the theater covers: 124 x 70 degrees (the plane's
// 16:9), centred on the front.
const float k_LonSpan = 124.f;
const float k_LatSpan = 70.f;

struct Sector {
	std::string id;
	float x = 0, y = 0;
	std::string owner, attacker, from;
	int stage = 0;
	float weight = 1.f;
};

struct Link {
	std::string a, b, side;
};

struct Rgba {
	float r, g, b, a;
};

const Rgba k_Red = { 214, 66, 52, 92 };
const Rgba k_Blu = { 66, 130, 196, 92 };
const Rgba k_RedArrow = { 236, 92, 66, 255 };
const Rgba k_BluArrow = { 98, 162, 226, 255 };
const Rgba k_RedRoad = { 236, 98, 80, 220 };
const Rgba k_BluRoad = { 116, 172, 226, 220 };
const Rgba k_Gold = { 255, 205, 90, 255 };

Rgba Alpha( Rgba c, float a )
{
	return { c.r, c.g, c.b, a };
}
Rgba Shade( Rgba c, float k )
{
	return { std::min( 255.f, c.r * k ), std::min( 255.f, c.g * k ), std::min( 255.f, c.b * k ), c.a };
}

uint32_t Hash( int a, int b )
{
	uint32_t h = (uint32_t)a * 73856093u ^ (uint32_t)b * 19349663u;
	h ^= h >> 13;
	h *= 0x5bd1e995u;
	h ^= h >> 15;
	return h;
}

//-----------------------------------------------------------------------------
// The planet's surface: generated, not drawn by anyone. An equirectangular
// map (longitude across, latitude down) of a dry, warm world -- the theater's
// continent in the middle, seas around it, other land beyond -- and a sheet of
// clouds. Noise is sampled on the sphere, so there is no seam at the date line
// and no pinching at the poles.
//-----------------------------------------------------------------------------
float Hash3( int x, int y, int z, int seed )
{
	uint32_t h = (uint32_t)x * 374761393u + (uint32_t)y * 668265263u + (uint32_t)z * 2147483647u + (uint32_t)seed * 144665u;
	h = ( h ^ ( h >> 13 ) ) * 1274126177u;
	return ( ( h ^ ( h >> 16 ) ) & 0xffffff ) / float( 0xffffff );
}

float ValueNoise( float x, float y, float z, int seed )
{
	const int xi = (int)std::floor( x ), yi = (int)std::floor( y ), zi = (int)std::floor( z );
	float fx = x - xi, fy = y - yi, fz = z - zi;
	fx = fx * fx * ( 3 - 2 * fx );
	fy = fy * fy * ( 3 - 2 * fy );
	fz = fz * fz * ( 3 - 2 * fz );
	auto lerp = []( float a, float b, float t ) { return a + ( b - a ) * t; };
	float c[ 2 ][ 2 ];
	for ( int j = 0; j < 2; ++j )
		for ( int k = 0; k < 2; ++k )
			c[ j ][ k ] = lerp( Hash3( xi, yi + j, zi + k, seed ), Hash3( xi + 1, yi + j, zi + k, seed ), fx );
	return lerp( lerp( c[ 0 ][ 0 ], c[ 1 ][ 0 ], fy ), lerp( c[ 0 ][ 1 ], c[ 1 ][ 1 ], fy ), fz );
}

float Fbm( float x, float y, float z, int seed, int octaves )
{
	float sum = 0, amp = 0.5f, norm = 0;
	for ( int i = 0; i < octaves; ++i )
	{
		sum += amp * ValueNoise( x, y, z, seed + i * 17 );
		norm += amp;
		x *= 2.03f;
		y *= 2.03f;
		z *= 2.03f;
		amp *= 0.5f;
	}
	return sum / norm;
}

struct PlanetMaps {
	int w = 0, h = 0;
	std::vector< Rml::byte > surface;	// premultiplied RGBA
	std::vector< Rml::byte > clouds;
};

float Smooth( float a, float b, float x )
{
	const float t = std::clamp( ( x - a ) / ( b - a ), 0.f, 1.f );
	return t * t * ( 3 - 2 * t );
}

// Height above the sea at a point of the unit sphere, about -1..1.
float Elevation( float x, float y, float z, float lon, float lat )
{
	float e = Fbm( x * 1.7f + 11.f, y * 1.7f + 3.f, z * 1.7f, 7, 6 ) * 2.f - 1.f;
	// The theater's continent, centred on the front of the planet (lon 0),
	// and two more out of sight.
	auto blob = [ & ]( float lon0, float lat0, float rLon, float rLat, float strength ) {
		float dl = std::fabs( lon - lon0 );
		dl = std::min( dl, 360.f - dl );
		const float d = std::sqrt( ( dl / rLon ) * ( dl / rLon ) + ( ( lat - lat0 ) / rLat ) * ( ( lat - lat0 ) / rLat ) );
		return strength * ( 1.f - Smooth( 0.55f, 1.15f, d ) );
	};
	e += blob( 0.f, 0.f, 80.f, 48.f, 0.62f );
	e += blob( 150.f, 30.f, 50.f, 30.f, 0.42f );
	e += blob( -130.f, -25.f, 45.f, 35.f, 0.4f );
	return e - 0.18f;
}

const PlanetMaps &Planet()
{
	static PlanetMaps maps;
	if ( !maps.surface.empty() )
		return maps;
	const int W = 1024, H = 512;
	maps.w = W;
	maps.h = H;
	maps.surface.resize( W * H * 4 );
	maps.clouds.resize( W * H * 4 );
	std::vector< float > elev( W * H );
	for ( int j = 0; j < H; ++j )
	{
		const float lat = 90.f - 180.f * ( j + 0.5f ) / H;
		for ( int i = 0; i < W; ++i )
		{
			const float lon = -180.f + 360.f * ( i + 0.5f ) / W;
			const float cl = std::cos( lat * k_Deg );
			const float x = cl * std::sin( lon * k_Deg ), y = std::sin( lat * k_Deg ), z = cl * std::cos( lon * k_Deg );
			elev[ j * W + i ] = Elevation( x, y, z, lon, lat );
		}
	}
	auto mix = []( Rgba a, Rgba b, float t ) {
		t = std::clamp( t, 0.f, 1.f );
		return Rgba{ a.r + ( b.r - a.r ) * t, a.g + ( b.g - a.g ) * t, a.b + ( b.b - a.b ) * t, 255 };
	};
	const Rgba deep = { 16, 34, 44, 255 }, shallow = { 40, 86, 94, 255 }, sand = { 200, 170, 114, 255 };
	const Rgba ochre = { 170, 122, 70, 255 }, olive = { 108, 108, 64, 255 }, hills = { 126, 86, 54, 255 };
	const Rgba rock = { 168, 150, 124, 255 }, snow = { 236, 230, 216, 255 }, ice = { 222, 228, 232, 255 };
	for ( int j = 0; j < H; ++j )
	{
		const float lat = 90.f - 180.f * ( j + 0.5f ) / H;
		for ( int i = 0; i < W; ++i )
		{
			const float lon = -180.f + 360.f * ( i + 0.5f ) / W;
			const float e = elev[ j * W + i ];
			const float cl = std::cos( lat * k_Deg );
			const float x = cl * std::sin( lon * k_Deg ), y = std::sin( lat * k_Deg ), z = cl * std::cos( lon * k_Deg );
			Rgba c;
			if ( e < 0.f )
			{
				c = mix( deep, shallow, 1.f + e / 0.35f );
				// Lighter water off the coast.
				c = mix( c, Rgba{ 70, 120, 120, 255 }, Smooth( -0.06f, 0.f, e ) * 0.5f );
			}
			else
			{
				const float wet = Fbm( x * 3.f + 5.f, y * 3.f, z * 3.f - 2.f, 29, 4 );
				c = mix( ochre, olive, Smooth( 0.45f, 0.65f, wet ) );
				c = mix( sand, c, Smooth( 0.f, 0.05f, e ) );
				c = mix( c, hills, Smooth( 0.22f, 0.4f, e ) );
				c = mix( c, rock, Smooth( 0.4f, 0.55f, e ) );
				c = mix( c, snow, Smooth( 0.58f, 0.7f, e ) );
				// Light from the north-west: slopes facing it are brighter.
				const int i2 = ( i + W - 2 ) % W, j2 = std::max( 0, j - 2 );
				const float slope = ( e - elev[ j2 * W + i2 ] ) * 9.f;
				const float k = std::clamp( 1.f + slope, 0.7f, 1.3f );
				c = { std::min( 255.f, c.r * k ), std::min( 255.f, c.g * k ), std::min( 255.f, c.b * k ), 255 };
			}
			// Polar ice.
			c = mix( c, ice, Smooth( 64.f, 76.f, std::fabs( lat ) + Fbm( x * 4.f, y * 4.f, z * 4.f, 41, 3 ) * 10.f ) );
			Rml::byte *px = &maps.surface[ ( j * W + i ) * 4 ];
			px[ 0 ] = (Rml::byte)c.r;
			px[ 1 ] = (Rml::byte)c.g;
			px[ 2 ] = (Rml::byte)c.b;
			px[ 3 ] = 255;

			// Clouds: soft bands, thicker at mid latitudes.
			const float n = Fbm( x * 2.4f + 40.f, y * 4.f, z * 2.4f, 53, 5 );
			const float a = Smooth( 0.52f, 0.72f, n ) * ( 0.55f + 0.45f * std::sin( std::fabs( lat ) * k_Deg * 2.f ) ) * 230.f;
			Rml::byte *cp = &maps.clouds[ ( j * W + i ) * 4 ];
			cp[ 3 ] = (Rml::byte)a;
			cp[ 0 ] = cp[ 1 ] = (Rml::byte)( 250 * a / 255 );
			cp[ 2 ] = (Rml::byte)( 244 * a / 255 );
		}
	}
	return maps;
}

// A point on screen, its depth (1 facing us, 0 on the rim) and its light.
struct Proj {
	float x, y, z, light;
	bool Front() const { return z > 0.f; }
};

// A mesh in screen pixels, relative to the element's content box.
class Builder {
public:
	void Clear() { m_Mesh = Rml::Mesh(); }
	bool Empty() const { return m_Mesh.indices.empty(); }
	Rml::Mesh Take() { return std::move( m_Mesh ); }

	int Vertex( float x, float y, Rgba c, float u = 0.f, float v = 0.f )
	{
		Rml::Vertex vert;
		vert.position = { x, y };
		vert.colour = Rml::Colourb( (Rml::byte)std::clamp( c.r, 0.f, 255.f ), (Rml::byte)std::clamp( c.g, 0.f, 255.f ),
			(Rml::byte)std::clamp( c.b, 0.f, 255.f ), (Rml::byte)std::clamp( c.a, 0.f, 255.f ) ).ToPremultiplied();
		vert.tex_coord = { u, v };
		m_Mesh.vertices.push_back( vert );
		return (int)m_Mesh.vertices.size() - 1;
	}
	void Tri( int a, int b, int c )
	{
		m_Mesh.indices.push_back( a );
		m_Mesh.indices.push_back( b );
		m_Mesh.indices.push_back( c );
	}
	void Quad( int a, int b, int c, int d )
	{
		Tri( a, b, c );
		Tri( a, c, d );
	}
	// A screen-space segment.
	void Line( float ax, float ay, float bx, float by, float w, Rgba c )
	{
		float dx = bx - ax, dy = by - ay;
		const float len = std::sqrt( dx * dx + dy * dy );
		if ( len < 0.01f )
			return;
		dx /= len;
		dy /= len;
		const float h = w * 0.5f;
		ax -= dx * h * 0.5f;
		ay -= dy * h * 0.5f;
		bx += dx * h * 0.5f;
		by += dy * h * 0.5f;
		const float nx = -dy * h, ny = dx * h;
		Quad( Vertex( ax + nx, ay + ny, c ), Vertex( bx + nx, by + ny, c ), Vertex( bx - nx, by - ny, c ), Vertex( ax - nx, ay - ny, c ) );
	}
	void Disc( float x, float y, float r, Rgba c, int segments = 10 )
	{
		const int centre = Vertex( x, y, c );
		int first = -1, prev = -1;
		for ( int i = 0; i < segments; ++i )
		{
			const float a = 2 * k_Pi * i / segments;
			const int v = Vertex( x + std::cos( a ) * r, y + std::sin( a ) * r, c );
			if ( prev >= 0 )
				Tri( centre, prev, v );
			else
				first = v;
			prev = v;
		}
		Tri( centre, prev, first );
	}
	// A ring between two radii, fading from one colour to another.
	void Ring( float x, float y, float r0, float r1, Rgba c0, Rgba c1, int segments = 96 )
	{
		int prev0 = -1, prev1 = -1, first0 = -1, first1 = -1;
		for ( int i = 0; i < segments; ++i )
		{
			const float a = 2 * k_Pi * i / segments, ca = std::cos( a ), sa = std::sin( a );
			const int v0 = Vertex( x + ca * r0, y + sa * r0, c0 ), v1 = Vertex( x + ca * r1, y + sa * r1, c1 );
			if ( prev0 >= 0 )
				Quad( prev0, prev1, v1, v0 );
			else
			{
				first0 = v0;
				first1 = v1;
			}
			prev0 = v0;
			prev1 = v1;
		}
		Quad( prev0, prev1, first1, first0 );
	}

private:
	Rml::Mesh m_Mesh;
};

//-----------------------------------------------------------------------------
class ElementWarMap final : public Rml::Element {
public:
	ElementWarMap( const Rml::String &tag, Runtime *pRuntime ) : Rml::Element( tag ), m_pRuntime( pRuntime )
	{
		// Stars, once.
		for ( int i = 0; i < 220; ++i )
		{
			const uint32_t h = Hash( i, 977 );
			m_Stars.push_back( { ( h & 0xffff ) / 65535.f, ( ( h >> 16 ) & 0xffff ) / 65535.f, 0.4f + ( Hash( i, 31 ) & 255 ) / 255.f * 1.4f, ( Hash( i, 7 ) & 255 ) / 40.f } );
		}
	}

protected:
	void OnUpdate() override
	{
		Read();
		Animate();
		PlaceMarkers();
	}

	void OnRender() override
	{
		Rml::RenderManager *pRender = GetRenderManager();
		if ( !pRender )
			return;
		const Rml::Vector2f size = GetBox().GetSize( Rml::BoxArea::Content );
		if ( size.x < 1 || size.y < 1 )
			return;
		const Rml::Vector2f offset = GetAbsoluteOffset( Rml::BoxArea::Content );
		Fit( size );

		if ( !m_bTextureTried )
		{
			// The planet: generated, unless the document names a picture
			// (an equirectangular map) to use instead.
			m_bTextureTried = true;
			const Rml::String source = GetAttribute< Rml::String >( "texture", "" );
			if ( !source.empty() && source != "planet" )
				m_Texture = pRender->LoadTexture( source );
			else
			{
				m_Surface = pRender->MakeCallbackTexture( []( const Rml::CallbackTextureInterface &ti ) {
					const PlanetMaps &maps = Planet();
					return ti.GenerateTexture( { maps.surface.data(), maps.surface.size() }, { maps.w, maps.h } );
				} );
				m_Clouds = pRender->MakeCallbackTexture( []( const Rml::CallbackTextureInterface &ti ) {
					const PlanetMaps &maps = Planet();
					return ti.GenerateTexture( { maps.clouds.data(), maps.clouds.size() }, { maps.w, maps.h } );
				} );
				m_Texture = m_Surface;
			}
		}
		if ( m_bDirty )
		{
			BuildRegions();
			m_bDirty = false;
		}

		// Each layer its own mesh: the game draws a dynamic mesh at a time and
		// those have a size limit.
		auto draw = [ & ]( Rml::Texture texture = {} ) {
			if ( m_Builder.Empty() )
				return;
			Rml::Geometry geometry = pRender->MakeGeometry( m_Builder.Take() );
			geometry.Render( offset, texture );
		};
		BuildSky();
		draw();
		BuildGlobe();
		draw( m_Texture );
		BuildGrid();
		draw();
		BuildTerritory();
		draw();
		if ( m_Clouds )
		{
			BuildClouds();
			draw( m_Clouds );
		}
		BuildLines();
		draw();
		BuildLive();
		draw();
		BuildRim();
		draw();
	}

	void ProcessDefaultAction( Rml::Event &event ) override
	{
		Rml::Element::ProcessDefaultAction( event );
		const Rml::Vector2f mouse( event.GetParameter< float >( "mouse_x", 0.f ), event.GetParameter< float >( "mouse_y", 0.f ) );
		const Rml::Vector2f local = mouse - GetAbsoluteOffset( Rml::BoxArea::Content );
		switch ( event.GetId() )
		{
		case Rml::EventId::Dragstart:
			m_DragFrom = local;
			m_DragLon = m_Lon;
			m_DragLat = m_Lat;
			m_SpinLon = m_SpinLat = 0.f;
			m_flDragAt = Now();
			m_bDragging = true;
			m_bDragged = false;
			break;
		case Rml::EventId::Drag:
			if ( m_bDragging )
			{
				const Rml::Vector2f d = local - m_DragFrom;
				m_bDragged = m_bDragged || std::fabs( d.x ) + std::fabs( d.y ) > 5.f;
				const float lon = m_DragLon - d.x / m_R / k_Deg;
				const float lat = std::clamp( m_DragLat + d.y / m_R / k_Deg, -75.f, 75.f );
				// How fast it is being turned, for the spin it keeps when let go.
				const float dt = std::max( 0.001f, Now() - m_flDragAt );
				m_SpinLon = 0.7f * m_SpinLon + 0.3f * ( lon - m_Lon ) / dt;
				m_SpinLat = 0.7f * m_SpinLat + 0.3f * ( lat - m_Lat ) / dt;
				m_flDragAt = Now();
				m_Lon = m_TargetLon = lon;
				m_Lat = m_TargetLat = lat;
				m_flIdleSince = Now();
			}
			break;
		case Rml::EventId::Dragend:
			m_bDragging = false;
			if ( Now() - m_flDragAt > 0.12f )
				m_SpinLon = m_SpinLat = 0.f;	// held still before letting go
			break;
		case Rml::EventId::Mousescroll:
			m_Zoom = std::clamp( m_Zoom * ( event.GetParameter< float >( "wheel_delta_y", 0.f ) > 0 ? 0.9f : 1.1f ), 0.8f, 1.9f );
			m_flIdleSince = Now();
			break;
		case Rml::EventId::Mouseout:
			if ( event.GetTargetElement() == this )
				SetHover( "" );
			break;
		case Rml::EventId::Mousemove:
			if ( event.GetTargetElement() == this && !m_bDragging )
				SetHover( SectorAt( local.x, local.y ) );
			break;
		case Rml::EventId::Click:
			if ( event.GetTargetElement() == this && !m_bDragged )
			{
				const std::string sector = SectorAt( local.x, local.y );
				const std::string path = GetAttribute< Rml::String >( "select", "" );
				if ( !sector.empty() && !path.empty() )
					m_pRuntime->Set( path, Node( sector ) );
			}
			m_bDragged = false;
			break;
		default:
			break;
		}
	}

private:
	struct Segment {
		float ax, ay, bx, by;
	};
	struct Hex {
		float x, y;
		int sector;
		bool blu;
		float alpha;
	};
	struct Star {
		float x, y, size, phase;
	};

	static float Now() { return (float)Rml::GetSystemInterface()->GetElapsedTime(); }
	float TheaterW() const { return GetAttribute< float >( "width", 1140.f ); }
	float TheaterH() const { return GetAttribute< float >( "height", 640.f ); }

	static std::string Str( const Node *pNode, const char *key )
	{
		const Node *p = pNode ? pNode->Find( key ) : nullptr;
		return p ? p->AsString() : std::string();
	}
	static double Num( const Node *pNode, const char *key )
	{
		const Node *p = pNode ? pNode->Find( key ) : nullptr;
		return p ? p->AsNumber() : 0.0;
	}

	//-------------------------------------------------------------------------
	// State
	void Read()
	{
		Node *pWar = m_pRuntime->Get( GetAttribute< Rml::String >( "source", "war" ) );
		std::string key;
		std::vector< Sector > sectors;
		std::vector< Link > links;
		if ( Node *pFronts = pWar ? pWar->Find( "fronts" ) : nullptr )
		{
			for ( size_t i = 0; i < pFronts->Size(); ++i )
			{
				const Node *f = pFronts->At( i );
				Sector s;
				s.id = Str( f, "id" );
				s.x = (float)Num( f, "x" );
				s.y = (float)Num( f, "y" );
				s.owner = Str( f, "owner" );
				s.attacker = Str( f, "attacker" );
				s.from = Str( f, "from" );
				s.stage = (int)Num( f, "stage" );
				const Node *hq = f->Find( "hq" );
				s.weight = hq && hq->AsBool() ? 1.25f : 1.f;
				key += s.id + ":" + std::to_string( (int)s.x ) + "," + std::to_string( (int)s.y ) + s.owner + s.attacker + s.from + std::to_string( s.stage ) + ";";
				sectors.push_back( std::move( s ) );
			}
		}
		if ( Node *pLinks = pWar ? pWar->Find( "links" ) : nullptr )
		{
			for ( size_t i = 0; i < pLinks->Size(); ++i )
			{
				const Node *l = pLinks->At( i );
				links.push_back( { Str( l, "a" ), Str( l, "b" ), Str( l, "side" ) } );
				key += links.back().a + "-" + links.back().b + links.back().side + ";";
			}
		}
		if ( key != m_Key )
		{
			m_Key = key;
			m_Sectors = std::move( sectors );
			m_Links = std::move( links );
			m_bDirty = true;
		}
		const std::string select = GetAttribute< Rml::String >( "select", "" );
		const Node *pSelected = select.empty() ? nullptr : m_pRuntime->Get( select );
		const std::string selected = pSelected ? pSelected->AsString() : std::string();
		if ( selected != m_Selected )
		{
			m_Selected = selected;
			// Turn the planet to it.
			if ( const Sector *s = Find( selected ) )
			{
				float lon, lat;
				ToLonLat( s->x, s->y, lon, lat );
				m_TargetLon = lon;
				m_TargetLat = std::clamp( lat * 0.6f, -30.f, 30.f );
				m_flIdleSince = Now();
			}
		}
	}

	void Animate()
	{
		const float now = Now();
		const float dt = std::clamp( now - m_flLast, 0.f, 0.1f );
		m_flLast = now;
		if ( !m_bDragging )
		{
			if ( std::fabs( m_SpinLon ) + std::fabs( m_SpinLat ) > 0.5f )
			{
				// Let go while turning: it keeps turning, and slows down.
				m_Lon += m_SpinLon * dt;
				m_Lat = std::clamp( m_Lat + m_SpinLat * dt, -75.f, 75.f );
				const float drag = std::exp( -dt * 2.2f );
				m_SpinLon *= drag;
				m_SpinLat *= drag;
				m_TargetLon = m_Lon;
				m_TargetLat = m_Lat;
				m_flIdleSince = now;
			}
			else
			{
				m_SpinLon = m_SpinLat = 0.f;
				const float k = std::min( 1.f, dt * 3.f );
				m_Lon += ( m_TargetLon - m_Lon ) * k;
				m_Lat += ( m_TargetLat - m_Lat ) * k;
			}
		}
		// Left alone, it breathes: a slow sway, never a spin.
		const float idle = std::clamp( ( now - m_flIdleSince - 4.f ) / 6.f, 0.f, 1.f );
		m_Sway = idle * 7.f * std::sin( now * 0.07f );
	}

	//-------------------------------------------------------------------------
	// The globe's geometry
	void Fit( const Rml::Vector2f &size )
	{
		m_W = size.x;
		m_H = size.y;
		m_Cx = size.x * GetAttribute< float >( "center-x", 0.5f );
		m_Cy = size.y * GetAttribute< float >( "center-y", 0.52f );
		// Sized on the 16:9 part of the element: a taller window (16:10, 4:3)
		// must not grow the planet into the panels at its sides.
		m_R = std::min( size.x * 0.5625f, size.y ) * GetAttribute< float >( "radius", 0.47f ) * m_Zoom;
	}

	void ToLonLat( float x, float y, float &lon, float &lat ) const
	{
		lon = ( x / TheaterW() - 0.5f ) * k_LonSpan;
		lat = ( 0.5f - y / TheaterH() ) * k_LatSpan;
	}

	Proj ProjectLonLat( float lon, float lat ) const
	{
		const float l = ( lon - m_Lon - m_Sway ) * k_Deg, p = lat * k_Deg, tilt = m_Lat * k_Deg;
		const float x = std::cos( p ) * std::sin( l );
		const float y = std::sin( p );
		const float z = std::cos( p ) * std::cos( l );
		const float y2 = y * std::cos( tilt ) - z * std::sin( tilt );
		const float z2 = y * std::sin( tilt ) + z * std::cos( tilt );
		// Light from the upper left, a little in front.
		const float lx = -0.55f, ly = 0.55f, lz = 0.63f;
		const float light = 0.28f + 0.72f * std::max( 0.f, x * lx + y2 * ly + z2 * lz );
		return { m_Cx + x * m_R, m_Cy - y2 * m_R, z2, light };
	}

	Proj Project( float x, float y ) const
	{
		float lon, lat;
		ToLonLat( x, y, lon, lat );
		return ProjectLonLat( lon, lat );
	}

	// Screen to theater; false off the planet.
	bool Unproject( float sx, float sy, float &x, float &y ) const
	{
		const float X = ( sx - m_Cx ) / m_R, Y = ( m_Cy - sy ) / m_R;
		const float d = X * X + Y * Y;
		if ( d >= 1.f )
			return false;
		const float Z = std::sqrt( 1.f - d ), tilt = m_Lat * k_Deg;
		const float y0 = Y * std::cos( tilt ) + Z * std::sin( tilt );
		const float z0 = -Y * std::sin( tilt ) + Z * std::cos( tilt );
		const float lat = std::asin( std::clamp( y0, -1.f, 1.f ) ) / k_Deg;
		const float lon = std::atan2( X, z0 ) / k_Deg + m_Lon + m_Sway;
		x = ( lon / k_LonSpan + 0.5f ) * TheaterW();
		y = ( 0.5f - lat / k_LatSpan ) * TheaterH();
		return true;
	}

	const Sector *Find( const std::string &id ) const
	{
		for ( const Sector &s : m_Sectors )
		{
			if ( s.id == id )
				return &s;
		}
		return nullptr;
	}

	int Nearest( float x, float y ) const
	{
		int best = -1;
		float bestD = 1e30f;
		for ( size_t i = 0; i < m_Sectors.size(); ++i )
		{
			const float dx = m_Sectors[ i ].x - x, dy = m_Sectors[ i ].y - y;
			const float d = std::sqrt( dx * dx + dy * dy ) / m_Sectors[ i ].weight;
			if ( d < bestD )
			{
				bestD = d;
				best = (int)i;
			}
		}
		return best;
	}

	std::string SectorAt( float sx, float sy )
	{
		float x, y;
		if ( !Unproject( sx, sy, x, y ) || x < 0 || y < 0 || x > TheaterW() || y > TheaterH() )
			return std::string();
		const int i = Nearest( x, y );
		return i >= 0 ? m_Sectors[ i ].id : std::string();
	}

	void SetHover( const std::string &id )
	{
		if ( id == m_Hover )
			return;
		m_Hover = id;
		const std::string path = GetAttribute< Rml::String >( "hover", "" );
		if ( !path.empty() )
			m_pRuntime->Set( path, Node( id ) );
	}

	// Sector markers ride the planet; the far side hides them.
	void PlaceMarkers()
	{
		if ( m_R <= 0 )
			return;
		for ( int i = 0; i < GetNumChildren(); ++i )
			PlaceIn( GetChild( i ) );
	}
	void PlaceIn( Rml::Element *pElement )
	{
		const Rml::String node = pElement->GetAttribute< Rml::String >( "node", "" );
		if ( node.empty() )
		{
			for ( int i = 0; i < pElement->GetNumChildren(); ++i )
			{
				if ( pElement->GetChild( i )->HasAttribute( "node" ) )
					PlaceIn( pElement->GetChild( i ) );
			}
			return;
		}
		const Sector *s = Find( node );
		if ( !s )
			return;
		const Proj p = Project( s->x, s->y );
		const bool bShown = p.z > 0.18f;
		const float x = std::round( p.x ), y = std::round( p.y );
		const Rml::Property *pLeft = pElement->GetLocalProperty( Rml::PropertyId::Left );
		const Rml::Property *pTop = pElement->GetLocalProperty( Rml::PropertyId::Top );
		if ( !pLeft || !pTop || pLeft->Get< float >() != x || pTop->Get< float >() != y )
		{
			pElement->SetProperty( Rml::PropertyId::Left, Rml::Property( x, Rml::Unit::PX ) );
			pElement->SetProperty( Rml::PropertyId::Top, Rml::Property( y, Rml::Unit::PX ) );
		}
		const bool bWasShown = pElement->GetAttribute< int >( "data-shown", 1 ) != 0;
		if ( bShown != bWasShown )
		{
			pElement->SetAttribute( "data-shown", bShown ? 1 : 0 );
			pElement->SetProperty( "visibility", bShown ? "visible" : "hidden" );
		}
	}

	//-------------------------------------------------------------------------
	// Which sector every hex belongs to, and the edges between sectors.
	// Rebuilt when the war changes; projected every frame.
	void BuildRegions()
	{
		m_Hexes.clear();
		m_Borders.clear();
		m_Front.clear();
		m_Outlines.clear();
		m_Regions.clear();
		if ( m_Sectors.empty() )
			return;
		const float W = TheaterW(), H = TheaterH();
		const float R = m_HexR;
		const float hexW = k_Sqrt3 * R, row = 1.5f * R;
		const int cols = (int)std::ceil( W / hexW ) + 1, rows = (int)std::ceil( H / row ) + 1;
		std::vector< int > grid( cols * rows, -1 );
		auto centre = [ & ]( int c, int r, float &x, float &y ) {
			x = c * hexW + ( ( r & 1 ) ? hexW * 0.5f : 0.f );
			y = r * row;
		};
		for ( int r = 0; r < rows; ++r )
		{
			for ( int c = 0; c < cols; ++c )
			{
				float x, y;
				centre( c, r, x, y );
				const int sector = Nearest( x, y );
				grid[ r * cols + c ] = sector;
				const float edge = std::min( std::min( x, W - x ), std::min( y, H - y ) );
				Hex hex;
				hex.x = x;
				hex.y = y;
				hex.sector = sector;
				hex.blu = m_Sectors[ sector ].owner == "blu";
				hex.alpha = ( 0.35f + 0.65f * std::clamp( edge / 90.f, 0.f, 1.f ) ) * ( 0.8f + 0.35f * ( ( Hash( c, r ) & 255 ) / 255.f ) );
				m_Regions[ m_Sectors[ sector ].id ].push_back( (int)m_Hexes.size() );
				m_Hexes.push_back( hex );
			}
		}
		for ( int r = 0; r < rows; ++r )
		{
			for ( int c = 0; c < cols; ++c )
			{
				float x, y;
				centre( c, r, x, y );
				const int here = grid[ r * cols + c ];
				const int odd = r & 1;
				const int nb[ 3 ][ 2 ] = { { c + odd, r - 1 }, { c + 1, r }, { c + odd, r + 1 } };
				for ( int e = 0; e < 3; ++e )
				{
					const int nc = nb[ e ][ 0 ], nr = nb[ e ][ 1 ];
					if ( nc < 0 || nr < 0 || nc >= cols || nr >= rows )
						continue;
					const int there = grid[ nr * cols + nc ];
					if ( there == here )
						continue;
					const float a0 = ( -90.f + 60.f * e ) * k_Deg, a1 = ( -90.f + 60.f * ( e + 1 ) ) * k_Deg;
					const Segment s = { x + std::cos( a0 ) * R, y + std::sin( a0 ) * R, x + std::cos( a1 ) * R, y + std::sin( a1 ) * R };
					( m_Sectors[ here ].owner != m_Sectors[ there ].owner ? m_Front : m_Borders ).push_back( s );
					m_Outlines[ m_Sectors[ here ].id ].push_back( s );
					m_Outlines[ m_Sectors[ there ].id ].push_back( s );
				}
			}
		}
	}

	//-------------------------------------------------------------------------
	// Layers, back to front.
	void BuildSky()
	{
		m_Builder.Clear();
		const float t = Now();
		for ( const Star &s : m_Stars )
		{
			const float twinkle = 0.55f + 0.45f * std::sin( t * 0.8f + s.phase );
			m_Builder.Disc( s.x * m_W, s.y * m_H, s.size, { 255, 240, 220, 150.f * twinkle }, 6 );
		}
		// Glow behind the planet.
		m_Builder.Ring( m_Cx, m_Cy, m_R * 0.98f, m_R * 1.35f, { 255, 190, 110, 70 }, { 255, 190, 110, 0 } );
	}

	// A lit sphere with an equirectangular map on it: the whole planet, the
	// far side culled.
	void Sphere( float radius, float lonShift, bool bLit, float alpha, int nLat = 30, int nLon = 60 )
	{
		std::vector< int > idx( ( nLat + 1 ) * ( nLon + 1 ) );
		std::vector< float > depth( idx.size() );
		const float r0 = m_R;
		m_R = r0 * radius;
		for ( int i = 0; i <= nLat; ++i )
		{
			const float lat = 90.f - 180.f * i / nLat;
			for ( int j = 0; j <= nLon; ++j )
			{
				const float lon = -180.f + 360.f * j / nLon;
				const Proj p = ProjectLonLat( lon + lonShift, lat );
				const float k = bLit ? 0.1f + 0.95f * p.light : 1.f;
				const float a = alpha * ( bLit ? 1.f : std::clamp( 0.25f + p.light, 0.f, 1.f ) );
				idx[ i * ( nLon + 1 ) + j ] = m_Builder.Vertex( p.x, p.y, { 255 * k, 248 * k, 236 * k, 255 * a }, (float)j / nLon, (float)i / nLat );
				depth[ i * ( nLon + 1 ) + j ] = p.z;
			}
		}
		m_R = r0;
		for ( int i = 0; i < nLat; ++i )
		{
			for ( int j = 0; j < nLon; ++j )
			{
				const int a = i * ( nLon + 1 ) + j, b = a + 1, c = a + nLon + 1, d = c + 1;
				if ( depth[ a ] < -0.02f && depth[ b ] < -0.02f && depth[ c ] < -0.02f && depth[ d ] < -0.02f )
					continue;
				m_Builder.Quad( idx[ a ], idx[ b ], idx[ d ], idx[ c ] );
			}
		}
	}

	void BuildGlobe()
	{
		m_Builder.Clear();
		Sphere( 1.f, 0.f, true, 1.f );
	}

	// Clouds drift slowly eastward, a little above the ground.
	void BuildClouds()
	{
		m_Builder.Clear();
		Sphere( 1.006f, std::fmod( Now() * 0.8f, 360.f ), false, 0.55f );
	}

	void BuildGrid()
	{
		m_Builder.Clear();
		const Rgba c = { 255, 226, 170, 22 };
		const float lonMid = m_Lon + m_Sway;
		for ( float lat = -75.f; lat <= 75.f; lat += 15.f )
			Curve( [ & ]( float t, float &lo, float &la ) { lo = lonMid - 95.f + 190.f * t; la = lat; }, 48, 1.f, c );
		for ( float lon = std::floor( ( lonMid - 90.f ) / 15.f ) * 15.f; lon <= lonMid + 90.f; lon += 15.f )
			Curve( [ & ]( float t, float &lo, float &la ) { lo = lon; la = -90.f + 180.f * t; }, 36, 1.f, c );
	}

	template < typename F >
	void Curve( F at, int steps, float width, Rgba c )
	{
		Proj prev = {};
		for ( int i = 0; i <= steps; ++i )
		{
			float lon, lat;
			at( (float)i / steps, lon, lat );
			const Proj p = ProjectLonLat( lon, lat );
			if ( i > 0 && p.Front() && prev.Front() )
				m_Builder.Line( prev.x, prev.y, p.x, p.y, width, Alpha( c, c.a * std::min( 1.f, p.z * 3.f ) ) );
			prev = p;
		}
	}

	void HexAt( const Hex &hex, float r, Rgba c, bool bShadeByLight )
	{
		const Proj centre = Project( hex.x, hex.y );
		if ( centre.z < 0.02f )
			return;
		const Rgba col = bShadeByLight ? Shade( c, 0.55f + 0.6f * centre.light ) : c;
		const Rgba fade = Alpha( col, col.a * std::min( 1.f, centre.z * 4.f ) );
		const int mid = m_Builder.Vertex( centre.x, centre.y, fade );
		int first = -1, prev = -1;
		for ( int i = 0; i < 6; ++i )
		{
			const float a = ( -90.f + 60.f * i ) * k_Deg;
			const Proj p = Project( hex.x + std::cos( a ) * r, hex.y + std::sin( a ) * r );
			const int v = m_Builder.Vertex( p.x, p.y, fade );
			if ( prev >= 0 )
				m_Builder.Tri( mid, prev, v );
			else
				first = v;
			prev = v;
		}
		m_Builder.Tri( mid, prev, first );
	}

	void BuildTerritory()
	{
		m_Builder.Clear();
		for ( const Hex &hex : m_Hexes )
		{
			const Rgba base = hex.blu ? k_Blu : k_Red;
			HexAt( hex, m_HexR * 0.88f, Alpha( base, base.a * hex.alpha ), true );
		}
	}

	void Segments( const std::vector< Segment > &segments, float width, Rgba c )
	{
		for ( const Segment &s : segments )
		{
			const Proj a = Project( s.ax, s.ay ), b = Project( s.bx, s.by );
			if ( a.z > 0.02f && b.z > 0.02f )
				m_Builder.Line( a.x, a.y, b.x, b.y, width * ( 0.5f + 0.5f * a.z ), Alpha( c, c.a * std::min( 1.f, a.z * 3.f ) ) );
		}
	}

	void BuildLines()
	{
		m_Builder.Clear();
		const float k = m_R / 300.f;	// line widths grow with the planet
		Segments( m_Borders, 1.4f * k, { 20, 14, 10, 130 } );

		for ( const Link &l : m_Links )
		{
			const Sector *a = Find( l.a ), *b = Find( l.b );
			if ( !a || !b )
				continue;
			const bool bFront = l.side == "front";
			const Rgba c = bFront ? k_Gold : l.side == "blu" ? k_BluRoad : k_RedRoad;
			const int steps = 16;
			Proj prev = Project( a->x, a->y );
			for ( int i = 1; i <= steps; ++i )
			{
				const float t = (float)i / steps;
				const Proj p = Project( a->x + ( b->x - a->x ) * t, a->y + ( b->y - a->y ) * t );
				if ( p.z > 0.02f && prev.z > 0.02f )
				{
					m_Builder.Line( prev.x, prev.y, p.x, p.y, 5.f * k, { 14, 10, 8, 170 } );
					if ( !bFront || ( i & 1 ) )
						m_Builder.Line( prev.x, prev.y, p.x, p.y, 2.4f * k, c );
				}
				prev = p;
			}
		}

		Segments( m_Front, 14.f * k, { 255, 214, 130, 34 } );
		Segments( m_Front, 6.f * k, { 18, 12, 8, 210 } );
		Segments( m_Front, 3.f * k, { 255, 240, 210, 255 } );
	}

	// What moves: the targets pulse, the selection and hover glow, arrows flow.
	void BuildLive()
	{
		m_Builder.Clear();
		const float t = Now();
		const float r = m_HexR * 0.88f;
		for ( const Sector &s : m_Sectors )
		{
			if ( s.attacker.empty() )
				continue;
			const float pulse = 0.5f + 0.5f * std::sin( t * 3.2f );
			const Rgba c = Alpha( s.attacker == "blu" ? k_BluArrow : k_RedArrow, 26.f + 46.f * pulse );
			for ( int i : m_Regions[ s.id ] )
				HexAt( m_Hexes[ i ], r, c, false );
		}
		if ( !m_Hover.empty() && m_Hover != m_Selected )
		{
			for ( int i : m_Regions[ m_Hover ] )
				HexAt( m_Hexes[ i ], r, { 255, 255, 255, 26 }, false );
		}
		if ( !m_Selected.empty() )
		{
			for ( int i : m_Regions[ m_Selected ] )
				HexAt( m_Hexes[ i ], r, { 255, 240, 200, 40 }, false );
			Segments( m_Outlines[ m_Selected ], 3.f * m_R / 300.f, k_Gold );
		}
		for ( const Sector &s : m_Sectors )
		{
			const Sector *from = s.from.empty() ? nullptr : Find( s.from );
			if ( from && !s.attacker.empty() )
				Arrow( *from, s, std::max( 1, s.stage ), s.attacker == "blu" ? k_BluArrow : k_RedArrow, t );
		}
	}

	void Arrow( const Sector &from, const Sector &to, int stage, Rgba colour, float t )
	{
		// A bowed path in the theater plane, stopping short of both markers,
		// projected point by point.
		float dx = to.x - from.x, dy = to.y - from.y;
		const float len = std::sqrt( dx * dx + dy * dy );
		if ( len < 100.f )
			return;
		dx /= len;
		dy /= len;
		const float ax = from.x + dx * 44, ay = from.y + dy * 44, bx = to.x - dx * 58, by = to.y - dy * 58;
		const float cx = ( ax + bx ) * 0.5f - dy * len * 0.3f, cy = ( ay + by ) * 0.5f + dx * len * 0.3f;
		auto at = [ & ]( float u ) {
			const float v = 1.f - u;
			return Project( v * v * ax + 2 * v * u * cx + u * u * bx, v * v * ay + 2 * v * u * cy + u * u * by );
		};
		const int steps = 24;
		std::vector< Proj > pts;
		for ( int i = 0; i <= steps; ++i )
			pts.push_back( at( (float)i / steps ) );
		for ( const Proj &p : pts )
		{
			if ( p.z < 0.05f )
				return;	// round the far side: leave it out
		}
		const float k = m_R / 300.f;
		const float w = ( 5.f + 2.2f * stage ) * k;
		auto strip = [ & ]( float extra, Rgba col ) {
			int prevL = -1, prevR = -1;
			for ( int i = 0; i <= steps; ++i )
			{
				const Proj &a = pts[ std::max( 0, i - 1 ) ], &b = pts[ std::min( steps, i + 1 ) ];
				float tx = b.x - a.x, ty = b.y - a.y;
				const float tl = std::sqrt( tx * tx + ty * ty );
				if ( tl > 0.0001f )
				{
					tx /= tl;
					ty /= tl;
				}
				const float half = ( w * ( 0.55f + 0.45f * i / steps ) + extra ) * 0.5f;
				const int l = m_Builder.Vertex( pts[ i ].x - ty * half, pts[ i ].y + tx * half, col );
				const int r = m_Builder.Vertex( pts[ i ].x + ty * half, pts[ i ].y - tx * half, col );
				if ( prevL >= 0 )
					m_Builder.Quad( prevL, prevR, r, l );
				prevL = l;
				prevR = r;
			}
			const Proj &end = pts[ steps ], &before = pts[ steps - 1 ];
			float tx = end.x - before.x, ty = end.y - before.y;
			const float tl = std::max( 0.0001f, std::sqrt( tx * tx + ty * ty ) );
			tx /= tl;
			ty /= tl;
			const float hw = w * 1.25f + extra * 0.7f, hl = w * 1.6f + extra * 0.6f, back = extra * 0.35f;
			m_Builder.Tri( m_Builder.Vertex( end.x - tx * back - ty * hw, end.y - ty * back + tx * hw, col ),
				m_Builder.Vertex( end.x - tx * back + ty * hw, end.y - ty * back - tx * hw, col ),
				m_Builder.Vertex( end.x + tx * hl, end.y + ty * hl, col ) );
		};
		strip( 4.f * k, { 0, 0, 0, 150 } );
		strip( 0.f, colour );

		for ( int i = 0; i < 4; ++i )
		{
			const float u = std::fmod( t * 0.32f + i / 4.f, 1.f ) * 0.92f;
			const Proj p = at( u ), q = at( std::min( 1.f, u + 0.02f ) );
			float tx = q.x - p.x, ty = q.y - p.y;
			const float tl = std::max( 0.0001f, std::sqrt( tx * tx + ty * ty ) );
			tx /= tl;
			ty /= tl;
			const float fade = std::sin( u / 0.92f * k_Pi );
			const Rgba col = { 255, 246, 226, 170.f * fade };
			const float s = w * ( 0.55f + 0.45f * u ) * 0.36f;
			m_Builder.Line( p.x - tx * s - ty * s, p.y - ty * s + tx * s, p.x, p.y, 1.6f * k, col );
			m_Builder.Line( p.x - tx * s + ty * s, p.y - ty * s - tx * s, p.x, p.y, 1.6f * k, col );
		}
	}

	// The atmosphere's edge, over everything on the planet.
	void BuildRim()
	{
		m_Builder.Clear();
		m_Builder.Ring( m_Cx, m_Cy, m_R * 0.82f, m_R, { 10, 6, 4, 0 }, { 10, 6, 4, 150 } );
		m_Builder.Ring( m_Cx, m_Cy, m_R * 0.985f, m_R * 1.012f, { 255, 214, 150, 70 }, { 255, 214, 150, 0 } );
	}

	Runtime *m_pRuntime;
	Builder m_Builder;
	Rml::Texture m_Texture;
	Rml::CallbackTexture m_Surface, m_Clouds;
	bool m_bTextureTried = false;
	std::string m_Key;
	std::vector< Sector > m_Sectors;
	std::vector< Link > m_Links;
	std::vector< Hex > m_Hexes;
	std::vector< Segment > m_Borders, m_Front;
	std::map< std::string, std::vector< Segment > > m_Outlines;
	std::map< std::string, std::vector< int > > m_Regions;
	std::vector< Star > m_Stars;
	std::string m_Selected, m_Hover;
	bool m_bDirty = true;
	const float m_HexR = 19.f;

	// The view: where the planet faces, how big it is.
	float m_W = 0, m_H = 0, m_Cx = 0, m_Cy = 0, m_R = 0;
	float m_Lon = 0, m_Lat = 0, m_TargetLon = 0, m_TargetLat = 0, m_Sway = 0, m_Zoom = 1.f;
	float m_flLast = 0, m_flIdleSince = 0;
	bool m_bDragging = false, m_bDragged = false;
	Rml::Vector2f m_DragFrom = { 0, 0 };
	float m_DragLon = 0, m_DragLat = 0;
	float m_SpinLon = 0, m_SpinLat = 0, m_flDragAt = 0;
};

class WarMapInstancer final : public Rml::ElementInstancer {
public:
	explicit WarMapInstancer( Runtime *pRuntime ) : m_pRuntime( pRuntime ) {}
	Rml::ElementPtr InstanceElement( Rml::Element *, const Rml::String &tag, const Rml::XMLAttributes & ) override
	{
		return Rml::ElementPtr( new ElementWarMap( tag, m_pRuntime ) );
	}
	void ReleaseElement( Rml::Element *pElement ) override { delete pElement; }

private:
	Runtime *m_pRuntime;
};

} // namespace

std::unique_ptr< Rml::ElementInstancer > MakeWarMapInstancer( Runtime *pRuntime )
{
	return std::unique_ptr< Rml::ElementInstancer >( new WarMapInstancer( pRuntime ) );
}

} // namespace fui
