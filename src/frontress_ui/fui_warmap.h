//========= Team Frontress ====================================================//
//
// <warmap>, the war's theater (see fui_warmap.cpp).
//
//=============================================================================
#pragma once

#include <memory>

namespace Rml {
class ElementInstancer;
}

namespace fui {

class Runtime;

std::unique_ptr< Rml::ElementInstancer > MakeWarMapInstancer( Runtime *pRuntime );

} // namespace fui
