// Package dailybilling estimates gemini-4 spend for the management panel.
//
// new-api logs are the billing authority (quota and USD). CPA usage is the
// tier authority. new-api does not record the CPA credential tier, so pro and
// ultra amounts are allocated from each day's share of successful CPA
// total_tokens. The split is an estimate.
//
// The new-api database is read-only and optional. When it is not configured
// the report says so and does not error.
package dailybilling
