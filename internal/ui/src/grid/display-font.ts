export const FONT_STACKS: Readonly<Record<string, string>> = Object.freeze({
  宋体: '"SimSun", "宋体", "Songti SC", "Noto Serif CJK SC", serif',
  新宋体: '"NSimSun", "新宋体", "SimSun", "宋体", serif',
  楷体: '"KaiTi", "楷体", "Kaiti SC", "STKaiti", serif',
  仿宋: '"FangSong", "仿宋", "STFangsong", serif',
  微软雅黑: '"Microsoft YaHei", "微软雅黑", "Microsoft YaHei UI", sans-serif',
  等线: '"DengXian", "等线", "Microsoft YaHei", sans-serif',
  黑体: '"SimHei", "黑体", "Microsoft YaHei", sans-serif',
  华文宋体: '"STSong", "华文宋体", "SimSun", serif',
  华文楷体: '"STKaiti", "华文楷体", "KaiTi", serif',
  华文仿宋: '"STFangsong", "华文仿宋", "FangSong", serif',
  隶书: '"LiSu", "隶书", "SimSun", serif',
  Arial: 'Arial, "Microsoft YaHei", sans-serif',
  'Microsoft Sans Serif': '"Microsoft Sans Serif", "SimSun", "宋体", sans-serif',
  Carlito: 'Carlito, Calibri, "Microsoft YaHei", sans-serif',
  Calibri: 'Calibri, Carlito, "Microsoft YaHei", sans-serif',
  Consolas: 'Consolas, "Microsoft YaHei", monospace',
});

export const FONT_ALIASES: Readonly<Record<string, string>> = Object.freeze({
  SimSun: '宋体',
  NSimSun: '新宋体',
  KaiTi: '楷体',
  FangSong: '仿宋',
  DengXian: '等线',
  SimHei: '黑体',
  'Microsoft YaHei': '微软雅黑',
  'Microsoft YaHei UI': '微软雅黑',
  STKaiti: '华文楷体',
  STSong: '华文宋体',
  STFangsong: '华文仿宋',
  LiSu: '隶书',
});

export function fontStack(value: string | null | undefined): string {
  const name = (value ?? '').trim().slice(0, 96);
  if (!name) return '';
  const normalized = FONT_ALIASES[name] ?? name;
  const known = FONT_STACKS[normalized];
  if (known) return known;
  return `"${name.replace(/["\\\n\r]/g, '')}", "Microsoft YaHei", "Noto Sans CJK SC", sans-serif`;
}

export function isKnownFont(font: string): boolean {
  return Object.hasOwn(FONT_STACKS, font);
}
