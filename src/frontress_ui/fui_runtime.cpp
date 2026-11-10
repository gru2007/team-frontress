//========= Team Frontress ====================================================//
#include "fui.h"
#include "fui_font.h"
#include "fui_warmap.h"

#include <RmlUi/Core.h>
#ifdef FUI_WITH_DEBUGGER
#include <RmlUi/Debugger.h>
#endif

#include <algorithm>
#include <cmath>
#include <cstdio>
#include <cctype>
#include <cstring>
#include <map>
#include <set>

namespace fui {

static const char *const k_pszRoot = "resource/fui/";
static const char *const k_pszModel = "fr";

//-----------------------------------------------------------------------------
// RmlUi interfaces, routed to the host
//-----------------------------------------------------------------------------
namespace {

class SystemInterface final : public Rml::SystemInterface {
public:
	Host *host = nullptr;
	Runtime *runtime = nullptr;

	double GetElapsedTime() override { return host->Time(); }

	// Replaces every #token in a text with its string, leaving the rest (and
	// {{ data }} bindings) alone. Tokens nobody has a string for stay as typed.
	int TranslateString( Rml::String &translated, const Rml::String &input ) override
	{
		translated.clear();
		int count = 0;
		size_t i = 0;
		while ( i < input.size() )
		{
			const size_t hash = input.find( '#', i );
			if ( hash == Rml::String::npos )
			{
				translated.append( input, i, Rml::String::npos );
				break;
			}
			translated.append( input, i, hash - i );
			size_t end = hash + 1;
			while ( end < input.size() && ( isalnum( (unsigned char)input[ end ] ) || input[ end ] == '_' ) )
				++end;
			const Rml::String token = input.substr( hash + 1, end - hash - 1 );
			Rml::String value;
			if ( !token.empty() && runtime->Lookup( token, value ) )
			{
				translated += value;
				++count;
			}
			else
				translated.append( input, hash, end - hash );
			i = end;
		}
		return count;
	}

	bool LogMessage( Rml::Log::Type type, const Rml::String &message ) override
	{
		int level = 2;
		switch ( type )
		{
		case Rml::Log::LT_ERROR:
		case Rml::Log::LT_ASSERT: level = 0; break;
		case Rml::Log::LT_WARNING: level = 1; break;
		case Rml::Log::LT_DEBUG: level = 3; break;
		default: break;
		}
		host->Log( level, message.c_str() );
		return true;
	}

	void SetMouseCursor( const Rml::String &cursor_name ) override { host->SetCursor( cursor_name ); }

	// "tex:..." and "avatar:..." name things the host draws, not files next
	// to the document: leave them alone.
	void JoinPath( Rml::String &translated_path, const Rml::String &document_path, const Rml::String &path ) override
	{
		const size_t colon = path.find( ':' );
		if ( colon != Rml::String::npos && colon > 1 && path.find( '/' ) > colon )
		{
			translated_path = path;
			return;
		}
		Rml::SystemInterface::JoinPath( translated_path, document_path, path );
	}
};

class FileInterface final : public Rml::FileInterface {
public:
	Host *host = nullptr;
	std::set< std::string > opened;

	struct File {
		std::string data;
		size_t pos = 0;
	};

	Rml::FileHandle Open( const Rml::String &path ) override
	{
		File *pFile = new File;
		if ( !host->ReadFile( path, pFile->data ) )
		{
			delete pFile;
			return 0;
		}
		opened.insert( path );
		return (Rml::FileHandle)pFile;
	}

	void Close( Rml::FileHandle file ) override { delete (File *)file; }

	size_t Read( void *buffer, size_t size, Rml::FileHandle file ) override
	{
		File *pFile = (File *)file;
		const size_t n = std::min( size, pFile->data.size() - pFile->pos );
		memcpy( buffer, pFile->data.data() + pFile->pos, n );
		pFile->pos += n;
		return n;
	}

	bool Seek( Rml::FileHandle file, long offset, int origin ) override
	{
		File *pFile = (File *)file;
		long base = origin == SEEK_SET ? 0 : origin == SEEK_CUR ? (long)pFile->pos : (long)pFile->data.size();
		const long pos = base + offset;
		if ( pos < 0 || pos > (long)pFile->data.size() )
			return false;
		pFile->pos = (size_t)pos;
		return true;
	}

	size_t Tell( Rml::FileHandle file ) override { return ( (File *)file )->pos; }
	size_t Length( Rml::FileHandle file ) override { return ( (File *)file )->data.size(); }

	bool LoadFile( const Rml::String &path, Rml::String &out ) override
	{
		std::string data;
		if ( !host->ReadFile( path, data ) )
			return false;
		opened.insert( path );
		out = std::move( data );
		return true;
	}
};

// The state tree as data variables. One definition per kind of node; a
// child's definition is picked by what the child holds right now.
class NodeDefinition;
NodeDefinition *DefinitionFor( const Node &node );

Node &NullNode()
{
	static Node s_Null;
	s_Null.SetNull();
	return s_Null;
}

class NodeDefinition final : public Rml::VariableDefinition {
public:
	explicit NodeDefinition( Rml::DataVariableType type ) : VariableDefinition( type ) {}

	bool Get( void *ptr, Rml::Variant &variant ) override
	{
		const Node &node = *(const Node *)ptr;
		switch ( node.GetType() )
		{
		case Node::Type::Bool: variant = node.AsBool(); return true;
		case Node::Type::Number:
		{
			const double n = node.AsNumber();
			if ( n == std::floor( n ) && std::fabs( n ) < 2e9 )
				variant = (int)n;
			else
				variant = n;
			return true;
		}
		case Node::Type::String: variant = node.AsString(); return true;
		case Node::Type::Null: variant = Rml::String(); return true;
		default: variant = (int)node.Size(); return true;
		}
	}

	bool Set( void *ptr, const Rml::Variant &variant ) override
	{
		Node &node = *(Node *)ptr;
		switch ( variant.GetType() )
		{
		case Rml::Variant::BOOL: node.SetBool( variant.Get< bool >() ); break;
		case Rml::Variant::INT:
		case Rml::Variant::INT64:
		case Rml::Variant::FLOAT:
		case Rml::Variant::DOUBLE: node.SetNumber( variant.Get< double >() ); break;
		default: node.SetString( variant.Get< Rml::String >() ); break;
		}
		return true;
	}

	int Size( void *ptr ) override { return (int)( (const Node *)ptr )->Size(); }

	Rml::DataVariable Child( void *ptr, const Rml::DataAddressEntry &address ) override
	{
		Node &node = *(Node *)ptr;
		Node *pChild = nullptr;
		if ( address.index >= 0 )
			pChild = node.At( (size_t)address.index );
		else if ( node.IsArray() && address.name == "size" )
			return Rml::MakeLiteralIntVariable( (int)node.Size() );
		else
			pChild = node.Find( address.name );
		if ( !pChild )
			pChild = &NullNode();	// read as empty, rather than a warning per frame
		return Rml::DataVariable( DefinitionFor( *pChild ), pChild );
	}
};

NodeDefinition *DefinitionFor( const Node &node )
{
	static NodeDefinition s_Scalar( Rml::DataVariableType::Scalar );
	static NodeDefinition s_Array( Rml::DataVariableType::Array );
	static NodeDefinition s_Struct( Rml::DataVariableType::Struct );
	switch ( node.GetType() )
	{
	case Node::Type::Array: return &s_Array;
	case Node::Type::Object: return &s_Struct;
	default: return &s_Scalar;
	}
}

std::string TopLevelOf( const std::string &path )
{
	const size_t dot = path.find( '.' );
	return dot == std::string::npos ? path : path.substr( 0, dot );
}

std::string VariantToString( const Rml::Variant &variant )
{
	return variant.Get< Rml::String >();
}

std::string FormatThousands( long long n )
{
	const bool bNegative = n < 0;
	std::string digits = std::to_string( bNegative ? -n : n ), out;
	for ( size_t i = 0; i < digits.size(); ++i )
	{
		if ( i && ( digits.size() - i ) % 3 == 0 )
			out += "\xC2\xA0";	// no-break space
		out += digits[ i ];
	}
	return ( bNegative ? "-" : "" ) + out;
}

} // namespace

// Sounds: an element (or a near ancestor) with sfx="..." says so on hover and
// plays its own sound on click.
class SoundListener final : public Rml::EventListener {
public:
	Host *host = nullptr;
	Rml::Element *lastHover = nullptr;

	void ProcessEvent( Rml::Event &event ) override
	{
		Rml::Element *pElement = event.GetTargetElement();
		for ( int depth = 0; pElement && depth < 5; ++depth, pElement = pElement->GetParentNode() )
		{
			const Rml::Variant *pSfx = pElement->GetAttribute( "sfx" );
			if ( !pSfx )
				continue;
			if ( event.GetId() == Rml::EventId::Mouseover )
			{
				if ( pElement != lastHover )
					host->PlaySound( "hover" );
				lastHover = pElement;
			}
			else
			{
				const Rml::String name = pSfx->Get< Rml::String >();
				host->PlaySound( name.empty() ? "click" : name );
			}
			return;
		}
		if ( event.GetId() == Rml::EventId::Mouseover )
			lastHover = nullptr;
	}
};

struct Runtime::Impl {
	SoundListener sounds;
	SystemInterface system;
	FileInterface files;
	std::unique_ptr< FontEngine > fonts;
	std::unique_ptr< Renderer > renderer;
	std::unique_ptr< Rml::ElementInstancer > warmap;
	Rml::DataModelHandle model;
	Node strings;			// current language
	Node fallbackStrings;	// english
	std::map< std::string, int64_t > fileTimes;
	bool debuggerReady = false;
	std::string nextScreen;
};

//-----------------------------------------------------------------------------
Runtime::Runtime() = default;
Runtime::~Runtime()
{
	Shutdown();
}

bool Runtime::Init( Host *pHost, DrawBackend *pBackend, int width, int height )
{
	if ( m_pContext )
		return true;

	m_pHost = pHost;
	m_pImpl.reset( new Impl );
	m_pImpl->system.host = pHost;
	m_pImpl->system.runtime = this;
	m_pImpl->files.host = pHost;
	m_pImpl->fonts.reset( new FontEngine );
	m_pImpl->renderer.reset( new Renderer( pBackend ) );

	Rml::SetSystemInterface( &m_pImpl->system );
	Rml::SetFileInterface( &m_pImpl->files );
	Rml::SetFontEngineInterface( m_pImpl->fonts.get() );
	Rml::SetRenderInterface( m_pImpl->renderer.get() );
	if ( !Rml::Initialise() )
	{
		pHost->Log( 0, "RmlUi failed to initialise" );
		return false;
	}

	LoadFonts();

	// Our own elements.
	m_pImpl->warmap = MakeWarMapInstancer( this );
	Rml::Factory::RegisterElementInstancer( "warmap", m_pImpl->warmap.get() );

	m_nWidth = width;
	m_nHeight = height;
	m_pContext = Rml::CreateContext( "frontress", Rml::Vector2i( width, height ) );
	if ( !m_pContext )
	{
		pHost->Log( 0, "Could not create the UI context" );
		Rml::Shutdown();
		return false;
	}
	SetViewport( width, height );
	m_pImpl->sounds.host = pHost;
	m_pContext->AddEventListener( "mouseover", &m_pImpl->sounds );
	m_pContext->AddEventListener( "click", &m_pImpl->sounds );

	LoadStrings();
	BindModel();
	m_Screen.clear();
	if ( Node *pScreen = Get( "ui.screen" ) )
		m_Screen = pScreen->AsString();
	if ( m_Screen.empty() )
		m_Screen = "home";
	LoadDocuments();
	if ( !m_pShell || m_Screens.empty() || !m_Screens.front().second )
	{
		Shutdown();
		return false;
	}
	return true;
}

void Runtime::Shutdown()
{
	if ( !m_pContext )
		return;
	CloseDocuments();
	m_pContext = nullptr;
	Rml::Shutdown();
	m_pImpl.reset();
}

void Runtime::SetViewport( int width, int height )
{
	m_nWidth = width;
	m_nHeight = height;
	if ( !m_pContext )
		return;
	m_pContext->SetDimensions( Rml::Vector2i( width, height ) );
	// Everything is laid out in dp, on a 1920x1080 design: the window is
	// always at least that many dp in both directions. Narrower screens
	// (16:10, 4:3) scale by width and get extra height, which the screens
	// absorb; with less than 1920 dp across, side columns and the middle
	// would overlap.
	const float ratio = std::min( height / 1080.f, width / 1920.f );
	m_pContext->SetDensityIndependentPixelRatio( std::max( 0.4f, ratio ) );
}

void Runtime::Update()
{
	if ( !m_pContext )
		return;
	if ( m_bHotReload && m_pHost->Time() >= m_flNextFileCheck )
	{
		m_flNextFileCheck = m_pHost->Time() + 0.5;
		CheckFiles();
	}
	if ( !m_pImpl->nextScreen.empty() )
	{
		std::string screen;
		screen.swap( m_pImpl->nextScreen );
		Navigate( screen );
	}
	m_pContext->Update();
}

void Runtime::Render()
{
	if ( !m_pContext )
		return;
	m_pImpl->renderer->BeginFrame( m_nWidth, m_nHeight );
	m_pContext->Render();
}

//-----------------------------------------------------------------------------
// State
//-----------------------------------------------------------------------------
Node *Runtime::Get( const std::string &path )
{
	return m_State.FindPath( path, false );
}

bool Runtime::Set( const std::string &path, Node &&value )
{
	const std::string top = TopLevelOf( path );
	Node *pNode = m_State.FindPath( path, true );
	if ( !pNode || pNode->Equals( value ) )
		return false;
	pNode->Assign( std::move( value ) );
	if ( m_pImpl && m_pImpl->model )
	{
		bool bBound = false;
		for ( const std::string &name : m_TopLevel )
			bBound = bBound || name == top;
		if ( bBound )
			m_pImpl->model.DirtyVariable( top );
		else
			m_pHost->Log( 1, ( "State '" + top + "' is not in schema.json; documents cannot see it" ).c_str() );
	}
	if ( path == "ui.screen" )
		Navigate( m_State.FindPath( path, false )->AsString() );
	return true;
}

void Runtime::Merge( const Node &values )
{
	Node &mutableValues = const_cast< Node & >( values );
	for ( size_t i = 0; i < values.MemberCount(); ++i )
	{
		const std::string &key = values.MemberName( i );
		Node &value = mutableValues.Member( i );
		Node *pExisting = m_State.Find( key );
		if ( value.IsObject() && pExisting && pExisting->IsObject() )
		{
			// One level deeper, so mock data can fill in part of a section.
			for ( size_t j = 0; j < value.MemberCount(); ++j )
				Set( key + "." + value.MemberName( j ), value.Member( j ).Clone() );
		}
		else
			Set( key, value.Clone() );
	}
}

void Runtime::BindModel()
{
	// schema.json: the top level, and the value everything starts with.
	std::string text, error;
	Node schema;
	if ( !m_pHost->ReadFile( std::string( k_pszRoot ) + "schema.json", text ) || !Node::Parse( text, schema, &error ) || !schema.IsObject() )
	{
		m_pHost->Log( 0, ( "resource/fui/schema.json: " + ( error.empty() ? std::string( "missing" ) : error ) ).c_str() );
		schema.SetObject();
	}
	m_pImpl->files.opened.insert( std::string( k_pszRoot ) + "schema.json" );

	// Values the host already wrote win over the defaults.
	for ( size_t i = 0; i < schema.MemberCount(); ++i )
	{
		Node &defaults = schema.Member( i );
		Node &current = m_State[ schema.MemberName( i ) ];
		if ( current.IsNull() )
			current.Assign( defaults.Clone() );
		else if ( defaults.IsObject() && current.IsObject() )
		{
			for ( size_t j = 0; j < defaults.MemberCount(); ++j )
			{
				if ( !current.Find( defaults.MemberName( j ) ) )
					current[ defaults.MemberName( j ) ].Assign( defaults.Member( j ).Clone() );
			}
		}
	}

	if ( m_pImpl->model )
	{
		m_pContext->RemoveDataModel( k_pszModel );
		m_pImpl->model = Rml::DataModelHandle();
	}
	Rml::DataModelConstructor constructor = m_pContext->CreateDataModel( k_pszModel );
	if ( !constructor )
		return;

	m_TopLevel.clear();
	for ( size_t i = 0; i < m_State.MemberCount(); ++i )
	{
		const std::string &name = m_State.MemberName( i );
		Node &node = m_State.Member( i );
		constructor.BindCustomDataVariable( name, Rml::DataVariable( DefinitionFor( node ), &node ) );
		m_TopLevel.push_back( name );
	}

	constructor.BindEventCallback( "nav", [ this ]( Rml::DataModelHandle, Rml::Event &, const Rml::VariantList &args ) {
		if ( !args.empty() )
			m_pImpl->nextScreen = VariantToString( args[ 0 ] );
	} );
	constructor.BindEventCallback( "set", [ this ]( Rml::DataModelHandle, Rml::Event &, const Rml::VariantList &args ) {
		if ( args.size() >= 2 )
		{
			Node value;
			switch ( args[ 1 ].GetType() )
			{
			case Rml::Variant::BOOL: value.SetBool( args[ 1 ].Get< bool >() ); break;
			case Rml::Variant::INT:
			case Rml::Variant::INT64:
			case Rml::Variant::FLOAT:
			case Rml::Variant::DOUBLE: value.SetNumber( args[ 1 ].Get< double >() ); break;
			default: value.SetString( VariantToString( args[ 1 ] ) ); break;
			}
			Set( VariantToString( args[ 0 ] ), std::move( value ) );
		}
	} );
	constructor.BindEventCallback( "toggle", [ this ]( Rml::DataModelHandle, Rml::Event &, const Rml::VariantList &args ) {
		if ( !args.empty() )
		{
			const std::string path = VariantToString( args[ 0 ] );
			Node *pNode = Get( path );
			Set( path, Node( !( pNode && pNode->AsBool() ) ) );
		}
	} );
	constructor.BindEventCallback( "cmd", [ this ]( Rml::DataModelHandle, Rml::Event &, const Rml::VariantList &args ) {
		if ( args.empty() )
			return;
		std::vector< std::string > rest;
		for ( size_t i = 1; i < args.size(); ++i )
			rest.push_back( VariantToString( args[ i ] ) );
		HandleEvent( VariantToString( args[ 0 ] ), rest );
	} );

	// {{ 'fui_play' | t }}, {{ 'fui_q_kills' | t(q.target) }}: %1, %2... are
	// the arguments; a number gets thousands separators, a "#token" is
	// translated too.
	constructor.RegisterTransformFunc( "t", [ this ]( const Rml::VariantList &args ) -> Rml::Variant {
		if ( args.empty() )
			return Rml::Variant();
		std::string token = VariantToString( args[ 0 ] );
		if ( !token.empty() && token[ 0 ] == '#' )
			token.erase( 0, 1 );
		std::string text = Translate( token );
		for ( size_t i = 1; i < args.size() && i < 10; ++i )
		{
			std::string value;
			const Rml::Variant::Type type = args[ i ].GetType();
			if ( type == Rml::Variant::INT || type == Rml::Variant::INT64 || type == Rml::Variant::FLOAT || type == Rml::Variant::DOUBLE )
				value = FormatThousands( (long long)std::llround( args[ i ].Get< double >() ) );
			else
			{
				value = VariantToString( args[ i ] );
				if ( value.size() > 1 && value[ 0 ] == '#' )
					value = Translate( value.substr( 1 ) );
			}
			const std::string mark = "%" + std::to_string( i );
			for ( size_t at = text.find( mark ); at != std::string::npos; at = text.find( mark, at + value.size() ) )
				text.replace( at, mark.size(), value );
		}
		return Rml::Variant( text );
	} );
	// {{ queue.seconds | clock }} -> 1:05
	constructor.RegisterTransformFunc( "clock", []( const Rml::VariantList &args ) -> Rml::Variant {
		const int seconds = args.empty() ? 0 : std::max( 0, args[ 0 ].Get< int >() );
		char buf[ 32 ];
		if ( seconds >= 3600 )
			snprintf( buf, sizeof( buf ), "%d:%02d:%02d", seconds / 3600, ( seconds / 60 ) % 60, seconds % 60 );
		else
			snprintf( buf, sizeof( buf ), "%d:%02d", seconds / 60, seconds % 60 );
		return Rml::Variant( Rml::String( buf ) );
	} );
	// {{ player.xp | thousands }} -> 12 450
	constructor.RegisterTransformFunc( "thousands", []( const Rml::VariantList &args ) -> Rml::Variant {
		return Rml::Variant( FormatThousands( args.empty() ? 0 : (long long)args[ 0 ].Get< double >() ) );
	} );
	// {{ quests.daily_left | duration }} -> 7 h 12 m / 12 m / 45 s
	constructor.RegisterTransformFunc( "duration", [ this ]( const Rml::VariantList &args ) -> Rml::Variant {
		const long long seconds = args.empty() ? 0 : std::max< long long >( 0, (long long)args[ 0 ].Get< double >() );
		char buf[ 64 ];
		if ( seconds >= 86400 )
			snprintf( buf, sizeof( buf ), "%lld %s %lld %s", seconds / 86400, Translate( "fui_unit_d" ).c_str(), ( seconds / 3600 ) % 24, Translate( "fui_unit_h" ).c_str() );
		else if ( seconds >= 3600 )
			snprintf( buf, sizeof( buf ), "%lld %s %lld %s", seconds / 3600, Translate( "fui_unit_h" ).c_str(), ( seconds / 60 ) % 60, Translate( "fui_unit_m" ).c_str() );
		else
			snprintf( buf, sizeof( buf ), "%lld %s", std::max< long long >( 1, seconds / 60 ), Translate( "fui_unit_m" ).c_str() );
		return Rml::Variant( Rml::String( buf ) );
	} );
	// {{ 0.42 | percent }} -> 42%
	constructor.RegisterTransformFunc( "percent", []( const Rml::VariantList &args ) -> Rml::Variant {
		const double f = args.empty() ? 0.0 : args[ 0 ].Get< double >();
		return Rml::Variant( std::to_string( (int)std::lround( f * 100.0 ) ) + "%" );
	} );
	// {{ rating.delta | signed }} -> +12
	constructor.RegisterTransformFunc( "signed", []( const Rml::VariantList &args ) -> Rml::Variant {
		const int n = args.empty() ? 0 : (int)std::lround( args[ 0 ].Get< double >() );
		return Rml::Variant( ( n > 0 ? "+" : "" ) + std::to_string( n ) );
	} );

	m_pImpl->model = constructor.GetModelHandle();
}

//-----------------------------------------------------------------------------
// Documents
//-----------------------------------------------------------------------------
void Runtime::LoadDocuments()
{
	m_pShell = m_pContext->LoadDocument( std::string( k_pszRoot ) + "shell.rml" );
	if ( !m_pShell )
		m_pHost->Log( 0, "resource/fui/shell.rml did not load" );
	if ( m_bHud )
	{
		m_bHud = false;
		SetHud( true );
	}
	else
		ApplyScreen();
}

void Runtime::CloseDocuments()
{
	if ( !m_pContext )
		return;
	if ( m_pShell )
		m_pShell->Close();
	m_pShell = nullptr;
	if ( m_pHud )
		m_pHud->Close();
	m_pHud = nullptr;
	for ( auto &screen : m_Screens )
	{
		if ( screen.second )
			screen.second->Close();
	}
	m_Screens.clear();
	m_pContext->Update();	// closed documents are released on update
}

void Runtime::SetHud( bool bHud )
{
	if ( bHud == m_bHud || !m_pContext )
		return;
	m_bHud = bHud;
	if ( bHud )
	{
		if ( !m_pHud )
		{
			m_pHud = m_pContext->LoadDocument( std::string( k_pszRoot ) + m_HudPath );
			if ( !m_pHud )
				m_pHost->Log( 0, ( "resource/fui/" + m_HudPath + " did not load" ).c_str() );
		}
		if ( m_pShell )
			m_pShell->Hide();
		for ( auto &screen : m_Screens )
		{
			if ( screen.second )
				screen.second->Hide();
		}
		if ( m_pHud )
			m_pHud->Show( Rml::ModalFlag::None, Rml::FocusFlag::None );
	}
	else
	{
		if ( m_pHud )
			m_pHud->Hide();
		ApplyScreen();
	}
}

void Runtime::SetHudDocument( const std::string &path )
{
	if ( path == m_HudPath )
		return;
	m_HudPath = path;
	if ( m_pHud )
	{
		m_pHud->Close();
		m_pHud = nullptr;
	}
	if ( m_bHud )
	{
		m_bHud = false;
		SetHud( true );
	}
}

std::string Runtime::HudAttribute( const char *pszName ) const
{
	return m_pHud ? m_pHud->GetAttribute< Rml::String >( pszName, "" ) : std::string();
}

bool Runtime::FocusHud( const std::string &id )
{
	if ( !m_bHud || !m_pHud )
		return false;
	Rml::Element *pElement = m_pHud->GetElementById( id );
	return pElement && pElement->Focus();
}

void Runtime::ApplyScreen()
{
	if ( m_bHud )
		return;
	// A screen is loaded fresh each time it is shown, so its entrance
	// animations play again; its state lives in the data model, not in it.
	for ( auto &screen : m_Screens )
	{
		if ( screen.second && screen.first != m_Screen )
			screen.second->Close();
	}
	Rml::ElementDocument *pShown = nullptr;
	for ( auto &screen : m_Screens )
	{
		if ( screen.first == m_Screen )
			pShown = screen.second;
	}
	m_Screens.clear();
	if ( !pShown )
	{
		pShown = m_pContext->LoadDocument( std::string( k_pszRoot ) + "screens/" + m_Screen + ".rml" );
		if ( !pShown )
			m_pHost->Log( 0, ( "resource/fui/screens/" + m_Screen + ".rml did not load" ).c_str() );
	}
	m_Screens.emplace_back( m_Screen, pShown );
	if ( pShown )
	{
		pShown->Show( Rml::ModalFlag::None, Rml::FocusFlag::None );
		pShown->SetProperty( "z-index", "0" );
	}
	if ( m_pShell )
	{
		m_pShell->Show( Rml::ModalFlag::None, Rml::FocusFlag::None );
		m_pShell->SetProperty( "z-index", "1" );
	}
	if ( m_pHud )
		m_pHud->Hide();
}

void Runtime::Navigate( const std::string &screen )
{
	if ( screen.empty() || screen == m_Screen )
		return;
	if ( screen.find_first_not_of( "abcdefghijklmnopqrstuvwxyz0123456789_-" ) != std::string::npos )
	{
		if ( m_pHost )
			m_pHost->Log( 1, ( "Not a screen name: " + screen ).c_str() );
		return;
	}
	m_Screen = screen;
	Set( "ui.modal", Node( "" ) );
	Node *pScreen = m_State.FindPath( "ui.screen", true );
	if ( pScreen->AsString() != screen )
	{
		pScreen->SetString( screen );
		if ( m_pImpl && m_pImpl->model )
			m_pImpl->model.DirtyVariable( "ui" );
	}
	if ( m_pContext )
		ApplyScreen();
	if ( m_pHost )
		m_pHost->OnNavigate( screen );
}

void Runtime::HandleEvent( const std::string &name, const std::vector< std::string > &args )
{
	if ( name == "nav" )
	{
		if ( !args.empty() )
			Navigate( args[ 0 ] );
		return;
	}
	if ( name == "reload" )
	{
		Reload();
		return;
	}
	m_pHost->OnCommand( name, args );
}

void Runtime::Reload()
{
	if ( !m_pContext )
		return;
	m_pHost->Log( 2, "Reloading the UI" );
	CloseDocuments();
	Rml::Factory::ClearStyleSheetCache();
	Rml::Factory::ClearTemplateCache();
	Rml::ReleaseTextures();
	m_pImpl->files.opened.clear();
	LoadStrings();
	BindModel();
	LoadDocuments();
	m_pImpl->fileTimes.clear();
	CheckFiles();
}

void Runtime::CheckFiles()
{
	bool bChanged = false;
	for ( const std::string &path : m_pImpl->files.opened )
	{
		const int64_t t = m_pHost->FileTime( path );
		auto it = m_pImpl->fileTimes.find( path );
		if ( it == m_pImpl->fileTimes.end() )
			m_pImpl->fileTimes[ path ] = t;
		else if ( it->second != t )
		{
			it->second = t;
			bChanged = true;
		}
	}
	if ( bChanged )
		Reload();
}

void Runtime::ShowDebugger( bool bShow )
{
#ifdef FUI_WITH_DEBUGGER
	if ( !m_pContext )
		return;
	if ( !m_pImpl->debuggerReady )
		m_pImpl->debuggerReady = Rml::Debugger::Initialise( m_pContext );
	Rml::Debugger::SetVisible( bShow );
#else
	(void)bShow;
	if ( m_pHost )
		m_pHost->Log( 1, "This build has no RmlUi debugger; use tools/fui-preview (F8)." );
#endif
}

//-----------------------------------------------------------------------------
// Strings and fonts
//-----------------------------------------------------------------------------
void Runtime::LoadStrings()
{
	auto load = [ this ]( const std::string &language, Node &out ) {
		std::string text, error;
		const std::string path = std::string( k_pszRoot ) + "lang/" + language + ".json";
		out.SetObject();
		if ( !m_pHost->ReadFile( path, text ) )
			return;
		m_pImpl->files.opened.insert( path );
		if ( !Node::Parse( text, out, &error ) )
			m_pHost->Log( 0, ( path + ": " + error ).c_str() );
	};
	load( "english", m_pImpl->fallbackStrings );
	const std::string language = m_pHost->Language();
	if ( language != "english" )
		load( language, m_pImpl->strings );
	else
		m_pImpl->strings.SetObject();
}

bool Runtime::Lookup( const std::string &token, std::string &out ) const
{
	if ( !m_pImpl )
		return false;
	if ( const Node *pString = m_pImpl->strings.Find( token ) )
	{
		out = pString->AsString();
		return true;
	}
	if ( const Node *pString = m_pImpl->fallbackStrings.Find( token ) )
	{
		out = pString->AsString();
		return true;
	}
	return false;
}

std::string Runtime::Translate( const std::string &token ) const
{
	if ( m_pImpl )
	{
		if ( const Node *pString = m_pImpl->strings.Find( token ) )
			return pString->AsString();
		if ( const Node *pString = m_pImpl->fallbackStrings.Find( token ) )
			return pString->AsString();
	}
	return "#" + token;
}

void Runtime::LoadFonts()
{
	std::string text, error;
	Node fonts;
	const std::string path = std::string( k_pszRoot ) + "fonts.json";
	if ( !m_pHost->ReadFile( path, text ) || !Node::Parse( text, fonts, &error ) || !fonts.IsArray() )
	{
		m_pHost->Log( 0, ( path + ": " + ( error.empty() ? std::string( "missing" ) : error ) ).c_str() );
		return;
	}
	for ( size_t i = 0; i < fonts.Size(); ++i )
	{
		const Node &font = *fonts.At( i );
		const Node *pFile = font.Find( "file" );
		if ( !pFile )
			continue;
		const Node *pFamily = font.Find( "family" );
		const Node *pWeight = font.Find( "weight" );
		const Node *pStyle = font.Find( "style" );
		const Node *pFallback = font.Find( "fallback" );
		const int weight = pWeight ? (int)pWeight->AsNumber( 0 ) : 0;
		m_pImpl->fonts->LoadFontFile( pFile->AsString(), pFamily ? pFamily->AsString() : std::string(),
			( pStyle && pStyle->AsString() == "italic" ) ? Rml::Style::FontStyle::Italic : Rml::Style::FontStyle::Normal,
			(Rml::Style::FontWeight)weight, pFallback && pFallback->AsBool() );
	}
}

} // namespace fui
