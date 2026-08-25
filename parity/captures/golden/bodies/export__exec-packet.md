# Technology spend: executive packet, 2026-07

Fully loaded technology spend for the closed month: **USD 41,584.63** across 5 sources.

## Where the money is

| source | closed month | fully loaded $ | current month to date $ |
|---|---|---:|---:|
| ai | 2026-07 | 2,380.46 | 1,163.90 (through 2026-08-15) `⌗e2bdf62d` |
| aws | 2026-07 | 4,411.58 | 1,610.59 (through 2026-08-15) `⌗33a5b724` |
| gcp | 2026-07 | 3,298.67 | 1,270.34 (through 2026-08-15) `⌗0b8a7ac1` |
| onprem | 2026-07 | 3,988.91 | 1,217.09 (through 2026-08-15) `⌗f2c0ebc6` |
| saas | 2026-07 | 27,505.00 | 27,246.00 (through 2026-08-01) `⌗e20ec458` |

## Practice

- KPIs: 23 computed, 6 on target, 6 off target (Forecast interval hit rate, Anomaly Detection Rate, Budget variance, Resource Utilization Efficiency, Telemetry coverage, License utilisation); 4 named but not computable here.
- Maturity: 13 capabilities graded - 6 run, 3 walk, 4 crawl; 8 not assessable yet.
- AI: USD 2,380.46 in the month, 77.5% inference `⌗b10a5ff7`.

## Decisions

**Renew or let lapse: $1,650.00 of monthly commitment ends within 90 days**  
a lapsed commitment reverts its scope to list price without anyone doing anything  
Evidence: commitment value expiring = 1650.0 `⌗be74d5d3`  
Owner: Finance with Engineering

**Approve or defer 6 team request(s) worth $2,060/month, largest: Two extra GPU training nodes**  
these are commitments to future run-rate, not this month's bill  
Evidence: request pipeline = 2060.0 `⌗intake-requests`  
Owner: Leadership with the requesting teams

**Off target: Forecast interval hit rate at 62.5 against 80**  
Share of elapsed windows whose actual landed inside the published interval. Not a canon KPI; the practice's own honesty check on the intervals it publishes.  
Evidence: Forecast interval hit rate = 62.5 `⌗a72056c9`  
Owner: FinOps practice

**Off target: Anomaly Detection Rate at 24.69 against 2**  
Share of spend sitting in movements the practice cannot explain. Canon bands: green under 2%, yellow 2-7%, red above 7%.  
Evidence: Anomaly Detection Rate = 24.69 `⌗32672c1b`  
Owner: FinOps practice

**Off target: Budget variance at 25.1 against 12**  
Projected month-end spend against budget, worst team.  
Evidence: Budget variance = 25.1 `⌗8f756ef4`  
Owner: FinOps practice

**Off target: Resource Utilization Efficiency at 49.4 against 60**  
Share of provisioned accelerator capacity actually used, averaged across the training fleet.  
Evidence: Resource Utilization Efficiency = 49.4 `⌗e16087f0`  
Owner: FinOps practice

**Off target: Telemetry coverage at 73.7 against 90**  
Share of compute spend with utilisation telemetry behind it. Findings only speak for the covered part, so the number belongs next to them.  
Evidence: Telemetry coverage = 73.7 `⌗29591d0d`  
Owner: FinOps practice

**Off target: License utilisation at 50.8 against 85**  
Share of purchased licences actually in use. The canon's active-to-provisioned ratio; the worst subscription is what is reported, because an average hides the one nobody opens.  
Evidence: License utilisation = 50.8 `⌗541feceb`  
Owner: FinOps practice

**Fund or accept: 4 capability at crawl (Anomaly Management, Budgeting, Licensing & SaaS, FinOps Assessment)**  
maturity moves with tooling and discipline, not with intent  
Evidence: capabilities at crawl = 4 `⌗assessment`  
Owner: Leadership

**AI run-rate: token spend up 82.1% across the series while the unit price is flat**  
volume, not price, is driving the AI bill; capping it is a product decision, not a procurement one  
Evidence: blended $/1M tokens = 6.1698 `⌗1c5ad42f`  
Owner: Product with Engineering

## What this packet does not cover

spend is fully loaded after allocation rules; business value is only as good as the denominators the product side reports, and no benefit figures are ingested

---
Figures are deterministic; every qid resolves in the console.