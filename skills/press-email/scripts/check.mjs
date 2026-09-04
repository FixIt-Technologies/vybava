// bun run check.mjs <email.html> — renders at 640/390 px, screenshots to /tmp/press-email/, checks every link. Exit 1 on any failure.
// Playwright is installed on first run into ~/.cache/press-email (never beside this script: the skill dir is a Go-embedded payload).
import { existsSync, mkdirSync, writeFileSync } from 'node:fs';
import { resolve, join } from 'node:path';
import { homedir } from 'node:os';
import { spawnSync } from 'node:child_process';

const file = process.argv[2];
if (!file) { console.error('usage: check.mjs <email.html>'); process.exit(2); }

const cache = join(homedir(), '.cache', 'press-email');
if (!existsSync(join(cache, 'node_modules', 'playwright'))) {
  mkdirSync(cache, { recursive: true });
  writeFileSync(join(cache, 'package.json'), '{ "name": "press-email-runtime", "private": true, "dependencies": { "playwright": "^1.50.0" } }\n');
  const r = spawnSync('bun', ['install', '--cwd', cache], { stdio: 'inherit' });
  if (r.status !== 0) process.exit(r.status ?? 1);
}
process.env.PLAYWRIGHT_BROWSERS_PATH ??= join(homedir(), '.local', 'share', 'vybava', 'playwright-browsers');
const { chromium } = await import(join(cache, 'node_modules', 'playwright', 'index.mjs'));

const url = 'file://' + resolve(file);
const out = '/tmp/press-email';
mkdirSync(out, { recursive: true });
const browser = await chromium.launch({ headless: true });
let failed = false;

for (const [name, width] of [['desktop', 640], ['mobile', 390]]) {
  const page = await browser.newPage({ viewport: { width, height: 900 } });
  await page.goto(url);
  const overflow = await page.evaluate(() => document.documentElement.scrollWidth > document.documentElement.clientWidth);
  await page.screenshot({ path: `${out}/${name}.png`, fullPage: true });
  console.log(`${name} ${width}px overflow=${overflow}  ${out}/${name}.png`);
  failed ||= overflow;
  await page.close();
}

const page = await browser.newPage({ userAgent: 'Mozilla/5.0 (Macintosh; Intel Mac OS X 14_0) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/128 Safari/537.36' });
await page.goto(url);
const links = [...new Set(await page.$$eval('a[href]', as => as.map(a => a.href)))];
for (const href of links) {
  try {
    const r = await page.goto(href, { waitUntil: 'domcontentloaded', timeout: 30000 });
    const status = r?.status() ?? 0;
    failed ||= status !== 200;
    console.log(`${status} ${href}${page.url() !== href ? ' -> ' + page.url() : ''} | ${(await page.title()).slice(0, 60)}`);
  } catch (e) { failed = true; console.log(`ERR ${href} ${e.message.split('\n')[0]}`); }
}
await browser.close();
process.exit(failed ? 1 : 0);
