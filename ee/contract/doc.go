// SPDX-License-Identifier: Apache-2.0

// Package contract holds the enterprise contracts: identity, authorization, policy, audit, budget
// and the run ledger entry. It is types, interfaces, reason codes and sentinel errors only, with
// no behaviour of its own, so ee/sso, ee/scim, ee/authz, ee/policy, ee/audit, ee/budget and the
// core all build against one definition.
//
// The package imports nothing from internal/ and carries no build tag, so other repositories can
// import github.com/KaaS-team/factory/ee/contract. It is Apache-2.0, unlike the rest of ee/.
//
// testdata/conformance.json holds language-neutral cases for every interface here. The Go test
// runs them against an in-memory reference implementation; a port in another language runs the
// same file. An implementation conforms when it passes every case.
package contract
