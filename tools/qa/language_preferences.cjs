const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { createHash } = require('node:crypto');
const { launch } = require('./browser_harness.cjs');

async function main() {
  const errors = [];
  const h = await launch({ errors });
  const results = [];
  try {
    const page = h.page;
    const navigate = async () => {
      await page.call('Page.navigate', { url: h.address });
      await page.wait('document.readyState === "complete" && window.ExcelLensSession');
    };
    const select = async (language) => {
      const token = await page.run(() => document.querySelector('meta[name="app-token"]').content);
      const response = await fetch(new URL('/api/interface', h.address), {
        method: 'POST',
        headers: { 'X-ExcelLens-Token': token, 'Content-Type': 'application/json' },
        body: JSON.stringify({ language }),
      });
      assert.equal(response.status, 200, await response.text());
    };
    const emptySearch = {
      'zh-Hans': '没有匹配的文件',
      'zh-Hant': '沒有相符的檔案',
      en: 'No matching files',
      ja: '一致するファイルがありません',
      ko: '일치하는 파일이 없습니다',
      ru: 'Подходящих файлов нет',
    };
    const duplicateLabel = {
      'zh-Hans': '查重',
      'zh-Hant': '查重',
      en: 'Find duplicates',
      ja: '重複チェック',
      ko: '중복 검사',
      ru: 'Поиск дубликатов',
    };
    const offer = async (language) => {
      await page.call('Network.enable');
      await page.call('Network.setExtraHTTPHeaders', { headers: { 'Accept-Language': language } });
    };
    const expectLanguage = async (language, heading) => {
      await page.wait('document.documentElement?.lang === ' + JSON.stringify(language) + ' && window.ExcelLensSession && document.querySelector(".landing-content h1")');
      const actual = await page.run(() => ({
        heading: document.querySelector('.landing-content h1').textContent.trim(),
        button: document.querySelector('#chooseFileBtn').textContent.trim(),
        findDuplicates: document.querySelector('#duplicateOpenBtn').textContent.trim(),
        lang: document.documentElement.lang,
        hasLanguageWidget: !!document.querySelector('[id="interfaceLanguage"]'),
      }));
      assert.equal(actual.lang, language);
      assert.equal(actual.heading, heading);
      assert.ok(actual.button && !actual.button.includes('html.main.'));
      assert.equal(actual.findDuplicates, duplicateLabel[language]);
      assert.equal(actual.hasLanguageWidget, false);
      await page.run(() => {
        const input = document.querySelector('#recentSearch');
        input.value = 'no-match-for-localization-test';
        input.dispatchEvent(new Event('input', { bubbles: true }));
      });
      await page.wait('document.querySelector("#recentList .recent-empty")?.textContent === ' + JSON.stringify(emptySearch[language]));
      results.push({ ...actual, emptySearch: emptySearch[language] });
    };

    await offer('fr-FR,ko-KR;q=0.9,en;q=0.6');
    await navigate();
    await expectLanguage('ko', '데이터 둘러보기');
    await page.run(() => {
      document.querySelector('#storageDialog').showModal();
      const fetchSettings = window.fetch.bind(window);
      window.fetch = async (input, options) => {
        const response = await fetchSettings(input, options);
        if (input === '/api/interface' && response.ok) {
          window.qaObservedLanguage = (await response.clone().json()).language;
        }
        return response;
      };
    });
    await offer('ja-JP');
    await page.run(() => window.dispatchEvent(new Event('languagechange')));
    await page.wait('window.qaObservedLanguage === "ja"');
    assert.equal(await page.evaluate('document.documentElement.lang'), 'ko');
    const closedAt = Date.now();
    await page.run(() => document.querySelector('#storageDialog').close());
    await expectLanguage('ja', 'データを閲覧');
    const closeSwitchMs = Date.now() - closedAt;
    assert.ok(closeSwitchMs < 8000, `Language switched ${closeSwitchMs}ms after the dialog closed`);
    await offer('fr-FR,ko-KR;q=0.9,en;q=0.6');
    for (const [locale, heading] of [
      ['zh-Hans','浏览数据'],
      ['zh-Hant','瀏覽資料'],
      ['en','Browse data'],
      ['ja','データを閲覧'],
      ['ko','데이터 둘러보기'],
      ['ru','Просмотр данных'],
    ]) {
      await select(locale);
      await expectLanguage(locale, heading);
    }

    const source = path.join(h.storage, 'Source-原文.csv');
    fs.writeFileSync(source, '编号,表示\n00123,한글 Русский 日本語\n');
    const digest = () => createHash('sha256').update(fs.readFileSync(source)).digest('hex');
    const original = digest();
    await page.file('#fileInput', source);
    await page.wait('window.ExcelLensPerformance().hasVisibleCoverage && !window.ExcelLensPerformance().sheetLoading');
    const sourceIsVisible = () => [...document.querySelectorAll('.grid-cell')].some(cell => cell.textContent === '00123');
    assert.equal(await page.run(sourceIsVisible), true);
    const expressions = await page.run(() => [
      document.querySelector('#searchAdvancedInput').placeholder,
      ...[...document.querySelectorAll('#searchAdvancedHelp code')].slice(1).map(el => el.textContent),
    ]);
    for (const query of expressions) {
      assert.deepEqual(await page.api('/api/search/validate', { query }), { valid: true });
    }
    await select('ja');
    assert.equal(await page.evaluate('document.documentElement.lang'), 'ru');
    assert.equal(await page.run(sourceIsVisible), true);
    await page.click('#homeBtn');
    await expectLanguage('ja', 'データを閲覧');
    assert.equal(digest(), original);

    await h.restart();
    await navigate();
    await expectLanguage('ja', 'データを閲覧');
    await select('');
    await expectLanguage('ko', '데이터 둘러보기');
    assert.deepEqual(errors, []);
    const receipt = process.env.QA_LANGUAGE_RECEIPT;
    if (receipt) fs.writeFileSync(receipt, JSON.stringify({ results, closeSwitchMs, expressions, original, errors }, null, 2) + '\n');
    console.log(JSON.stringify({ checks: results.length, closeSwitchMs, validSearchExamples: expressions.length, sourceUnchanged: true, errors: errors.length }));
  } finally { await h.close(); }
}

main().catch(error => { console.error(error); process.exitCode = 1; });
