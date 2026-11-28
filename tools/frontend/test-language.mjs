import { readFileSync } from 'node:fs';
import { initializeLocalization, parseMessageCatalog } from '../../internal/ui/src/core/i18n.ts';

const catalog = new URL('../../internal/i18n/catalogs/zh-Hans.json', import.meta.url);
initializeLocalization('zh-Hans', parseMessageCatalog(readFileSync(catalog, 'utf8')));
