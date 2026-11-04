//========= Team Frontress ====================================================//
//
// The UI's state tree: a small JSON-shaped value. The game (or the previewer)
// writes it, the RML documents read it through the "fr" data model.
//
//=============================================================================
#pragma once

#include <memory>
#include <string>
#include <utility>
#include <vector>

namespace fui {

class Node {
public:
	enum class Type { Null, Bool, Number, String, Array, Object };

	Node() = default;
	Node( bool b ) { SetBool( b ); }
	Node( int n ) { SetNumber( n ); }
	Node( double n ) { SetNumber( n ); }
	Node( const char *s ) { SetString( s ? s : "" ); }
	Node( std::string s ) { SetString( std::move( s ) ); }

	Node( Node && ) = default;
	Node &operator=( Node && ) = default;
	Node( const Node & ) = delete;
	Node &operator=( const Node & ) = delete;

	Type GetType() const { return m_eType; }
	bool IsNull() const { return m_eType == Type::Null; }
	bool IsArray() const { return m_eType == Type::Array; }
	bool IsObject() const { return m_eType == Type::Object; }

	void SetNull();
	void SetBool( bool b );
	void SetNumber( double n );
	void SetString( std::string s );
	void SetArray();
	void SetObject();

	bool AsBool( bool bDefault = false ) const;
	double AsNumber( double flDefault = 0.0 ) const;
	std::string AsString() const;	// numbers and bools are formatted

	// Objects. operator[] turns a non-object into an empty object first.
	Node &operator[]( const std::string &key );
	const Node *Find( const std::string &key ) const;
	Node *Find( const std::string &key );
	// A dotted path ("ui.screen", "locker.slots.2.item"); array parts are numbers.
	Node *FindPath( const std::string &path, bool bCreate );

	// Arrays. Append turns a non-array into an empty array first.
	Node &Append();
	Node &Append( Node &&value );
	size_t Size() const;
	Node *At( size_t i );
	const Node *At( size_t i ) const;

	size_t MemberCount() const { return m_Object.size(); }
	const std::string &MemberName( size_t i ) const { return m_Object[ i ].first; }
	Node &Member( size_t i ) { return *m_Object[ i ].second; }

	Node Clone() const;
	bool Equals( const Node &other ) const;

	// Replace the contents but keep this object's address, which the data
	// model holds on to.
	void Assign( Node &&other );

	static bool Parse( const std::string &text, Node &out, std::string *pError = nullptr );
	std::string Dump() const;

private:
	Type m_eType = Type::Null;
	bool m_bBool = false;
	double m_flNumber = 0.0;
	std::string m_String;
	std::vector< std::unique_ptr< Node > > m_Array;
	std::vector< std::pair< std::string, std::unique_ptr< Node > > > m_Object;
};

} // namespace fui
