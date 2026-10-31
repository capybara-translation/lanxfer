// Pure formatting helpers. Nothing here touches the DOM or the clock: callers
// pass "now" in, so the functions are easy to check.

const SIZE_UNITS = ["B", "KiB", "MiB", "GiB", "TiB", "PiB"];

// formatSize renders a byte count in binary units: "512 B", "4.8 GiB",
// "118 MiB". Values below 10 get one decimal place.
export function formatSize(bytes: number): string {
    let v = Number.isFinite(bytes) && bytes > 0 ? bytes : 0;
    let i = 0;
    while (v >= 1024 && i < SIZE_UNITS.length - 1) {
        v /= 1024;
        i++;
    }
    if (i === 0) {
        return `${Math.round(v)} B`;
    }
    // 1023.7 KiB would print as "1024 KiB"; show it in the next unit.
    if (Math.round(v) >= 1024 && i < SIZE_UNITS.length - 1) {
        v /= 1024;
        i++;
    }
    return `${v < 9.95 ? v.toFixed(1) : Math.round(v)} ${SIZE_UNITS[i]}`;
}

// formatSpeed renders bytes per second: "118 MiB/s".
export function formatSpeed(bytesPerSecond: number): string {
    return `${formatSize(bytesPerSecond)}/s`;
}

// formatDuration renders seconds as m:ss or h:mm:ss.
export function formatDuration(seconds: number): string {
    if (!Number.isFinite(seconds) || seconds < 0) {
        return "0:00";
    }
    if (seconds >= 86400) {
        return "more than a day";
    }
    const total = Math.round(seconds);
    const h = Math.floor(total / 3600);
    const m = Math.floor((total % 3600) / 60);
    const s = total % 60;
    const ss = String(s).padStart(2, "0");
    if (h > 0) {
        return `${h}:${String(m).padStart(2, "0")}:${ss}`;
    }
    return `${m}:${ss}`;
}

// formatPercent renders sent/total as a whole percent, 0 when total is 0.
export function formatPercent(sent: number, total: number): string {
    return `${Math.round(percent(sent, total))}%`;
}

// percent is sent/total as 0..100, 0 when total is not positive.
export function percent(sent: number, total: number): number {
    if (!(total > 0) || !(sent > 0)) {
        return 0;
    }
    return Math.min(100, (sent / total) * 100);
}

// parseTime turns an RFC 3339 string into milliseconds, or null for a missing
// time. Go encodes a zero time as year 1, which is also "missing".
export function parseTime(iso: string | undefined | null): number | null {
    if (!iso) {
        return null;
    }
    const ms = Date.parse(iso);
    if (!Number.isFinite(ms) || ms <= 0) {
        return null;
    }
    return ms;
}

const MINUTE = 60_000;
const HOUR = 60 * MINUTE;
const DAY = 24 * HOUR;

// formatRelative renders how long ago ms was: "just now", "5 min ago",
// "3 h ago", "yesterday", "4 days ago", then a date.
export function formatRelative(ms: number, now: number): string {
    const diff = now - ms;
    if (diff < MINUTE) {
        return "just now";
    }
    if (diff < HOUR) {
        return `${Math.floor(diff / MINUTE)} min ago`;
    }
    if (diff < DAY) {
        return `${Math.floor(diff / HOUR)} h ago`;
    }
    const days = daysBetween(ms, now);
    if (days <= 1) {
        return "yesterday";
    }
    if (days < 7) {
        return `${days} days ago`;
    }
    return new Date(ms).toLocaleDateString(undefined, {year: "numeric", month: "short", day: "numeric"});
}

// formatClock renders a message time: "10:12" for today, "Oct 30, 10:12"
// for earlier days, and with the year for other years.
export function formatClock(ms: number, now: number): string {
    const d = new Date(ms);
    const n = new Date(now);
    const time = d.toLocaleTimeString(undefined, {hour: "2-digit", minute: "2-digit"});
    if (d.toDateString() === n.toDateString()) {
        return time;
    }
    const date = d.toLocaleDateString(
        undefined,
        d.getFullYear() === n.getFullYear()
            ? {month: "short", day: "numeric"}
            : {year: "numeric", month: "short", day: "numeric"},
    );
    return `${date}, ${time}`;
}

// daysBetween counts calendar days from a to b (b later than a).
function daysBetween(a: number, b: number): number {
    const da = new Date(a);
    const db = new Date(b);
    const ua = Date.UTC(da.getFullYear(), da.getMonth(), da.getDate());
    const ub = Date.UTC(db.getFullYear(), db.getMonth(), db.getDate());
    return Math.round((ub - ua) / DAY);
}

// byteLength is the UTF-8 length of s, which is how the Go side limits text.
export function byteLength(s: string): number {
    return new TextEncoder().encode(s).length;
}

// plural renders "1 file" or "3 files".
export function plural(n: number, noun: string): string {
    return `${n} ${noun}${n === 1 ? "" : "s"}`;
}

// ---- text from other machines -----------------------------------------
//
// Names, messages and error texts come from another machine. They are shown
// through textContent, so they cannot add markup, but they can still contain
// characters that reorder what is around them: "invoice", then the
// right-to-left override, then "fdp.exe" is drawn as "invoiceexe.pdf". These
// helpers drop those characters for display only; the history keeps the raw
// text. The rule matches the receiver's own (isBidiControl and sanitize in
// internal/server/inbox.go): explicit embeddings, overrides and isolates go,
// and so do control characters. Marks such as LRM and RLM stay, and ordinary
// right-to-left text is untouched. Code points are compared as numbers so
// that no escape sequence appears in the source.

// isBidiControl reports whether cp is an explicit embedding, override or
// isolate: U+202A to U+202E and U+2066 to U+2069.
function isBidiControl(cp: number): boolean {
    return (cp >= 0x202a && cp <= 0x202e) || (cp >= 0x2066 && cp <= 0x2069);
}

// isControl reports whether cp is a C0 or C1 control character, which is what
// Go's unicode.IsControl reports.
function isControl(cp: number): boolean {
    return cp <= 0x1f || (cp >= 0x7f && cp <= 0x9f);
}

const LF = 0x0a;
const CR = 0x0d;
const TAB = 0x09;
const LINE_SEP = 0x2028;
const PARA_SEP = 0x2029;

// cleanText cleans message text for display: bidirectional controls and
// control characters are removed, except newline and tab. CR LF counts as a
// newline, and a lone CR is dropped.
export function cleanText(s: string): string {
    let out = "";
    let changed = false;
    for (const ch of s) {
        const cp = ch.codePointAt(0) as number;
        if (cp === LF || cp === TAB || (!isControl(cp) && !isBidiControl(cp))) {
            out += ch;
        } else {
            changed = true;
        }
    }
    return changed ? out : s;
}

// cleanLabel cleans text shown on one line, such as a name or a path: line
// breaks, tabs and the Unicode line and paragraph separators become spaces,
// and bidirectional controls and other control characters are removed.
export function cleanLabel(s: string): string {
    let out = "";
    let changed = false;
    for (const ch of s) {
        const cp = ch.codePointAt(0) as number;
        if (cp === LF || cp === CR || cp === TAB || cp === LINE_SEP || cp === PARA_SEP) {
            out += " ";
            changed = true;
        } else if (isControl(cp) || isBidiControl(cp)) {
            changed = true;
        } else {
            out += ch;
        }
    }
    return changed ? out : s;
}
