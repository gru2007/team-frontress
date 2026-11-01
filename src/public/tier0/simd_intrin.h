//========= Copyright Valve Corporation, All rights reserved. ============//
//
// Purpose: One place to pull in the SSE intrinsics.
//
// On ARM (Apple Silicon) the SSE intrinsics are provided by sse2neon, which
// maps them onto NEON. x86/x64 builds are unchanged.
//
//=============================================================================//

#ifndef TIER0_SIMD_INTRIN_H
#define TIER0_SIMD_INTRIN_H
#pragma once

#if defined( __aarch64__ ) || defined( __arm__ ) || defined( _M_ARM64 ) || defined( _M_ARM )
#include "../../thirdparty/sse2neon/sse2neon.h"
#else
#include <xmmintrin.h>
#include <emmintrin.h>
#endif

#endif // TIER0_SIMD_INTRIN_H
