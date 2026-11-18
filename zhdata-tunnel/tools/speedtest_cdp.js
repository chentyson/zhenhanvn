// Drive a real speedtest.net session through a SOCKS proxy with Playwright.
// Records every :8080 request/response (full URL, size param, timing) and the
// per-second page text snapshot; stops only after the test has gone quiet.
const fs = require('fs');
const PW = 'C:/Users/tyson/AppData/Local/npm-cache/_npx/e41f203b7505f1fb/node_modules/playwright-core';
const { chromium } = require(PW);
const CHROME = process.env.LOCALAPPDATA + '/ms-playwright/chromium-1223/chrome-win64/chrome.exe';

const socks = process.argv[2] || '127.0.0.1:49888';
const out = process.argv[3] || 'speedtest_result.json';
const headless = (process.argv[4] || '1') !== '0';
const log = (...a) => console.log(new Date().toISOString().slice(11, 23), ...a);

async function main() {
  const t0 = Date.now();
  const browser = await chromium.launch({
    executablePath: CHROME, headless,
    proxy: { server: 'socks5://' + socks },
    args: ['--no-sandbox', '--disable-dev-shm-usage'],
  });
  const ctx = await browser.newContext({ viewport: { width: 1280, height: 900 } });
  const page = await ctx.newPage();
  page.setDefaultTimeout(120000);
  const ev = [];
  page.on('request', r => {
    const u = r.url();
    if (u.includes(':8080')) ev.push({ t: Date.now() - t0, kind: 'req', method: r.method(), url: u });
  });
  page.on('response', async r => {
    const u = r.url();
    if (u.includes(':8080')) {
      let body = '';
      try { body = (await r.text()).slice(0, 200); } catch (e) { }
      ev.push({ t: Date.now() - t0, kind: 'resp', status: r.status(), url: u.slice(0, 400), body });
    }
  });
  page.on('requestfinished', r => {
    const u = r.url();
    if (u.includes(':8080')) ev.push({ t: Date.now() - t0, kind: 'fin', url: u.slice(0, 400) });
  });
  page.on('requestfailed', r => {
    const u = r.url();
    if (u.includes(':8080')) ev.push({ t: Date.now() - t0, kind: 'fail', url: u.slice(0, 400), err: r.failure() && r.failure().errorText });
  });

  log('navigate', socks, 'headless=' + headless);
  await page.goto('https://www.speedtest.net/', { waitUntil: 'domcontentloaded', timeout: 120000 });
  for (let i = 0; i < 80; i++) {
    const st = await page.evaluate(() => ({ finding: /Finding optimal server/i.test(document.body.innerText), hasGo: /(^|\n)GO(\n|$)/.test(document.body.innerText) }));
    if (st.hasGo && !st.finding) break;
    await page.waitForTimeout(500);
  }
  try {
    await page.locator('[role=button]:has-text("GO"), button:has-text("GO")').first().click({ timeout: 8000 });
  } catch (e) {
    try { await page.getByText('GO', { exact: true }).first().click({ timeout: 8000, force: true }); } catch (e2) { log('CLICK FAILED', e2.message); }
  }
  log('clicked GO');

  const samples = [];
  let last8080 = Date.now();
  while (Date.now() - t0 < 150000) {
    await page.waitForTimeout(1000);
    const s = await page.evaluate(() => {
      const body = document.body.innerText || '';
      const leaf = [...document.querySelectorAll('*')].filter(e => e.children.length === 0)
        .map(e => ({ v: (e.textContent || '').trim(), c: (typeof e.className === 'string' ? e.className : '').slice(0, 50) }))
        .filter(x => /^\d+(\.\d+)?$/.test(x.v)).slice(0, 14);
      return { leaf, bodyLen: body.length, tail: body.slice(-300).replace(/\n+/g, '|') };
    });
    samples.push({ t: Date.now() - t0, ...s });
    const since = Date.now() - last8080;
    if (ev.length && since > 25000 && Date.now() - t0 > 55000) { log('quiet', (since / 1000) | 0, 's -> stop'); break; }
    if (ev.length) last8080 = Math.max(last8080, ev[ev.length - 1].t + t0);
    if (samples.length % 10 === 0) log('t=' + ((Date.now() - t0) / 1000 | 0) + 's events=' + ev.length);
  }
  const finalText = await page.evaluate(() => (document.body.innerText || '').slice(-1500));
  const res = { socks, headless, startedAt: new Date(t0).toISOString(), totalMs: Date.now() - t0, events: ev, finalText, samples };
  fs.writeFileSync(out, JSON.stringify(res, null, 2));
  log('done', ((Date.now() - t0) / 1000).toFixed(1) + 's events=' + ev.length, '->', out);
  await browser.close();
}
main().catch(e => { console.error('FATAL', e); process.exit(1); });
