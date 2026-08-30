// House-style docx generator template (docx-js).
// Copy into the document home <exports>/<project>/docx/<type>/<slug>/,
// `npm install docx@8`, fill the CONTENT section, run with node. The rendered
// docx lands next to this script.
//
// Issuer identity, day rate and brand tokens are NEVER hardcoded here — they
// come from the machine-local identity file that `press identity` manages, so
// this template is publishable and every issuer gets their own house style.
import * as fs from 'fs';
import * as path from 'path';
import { fileURLToPath } from 'url';
import { execFileSync } from 'child_process';
import {
  Document, Packer, Paragraph, TextRun, ImageRun, Table, TableRow, TableCell,
  WidthType, BorderStyle, AlignmentType, Header, Footer, PageNumber, TabStopType, ShadingType,
} from 'docx';

// ── Issuer identity + house style, from the local press identity file ──
// `press identity init` scaffolds it; fill it in once per machine.
const { identity, missing } = JSON.parse(
  execFileSync('press', ['identity', 'show', '--json'], { encoding: 'utf8' }),
);
if (missing.length) {
  throw new Error(`press identity is incomplete — fill in ${missing.join(', ')} (press identity path)`);
}
const ISSUER = identity.issuer;
const RATE = identity.commercial;

const ACCENT = identity.brand.accent;          // headings, header/footer rules
const FILL_HEAD = identity.brand.tableHead;    // table header fill
const FILL_ROW = identity.brand.zebra;         // zebra row fill
const BORDER = identity.brand.hairline;        // table hairlines
const TEXT = identity.brand.text;              // body text
const MUTED = identity.brand.muted;            // captions, header/footer text
const FONT = identity.brand.font || 'Arial';
// body sz 21 (10.5pt) · tables sz 19 · A4 with 1134-twip margins

// This script is copied INTO the document home
// (<exports>/<project>/docx/<type>/<slug>/) and run from there, which is
// normally outside a git repository — so `press resolve` cannot and must not be
// used here. The output simply lands beside the script.
//
// PRESS_OUT overrides the file NAME only — it is deliberately reduced to its
// basename so the deliverable cannot be written outside the document home, and
// so `press index add --file` keeps describing where the file really is.
// Nothing hardcodes an exports root, so a custom PRESS_EXPORTS is honoured
// automatically by virtue of being where the document home already is.
const HOME_DIR = path.dirname(fileURLToPath(import.meta.url));
const OUT = path.join(HOME_DIR, path.basename(process.env.PRESS_OUT || 'CHANGE_ME.docx'));

// ── Helpers ──
const thin = { style: BorderStyle.SINGLE, size: 4, color: BORDER };
const tableBorders = { top: thin, bottom: thin, left: thin, right: thin, insideHorizontal: thin, insideVertical: thin };

// mini-markdown: **bold** spans inside any text
function runs(text, base) {
  const parts = String(text).split(/\*\*/);
  return parts.map((p, i) => new TextRun({ text: p, font: FONT, bold: i % 2 === 1 || base.bold, size: base.size, color: base.color, italics: base.italics }));
}
const body = (text, opts = {}) => new Paragraph({ spacing: { after: 120, line: 276 }, children: runs(text, { size: 21, color: TEXT, ...opts }) });
const h1 = (num, text) => new Paragraph({
  spacing: { before: 360, after: 160 },
  children: [new TextRun({ text: `${num} · ${text}`, font: FONT, bold: true, size: 30, color: ACCENT })],
});
const h2 = (text) => new Paragraph({ spacing: { before: 240, after: 100 }, children: [new TextRun({ text, font: FONT, bold: true, size: 24, color: TEXT })] });
const bullet = (text) => new Paragraph({ spacing: { after: 60, line: 276 }, bullet: { level: 0 }, children: runs(text, { size: 21, color: TEXT }) });
const caption = (text) => new Paragraph({
  spacing: { before: 60, after: 240 }, alignment: AlignmentType.CENTER,
  children: [new TextRun({ text, font: FONT, italics: true, size: 18, color: MUTED })],
});
// Screenshot embed: pass natural pixel size via ratio (default 1440×900); page fits ~640px wide
const img = (file, cap, w = 620, ratio = 900 / 1440) => [
  new Paragraph({
    spacing: { before: 120, after: 0 }, alignment: AlignmentType.CENTER,
    children: [new ImageRun({ data: fs.readFileSync(file), transformation: { width: w, height: Math.round(w * ratio) } })],
  }),
  caption(cap),
];
const cell = (text, { fill, bold, width, size = 19, align } = {}) => new TableCell({
  shading: fill ? { type: ShadingType.CLEAR, fill } : undefined,
  width: width ? { size: width, type: WidthType.PERCENTAGE } : undefined,
  margins: { top: 80, bottom: 80, left: 120, right: 120 },
  children: [new Paragraph({ alignment: align, children: runs(text, { size, color: TEXT, bold }) })],
});
const table = (headRow, rows, widths, aligns = []) => new Table({
  width: { size: 100, type: WidthType.PERCENTAGE },
  borders: tableBorders,
  rows: [
    new TableRow({ tableHeader: true, children: headRow.map((t, i) => cell(t, { fill: FILL_HEAD, bold: true, width: widths?.[i], align: aligns[i] })) }),
    ...rows.map((r, ri) => new TableRow({ children: r.map((t, i) => cell(t, { fill: ri % 2 ? FILL_ROW : undefined, width: widths?.[i], align: aligns[i] })) })),
  ],
});
const spacer = () => new Paragraph({ spacing: { after: 120 }, children: [] });

// Title block: big accent title + bold subtitle + muted tagline
const titleBlock = (title, subtitle, tagline) => [
  new Paragraph({ spacing: { before: 240, after: 40 }, children: [new TextRun({ text: title, font: FONT, bold: true, size: 48, color: ACCENT })] }),
  new Paragraph({ spacing: { after: 40 }, children: [new TextRun({ text: subtitle, font: FONT, bold: true, size: 28, color: TEXT })] }),
  new Paragraph({ spacing: { after: 240 }, children: [new TextRun({ text: tagline, font: FONT, size: 20, color: MUTED })] }),
];

const makeHeader = (docLabel) => new Header({
  children: [new Paragraph({
    border: { bottom: { style: BorderStyle.SINGLE, size: 8, color: ACCENT } },
    tabStops: [{ type: TabStopType.RIGHT, position: 9640 }],
    spacing: { after: 120 },
    children: [
      new TextRun({ text: ISSUER.name, font: FONT, bold: true, size: 18, color: ACCENT }),
      new TextRun({ text: `\t${docLabel}`, font: FONT, size: 18, color: MUTED }),
    ],
  })],
});
const makeFooter = () => new Footer({
  children: [new Paragraph({
    border: { top: { style: BorderStyle.SINGLE, size: 8, color: ACCENT } },
    tabStops: [{ type: TabStopType.RIGHT, position: 9640 }],
    spacing: { before: 120 },
    children: [
      new TextRun({ text: `${ISSUER.name} · IČO ${ISSUER.ico} · ${ISSUER.address}`, font: FONT, size: 16, color: MUTED }),
      new TextRun({ text: '\tStrana ', font: FONT, size: 16, color: MUTED }),
      new TextRun({ children: [PageNumber.CURRENT], font: FONT, size: 16, color: MUTED }),
      new TextRun({ text: ' / ', font: FONT, size: 16, color: MUTED }),
      new TextRun({ children: [PageNumber.TOTAL_PAGES], font: FONT, size: 16, color: MUTED }),
    ],
  })],
});

// Supplier identity (from the local identity file) + client columns
const identityTable = (client, issued, validUntil) => table(['', 'Dodavatel', 'Objednatel'], [
  ['Společnost', ISSUER.name, client.name],
  ['IČO', ISSUER.ico, client.ico],
  ['DIČ', ISSUER.dic, client.dic],
  ['Sídlo', ISSUER.address, client.address],
  ['Datová schránka', ISSUER.dataBox || '—', client.ds ?? '—'],
  ['Datum vystavení', issued, ''],
  ['Platnost nabídky do', validUntil, ''],
], [24, 38, 38]);

// The rate sentence that opens every `Cena` chapter.
const rateSentence = () =>
  `Sazba pro položky nad rámec pevné ceny: **${RATE.dayRate.toLocaleString('cs-CZ')} ${RATE.currency} / ${RATE.rateUnit}**. ${RATE.vatNote}`;

// ── CONTENT — replace everything below ──
const children = [
  ...titleBlock('Cenová nabídka', 'PŘEDMĚT — dodávka, provoz a podpora', 'CHANGE_ME — obor a zaměření dodavatele'),
  identityTable({ name: 'NÁZEV KLIENTA', ico: '…', dic: '…', address: '…', ds: '…' }, 'D. měsíce RRRR', 'D. měsíce RRRR'),
  spacer(),
  h1('1', 'Předmět nabídky'),
  body('…'),
  // h1/h2/bullet/table/img per the skill's document anatomy
];

const doc = new Document({
  creator: ISSUER.name,
  title: 'CHANGE_ME',
  styles: { default: { document: { run: { font: FONT, size: 21, color: TEXT } } } },
  sections: [{
    properties: { page: { margin: { top: 1134, right: 1134, bottom: 1134, left: 1134 } } },
    headers: { default: makeHeader('Cenová nabídka — CHANGE_ME') },
    footers: { default: makeFooter() },
    children,
  }],
});

Packer.toBuffer(doc).then((buf) => {
  fs.writeFileSync(OUT, buf);
  console.log('written', OUT, buf.length, 'bytes');
});
