//========= Team Frontress ====================================================//
#include "tf2content.h"

#include <algorithm>
#include <cstring>
#include <fstream>
#include <sstream>

static std::string Lower( std::string s )
{
	std::transform( s.begin(), s.end(), s.begin(), []( unsigned char c ) { return (char)std::tolower( c ); } );
	std::replace( s.begin(), s.end(), '\\', '/' );
	return s;
}

static bool ReadAll( const std::string &path, std::string &out )
{
	std::ifstream f( path, std::ios::binary );
	if ( !f )
		return false;
	std::ostringstream ss;
	ss << f.rdbuf();
	out = ss.str();
	return true;
}

bool TF2Content::Mount( const std::string &tf2Dir )
{
	m_LooseRoots = { tf2Dir + "/tf/", tf2Dir + "/hl2/", tf2Dir + "/platform/" };
	bool bAny = false;
	for ( const char *pszVPK : { "/tf/tf2_misc_dir.vpk", "/tf/tf2_textures_dir.vpk", "/hl2/hl2_misc_dir.vpk", "/hl2/hl2_textures_dir.vpk",
			  "/platform/platform_misc_dir.vpk" } )
		bAny |= MountVPK( tf2Dir + pszVPK );
	return bAny;
}

bool TF2Content::MountVPK( const std::string &dirFile )
{
	std::string data;
	if ( !ReadAll( dirFile, data ) || data.size() < 12 )
		return false;
	uint32_t sig, version, treeSize;
	memcpy( &sig, &data[ 0 ], 4 );
	memcpy( &version, &data[ 4 ], 4 );
	memcpy( &treeSize, &data[ 8 ], 4 );
	if ( sig != 0x55aa1234 )
		return false;
	const size_t headerSize = version == 1 ? 12 : 28;
	const std::string base = dirFile.substr( 0, dirFile.size() - strlen( "_dir.vpk" ) );

	size_t i = headerSize;
	const size_t end = headerSize + treeSize;
	auto cstr = [ & ]() {
		const size_t j = data.find( '\0', i );
		std::string s = data.substr( i, j - i );
		i = j + 1;
		return s;
	};
	while ( i < end )
	{
		const std::string ext = cstr();
		if ( ext.empty() )
			break;
		for ( ;; )
		{
			const std::string dir = cstr();
			if ( dir.empty() )
				break;
			for ( ;; )
			{
				const std::string name = cstr();
				if ( name.empty() )
					break;
				uint16_t preloadBytes, archive;
				uint32_t offset, length;
				memcpy( &preloadBytes, &data[ i + 4 ], 2 );
				memcpy( &archive, &data[ i + 6 ], 2 );
				memcpy( &offset, &data[ i + 8 ], 4 );
				memcpy( &length, &data[ i + 12 ], 4 );
				i += 18;
				Entry entry;
				entry.preload = data.substr( i, preloadBytes );
				i += preloadBytes;
				entry.offset = offset;
				entry.length = length;
				if ( archive == 0x7fff )
				{
					entry.archive = dirFile;
					entry.offset += (uint32_t)end;
				}
				else
				{
					char buf[ 16 ];
					snprintf( buf, sizeof( buf ), "_%03u.vpk", archive );
					entry.archive = base + buf;
				}
				const std::string path = Lower( ( dir == " " ? std::string() : dir + "/" ) + name + "." + ext );
				m_Entries.emplace( path, std::move( entry ) );
			}
		}
	}
	return true;
}

bool TF2Content::ReadFile( const std::string &path, std::string &out ) const
{
	const std::string key = Lower( path );
	for ( const std::string &root : m_LooseRoots )
	{
		if ( ReadAll( root + path, out ) )
			return true;
	}
	auto it = m_Entries.find( key );
	if ( it == m_Entries.end() )
		return false;
	out = it->second.preload;
	if ( it->second.length )
	{
		std::ifstream f( it->second.archive, std::ios::binary );
		if ( !f )
			return false;
		f.seekg( it->second.offset );
		std::string rest( it->second.length, '\0' );
		f.read( &rest[ 0 ], it->second.length );
		out += rest;
	}
	return true;
}

//-----------------------------------------------------------------------------
// VTF
//-----------------------------------------------------------------------------
namespace {

enum {
	FMT_RGBA8888 = 0, FMT_ABGR8888 = 1, FMT_RGB888 = 2, FMT_BGR888 = 3, FMT_I8 = 5, FMT_IA88 = 6, FMT_A8 = 8,
	FMT_ARGB8888 = 11, FMT_BGRA8888 = 12, FMT_DXT1 = 13, FMT_DXT3 = 14, FMT_DXT5 = 15, FMT_BGRX8888 = 16, FMT_DXT1_ONEBITALPHA = 20,
};

size_t ImageSize( int fmt, int w, int h )
{
	w = std::max( w, 1 );
	h = std::max( h, 1 );
	switch ( fmt )
	{
	case FMT_DXT1:
	case FMT_DXT1_ONEBITALPHA: return (size_t)( ( w + 3 ) / 4 ) * ( ( h + 3 ) / 4 ) * 8;
	case FMT_DXT3:
	case FMT_DXT5: return (size_t)( ( w + 3 ) / 4 ) * ( ( h + 3 ) / 4 ) * 16;
	case FMT_RGB888:
	case FMT_BGR888: return (size_t)w * h * 3;
	case FMT_I8:
	case FMT_A8: return (size_t)w * h;
	case FMT_IA88: return (size_t)w * h * 2;
	default: return (size_t)w * h * 4;
	}
}

void Color565( uint16_t c, uint8_t *out )
{
	out[ 0 ] = (uint8_t)( ( ( c >> 11 ) & 31 ) * 255 / 31 );
	out[ 1 ] = (uint8_t)( ( ( c >> 5 ) & 63 ) * 255 / 63 );
	out[ 2 ] = (uint8_t)( ( c & 31 ) * 255 / 31 );
	out[ 3 ] = 255;
}

void DecodeDXT( const uint8_t *src, int fmt, int w, int h, uint8_t *rgba )
{
	const int bw = ( w + 3 ) / 4, bh = ( h + 3 ) / 4;
	const size_t blockSize = ( fmt == FMT_DXT1 || fmt == FMT_DXT1_ONEBITALPHA ) ? 8 : 16;
	for ( int by = 0; by < bh; ++by )
	{
		for ( int bx = 0; bx < bw; ++bx )
		{
			const uint8_t *block = src + ( (size_t)by * bw + bx ) * blockSize;
			uint8_t alpha[ 16 ];
			std::fill( alpha, alpha + 16, 255 );
			const uint8_t *colorBlock = block;
			if ( fmt == FMT_DXT3 )
			{
				for ( int i = 0; i < 16; ++i )
					alpha[ i ] = (uint8_t)( ( ( block[ i / 2 ] >> ( ( i & 1 ) * 4 ) ) & 15 ) * 17 );
				colorBlock = block + 8;
			}
			else if ( fmt == FMT_DXT5 )
			{
				uint8_t a[ 8 ] = { block[ 0 ], block[ 1 ] };
				if ( a[ 0 ] > a[ 1 ] )
					for ( int i = 2; i < 8; ++i ) a[ i ] = (uint8_t)( ( ( 8 - i ) * a[ 0 ] + ( i - 1 ) * a[ 1 ] ) / 7 );
				else
				{
					for ( int i = 2; i < 6; ++i ) a[ i ] = (uint8_t)( ( ( 6 - i ) * a[ 0 ] + ( i - 1 ) * a[ 1 ] ) / 5 );
					a[ 6 ] = 0;
					a[ 7 ] = 255;
				}
				uint64_t bits = 0;
				for ( int i = 0; i < 6; ++i )
					bits |= (uint64_t)block[ 2 + i ] << ( 8 * i );
				for ( int i = 0; i < 16; ++i )
					alpha[ i ] = a[ ( bits >> ( 3 * i ) ) & 7 ];
				colorBlock = block + 8;
			}

			const uint16_t c0 = colorBlock[ 0 ] | ( colorBlock[ 1 ] << 8 );
			const uint16_t c1 = colorBlock[ 2 ] | ( colorBlock[ 3 ] << 8 );
			uint8_t pal[ 4 ][ 4 ];
			Color565( c0, pal[ 0 ] );
			Color565( c1, pal[ 1 ] );
			const bool bFour = c0 > c1 || fmt != FMT_DXT1 && fmt != FMT_DXT1_ONEBITALPHA;
			for ( int k = 0; k < 3; ++k )
			{
				if ( bFour )
				{
					pal[ 2 ][ k ] = (uint8_t)( ( 2 * pal[ 0 ][ k ] + pal[ 1 ][ k ] ) / 3 );
					pal[ 3 ][ k ] = (uint8_t)( ( pal[ 0 ][ k ] + 2 * pal[ 1 ][ k ] ) / 3 );
				}
				else
				{
					pal[ 2 ][ k ] = (uint8_t)( ( pal[ 0 ][ k ] + pal[ 1 ][ k ] ) / 2 );
					pal[ 3 ][ k ] = 0;
				}
			}
			pal[ 2 ][ 3 ] = 255;
			pal[ 3 ][ 3 ] = bFour ? 255 : 0;
			const uint32_t indices = colorBlock[ 4 ] | ( colorBlock[ 5 ] << 8 ) | ( colorBlock[ 6 ] << 16 ) | ( (uint32_t)colorBlock[ 7 ] << 24 );
			for ( int i = 0; i < 16; ++i )
			{
				const int x = bx * 4 + ( i & 3 ), y = by * 4 + ( i >> 2 );
				if ( x >= w || y >= h )
					continue;
				uint8_t *p = rgba + ( (size_t)y * w + x ) * 4;
				const uint8_t *c = pal[ ( indices >> ( 2 * i ) ) & 3 ];
				p[ 0 ] = c[ 0 ];
				p[ 1 ] = c[ 1 ];
				p[ 2 ] = c[ 2 ];
				p[ 3 ] = (uint8_t)( c[ 3 ] * alpha[ i ] / 255 );
			}
		}
	}
}

} // namespace

bool TF2Content::LoadVTF( const std::string &name, std::vector< uint8_t > &rgba, int &w, int &h ) const
{
	std::string data;
	std::string path = "materials/" + name;
	if ( path.size() < 4 || path.compare( path.size() - 4, 4, ".vtf" ) != 0 )
		path += ".vtf";
	if ( !ReadFile( path, data ) || data.size() < 80 || data.compare( 0, 4, std::string( "VTF\0", 4 ) ) != 0 )
		return false;

	const uint8_t *p = (const uint8_t *)data.data();
	uint32_t vmajor, vminor, headerSize, flags, highFmt, lowFmt;
	uint16_t width, height, frames;
	memcpy( &vmajor, p + 4, 4 );
	memcpy( &vminor, p + 8, 4 );
	memcpy( &headerSize, p + 12, 4 );
	memcpy( &width, p + 16, 2 );
	memcpy( &height, p + 18, 2 );
	memcpy( &flags, p + 20, 4 );
	memcpy( &frames, p + 24, 2 );
	memcpy( &highFmt, p + 52, 4 );
	const int mips = p[ 56 ];
	memcpy( &lowFmt, p + 57, 4 );
	const int lowW = p[ 61 ], lowH = p[ 62 ];
	(void)vmajor;

	size_t offset = 0;
	if ( vminor >= 3 )
	{
		uint32_t numResources;
		memcpy( &numResources, p + 68, 4 );
		for ( uint32_t r = 0; r < numResources; ++r )
		{
			const uint8_t *res = p + 80 + r * 8;
			if ( res[ 0 ] == 0x30 && res[ 1 ] == 0 && res[ 2 ] == 0 )
				memcpy( &offset, res + 4, 4 ), offset &= 0xffffffff;
		}
		if ( !offset )
			return false;
	}
	else
	{
		offset = headerSize + ( lowFmt != 0xffffffff ? ImageSize( (int)lowFmt, lowW, lowH ) : 0 );
	}

	const int faces = ( flags & 0x4000 ) ? ( vminor < 5 ? 7 : 6 ) : 1;
	for ( int mip = mips - 1; mip > 0; --mip )
		offset += ImageSize( (int)highFmt, width >> mip, height >> mip ) * std::max< int >( frames, 1 ) * faces;

	const size_t size = ImageSize( (int)highFmt, width, height );
	if ( offset + size > data.size() )
		return false;
	const uint8_t *src = p + offset;
	w = width;
	h = height;
	rgba.assign( (size_t)w * h * 4, 255 );
	switch ( highFmt )
	{
	case FMT_DXT1:
	case FMT_DXT1_ONEBITALPHA:
	case FMT_DXT3:
	case FMT_DXT5: DecodeDXT( src, (int)highFmt, w, h, rgba.data() ); break;
	default:
		for ( size_t i = 0; i < (size_t)w * h; ++i )
		{
			uint8_t *o = &rgba[ i * 4 ];
			switch ( highFmt )
			{
			case FMT_RGBA8888: memcpy( o, src + i * 4, 4 ); break;
			case FMT_ABGR8888: o[ 0 ] = src[ i * 4 + 3 ]; o[ 1 ] = src[ i * 4 + 2 ]; o[ 2 ] = src[ i * 4 + 1 ]; o[ 3 ] = src[ i * 4 ]; break;
			case FMT_ARGB8888: o[ 0 ] = src[ i * 4 + 1 ]; o[ 1 ] = src[ i * 4 + 2 ]; o[ 2 ] = src[ i * 4 + 3 ]; o[ 3 ] = src[ i * 4 ]; break;
			case FMT_BGRA8888: o[ 0 ] = src[ i * 4 + 2 ]; o[ 1 ] = src[ i * 4 + 1 ]; o[ 2 ] = src[ i * 4 ]; o[ 3 ] = src[ i * 4 + 3 ]; break;
			case FMT_BGRX8888: o[ 0 ] = src[ i * 4 + 2 ]; o[ 1 ] = src[ i * 4 + 1 ]; o[ 2 ] = src[ i * 4 ]; break;
			case FMT_RGB888: memcpy( o, src + i * 3, 3 ); break;
			case FMT_BGR888: o[ 0 ] = src[ i * 3 + 2 ]; o[ 1 ] = src[ i * 3 + 1 ]; o[ 2 ] = src[ i * 3 ]; break;
			case FMT_I8: o[ 0 ] = o[ 1 ] = o[ 2 ] = src[ i ]; break;
			case FMT_IA88: o[ 0 ] = o[ 1 ] = o[ 2 ] = src[ i * 2 ]; o[ 3 ] = src[ i * 2 + 1 ]; break;
			case FMT_A8: o[ 0 ] = o[ 1 ] = o[ 2 ] = 255; o[ 3 ] = src[ i ]; break;
			default: return false;
			}
		}
	}
	return true;
}
