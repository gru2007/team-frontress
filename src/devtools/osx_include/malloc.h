// macOS has no <malloc.h>. Only the macOS build (devtools/vpc_osx.py) puts
// this directory on the include path.
#pragma once
#include <stdlib.h>
#include <malloc/malloc.h>
