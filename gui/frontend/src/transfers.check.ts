// A plain check of how history entries are matched to live cards. Run it with
// "npm run check" (Node strips the types itself). It throws on the first
// failure.

import {LiveTransfers} from "./transfers.ts";

function update(id: string, name: string, path: string, done = false) {
    return {
        ID: id, Name: name, path, Folder: false, Sent: 0, Total: 10, Files: 0, Skipped: 0,
        Done: done, Status: done ? "ok" : "", Error: "",
    };
}

function entry(name: string, path: string | undefined, dir = "out", kind = "file") {
    return {v: 1, time: "2026-01-01T00:00:00Z", dir, kind, name, path, status: "ok"} as any;
}

function eq<T>(got: T, want: T, what: string): void {
    if (got !== want) {
        throw new Error(`${what}: got ${JSON.stringify(got)}, want ${JSON.stringify(want)}`);
    }
}

// Two same-named files from different folders, sent by separate drops: the
// entry of the second removes the second card, not the oldest one.
{
    const live = new LiveTransfers();
    live.apply("k", update("t1", "a.txt", "/one/a.txt"), 0);
    live.apply("k", update("t2", "a.txt", "/two/a.txt"), 0);
    const hit = live.entryAdded("k", entry("a.txt", "/two/a.txt"));
    eq(hit?.id, "t2", "the card with the entry's path goes");
    eq(live.list().map((c) => c.id).join(","), "t1", "the other card stays");
}

// The same path twice: oldest first, and the entry's chat before others.
{
    const live = new LiveTransfers();
    live.apply("x", update("t1", "a.txt", "/p/a.txt"), 0);
    live.apply("k", update("t2", "a.txt", "/p/a.txt"), 0);
    live.apply("k", update("t3", "a.txt", "/p/a.txt"), 0);
    eq(live.entryAdded("k", entry("a.txt", "/p/a.txt"))?.id, "t2", "same chat, oldest");
    eq(live.entryAdded("k", entry("a.txt", "/p/a.txt"))?.id, "t3", "same chat, next");
    eq(live.entryAdded("k", entry("a.txt", "/p/a.txt"))?.id, "t1", "other chat, last");
}

// No matching path, no card: not by name, and not by an empty path.
{
    const live = new LiveTransfers();
    live.apply("k", update("t1", "a.txt", "/one/a.txt"), 0);
    eq(live.entryAdded("k", entry("a.txt", "/elsewhere/a.txt")), null, "same name, other path");
    eq(live.entryAdded("k", entry("a.txt", undefined)), null, "entry without a path");
    eq(live.entryAdded("k", entry("a.txt", "")), null, "entry with an empty path");
    eq(live.entryAdded("k", entry("a.txt", "/one/a.txt", "in")), null, "incoming entry");
    eq(live.entryAdded("k", entry("hi", "/one/a.txt", "out", "text")), null, "text entry");
    eq(live.list().length, 1, "the card is still there");
}
