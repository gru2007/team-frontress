/*
 * d9mtmetal.dll: the PE half of D9MT's Metal unixlib, rebuilt so that it loads
 * on upstream Wine.
 *
 * The d9mt package builds this DLL against import libraries generated from
 * CrossOver's ntdll, which exports __wine_unix_call. Upstream Wine does not --
 * there, __wine_unix_call is winecrt0's one-line wrapper around the
 * __wine_unix_call_dispatcher pointer -- so the import resolves to a stub and
 * the client aborts the first time D9MT crosses into its unixlib, after the
 * Steam handshake and before a single frame:
 *
 *     wine: Call from 00006FFFFFF66798 to unimplemented function
 *           ntdll.dll.__wine_unix_call, aborting
 *
 * Dispatching through the pointer instead works on every Wine, CrossOver's
 * included, and the ntdll entry points are resolved by name at load time so
 * that neither one is an import that could fail to resolve.
 *
 * This is a drop-in for d9mt-x64's x86_64-windows/d9mtmetal.dll and pairs with
 * that package's unchanged x86_64-unix/d9mtmetal.so: same export, same unixlib
 * protocol. It stays here only until the fix lands in the d9mt build itself.
 */
#include <windows.h>

typedef UINT64 unixlib_handle_t;

/* Wine's MEMORY_INFORMATION_CLASS for "give this module its unixlib handle". */
#define MemoryWineUnixFuncs 1000

typedef NTSTATUS (WINAPI *unix_call_t)(unixlib_handle_t, unsigned int, void *);
typedef NTSTATUS (WINAPI *query_virtual_memory_t)(HANDLE, LPCVOID, int, PVOID, SIZE_T, SIZE_T *);

static unixlib_handle_t       d9mt_unix_handle;
static unix_call_t            d9mt_unix_call;
static query_virtual_memory_t d9mt_query_virtual_memory;

extern IMAGE_DOS_HEADER __ImageBase; /* this DLL's own base, like winecrt0 */

static void d9mt_resolve_ntdll(void)
{
    HMODULE ntdll = GetModuleHandleA("ntdll.dll");
    unix_call_t *dispatcher;

    if (!ntdll) return;

    d9mt_query_virtual_memory =
        (query_virtual_memory_t)GetProcAddress(ntdll, "NtQueryVirtualMemory");

    /* An exported variable: the address of the pointer, not the pointer. */
    dispatcher = (unix_call_t *)GetProcAddress(ntdll, "__wine_unix_call_dispatcher");
    if (dispatcher && *dispatcher) d9mt_unix_call = *dispatcher;
    else d9mt_unix_call = (unix_call_t)GetProcAddress(ntdll, "__wine_unix_call");
}

static NTSTATUS d9mt_query_unix_handle(void)
{
    if (!d9mt_query_virtual_memory) return STATUS_ENTRYPOINT_NOT_FOUND;
    return d9mt_query_virtual_memory(GetCurrentProcess(), &__ImageBase,
                                     MemoryWineUnixFuncs, &d9mt_unix_handle,
                                     sizeof(d9mt_unix_handle), NULL);
}

BOOL WINAPI DllMain(HINSTANCE instance, DWORD reason, LPVOID reserved)
{
    if (reason == DLL_PROCESS_ATTACH)
    {
        DisableThreadLibraryCalls(instance);
        d9mt_resolve_ntdll();
        /* Must run at attach time: Wine's bookkeeping for the unixlib is only
         * guaranteed alive while the module load is in progress. */
        d9mt_query_unix_handle();
    }
    return TRUE;
}

/* Lazy, so that a missing unixlib is a status D9MT can report rather than a
 * failed DLL load. */
__declspec(dllexport) int __cdecl D9MT_UnixCall(unsigned int code, void *params)
{
    if (!d9mt_unix_handle)
    {
        NTSTATUS status = d9mt_query_unix_handle();
        if (status) return status;
    }
    if (!d9mt_unix_call) return STATUS_ENTRYPOINT_NOT_FOUND;
    return d9mt_unix_call(d9mt_unix_handle, code, params);
}
