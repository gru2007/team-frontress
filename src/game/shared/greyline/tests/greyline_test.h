//========= GREYLINE FRONTRESS ============================================//
//
// Purpose: just enough test framework, and no dependency to install.
//
// The tree has no C++ test runner and adding one for two hundred assertions
// would be a bigger decision than the tests are worth. This is the whole
// harness: a check macro that reports the file, line, and both values, and a
// registry that runs everything and returns a shell exit code.
//
//=========================================================================//

#ifndef GREYLINE_TEST_H
#define GREYLINE_TEST_H

#include <cstdio>
#include <cstring>
#include <string>
#include <vector>

namespace greylinetest
{

struct Test_t
{
	const char *m_pszName;
	void ( *m_pfnRun )();
};

inline std::vector<Test_t> &Registry()
{
	static std::vector<Test_t> s_Tests;
	return s_Tests;
}

inline int &Failures()
{
	static int s_nFailures = 0;
	return s_nFailures;
}

inline const char *&CurrentTest()
{
	static const char *s_pszCurrent = "";
	return s_pszCurrent;
}

struct Registrar_t
{
	Registrar_t( const char *pszName, void ( *pfn )() )
	{
		Test_t t = { pszName, pfn };
		Registry().push_back( t );
	}
};

inline void Fail( const char *pszFile, int nLine, const std::string &strWhat )
{
	++Failures();
	std::fprintf( stderr, "  FAIL %s\n    %s:%d\n    %s\n",
		CurrentTest(), pszFile, nLine, strWhat.c_str() );
}

// Renders a value for a failure message. Overloads rather than a template so a
// null char* prints as "(null)" instead of crashing the test run.
inline std::string Show( const char *psz ) { return psz ? std::string( "\"" ) + psz + "\"" : "(null)"; }
inline std::string Show( const std::string &s ) { return "\"" + s + "\""; }
inline std::string Show( int v ) { return std::to_string( v ); }
inline std::string Show( long long v ) { return std::to_string( v ); }
inline std::string Show( unsigned long long v ) { return std::to_string( v ); }
inline std::string Show( bool v ) { return v ? "true" : "false"; }

inline bool Same( const char *a, const char *b )
{
	if ( !a || !b )
		return a == b;
	return std::strcmp( a, b ) == 0;
}
inline bool Same( int a, int b ) { return a == b; }
inline bool Same( long long a, long long b ) { return a == b; }
inline bool Same( unsigned long long a, unsigned long long b ) { return a == b; }
inline bool Same( bool a, bool b ) { return a == b; }
inline bool Same( const std::string &a, const std::string &b ) { return a == b; }

inline int RunAll( const char *pszSuite )
{
	std::printf( "%s: %zu tests\n", pszSuite, Registry().size() );
	for ( size_t i = 0; i < Registry().size(); ++i )
	{
		CurrentTest() = Registry()[i].m_pszName;
		const int nBefore = Failures();
		Registry()[i].m_pfnRun();
		if ( Failures() == nBefore )
		{
			std::printf( "  ok   %s\n", Registry()[i].m_pszName );
		}
	}

	if ( Failures() > 0 )
	{
		std::fprintf( stderr, "\n%s: %d assertion(s) failed\n", pszSuite, Failures() );
		return 1;
	}
	std::printf( "%s: all passed\n", pszSuite );
	return 0;
}

} // namespace greylinetest

#define GREYLINE_TEST( name )                                                  \
	static void name();                                                        \
	static greylinetest::Registrar_t g_Register_##name( #name, name );          \
	static void name()

#define CHECK( cond )                                                          \
	do {                                                                       \
		if ( !( cond ) )                                                       \
			greylinetest::Fail( __FILE__, __LINE__, "expected: " #cond );      \
	} while ( 0 )

#define CHECK_EQ( got, want )                                                  \
	do {                                                                       \
		if ( !greylinetest::Same( ( got ), ( want ) ) )                        \
			greylinetest::Fail( __FILE__, __LINE__,                            \
				std::string( #got " == " #want "\n      got:  " ) +            \
				greylinetest::Show( got ) + "\n      want: " +                 \
				greylinetest::Show( want ) );                                  \
	} while ( 0 )

#endif // GREYLINE_TEST_H
