#import <Cocoa/Cocoa.h>

// Implemented in Go (toastclick_darwin.go).
extern void lanxferToastClicked(void);

static id gToastMonitor = nil;

static void runOnMainThread(dispatch_block_t block) {
	if ([NSThread isMainThread]) {
		block();
	} else {
		dispatch_async(dispatch_get_main_queue(), block);
	}
}

void lanxferInstallToastMonitor(void *window) {
	NSWindow *toast = (__bridge NSWindow *)window;
	runOnMainThread(^{
		if (gToastMonitor != nil) {
			[NSEvent removeMonitor:gToastMonitor];
			gToastMonitor = nil;
		}
		// The block keeps the window alive for as long as the monitor lives.
		gToastMonitor = [NSEvent addLocalMonitorForEventsMatchingMask:NSEventMaskLeftMouseDown
			handler:^NSEvent *(NSEvent *event) {
				if (event.window == toast) {
					lanxferToastClicked();
				}
				// Never swallow the event: the page gets it too.
				return event;
			}];
	});
}

void lanxferRemoveToastMonitor(void) {
	runOnMainThread(^{
		if (gToastMonitor != nil) {
			[NSEvent removeMonitor:gToastMonitor];
			gToastMonitor = nil;
		}
	});
}
