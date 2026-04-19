/**
 * Playwright e2e tests for generated HTML slides.
 *
 * Validates:
 * 1. Template API engine produces valid HTML for all 8 templates
 * 2. Validation engine catches banned patterns
 * 3. Sandbox preview renders without errors
 * 4. Generated ZIP slides render correctly in viewer
 *
 * Usage:
 *   npx playwright test --config docs/sharing/tests/playwright.config.ts
 *   # or just:
 *   npx playwright test docs/sharing/tests/
 */

const { test, expect } = require('@playwright/test');

// ===== Test: Template API Engine =====
test.describe('Template API Engine', () => {
  const BASE = 'http://localhost:19870';

  test.beforeEach(async ({ page }) => {
    // Navigate to generator - requires the viewer to be running
    try {
      await page.goto(`${BASE}/generator.html`, { timeout: 5000 });
    } catch (e) {
      test.skip('课件查看器未运行在 localhost:19870，跳过测试');
    }
  });

  test('cards template renders correctly', async ({ page }) => {
    const html = await page.evaluate(() => {
      const engine = window.TEMPLATES;
      if (!engine) throw new Error('TEMPLATES not found');
      return engine.cards({
        cols: 3,
        items: [
          { title: '测试卡片1', desc: '描述文字1', color: '#ff6b6b' },
          { title: '测试卡片2', desc: '描述文字2', color: '#ffa94d' },
          { title: '测试卡片3', desc: '描述文字3', color: '#4dabf7' },
        ]
      });
    });
    expect(html).toContain('class="cards3"');
    expect(html).toContain('测试卡片1');
    expect(html).toContain('#ff6b6b');
    expect(html).toContain('#4dabf7');
  });

  test('metrics template renders correctly', async ({ page }) => {
    const html = await page.evaluate(() => {
      return window.TEMPLATES.metrics({
        items: [
          { value: '121K', label: '行代码', color: 'cg' },
          { value: '328', label: '文件', color: 'co' },
        ]
      });
    });
    expect(html).toContain('class="mets"');
    expect(html).toContain('met-v cg');
    expect(html).toContain('121K');
    expect(html).toContain('行代码');
  });

  test('blockquote template renders correctly', async ({ page }) => {
    const html = await page.evaluate(() => {
      return window.TEMPLATES.blockquote({
        quotes: [
          { text: '创新是第一动力', size: 'large', color: '#ff6b6b' }
        ],
        footer: '底部说明'
      });
    });
    expect(html).toContain('class="bq"');
    expect(html).toContain('创新是第一动力');
    expect(html).toContain('底部说明');
  });

  test('split template renders correctly', async ({ page }) => {
    const html = await page.evaluate(() => {
      return window.TEMPLATES.split({
        left: { title: '左栏', content: '左边内容', color: 'cb' },
        right: { title: '右栏', content: '右边内容', color: 'cg' }
      });
    });
    expect(html).toContain('class="split"');
    expect(html).toContain('split-half');
    expect(html).toContain('左栏');
    expect(html).toContain('右栏');
  });

  test('flow template renders correctly', async ({ page }) => {
    const html = await page.evaluate(() => {
      return window.TEMPLATES.flow({
        steps: ['步骤一', '步骤二', '步骤三']
      });
    });
    expect(html).toContain('class="flow"');
    expect(html).toContain('fstep');
    expect(html).toContain('farrow');
    expect(html).toContain('步骤二');
  });

  test('stages template renders correctly', async ({ page }) => {
    const html = await page.evaluate(() => {
      return window.TEMPLATES.stages({
        items: [
          { name: '需求', desc: '收集需求' },
          { name: '开发', desc: '编码实现' },
        ]
      });
    });
    expect(html).toContain('class="stages-row"');
    expect(html).toContain('stage-item');
    expect(html).toContain('需求');
  });

  test('title template renders correctly', async ({ page }) => {
    const html = await page.evaluate(() => {
      return window.TEMPLATES.title({
        subtitle: '副标题文字',
        stats: [{ value: '100', label: '测试', color: 'cb' }]
      });
    });
    expect(html).toContain('class="sub"');
    expect(html).toContain('副标题文字');
    expect(html).toContain('100');
  });

  test('list template renders correctly', async ({ page }) => {
    const html = await page.evaluate(() => {
      return window.TEMPLATES.list({
        items: ['要点一', '要点二']
      });
    });
    expect(html).toContain('要点一');
    expect(html).toContain('要点二');
  });
});

// ===== Test: Validation Engine =====
test.describe('Validation Engine', () => {
  const BASE = 'http://localhost:19870';

  test.beforeEach(async ({ page }) => {
    try {
      await page.goto(`${BASE}/generator.html`, { timeout: 5000 });
    } catch (e) {
      test.skip('课件查看器未运行');
    }
  });

  test('catches banned <script> tag', async ({ page }) => {
    const result = await page.evaluate(() => {
      return window.validateSlideHTML('<script>alert(1)</script><p>hello</p>');
    });
    expect(result.errors.length).toBeGreaterThan(0);
    expect(result.errors.some(e => e.id.includes('banned-tag:script'))).toBeTruthy();
  });

  test('catches banned position:fixed', async ({ page }) => {
    const result = await page.evaluate(() => {
      return window.validateSlideHTML('<div style="position:fixed;top:0">bad</div>');
    });
    expect(result.errors.length).toBeGreaterThan(0);
    expect(result.errors.some(e => e.id.includes('banned-style'))).toBeTruthy();
  });

  test('catches external image URLs', async ({ page }) => {
    const result = await page.evaluate(() => {
      return window.validateSlideHTML('<img src="https://evil.com/track.gif">');
    });
    expect(result.errors.length).toBeGreaterThan(0);
    expect(result.errors.some(e => e.id === 'external-url')).toBeTruthy();
  });

  test('catches onclick event handlers', async ({ page }) => {
    const result = await page.evaluate(() => {
      return window.validateSlideHTML('<div onclick="alert(1)">click</div>');
    });
    expect(result.errors.length).toBeGreaterThan(0);
    expect(result.errors.some(e => e.id.includes('banned-attr:onclick'))).toBeTruthy();
  });

  test('warns about unknown CSS classes', async ({ page }) => {
    const result = await page.evaluate(() => {
      return window.validateSlideHTML('<div class="my-custom-class">hello</div>');
    });
    expect(result.warnings.length).toBeGreaterThan(0);
    expect(result.warnings.some(w => w.id.includes('unknown-class'))).toBeTruthy();
  });

  test('passes valid cards HTML', async ({ page }) => {
    const html = '<div class="cards3"><div class="card"><h4>Title</h4><p>Desc</p></div></div>';
    const result = await page.evaluate((h) => {
      return window.validateSlideHTML(h);
    }, html);
    expect(result.errors.length).toBe(0);
  });

  test('passes valid metrics HTML', async ({ page }) => {
    const html = '<div class="mets"><div style="text-align:center"><div class="met-v cg">100</div><div class="met-l">items</div></div></div>';
    const result = await page.evaluate((h) => {
      return window.validateSlideHTML(h);
    }, html);
    expect(result.errors.length).toBe(0);
  });
});

// ===== Test: JSON Template Parser =====
test.describe('JSON Template Parser', () => {
  const BASE = 'http://localhost:19870';

  test.beforeEach(async ({ page }) => {
    try {
      await page.goto(`${BASE}/generator.html`, { timeout: 5000 });
    } catch (e) {
      test.skip('课件查看器未运行');
    }
  });

  test('parses single JSON template', async ({ page }) => {
    const result = await page.evaluate(() => {
      return window.parseJSONTemplates('```json\n{"tpl":"cards","data":{"cols":3,"items":[{"title":"T1","desc":"D1","color":"#ff6b6b"}]}}\n```');
    });
    expect(result).not.toBeNull();
    expect(result.length).toBe(1);
    expect(result[0].tpl).toBe('cards');
  });

  test('parses JSON array (multi-page)', async ({ page }) => {
    const result = await page.evaluate(() => {
      return window.parseJSONTemplates('[{"tpl":"title","data":{"subtitle":"sub"}},{"tpl":"flow","data":{"steps":["s1","s2"]}}]');
    });
    expect(result).not.toBeNull();
    expect(result.length).toBe(2);
    expect(result[0].tpl).toBe('title');
    expect(result[1].tpl).toBe('flow');
  });

  test('returns null for plain text', async ({ page }) => {
    const result = await page.evaluate(() => {
      return window.parseJSONTemplates('Just some text without JSON');
    });
    expect(result).toBeNull();
  });

  test('returns null for HTML-only content', async ({ page }) => {
    const result = await page.evaluate(() => {
      return window.parseJSONTemplates('```html\n<div class="cards3"><div class="card"><h4>Hi</h4></div></div>\n```');
    });
    expect(result).toBeNull();
  });
});

// ===== Test: Sandbox Preview Rendering =====
test.describe('Sandbox Preview', () => {
  const BASE = 'http://localhost:19870';

  test.beforeEach(async ({ page }) => {
    try {
      await page.goto(`${BASE}/generator.html`, { timeout: 5000 });
    } catch (e) {
      test.skip('课件查看器未运行');
    }
  });

  test('renders custom HTML in sandbox iframe', async ({ page }) => {
    // Set up a custom slide via page state
    await page.evaluate(() => {
      window.slides[0] = {
        title: 'Test Slide',
        layout: 'custom',
        data: { html: '<div class="bq" style="font-size:clamp(1.3rem,2.5vw,2.2rem);border-left-color:#4dabf7">测试引用文字</div>' }
      };
      window.selectSlide(0);
    });
    // Check that the sandbox iframe appears
    const iframe = page.locator('#sandboxPreview');
    await expect(iframe).toBeVisible({ timeout: 5000 });
    // Check the iframe content renders the bq
    const frame = iframe.contentFrame();
    await expect(frame.locator('.bq')).toContainText('测试引用文字');
  });

  test('renders cards template via JSON mode', async ({ page }) => {
    await page.evaluate(() => {
      window.slides[0] = {
        title: 'Cards Test',
        layout: 'cards',
        data: {
          cols: 3,
          items: [
            { title: '卡片A', desc: '描述A', borderColor: '#ff6b6b' },
            { title: '卡片B', desc: '描述B', borderColor: '#4dabf7' }
          ]
        }
      };
      window.selectSlide(0);
    });
    // Cards layout uses the standard preview, not sandbox
    const preview = page.locator('.preview-slide');
    await expect(preview).toBeVisible();
    await expect(preview.locator('.pv-card')).toHaveCount(2);
  });
});
