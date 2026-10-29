// Package dailybilling estimates subscription-tier spend for the management panel.
//
// new-api logs are the billing authority (quota and USD) for every successful
// charge, not one model family. CPA usage is the tier authority. new-api does
// not record the CPA credential tier, so pro, ultra, and other amounts are
// allocated from each day's share of successful CPA total_tokens. The split is
// an estimate. Optional per-credential monthly or daily costs estimate gross
// profit from that day's active credentials; they are configuration, not a
// second billing source.
//
// The new-api database is read-only and optional. When it is not configured
// the report says so and does not error.
package dailybilling
