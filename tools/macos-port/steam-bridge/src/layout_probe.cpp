/* Prints the size and alignment of every params struct.
 *
 * Built once by each compiler; verify-layout.sh turns the Unix run's output
 * into static_asserts and compiles them with mingw-w64.  If the two toolchains
 * ever disagree about a struct, the bridge would silently read the wrong bytes
 * across the ABI boundary, so this is checked rather than assumed.
 */

#include <stdio.h>

#include "bridge_types.h"
#include "bridge_calls.h"
#include "params_list.h"

int main( void )
{
#define STEAM_BRIDGE_PROBE( name ) \
    printf( "%s %zu %zu\n", #name, sizeof(struct name), alignof(struct name) );

    STEAM_BRIDGE_PARAMS_LIST( STEAM_BRIDGE_PROBE )
    printf( "steam_bridge_call_count %d 0\n", (int)steam_bridge_call_count );
    return 0;
}
