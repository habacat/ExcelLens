import { test } from 'node:test';
import assert from 'node:assert/strict';
import {
  DEFAULT_ANALYSIS_NUMBER_OPTIONS,
  formatAnalysisNumber,
  getAnalysisNumberOptions,
  setAnalysisNumberOptions,
  type FormattableAnalysisCell,
} from './numbers.ts';

const cell = (exact: string, display: string = exact): FormattableAnalysisCell => ({
  exact,
  display,
});

test('derived ratios and cumulative cells reuse display preferences without formatting the original aggregate', () => {
  setAnalysisNumberOptions(DEFAULT_ANALYSIS_NUMBER_OPTIONS);
  try {
    const ratio: FormattableAnalysisCell = {
      exact: '9007199254740993.31',
      display: '9007199254740993.31',
      comparison: {
        mode: 'percent_grand',
        exact: '1/3',
        display: '33.333%',
        percent: true,
      },
    };
    const before = JSON.stringify(ratio);
    assert.equal(formatAnalysisNumber(ratio), '33.333%');
    assert.equal(
      formatAnalysisNumber(ratio, {
        ...DEFAULT_ANALYSIS_NUMBER_OPTIONS,
        scientific: false,
        groupThousands: false,
      }),
      '33.333%',
    );
    assert.equal(JSON.stringify(ratio), before);

    assert.equal(
      formatAnalysisNumber({
        exact: '5',
        display: '5',
        comparison: {
          mode: 'running_total',
          exact: '1234567.89',
          display: '1234567.89',
          percent: false,
        },
      }),
      '1,234,567.89',
    );
    assert.equal(
      formatAnalysisNumber({
        comparison: {
          mode: 'percent_grand',
          exact: '100000000000',
          display: '10000000000000.00%',
          percent: true,
        },
      }),
      '1.00e+13%',
    );
    for (const display of ['分母为0', '无有效值', '分母无有效值']) {
      assert.equal(
        formatAnalysisNumber({
          exact: '0',
          display: '0.00',
          comparison: {
            mode: 'percent_grand',
            exact: '',
            display,
            percent: true,
            status: 'unavailable',
          },
        }),
        display,
      );
    }
  } finally {
    setAnalysisNumberOptions(DEFAULT_ANALYSIS_NUMBER_OPTIONS);
  }
});

test('thousands grouping preserves decimal digits beyond floating point precision and changes only the display', () => {
  setAnalysisNumberOptions(DEFAULT_ANALYSIS_NUMBER_OPTIONS);
  try {
    const options = { ...DEFAULT_ANALYSIS_NUMBER_OPTIONS, scientific: false };
    const cases: readonly (readonly [string, string])[] = [
      ['9007199254741015.11', '9,007,199,254,741,015.11'],
      ['-1234567.00', '-1,234,567.00'],
      ['+1234.5000', '+1,234.5000'],
      ['123.00', '123.00'],
      ['0.00', '0.00'],
    ];
    for (const [input, expected] of cases) {
      const value = Object.freeze(cell(input));
      assert.equal(formatAnalysisNumber(value, options), expected);
      assert.equal(value.exact, input);
      assert.equal(value.display, input);
    }
  } finally {
    setAnalysisNumberOptions(DEFAULT_ANALYSIS_NUMBER_OPTIONS);
  }
});

test('default scientific notation rounds once from exact decimals, including negatives and carry to the next exponent', () => {
  setAnalysisNumberOptions(DEFAULT_ANALYSIS_NUMBER_OPTIONS);
  try {
    const cases: readonly (readonly [string, string, string])[] = [
      ['9007199254741015.11', '9007199254741015.11', '9.01e+15'],
      ['1286742750677287.87', '1286742750677287.87', '1.29e+15'],
      ['1004999999999.999', '1005000000000.00', '1.00e+12'],
      ['1005000000000', '1005000000000.00', '1.01e+12'],
      ['-9995000000000', '-9995000000000.00', '-1.00e+13'],
      ['1.2345e+80', `1${'0'.repeat(80)}`, '1.23e+80'],
      ['999999999999.999', '1000000000000.00', '1,000,000,000,000.00'],
      ['1000000000000', '1000000000000.00', '1.00e+12'],
    ];
    for (const [exact, display, expected] of cases) {
      assert.equal(formatAnalysisNumber(cell(exact, display)), expected, exact);
    }
  } finally {
    setAnalysisNumberOptions(DEFAULT_ANALYSIS_NUMBER_OPTIONS);
  }
});

test('rational averages use exact integer arithmetic even with large numerator and denominator', () => {
  setAnalysisNumberOptions(DEFAULT_ANALYSIS_NUMBER_OPTIONS);
  try {
    const cases: readonly (readonly [string, string, string])[] = [
      ['201000000000000/200', '1005000000000.00', '1.01e+12'],
      ['200999999999999/200', '1005000000000.00', '1.00e+12'],
      ['-1000000000000000/-3', '333333333333333.33', '3.33e+14'],
      ['-1000000000000000/3', '-333333333333333.33', '-3.33e+14'],
      [`1${'0'.repeat(450)}/3${'0'.repeat(434)}`, '3333333333333333.33', '3.33e+15'],
      ['109/30', '3.63', '3.63'],
    ];
    for (const [exact, display, expected] of cases) {
      assert.equal(formatAnalysisNumber(cell(exact, display)), expected, exact);
    }
  } finally {
    setAnalysisNumberOptions(DEFAULT_ANALYSIS_NUMBER_OPTIONS);
  }
});

test('independent settings and threshold apply without rewriting cells; malformed and missing values stay truthful', () => {
  setAnalysisNumberOptions(DEFAULT_ANALYSIS_NUMBER_OPTIONS);
  try {
    const large = cell('9007199254741015.11');
    const normal = cell('1234567.89');
    setAnalysisNumberOptions({ ...DEFAULT_ANALYSIS_NUMBER_OPTIONS, groupThousands: false });
    assert.equal(formatAnalysisNumber(large), '9.01e+15');
    assert.equal(formatAnalysisNumber(normal), '1234567.89');

    setAnalysisNumberOptions({ ...DEFAULT_ANALYSIS_NUMBER_OPTIONS, scientific: false });
    assert.equal(formatAnalysisNumber(large), '9,007,199,254,741,015.11');

    setAnalysisNumberOptions({ groupThousands: false, scientific: false, scientificExponent: 9 });
    assert.equal(formatAnalysisNumber(large), large.display);

    for (const exponent of [9, 12, 15] as const) {
      const settings = { ...DEFAULT_ANALYSIS_NUMBER_OPTIONS, scientificExponent: exponent };
      assert.equal(
        formatAnalysisNumber(cell(`1e${exponent}`, `1${'0'.repeat(exponent)}.00`), settings),
        `1.00e+${exponent}`,
      );
      assert.ok(
        !formatAnalysisNumber(
          cell(`9e${exponent - 1}`, `9${'0'.repeat(exponent - 1)}.00`),
          settings,
        ).includes('e+'),
      );
    }

    for (const value of [null, {}, cell('', '')]) {
      assert.equal(formatAnalysisNumber(value), '—');
    }
    for (const exact of ['NaN', 'Infinity', '1/0', '1/2/3', '1e99999', `1${'0'.repeat(9000)}`]) {
      assert.equal(formatAnalysisNumber(cell(exact, '无法计算')), '无法计算');
    }
    assert.equal(formatAnalysisNumber(cell('', '#VALUE!')), '#VALUE!');
    assert.throws(() => setAnalysisNumberOptions({ scientific: true }));
    assert.equal(getAnalysisNumberOptions().groupThousands, false);

    const copy = { ...getAnalysisNumberOptions() };
    const mutableCopy: { groupThousands: boolean } = copy;
    mutableCopy.groupThousands = true;
    assert.equal(getAnalysisNumberOptions().groupThousands, false);
  } finally {
    setAnalysisNumberOptions(DEFAULT_ANALYSIS_NUMBER_OPTIONS);
  }
});
