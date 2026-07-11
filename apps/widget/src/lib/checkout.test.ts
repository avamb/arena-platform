/**
 * checkout.test.ts — Unit tests for WID-D checkout pure helpers.
 *
 * Tests cover:
 *  • editDistance (Levenshtein)
 *  • suggestEmailFix (typo detection for all target providers)
 *  • isValidEmail
 *  • validateBuyerForm (buyer_fields-driven validation, all locales)
 *  • isBuyerFormValid
 *  • buildCheckoutPayload
 *  • formatPrice
 *  • isCheckoutPending / isCheckoutRecoverable
 *  • getCheckoutI18n / interpolate
 *  • All four locale i18n tables present and non-empty
 */

import { describe, test, expect } from 'vitest';
import {
  editDistance,
  suggestEmailFix,
  isValidEmail,
  validateBuyerForm,
  isBuyerFormValid,
  buildCheckoutPayload,
  formatPrice,
  isCheckoutPending,
  isCheckoutRecoverable,
  interpolate,
  getCheckoutI18n,
  CHECKOUT_I18N,
  type BuyerFieldConfig,
  type BuyerFormValues,
} from './checkout.js';

// ─── editDistance ─────────────────────────────────────────────────────────────

describe('editDistance', () => {
  test('identical strings → 0', () => {
    expect(editDistance('gmail.com', 'gmail.com')).toBe(0);
  });

  test('empty vs non-empty → length', () => {
    expect(editDistance('', 'abc')).toBe(3);
    expect(editDistance('abc', '')).toBe(3);
  });

  test('single insertion', () => {
    expect(editDistance('gmal.com', 'gmail.com')).toBe(1);
  });

  test('single deletion', () => {
    expect(editDistance('gmail.com', 'gmal.com')).toBe(1);
  });

  test('single substitution', () => {
    expect(editDistance('gzail.com', 'gmail.com')).toBe(1);
  });

  test('transposition counts as 2 (LCS is not Damerau)', () => {
    // gmial vs gmail: swap i and a → one deletion + one insertion = 2
    expect(editDistance('gmial.com', 'gmail.com')).toBe(2);
  });

  test('completely different', () => {
    const d = editDistance('abc', 'xyz');
    expect(d).toBe(3);
  });
});

// ─── suggestEmailFix ─────────────────────────────────────────────────────────

describe('suggestEmailFix', () => {
  test('already correct gmail.com → null', () => {
    expect(suggestEmailFix('alice@gmail.com')).toBeNull();
  });

  test('already correct yandex.ru → null', () => {
    expect(suggestEmailFix('bob@yandex.ru')).toBeNull();
  });

  test('already correct seznam.cz → null', () => {
    expect(suggestEmailFix('user@seznam.cz')).toBeNull();
  });

  test('gmial.com → gmail.com (transposition)', () => {
    expect(suggestEmailFix('alice@gmial.com')).toBe('alice@gmail.com');
  });

  test('gmal.com → gmail.com (deletion)', () => {
    expect(suggestEmailFix('alice@gmal.com')).toBe('alice@gmail.com');
  });

  test('gnail.com → gmail.com (substitution)', () => {
    expect(suggestEmailFix('alice@gnail.com')).toBe('alice@gmail.com');
  });

  test('outlok.com → outlook.com (deletion)', () => {
    expect(suggestEmailFix('user@outlok.com')).toBe('user@outlook.com');
  });

  test('yandex.com close variant (already known) → null', () => {
    expect(suggestEmailFix('user@yandex.com')).toBeNull();
  });

  test('hotnail.com → hotmail.com (substitution)', () => {
    expect(suggestEmailFix('user@hotnail.com')).toBe('user@hotmail.com');
  });

  test('yahooo.com → yahoo.com (deletion)', () => {
    expect(suggestEmailFix('user@yahooo.com')).toBe('user@yahoo.com');
  });

  test('completely unknown domain → null', () => {
    expect(suggestEmailFix('user@verylongunknowndomain.io')).toBeNull();
  });

  test('no @ sign → null', () => {
    expect(suggestEmailFix('notanemail')).toBeNull();
  });

  test('empty string → null', () => {
    expect(suggestEmailFix('')).toBeNull();
  });

  test('domain part only → null', () => {
    expect(suggestEmailFix('@gmail.com')).toBeNull();
  });

  test('trims whitespace before checking', () => {
    expect(suggestEmailFix('  alice@gmial.com  ')).toBe('alice@gmail.com');
  });

  test('seznam.cz already known → null', () => {
    expect(suggestEmailFix('pepa@seznam.cz')).toBeNull();
  });
});

// ─── isValidEmail ─────────────────────────────────────────────────────────────

describe('isValidEmail', () => {
  test('valid: simple address', () => {
    expect(isValidEmail('user@example.com')).toBe(true);
  });

  test('valid: subdomain', () => {
    expect(isValidEmail('user@mail.example.co.uk')).toBe(true);
  });

  test('invalid: no @', () => {
    expect(isValidEmail('userexample.com')).toBe(false);
  });

  test('invalid: @ at position 0', () => {
    expect(isValidEmail('@example.com')).toBe(false);
  });

  test('invalid: domain too short', () => {
    expect(isValidEmail('user@ab')).toBe(false);
  });

  test('invalid: domain has no dot', () => {
    expect(isValidEmail('user@localhost')).toBe(false);
  });

  test('invalid: empty string', () => {
    expect(isValidEmail('')).toBe(false);
  });
});

// ─── validateBuyerForm ───────────────────────────────────────────────────────

const emailOnlyField: BuyerFieldConfig[] = [
  { key: 'email', required: true, enabled: true },
];

const allFields: BuyerFieldConfig[] = [
  { key: 'email', required: true, enabled: true },
  { key: 'name', required: true, enabled: true },
  { key: 'phone', required: true, enabled: true },
];

const nameDisabled: BuyerFieldConfig[] = [
  { key: 'email', required: true, enabled: true },
  { key: 'name', required: true, enabled: false }, // disabled — must not validate
  { key: 'phone', required: false, enabled: true },
];

function values(overrides: Partial<BuyerFormValues> = {}): BuyerFormValues {
  return { email: 'user@gmail.com', name: 'Jane', phone: '+1555', ...overrides };
}

describe('validateBuyerForm', () => {
  test('all fields valid → empty errors', () => {
    const errs = validateBuyerForm(values(), allFields);
    expect(errs).toEqual({});
  });

  test('missing email → email error', () => {
    const errs = validateBuyerForm(values({ email: '' }), emailOnlyField);
    expect(errs.email).toBeTruthy();
    expect(errs.name).toBeUndefined();
  });

  test('invalid email format → email error', () => {
    const errs = validateBuyerForm(values({ email: 'not-an-email' }), emailOnlyField);
    expect(errs.email).toBeTruthy();
  });

  test('missing name when required → name error', () => {
    const errs = validateBuyerForm(values({ name: '' }), allFields);
    expect(errs.name).toBeTruthy();
  });

  test('missing phone when required → phone error', () => {
    const errs = validateBuyerForm(values({ phone: '' }), allFields);
    expect(errs.phone).toBeTruthy();
  });

  test('disabled name field is never validated', () => {
    const errs = validateBuyerForm(values({ name: '' }), nameDisabled);
    expect(errs.name).toBeUndefined();
  });

  test('optional phone (required=false) empty → no error', () => {
    const fields: BuyerFieldConfig[] = [
      { key: 'email', required: true, enabled: true },
      { key: 'phone', required: false, enabled: true },
    ];
    const errs = validateBuyerForm(values({ phone: '' }), fields);
    expect(errs.phone).toBeUndefined();
  });

  test('locale ru → Russian error messages', () => {
    const errs = validateBuyerForm(values({ email: '' }), emailOnlyField, 'ru');
    expect(errs.email).toBe(CHECKOUT_I18N.ru.email_required);
  });

  test('locale cs → Czech error messages', () => {
    const errs = validateBuyerForm(values({ email: 'bad' }), emailOnlyField, 'cs');
    expect(errs.email).toBe(CHECKOUT_I18N.cs.email_invalid);
  });

  test('locale he → Hebrew error messages', () => {
    const errs = validateBuyerForm(values({ email: '' }), emailOnlyField, 'he');
    expect(errs.email).toBe(CHECKOUT_I18N.he.email_required);
  });
});

// ─── isBuyerFormValid ─────────────────────────────────────────────────────────

describe('isBuyerFormValid', () => {
  test('empty errors → valid', () => {
    expect(isBuyerFormValid({})).toBe(true);
  });

  test('email error → invalid', () => {
    expect(isBuyerFormValid({ email: 'required' })).toBe(false);
  });

  test('name error → invalid', () => {
    expect(isBuyerFormValid({ name: 'required' })).toBe(false);
  });

  test('phone error → invalid', () => {
    expect(isBuyerFormValid({ phone: 'required' })).toBe(false);
  });
});

// ─── buildCheckoutPayload ─────────────────────────────────────────────────────

describe('buildCheckoutPayload', () => {
  const sessionId = 'sess-uuid-1234';
  const formValues: BuyerFormValues = { email: 'buyer@gmail.com', name: 'Jane Smith', phone: '' };
  const fields: BuyerFieldConfig[] = [
    { key: 'email', required: true, enabled: true },
    { key: 'name', required: true, enabled: true },
    { key: 'phone', required: false, enabled: false },
  ];

  test('holder_email and buyer.email match', () => {
    const p = buildCheckoutPayload(sessionId, formValues, [], [], fields);
    expect(p.holder_email).toBe('buyer@gmail.com');
    expect(p.buyer?.email).toBe('buyer@gmail.com');
  });

  test('session_id is set', () => {
    const p = buildCheckoutPayload(sessionId, formValues, [], [], fields);
    expect(p.session_id).toBe(sessionId);
  });

  test('seats included when non-empty', () => {
    const p = buildCheckoutPayload(sessionId, formValues, ['A-1-2', 'A-1-3'], [], fields);
    expect(p.seats).toEqual(['A-1-2', 'A-1-3']);
  });

  test('ga_items included when non-empty', () => {
    const gaItems = [{ tier_id: 'tier-1', quantity: 2 }];
    const p = buildCheckoutPayload(sessionId, formValues, [], gaItems, fields);
    expect(p.ga_items).toEqual(gaItems);
  });

  test('seats absent when empty', () => {
    const p = buildCheckoutPayload(sessionId, formValues, [], [], fields);
    expect(p.seats).toBeUndefined();
  });

  test('enabled name field included in buyer', () => {
    const p = buildCheckoutPayload(sessionId, formValues, [], [], fields);
    expect(p.buyer?.name).toBe('Jane Smith');
  });

  test('disabled phone field → null in buyer', () => {
    const p = buildCheckoutPayload(sessionId, formValues, [], [], fields);
    expect(p.buyer?.phone).toBeNull();
  });

  test('trims whitespace from email and name', () => {
    const v: BuyerFormValues = { email: '  buyer@gmail.com  ', name: '  Jane  ', phone: '' };
    const p = buildCheckoutPayload(sessionId, v, [], [], fields);
    expect(p.holder_email).toBe('buyer@gmail.com');
    expect(p.buyer?.name).toBe('Jane');
  });
});

// ─── formatPrice ─────────────────────────────────────────────────────────────

describe('formatPrice', () => {
  test('formats kopecks as rubles', () => {
    const result = formatPrice(1099, 'RUB');
    expect(result).toContain('10');
  });

  test('formats cents as dollars', () => {
    const result = formatPrice(1500, 'USD');
    expect(result).toContain('15');
  });

  test('zero amount', () => {
    const result = formatPrice(0, 'EUR');
    expect(result).toContain('0');
  });

  test('empty currency → raw integer string', () => {
    expect(formatPrice(500, '')).toBe('500');
  });
});

// ─── isCheckoutPending / isCheckoutRecoverable ────────────────────────────────

describe('isCheckoutPending', () => {
  test('pending → true', () => expect(isCheckoutPending('pending')).toBe(true));
  test('paid → false', () => expect(isCheckoutPending('paid')).toBe(false));
  test('expired → false', () => expect(isCheckoutPending('expired')).toBe(false));
  test('failed → false', () => expect(isCheckoutPending('failed')).toBe(false));
});

describe('isCheckoutRecoverable', () => {
  test('expired → true', () => expect(isCheckoutRecoverable('expired')).toBe(true));
  test('pending → false', () => expect(isCheckoutRecoverable('pending')).toBe(false));
  test('paid → false', () => expect(isCheckoutRecoverable('paid')).toBe(false));
  test('failed → false', () => expect(isCheckoutRecoverable('failed')).toBe(false));
});

// ─── i18n ─────────────────────────────────────────────────────────────────────

describe('CHECKOUT_I18N', () => {
  const LOCALES: Array<keyof typeof CHECKOUT_I18N> = ['en', 'ru', 'cs', 'he'];
  const REQUIRED_KEYS: Array<keyof typeof CHECKOUT_I18N['en']> = [
    'email_label', 'email_required', 'email_invalid', 'email_suggestion',
    'name_label', 'name_required', 'phone_label', 'phone_required',
    'submit_label', 'status_paid', 'status_expired', 'status_failed',
    'status_pending', 'recover_label', 'retry_label', 'send_again',
  ];

  for (const locale of LOCALES) {
    test(`${locale}: all required keys present and non-empty`, () => {
      const table = CHECKOUT_I18N[locale];
      for (const key of REQUIRED_KEYS) {
        expect(table[key], `${locale}.${key}`).toBeTruthy();
      }
    });
  }
});

describe('getCheckoutI18n', () => {
  test('known locale returns correct table', () => {
    expect(getCheckoutI18n('ru')).toBe(CHECKOUT_I18N.ru);
  });

  test('unknown locale falls back to en', () => {
    expect(getCheckoutI18n('xx')).toBe(CHECKOUT_I18N.en);
  });

  test('empty string falls back to en', () => {
    expect(getCheckoutI18n('')).toBe(CHECKOUT_I18N.en);
  });
});

describe('interpolate', () => {
  test('replaces single placeholder', () => {
    expect(interpolate('Did you mean {suggestion}?', { suggestion: 'gmail.com' }))
      .toBe('Did you mean gmail.com?');
  });

  test('leaves unknown placeholders intact', () => {
    expect(interpolate('Hello {name}!', {})).toBe('Hello {name}!');
  });

  test('replaces multiple placeholders', () => {
    expect(interpolate('{a} and {b}', { a: 'X', b: 'Y' })).toBe('X and Y');
  });

  test('no placeholders → unchanged', () => {
    expect(interpolate('No change', { suggestion: 'gmail.com' })).toBe('No change');
  });
});
