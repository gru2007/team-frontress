/*
 * macdrv_shim: the Cocoa entry points D9MT's winemetal looks for, on a Wine
 * that does not export them.
 *
 * DXMT's winemetal.so -- the piece D9MT presents through -- reaches the Metal
 * layer of a window with dlsym(RTLD_DEFAULT, "macdrv_functions"), a table
 * CodeWeavers adds to winemac's unixlib. Upstream Wine exports nothing from
 * that unixlib but its two call tables, so on the LGPL runtime this port ships
 * the lookup fails, every frame, and the client draws nothing at all:
 *
 *     err: d9mt: Presenter: CreateMetalViewFromHWND failed
 *     err: d9mt: Presenter: deferred surface creation after error
 *          VK_ERROR_SURFACE_LOST_KHR
 *
 * Nothing behind that table is actually private to Wine. The window is an
 * NSWindow in [NSApp windows] that carries its HWND on its own -hwnd property,
 * its content view is the WineContentView, and -newMetalViewWithDevice: is the
 * very method upstream's own macdrv_view_create_metal_view calls. So this
 * library answers the lookup itself, in terms of AppKit and the Objective-C
 * runtime, and the launcher puts it in the process with DYLD_INSERT_LIBRARIES.
 *
 * The struct winemetal reads back is the one handed out here, so CrossOver's
 * macdrv_win_data layout is reproduced rather than depended on -- upstream's
 * has since lost the cocoa_view/client_cocoa_view pair that winemetal indexes,
 * and that mismatch would be a wild pointer rather than a missing symbol.
 */
#import <AppKit/AppKit.h>
#import <Metal/Metal.h>
#import <QuartzCore/QuartzCore.h>

#include <objc/message.h>
#include <objc/runtime.h>
#include <stdio.h>
#include <stdlib.h>

typedef void *macdrv_view, *macdrv_window, *macdrv_metal_device;
typedef void *macdrv_metal_view, *macdrv_metal_layer;

/* CrossOver's layout, which is what winemetal casts the result of
 * get_win_data() to. Only the fourth field is ever read. */
struct macdrv_win_data
{
    void          *hwnd;
    macdrv_window  cocoa_window;
    macdrv_view    cocoa_view;
    macdrv_view    client_cocoa_view;
};

/* The presenter retries on every frame, so a failure that is not rate-limited
 * is a log file per minute. The first few are what a report needs; the rest say
 * nothing new. */
#define SHIM_MAX_COMPLAINTS 3

static void shim_log(const char *format, ...) __attribute__((format(printf, 1, 2)));

static void shim_log(const char *format, ...)
{
    va_list args;

    va_start(args, format);
    fprintf(stderr, "macdrv shim: ");
    vfprintf(stderr, format, args);
    fprintf(stderr, "\n");
    va_end(args);
}

/* Wine's Cocoa objects are the main thread's, and the caller here is whichever
 * thread D9MT presents on. */
static void shim_on_main_thread(dispatch_block_t block)
{
    if ([NSThread isMainThread]) block();
    else dispatch_sync(dispatch_get_main_queue(), block);
}

static id shim_window_for_hwnd(void *hwnd)
{
    __block id found = nil;

    shim_on_main_thread(^{
        for (NSWindow *window in [NSApp windows])
        {
            void *window_hwnd;

            /* -hwnd is WineWindow's; every other window in the process answers
             * no, which is also how a Wine without it is recognised. */
            if (![window respondsToSelector:@selector(hwnd)]) continue;
            window_hwnd = ((void *(*)(id, SEL))objc_msgSend)(window, @selector(hwnd));
            if (window_hwnd != hwnd) continue;
            found = window;
            break;
        }
    });

    return found;
}

__attribute__((visibility("default")))
struct macdrv_win_data *get_win_data(void *hwnd)
{
    static int complaints;
    struct macdrv_win_data *data;
    __block id view = nil;
    id window;

    if (!(window = shim_window_for_hwnd(hwnd)))
    {
        if (complaints++ < SHIM_MAX_COMPLAINTS)
            shim_log("no Wine window owns hwnd %p; is this a Wine with a Cocoa driver?", hwnd);
        return NULL;
    }

    shim_on_main_thread(^{ view = [(NSWindow *)window contentView]; });
    if (!view || ![view respondsToSelector:@selector(newMetalViewWithDevice:)])
    {
        if (complaints++ < SHIM_MAX_COMPLAINTS)
            shim_log("the content view of hwnd %p is %s, which cannot make a Metal view",
                     hwnd, view ? object_getClassName(view) : "(none)");
        return NULL;
    }

    if (!(data = calloc(1, sizeof(*data)))) return NULL;
    data->hwnd = hwnd;
    data->cocoa_window = (macdrv_window)window;
    data->cocoa_view = (macdrv_view)view;
    data->client_cocoa_view = (macdrv_view)view;
    return data;
}

__attribute__((visibility("default")))
void release_win_data(struct macdrv_win_data *data)
{
    free(data);
}

__attribute__((visibility("default")))
macdrv_window macdrv_get_cocoa_window(void *hwnd, BOOL require_on_screen)
{
    id window = shim_window_for_hwnd(hwnd);

    if (window && require_on_screen && ![(NSWindow *)window isVisible]) return NULL;
    return (macdrv_window)window;
}

__attribute__((visibility("default")))
macdrv_metal_device macdrv_create_metal_device(void)
{
    return (macdrv_metal_device)MTLCreateSystemDefaultDevice();
}

__attribute__((visibility("default")))
void macdrv_release_metal_device(macdrv_metal_device d)
{
    [(id<MTLDevice>)d release];
}

/* -newMetalViewWithDevice: adds the Metal view to the content view and caches
 * it, exactly as it does for Wine's own Vulkan path; calling it twice returns
 * the same view. */
__attribute__((visibility("default")))
macdrv_metal_view macdrv_view_create_metal_view(macdrv_view v, macdrv_metal_device d)
{
    static int reported;
    __block id metal_view = nil;

    shim_on_main_thread(^{
        metal_view = ((id (*)(id, SEL, id))objc_msgSend)((id)v,
                @selector(newMetalViewWithDevice:), (id)d);
    });

    if (!reported)
    {
        reported = 1;
        shim_log("%s Metal view for %s", metal_view ? "created the" : "FAILED to create a",
                 object_getClassName((id)v));
    }
    return (macdrv_metal_view)metal_view;
}

__attribute__((visibility("default")))
macdrv_metal_layer macdrv_view_get_metal_layer(macdrv_metal_view v)
{
    __block id layer = nil;

    shim_on_main_thread(^{ layer = [(NSView *)v layer]; });
    return (macdrv_metal_layer)layer;
}

__attribute__((visibility("default")))
void macdrv_view_release_metal_view(macdrv_metal_view v)
{
    shim_on_main_thread(^{
        [(NSView *)v removeFromSuperview];
        [(NSView *)v release];
    });
}

/* winemetal never calls this one; it is in the table, so it is answered. */
static void macdrv_init_display_devices(BOOL force)
{
    (void)force;
}

/* The order is winemetal's struct macdrv_functions_t. It looks this table up
 * first and falls back to the individual symbols above, so both are exported. */
__attribute__((visibility("default")))
struct
{
    void (*macdrv_init_display_devices)(BOOL);
    struct macdrv_win_data *(*get_win_data)(void *hwnd);
    void (*release_win_data)(struct macdrv_win_data *data);
    macdrv_window (*macdrv_get_cocoa_window)(void *hwnd, BOOL require_on_screen);
    macdrv_metal_device (*macdrv_create_metal_device)(void);
    void (*macdrv_release_metal_device)(macdrv_metal_device d);
    macdrv_metal_view (*macdrv_view_create_metal_view)(macdrv_view v, macdrv_metal_device d);
    macdrv_metal_layer (*macdrv_view_get_metal_layer)(macdrv_metal_view v);
    void (*macdrv_view_release_metal_view)(macdrv_metal_view v);
    void (*on_main_thread)(dispatch_block_t b);
} macdrv_functions = {
    macdrv_init_display_devices,
    get_win_data,
    release_win_data,
    macdrv_get_cocoa_window,
    macdrv_create_metal_device,
    macdrv_release_metal_device,
    macdrv_view_create_metal_view,
    macdrv_view_get_metal_layer,
    macdrv_view_release_metal_view,
    shim_on_main_thread,
};
