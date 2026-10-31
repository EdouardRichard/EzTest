const { chromium } = require('D:/Project_new/EzTest/executor/node_modules/playwright');
const path = require('node:path');

const CHROME = 'C:/Program Files/Google/Chrome/Application/chrome.exe';
const BASE = 'file:///D:/Project_new/EzTest/docs/version-V0.2/ui/mock.html';

(async () => {
  const browser = await chromium.launch({
    executablePath: CHROME,
    headless: true,
    args: ['--allow-file-access-from-files', '--font-render-hinting=none'],
  });
  const context = await browser.newContext({
    viewport: { width: 1600, height: 1100 },
    deviceScaleFactor: 1.5,
  });
  const page = await context.newPage();
  for (const name of ['requirements', 'design', 'chain']) {
    await page.goto(`${BASE}?screen=${name}`);
    await page.waitForTimeout(900);
    const target = path.join(__dirname, `${name}.png`);
    await page.screenshot({ path: target, fullPage: false });
    console.log(`shot ${name} -> ${target}`);
  }
  await browser.close();
})().catch((error) => {
  console.error(error);
  process.exit(1);
});
