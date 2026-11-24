// Render the readable PNG from the same SVG used by the offline reviewer.
const assert = require('node:assert/strict');
const path = require('node:path');
const { pathToFileURL } = require('node:url');
const { chromium } = require(path.resolve(__dirname, '../../../frontend/node_modules/playwright'));

async function renderPreview() {
  const base = path.join(__dirname, 'enterprise-platform');
  const browser = await chromium.launch();
  try {
    const page = await browser.newPage({ viewport: { width: 1440, height: 900 } });
    await page.goto(pathToFileURL(base + '.svg').href);
    await page.evaluate(() => document.fonts.ready);
    const overflows = await page.locator('.card').evaluateAll(cards => cards.flatMap(card => {
      const box = card.querySelector('rect').getBBox();
      return [...card.querySelectorAll('text')].filter((text, i) => {
        const label = text.getBBox();
        const rightMargin = i === 0 ? 52 : 1;
        return label.x < box.x || label.x + label.width > box.x + box.width - rightMargin ||
          label.y + label.height > box.y + box.height;
      }).map(text => [card.dataset.key, text.textContent]);
    }));
    assert.deepEqual(overflows, [], 'Component labels must stay within their nodes');
    await page.locator('svg').screenshot({path: base + '.png', timeout: 30000});
  } finally {
    await browser.close();
  }
}
renderPreview().catch(error => { console.error(error); process.exitCode = 1; });
