// A plain check of the text cleaners in format.ts. The project has no test
// runner; run it with "npm run check" (Node strips the types itself). It
// throws on the first failure.

import {cleanLabel, cleanText} from "./format.ts";

// Every character is built from its number, so no escape sequence is written.
const ch = (cp: number): string => String.fromCodePoint(cp);
const RLO = ch(0x202e);
const LRI = ch(0x2066);
const PDI = ch(0x2069);
const RLM = ch(0x200f);
const NEL = ch(0x85);
const LINE_SEP = ch(0x2028);
const NUL = ch(0);
const HEBREW = ch(0x5e9) + ch(0x5dc) + ch(0x5d5) + ch(0x5dd);
const FOLDER = ch(0x1f4c1);

function eq(got: string, want: string, what: string): void {
    if (got !== want) {
        throw new Error(`${what}: got ${JSON.stringify(got)}, want ${JSON.stringify(want)}`);
    }
}

eq(cleanLabel("invoice" + RLO + "fdp.exe"), "invoicefdp.exe", "override in a name");
eq(cleanText("a" + LRI + "b" + PDI + "c"), "abc", "isolates in text");
eq(cleanText("a\nb\tc\r\nd\re" + NEL + NUL), "a\nb\tc\nde", "controls in text");
eq(cleanLabel("a\nb\tc" + LINE_SEP + "d" + NEL), "a b c d", "line breaks in a label");
eq(cleanText(HEBREW + RLM + "x"), HEBREW + RLM + "x", "right-to-left text and marks stay");
eq(cleanLabel(HEBREW), HEBREW, "right-to-left label stays");
eq(cleanText(FOLDER + "x"), FOLDER + "x", "characters outside the BMP stay");
eq(cleanText(""), "", "empty");
