// Operating-system dependent wording and keys.

export type OS = "mac" | "windows" | "other";

// detectOS maps the browser's platform strings to an OS.
export function detectOS(platform: string, userAgent: string): OS {
    const p = `${platform} ${userAgent}`.toLowerCase();
    if (p.includes("mac") && !p.includes("iphone") && !p.includes("ipad")) {
        return "mac";
    }
    if (p.includes("win")) {
        return "windows";
    }
    return "other";
}

export function currentOS(): OS {
    const nav = navigator as Navigator & { userAgentData?: { platform?: string } };
    return detectOS(nav.userAgentData?.platform ?? nav.platform ?? "", nav.userAgent ?? "");
}

// revealLabel names the button that shows a file in the file manager.
export function revealLabel(os: OS): string {
    switch (os) {
        case "mac":
            return "Show in Finder";
        case "windows":
            return "Show in Explorer";
        default:
            return "Show in Folder";
    }
}

// sendShortcut is the hint next to the message box.
export function sendShortcut(os: OS): string {
    return os === "mac" ? "⌘+Enter" : "Ctrl+Enter";
}

// isSendKey tells whether a key press in the message box sends the message.
export function isSendKey(os: OS, e: { key: string; metaKey: boolean; ctrlKey: boolean; isComposing?: boolean; keyCode?: number }): boolean {
    // keyCode 229 marks a key handled by an input method; WKWebView does not
    // always set isComposing for the Enter that confirms a conversion.
    if (e.key !== "Enter" || e.isComposing || e.keyCode === 229) {
        return false;
    }
    return os === "mac" ? e.metaKey : e.ctrlKey;
}
