# Security Policy

costcrew is a FinOps console in which a crew of agents analyses cloud, SaaS
and AI spend and a person reviews every conclusion before it counts; its
trust boundary matters because a compromise could expose an organisation's
spend data, corrupt its hash-chained audit journal, or reach a live
model-provider call that spends real money.

## Reporting a vulnerability

Please report security issues privately, not in public issues or pull requests: open a
GitHub private security advisory at
<https://github.com/TAIPANBOX/costcrew/security/advisories/new>. Include the affected
version or commit, a description and a minimal reproduction. We aim to acknowledge
within a few days and to fix high-severity issues before any public disclosure, with
coordinated disclosure within 90 days of the report. There is no bug-bounty programme;
reporters are credited in the advisory unless they prefer otherwise.

## Supported versions

Before this repository's 1.0, only `main` is supported: fixes land on `main` and are
not backported. From its 1.0 tag, the newest minor gets every fix and the previous
minor gets security-relevant fixes for 90 days after the newer one is tagged.

## Verifying a build

Every change passes the repository's gates before merge: `go test ./...`,
`./scripts/gates-have-teeth.sh`, `./scripts/features-are-bound.sh`,
`./scripts/roles-are-bound.sh`, `./parity/gate-has-teeth.sh parity/captures/golden`,
`gofmt -l .`, `go vet ./...`, and `staticcheck ./...`. Release assets are signed
keyless with Sigstore and carry a provenance attestation and an SBOM; the README's
verify block shows how to check them.
