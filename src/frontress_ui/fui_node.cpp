//========= Team Frontress ====================================================//
#include "fui_node.h"

#include <cmath>
#include <cstdio>
#include <cstdlib>
#include <cstring>

namespace fui {

void Node::SetNull()
{
	m_eType = Type::Null;
	m_String.clear();
	m_Array.clear();
	m_Object.clear();
}

void Node::SetBool( bool b )
{
	SetNull();
	m_eType = Type::Bool;
	m_bBool = b;
}

void Node::SetNumber( double n )
{
	SetNull();
	m_eType = Type::Number;
	m_flNumber = n;
}

void Node::SetString( std::string s )
{
	SetNull();
	m_eType = Type::String;
	m_String = std::move( s );
}

void Node::SetArray()
{
	SetNull();
	m_eType = Type::Array;
}

void Node::SetObject()
{
	SetNull();
	m_eType = Type::Object;
}

bool Node::AsBool( bool bDefault ) const
{
	switch ( m_eType )
	{
	case Type::Bool: return m_bBool;
	case Type::Number: return m_flNumber != 0.0;
	case Type::String: return !m_String.empty() && m_String != "0" && m_String != "false";
	case Type::Array: return !m_Array.empty();
	case Type::Object: return true;
	default: return bDefault;
	}
}

double Node::AsNumber( double flDefault ) const
{
	switch ( m_eType )
	{
	case Type::Bool: return m_bBool ? 1.0 : 0.0;
	case Type::Number: return m_flNumber;
	case Type::String: return m_String.empty() ? flDefault : std::atof( m_String.c_str() );
	default: return flDefault;
	}
}

static std::string FormatNumber( double n )
{
	char buf[ 64 ];
	if ( std::isfinite( n ) && n == std::floor( n ) && std::fabs( n ) < 1e15 )
		std::snprintf( buf, sizeof( buf ), "%.0f", n );
	else
		std::snprintf( buf, sizeof( buf ), "%.6g", n );
	return buf;
}

std::string Node::AsString() const
{
	switch ( m_eType )
	{
	case Type::Bool: return m_bBool ? "true" : "false";
	case Type::Number: return FormatNumber( m_flNumber );
	case Type::String: return m_String;
	default: return std::string();
	}
}

Node &Node::operator[]( const std::string &key )
{
	if ( m_eType != Type::Object )
		SetObject();
	for ( auto &member : m_Object )
	{
		if ( member.first == key )
			return *member.second;
	}
	m_Object.emplace_back( key, std::unique_ptr< Node >( new Node ) );
	return *m_Object.back().second;
}

const Node *Node::Find( const std::string &key ) const
{
	if ( m_eType != Type::Object )
		return nullptr;
	for ( const auto &member : m_Object )
	{
		if ( member.first == key )
			return member.second.get();
	}
	return nullptr;
}

Node *Node::Find( const std::string &key )
{
	return const_cast< Node * >( static_cast< const Node * >( this )->Find( key ) );
}

Node *Node::FindPath( const std::string &path, bool bCreate )
{
	Node *pNode = this;
	size_t start = 0;
	while ( pNode && start <= path.size() )
	{
		size_t end = path.find( '.', start );
		if ( end == std::string::npos )
			end = path.size();
		const std::string part = path.substr( start, end - start );
		if ( part.empty() )
			return nullptr;

		const bool bIndex = part.find_first_not_of( "0123456789" ) == std::string::npos;
		if ( bIndex && pNode->IsArray() )
		{
			const size_t i = (size_t)std::strtoul( part.c_str(), nullptr, 10 );
			while ( bCreate && i >= pNode->Size() )
				pNode->Append();
			pNode = pNode->At( i );
		}
		else
		{
			pNode = bCreate ? &( *pNode )[ part ] : pNode->Find( part );
		}
		start = end + 1;
	}
	return pNode;
}

Node &Node::Append()
{
	if ( m_eType != Type::Array )
		SetArray();
	m_Array.emplace_back( new Node );
	return *m_Array.back();
}

Node &Node::Append( Node &&value )
{
	Node &node = Append();
	node = std::move( value );
	return node;
}

size_t Node::Size() const
{
	return m_eType == Type::Array ? m_Array.size() : 0;
}

Node *Node::At( size_t i )
{
	return ( m_eType == Type::Array && i < m_Array.size() ) ? m_Array[ i ].get() : nullptr;
}

const Node *Node::At( size_t i ) const
{
	return ( m_eType == Type::Array && i < m_Array.size() ) ? m_Array[ i ].get() : nullptr;
}

Node Node::Clone() const
{
	Node copy;
	copy.m_eType = m_eType;
	copy.m_bBool = m_bBool;
	copy.m_flNumber = m_flNumber;
	copy.m_String = m_String;
	for ( const auto &item : m_Array )
		copy.m_Array.emplace_back( new Node( item->Clone() ) );
	for ( const auto &member : m_Object )
		copy.m_Object.emplace_back( member.first, std::unique_ptr< Node >( new Node( member.second->Clone() ) ) );
	return copy;
}

bool Node::Equals( const Node &other ) const
{
	if ( m_eType != other.m_eType )
		return false;
	switch ( m_eType )
	{
	case Type::Null: return true;
	case Type::Bool: return m_bBool == other.m_bBool;
	case Type::Number: return m_flNumber == other.m_flNumber;
	case Type::String: return m_String == other.m_String;
	case Type::Array:
		if ( m_Array.size() != other.m_Array.size() )
			return false;
		for ( size_t i = 0; i < m_Array.size(); ++i )
		{
			if ( !m_Array[ i ]->Equals( *other.m_Array[ i ] ) )
				return false;
		}
		return true;
	case Type::Object:
		if ( m_Object.size() != other.m_Object.size() )
			return false;
		for ( size_t i = 0; i < m_Object.size(); ++i )
		{
			if ( m_Object[ i ].first != other.m_Object[ i ].first || !m_Object[ i ].second->Equals( *other.m_Object[ i ].second ) )
				return false;
		}
		return true;
	}
	return false;
}

void Node::Assign( Node &&other )
{
	m_eType = other.m_eType;
	m_bBool = other.m_bBool;
	m_flNumber = other.m_flNumber;
	m_String = std::move( other.m_String );
	m_Array = std::move( other.m_Array );
	m_Object = std::move( other.m_Object );
	other.SetNull();
}

//-----------------------------------------------------------------------------
// JSON
//-----------------------------------------------------------------------------
namespace {

struct Parser {
	const char *p;
	const char *end;
	std::string error;

	void Skip()
	{
		while ( p < end )
		{
			if ( *p == ' ' || *p == '\t' || *p == '\n' || *p == '\r' )
				++p;
			else if ( p + 1 < end && p[ 0 ] == '/' && p[ 1 ] == '/' )	// comments, for hand-written files
			{
				while ( p < end && *p != '\n' )
					++p;
			}
			else
				break;
		}
	}

	bool Fail( const char *pszWhat )
	{
		if ( error.empty() )
			error = pszWhat;
		return false;
	}

	static void AppendUTF8( std::string &out, unsigned cp )
	{
		if ( cp < 0x80 )
			out += (char)cp;
		else if ( cp < 0x800 )
		{
			out += (char)( 0xC0 | ( cp >> 6 ) );
			out += (char)( 0x80 | ( cp & 0x3F ) );
		}
		else if ( cp < 0x10000 )
		{
			out += (char)( 0xE0 | ( cp >> 12 ) );
			out += (char)( 0x80 | ( ( cp >> 6 ) & 0x3F ) );
			out += (char)( 0x80 | ( cp & 0x3F ) );
		}
		else
		{
			out += (char)( 0xF0 | ( cp >> 18 ) );
			out += (char)( 0x80 | ( ( cp >> 12 ) & 0x3F ) );
			out += (char)( 0x80 | ( ( cp >> 6 ) & 0x3F ) );
			out += (char)( 0x80 | ( cp & 0x3F ) );
		}
	}

	bool Hex4( unsigned &cp )
	{
		if ( end - p < 4 )
			return Fail( "bad \\u escape" );
		cp = 0;
		for ( int i = 0; i < 4; ++i )
		{
			const char c = *p++;
			cp <<= 4;
			if ( c >= '0' && c <= '9' ) cp |= c - '0';
			else if ( c >= 'a' && c <= 'f' ) cp |= c - 'a' + 10;
			else if ( c >= 'A' && c <= 'F' ) cp |= c - 'A' + 10;
			else return Fail( "bad \\u escape" );
		}
		return true;
	}

	bool String( std::string &out )
	{
		++p;	// opening quote
		while ( p < end && *p != '"' )
		{
			if ( *p == '\\' )
			{
				if ( ++p >= end )
					return Fail( "unterminated string" );
				const char c = *p++;
				switch ( c )
				{
				case 'n': out += '\n'; break;
				case 't': out += '\t'; break;
				case 'r': out += '\r'; break;
				case 'b': out += '\b'; break;
				case 'f': out += '\f'; break;
				case 'u':
				{
					unsigned cp;
					if ( !Hex4( cp ) )
						return false;
					if ( cp >= 0xD800 && cp < 0xDC00 && end - p >= 6 && p[ 0 ] == '\\' && p[ 1 ] == 'u' )
					{
						p += 2;
						unsigned lo;
						if ( !Hex4( lo ) )
							return false;
						cp = 0x10000 + ( ( cp - 0xD800 ) << 10 ) + ( lo - 0xDC00 );
					}
					AppendUTF8( out, cp );
					break;
				}
				default: out += c; break;
				}
			}
			else
				out += *p++;
		}
		if ( p >= end )
			return Fail( "unterminated string" );
		++p;
		return true;
	}

	bool Value( Node &out, int depth )
	{
		if ( depth > 64 )
			return Fail( "nested too deeply" );
		Skip();
		if ( p >= end )
			return Fail( "unexpected end" );

		const char c = *p;
		if ( c == '{' )
		{
			++p;
			out.SetObject();
			Skip();
			if ( p < end && *p == '}' )
			{
				++p;
				return true;
			}
			for ( ;; )
			{
				Skip();
				if ( p >= end || *p != '"' )
					return Fail( "expected a key" );
				std::string key;
				if ( !String( key ) )
					return false;
				Skip();
				if ( p >= end || *p != ':' )
					return Fail( "expected ':'" );
				++p;
				if ( !Value( out[ key ], depth + 1 ) )
					return false;
				Skip();
				if ( p < end && *p == ',' )
				{
					++p;
					Skip();
					if ( p < end && *p == '}' )	// trailing comma
					{
						++p;
						return true;
					}
					continue;
				}
				if ( p < end && *p == '}' )
				{
					++p;
					return true;
				}
				return Fail( "expected ',' or '}'" );
			}
		}
		if ( c == '[' )
		{
			++p;
			out.SetArray();
			Skip();
			if ( p < end && *p == ']' )
			{
				++p;
				return true;
			}
			for ( ;; )
			{
				if ( !Value( out.Append(), depth + 1 ) )
					return false;
				Skip();
				if ( p < end && *p == ',' )
				{
					++p;
					Skip();
					if ( p < end && *p == ']' )
					{
						++p;
						return true;
					}
					continue;
				}
				if ( p < end && *p == ']' )
				{
					++p;
					return true;
				}
				return Fail( "expected ',' or ']'" );
			}
		}
		if ( c == '"' )
		{
			std::string s;
			if ( !String( s ) )
				return false;
			out.SetString( std::move( s ) );
			return true;
		}
		if ( end - p >= 4 && !std::strncmp( p, "true", 4 ) )
		{
			p += 4;
			out.SetBool( true );
			return true;
		}
		if ( end - p >= 5 && !std::strncmp( p, "false", 5 ) )
		{
			p += 5;
			out.SetBool( false );
			return true;
		}
		if ( end - p >= 4 && !std::strncmp( p, "null", 4 ) )
		{
			p += 4;
			out.SetNull();
			return true;
		}
		if ( c == '-' || ( c >= '0' && c <= '9' ) )
		{
			std::string num;
			while ( p < end && ( ( *p >= '0' && *p <= '9' ) || *p == '-' || *p == '+' || *p == '.' || *p == 'e' || *p == 'E' ) )
				num += *p++;
			out.SetNumber( std::atof( num.c_str() ) );
			return true;
		}
		return Fail( "unexpected character" );
	}
};

void DumpString( std::string &out, const std::string &s )
{
	out += '"';
	for ( unsigned char c : s )
	{
		switch ( c )
		{
		case '"': out += "\\\""; break;
		case '\\': out += "\\\\"; break;
		case '\n': out += "\\n"; break;
		case '\r': out += "\\r"; break;
		case '\t': out += "\\t"; break;
		default:
			if ( c < 0x20 )
			{
				char buf[ 8 ];
				std::snprintf( buf, sizeof( buf ), "\\u%04x", c );
				out += buf;
			}
			else
				out += (char)c;
		}
	}
	out += '"';
}

void DumpNode( std::string &out, const Node &node );

} // namespace

bool Node::Parse( const std::string &text, Node &out, std::string *pError )
{
	Parser parser{ text.data(), text.data() + text.size(), std::string() };
	// A UTF-8 byte order mark, which some editors write.
	if ( text.size() >= 3 && (unsigned char)text[ 0 ] == 0xEF && (unsigned char)text[ 1 ] == 0xBB && (unsigned char)text[ 2 ] == 0xBF )
		parser.p += 3;

	Node result;
	bool bOK = parser.Value( result, 0 );
	if ( bOK )
	{
		parser.Skip();
		if ( parser.p != parser.end )
			bOK = parser.Fail( "trailing characters" );
	}
	if ( !bOK )
	{
		if ( pError )
		{
			int line = 1;
			for ( const char *q = text.data(); q < parser.p; ++q )
				line += ( *q == '\n' );
			*pError = parser.error + " on line " + std::to_string( line );
		}
		return false;
	}
	out = std::move( result );
	return true;
}

namespace {
void DumpNode( std::string &out, const Node &node )
{
	switch ( node.GetType() )
	{
	case Node::Type::Null: out += "null"; break;
	case Node::Type::Bool:
	case Node::Type::Number: out += node.AsString(); break;
	case Node::Type::String: DumpString( out, node.AsString() ); break;
	case Node::Type::Array:
		out += '[';
		for ( size_t i = 0; i < node.Size(); ++i )
		{
			if ( i )
				out += ',';
			DumpNode( out, *node.At( i ) );
		}
		out += ']';
		break;
	case Node::Type::Object:
	{
		out += '{';
		Node &mutableNode = const_cast< Node & >( node );
		for ( size_t i = 0; i < node.MemberCount(); ++i )
		{
			if ( i )
				out += ',';
			DumpString( out, node.MemberName( i ) );
			out += ':';
			DumpNode( out, mutableNode.Member( i ) );
		}
		out += '}';
		break;
	}
	}
}
} // namespace

std::string Node::Dump() const
{
	std::string out;
	DumpNode( out, *this );
	return out;
}

} // namespace fui
