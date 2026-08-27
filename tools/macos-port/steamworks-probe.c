#define _DARWIN_C_SOURCE
#include <dlfcn.h>
#include <stdbool.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>

typedef bool (*SteamAPI_InitSafeFn)(void);
typedef void (*SteamAPI_ShutdownFn)(void);
typedef int32_t (*SteamAPI_GetHSteamUserFn)(void);
typedef void *(*SteamInternal_FindUserInterfaceFn)(int32_t, const char *);
typedef bool (*SteamAppsIsInstalledFn)(void *, uint32_t);
typedef uint32_t (*SteamAppsGetInstallDirFn)(void *, uint32_t, char *, uint32_t);

static void *load_symbol(void *library, const char *name)
{
	void *symbol = dlsym(library, name);
	if (!symbol)
	{
		fprintf(stderr, "missing Steamworks symbol %s: %s\n", name, dlerror());
		exit(2);
	}
	return symbol;
}

int main(int argc, char **argv)
{
	if (argc != 2)
	{
		fprintf(stderr, "usage: %s /path/to/libsteam_api.dylib\n", argv[0]);
		return 2;
	}

	setenv("SteamAppId", "5147520", 0);
	setenv("SteamGameId", "5147520", 0);

	void *library = dlopen(argv[1], RTLD_NOW | RTLD_LOCAL);
	if (!library)
	{
		fprintf(stderr, "cannot load %s: %s\n", argv[1], dlerror());
		return 2;
	}

	SteamAPI_InitSafeFn steam_init = (SteamAPI_InitSafeFn)load_symbol(library, "SteamAPI_InitSafe");
	SteamAPI_ShutdownFn steam_shutdown = (SteamAPI_ShutdownFn)load_symbol(library, "SteamAPI_Shutdown");
	SteamAPI_GetHSteamUserFn get_user = (SteamAPI_GetHSteamUserFn)load_symbol(library, "SteamAPI_GetHSteamUser");
	SteamInternal_FindUserInterfaceFn find_interface = (SteamInternal_FindUserInterfaceFn)load_symbol(library, "SteamInternal_FindOrCreateUserInterface");
	SteamAppsIsInstalledFn is_installed = (SteamAppsIsInstalledFn)load_symbol(library, "SteamAPI_ISteamApps_BIsAppInstalled");
	SteamAppsGetInstallDirFn get_install_dir = (SteamAppsGetInstallDirFn)load_symbol(library, "SteamAPI_ISteamApps_GetAppInstallDir");

	if (!steam_init())
	{
		fprintf(stderr, "SteamAPI_Init failed; start native Steam and log in\n");
		dlclose(library);
		return 1;
	}

	void *apps = find_interface(get_user(), "STEAMAPPS_INTERFACE_VERSION008");
	if (!apps)
	{
		fprintf(stderr, "native Steam does not provide ISteamApps008\n");
		steam_shutdown();
		dlclose(library);
		return 1;
	}

	void **vtable = *(void ***)apps;
	void **type_info = (void **)vtable[-1];
	const char *type_name = type_info ? (const char *)type_info[1] : NULL;
	Dl_info image = {0};
	dladdr(vtable[0], &image);
	printf("ISteamApps008 RTTI: %s\n", type_name ? type_name : "<unavailable>");
	printf("ISteamApps008 image: %s\n", image.dli_fname ? image.dli_fname : "<unavailable>");

	char path[4096] = {0};
	bool installed = is_installed(apps, 440);
	uint32_t length = get_install_dir(apps, 440, path, sizeof(path));
	printf("BIsAppInstalled(440): %s\n", installed ? "true" : "false");
	printf("GetAppInstallDir(440): %s%s\n", length ? path : "<empty>", length ? "" : "");

	steam_shutdown();
	dlclose(library);
	return installed && length ? 0 : 1;
}
