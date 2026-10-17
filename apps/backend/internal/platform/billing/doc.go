// Package billing is the platform's organizer billing: billing accounts per
// (organization, currency), their dated terms, versioned price lists and
// individual organization rates (billing wave B-A, spec
// 08_architecture/41_billing_credits_architecture_ru.md and
// 42_billing_execution_spec_ru.md).
//
// This first slice (task B1-1) only creates accounts and routes a venue
// country to a currency and an issuer. Rating (Rate, TermsAt) is task B1-2,
// collection of billable events block B2. Every rate, threshold and cap is a
// price-list row seeded by migration 0157, never a constant in this package.
//
// Money is int64 minor units with the currency next to it, never a float.
package billing
