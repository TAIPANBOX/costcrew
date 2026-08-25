# What the FinOps crew produced, 2026-07
**Identified: USD 9,113.17 per month** (USD 109,358.04 annualised), against a fully loaded technology spend of USD 41,584.63 for the closed month.
> Every figure here is identified, not realised: the crew proposes and never acts on the estate, so a finding becomes a saving only when a person makes the change.
## Where the money is
| area | source | finding | $/month | basis |
|---|---|---|---:|---|
| Licensing | saas | unused seats across 6 subscriptions | 7,344.00 | seats provisioned and not active, at the unit price `⌗541feceb` |
| Usage optimisation | gcp | 10 oversized resources on gcp | 445.00 | proven against measured p95 utilisation; the resize keeps the resource under the headroom line `⌗29591d0d` |
| Usage optimisation | aws | 7 oversized resources on aws | 374.65 | proven against measured p95 utilisation; the resize keeps the resource under the headroom line `⌗c2933bb7` |
| Planning | intake | 3 reduction requests in the pipeline | 350.00 | team-submitted reductions, estimated by the desks `⌗intake-requests` |
| Usage optimisation | gcp | 3 idle resources on gcp | 294.53 | p95 utilisation at or under the idle threshold `⌗29591d0d` |
| Usage optimisation | aws | 3 idle resources on aws | 294.49 | p95 utilisation at or under the idle threshold `⌗c2933bb7` |
| Rate optimisation | gcp | unused commitment on Compute Engine CUD | 10.50 | commitment paid for beyond what the scope consumed `⌗e20a0bab` |
## What the crew did
- 113 deliverables reviewed and posted
- 10 decisions raised for people above the practice
- 0 chargeback periods closed
- 0 explainers published to teams
- 20.0% of anomaly excess explained by a registered driver
## What the crew cost
- USD 9.23 in model spend this month over 27 priced runs (cost known for 51.9% of runs)
- identified 9,113.17 per month against a crew cost of 9.23; the ratio is only meaningful once someone acts on the findings
## Practice position
- KPIs: 6 of 23 on target; off target: Forecast interval hit rate, Anomaly Detection Rate, Budget variance, Resource Utilization Efficiency, Telemetry coverage, License utilisation
- Maturity: 6 run, 3 walk, 4 crawl across 13 graded capabilities
## Decisions this pack asks for
- **Renew or let lapse: $1,650.00 of monthly commitment ends within 90 days** - a lapsed commitment reverts its scope to list price without anyone doing anything (owner: Finance with Engineering)
- **Approve or defer 6 team request(s) worth $2,060/month, largest: Two extra GPU training nodes** - these are commitments to future run-rate, not this month's bill (owner: Leadership with the requesting teams)
- **Off target: Forecast interval hit rate at 62.5 against 80** - Share of elapsed windows whose actual landed inside the published interval. Not a canon KPI; the practice's own honesty check on the intervals it publishes. (owner: FinOps practice)
- **Off target: Anomaly Detection Rate at 24.69 against 2** - Share of spend sitting in movements the practice cannot explain. Canon bands: green under 2%, yellow 2-7%, red above 7%. (owner: FinOps practice)
- **Off target: Budget variance at 25.1 against 12** - Projected month-end spend against budget, worst team. (owner: FinOps practice)
- **Off target: Resource Utilization Efficiency at 49.4 against 60** - Share of provisioned accelerator capacity actually used, averaged across the training fleet. (owner: FinOps practice)
- **Off target: Telemetry coverage at 73.7 against 90** - Share of compute spend with utilisation telemetry behind it. Findings only speak for the covered part, so the number belongs next to them. (owner: FinOps practice)
- **Off target: License utilisation at 50.8 against 85** - Share of purchased licences actually in use. The canon's active-to-provisioned ratio; the worst subscription is what is reported, because an average hides the one nobody opens. (owner: FinOps practice)
- **Fund or accept: 4 capability at crawl (Anomaly Management, Budgeting, Licensing & SaaS, FinOps Assessment)** - maturity moves with tooling and discipline, not with intent (owner: Leadership)
- **AI run-rate: token spend up 82.1% across the series while the unit price is flat** - volume, not price, is driving the AI bill; capping it is a product decision, not a procurement one (owner: Product with Engineering)
## What this is not
not a forecast of savings, not a commitment, and not a claim that anything has already been cut.
---
Every figure is deterministic and carries a qid that resolves in the console.