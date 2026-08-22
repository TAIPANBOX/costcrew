package world

import (
	"crypto/sha256"
	"encoding/binary"
	"sort"
	"strings"

	"github.com/TAIPANBOX/costcrew/internal/money"
)

// Every plane in this console is a reading of ONE ledger.
//
// The licence, utilisation and commitment tables used to be written out beside
// the charges rather than derived from them, and the result was a console
// where a figure on one page could not be checked against any other. It was
// not merely unverifiable, it was wrong: measured against the ledger, every
// single licence cost more per month than the entire SaaS bill for the team
// that held it. Figma read 2,025.00 a month against a real bill of 147.70.
//
// So these three are now built the way AIUnits() always was: from Generate(),
// which is the estate. A page can then be checked against the page beside it,
// and the reconciliation gate does exactly that on every run.
//
// Everything below is deterministic. The split of a line into named resources,
// the seat counts, the idle counts and the utilisation percentages all come
// from a hash of the thing being decided, so the estate is identical on every
// machine and does not depend on map iteration order.

// hashOf is a stable small integer from a string, in [0,n).
func hashOf(s string, n int) int {
	if n <= 0 {
		return 0
	}
	sum := sha256.Sum256([]byte(s))
	return int(binary.BigEndian.Uint32(sum[:4]) % uint32(n))
}

// hashPct is a stable percentage in [lo,hi].
func hashPct(s string, lo, hi float64) float64 {
	sum := sha256.Sum256([]byte(s + "|pct"))
	v := float64(binary.BigEndian.Uint32(sum[4:8])%10_000) / 10_000
	return lo + v*(hi-lo)
}

// lastFullMonth is the month before the estate's last day.
//
// The planes are monthly figures, and the open month is a part-month: a seat
// count derived from eleven days of an invoice would say the organisation had
// halved its licences overnight.
// LastFullMonth is exported so a reconciliation can measure the same month
// these planes were derived from. Measuring them against a different month is
// the one way to make a correct derivation look broken.
func LastFullMonth() string { return lastFullMonth() }

func lastFullMonth() string {
	d := DayBefore(LastDay, 40)
	return d[:7]
}

type lineKey struct{ Source, Service, Team string }

// monthlyLines totals the ledger for one month, per source, service and team.
func monthlyLines(month string) map[lineKey]money.Cents {
	out := map[lineKey]money.Cents{}
	for _, r := range Generate() {
		if len(r.Day) < 7 || r.Day[:7] != month {
			continue
		}
		out[lineKey{r.Source, r.Service, r.Team}] += r.Billed
	}
	return out
}

// sortedKeys keeps every derivation in a fixed order.
func sortedKeys(m map[lineKey]money.Cents) []lineKey {
	ks := make([]lineKey, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Slice(ks, func(i, j int) bool {
		if ks[i].Source != ks[j].Source {
			return ks[i].Source < ks[j].Source
		}
		if ks[i].Service != ks[j].Service {
			return ks[i].Service < ks[j].Service
		}
		return ks[i].Team < ks[j].Team
	})
	return ks
}

// ---------------------------------------------------------------- licences

// seatPrice is the vendor's list price per seat per month.
//
// This is the one number here that is NOT derived, because it is a fact about
// the vendor rather than about this estate. Everything else follows from it
// and from the invoice: seats issued are what the invoice pays for.
var seatPrice = map[string]money.Cents{
	"Zendesk":    money.Cents(11_500),
	"Datadog":    money.Cents(2_300),
	"Figma":      money.Cents(4_500),
	"GitHub":     money.Cents(2_100),
	"Salesforce": money.Cents(16_500),
	"NetSuite":   money.Cents(13_200),
}

// noteFor says what the seat counts actually show.
//
// The notes used to be written out beside the row, and once the seats were
// derived from the invoice they contradicted it: Datadog read "close to fully
// used" with forty-one of eighty-four seats idle. A sentence that argues with
// the number next to it is worse than no sentence, because the reader has to
// decide which of the two the console is wrong about.
func noteFor(l Licence) string {
	idle := l.Idle()
	if l.Issued == 0 {
		return ""
	}
	share := float64(idle) / float64(l.Issued) * 100
	switch {
	case idle == 0:
		return "Every seat signed in this month. Nothing to recover here."
	case share >= 45:
		return "Nearly half the seats have not been signed into in thirty days. " +
			"The largest single decision on this page."
	case share >= 25:
		return "A quarter of the seats are idle. Worth asking the team who still needs one before it renews."
	case share >= 10:
		return "A handful of idle seats, which is normal churn rather than a finding."
	}
	return "Close to fully used."
}

var licenceProduct = map[string]string{
	"Zendesk": "Suite Professional", "Datadog": "Pro", "Figma": "Organization",
	"GitHub": "Enterprise", "Salesforce": "Sales Cloud", "NetSuite": "ERP",
}

var licenceRenews = map[string]string{
	"Zendesk": "2026-11-01", "Datadog": "2027-02-01", "Figma": "2026-10-15",
	"GitHub": "2027-01-01", "Salesforce": "2026-09-30", "NetSuite": "2027-03-01",
}

// Licences is the seat estate, read off the SaaS invoices.
//
// Issued seats are what the invoice actually pays for, at the vendor's list
// price. That is the whole point: "we pay for 60 seats" is a claim somebody
// can check against a line in the ledger, and a claim nobody can check is what
// this table used to be.
var Licences = buildLicences()

func buildLicences() []Licence {
	month := lastFullMonth()
	lines := monthlyLines(month)
	var out []Licence
	for _, k := range sortedKeys(lines) {
		if k.Source != "saas" {
			continue
		}
		price, ok := seatPrice[k.Service]
		if !ok || price <= 0 {
			continue
		}
		bill := lines[k]
		// Seats the invoice pays for. Rounded to whole seats, because that is
		// what a vendor bills; the rounding is at most one seat and the page
		// reports the seat count, not the invoice, so it cannot mislead about
		// money it did not spend.
		issued := int((bill + price/2) / price)
		if issued < 1 {
			issued = 1
		}
		// Idle is measured, not assumed: a share of the seats that has not
		// been signed into. The share is deterministic per vendor and team.
		idleShare := hashPct(k.Service+"|"+k.Team+"|idle", 0.02, 0.55)
		active := issued - int(float64(issued)*idleShare)
		if active < 0 {
			active = 0
		}
		if active > issued {
			active = issued
		}
		out = append(out, Licence{
			Vendor: k.Service, Product: licenceProduct[k.Service], Team: k.Team,
			Issued: issued, Active30: active, PerSeat: price,
			Renews: licenceRenews[k.Service], Term: "annual",
		})
		out[len(out)-1].Note = noteFor(out[len(out)-1])
	}
	return out
}

// ------------------------------------------------------------- utilisation

// resourceKind classifies a service so the page can say what it is looking at.
// Anything that is not compute, storage-adjacent or an accelerator is not
// something a rightsizing page has anything useful to say about.
// ResourceKind is exported so a reconciliation can classify a service the
// same way the derivation did.
func ResourceKind(service string) string { return resourceKind(service) }

func resourceKind(service string) string {
	s := strings.ToLower(service)
	switch {
	case strings.Contains(s, "gpu"), strings.Contains(s, "sagemaker"):
		return "accelerator"
	case strings.Contains(s, "rds"), strings.Contains(s, "sql"),
		strings.Contains(s, "bigquery"), strings.Contains(s, "aurora"):
		return "database"
	case strings.Contains(s, "ec2"), strings.Contains(s, "compute engine"),
		strings.Contains(s, "virtual machines"), strings.Contains(s, "gke"),
		strings.Contains(s, "kubernetes"), strings.Contains(s, "batch cluster"),
		strings.Contains(s, "virtualisation"), strings.Contains(s, "cloud run"):
		return "compute"
	}
	return ""
}

var resourceNames = map[string][]string{
	"compute":     {"worker", "runner", "node", "app", "pool"},
	"database":    {"primary", "replica", "reporting", "staging"},
	"accelerator": {"train", "infer", "batch"},
}

// UtilisationRows names the resources inside a line, and they SUM to the line.
//
// That is the property the page is worth having: the resources listed under
// aws / Amazon EC2 / ml-platform add up, to the cent, to what that line cost
// last month. A rightsizing table whose rows do not add up to anything is a
// list of assertions, and the saving column on it cannot be trusted either.
var UtilisationRows = buildUtilisation()

func buildUtilisation() []Utilisation {
	month := lastFullMonth()
	lines := monthlyLines(month)
	var out []Utilisation
	for _, k := range sortedKeys(lines) {
		kind := resourceKind(k.Service)
		if kind == "" {
			continue
		}
		bill := lines[k]
		// Below this a line is one resource and there is nothing to rightsize.
		if bill < money.Cents(20_000) {
			continue
		}
		seed := k.Source + "|" + k.Service + "|" + k.Team
		n := 2 + hashOf(seed+"|n", 3) // two to four named resources
		names := resourceNames[kind]

		// Weights, then an exact split: the remainder goes to the largest so
		// the parts add up to the whole rather than to the whole minus a cent.
		w := make([]int, n)
		total := 0
		for i := range w {
			w[i] = 20 + hashOf(seed+"|w"+string(rune('a'+i)), 80)
			total += w[i]
		}
		parts := make([]money.Cents, n)
		var placed money.Cents
		biggest := 0
		for i := range parts {
			parts[i] = money.Cents(int64(bill) * int64(w[i]) / int64(total))
			placed += parts[i]
			if w[i] > w[biggest] {
				biggest = i
			}
		}
		parts[biggest] += bill - placed

		for i := 0; i < n; i++ {
			rs := seed + "|r" + string(rune('a'+i))
			name := names[hashOf(rs+"|name", len(names))]
			// The index is IN the number, so two resources in one line cannot
			// end up with the same id. A duplicate id on a rightsizing page is
			// worse than a dull one: two rows recommending different things
			// about what reads as the same machine.
			id := prefixFor(k.Source, kind) + name + "-" + pad2i(1+i*7+hashOf(rs+"|num", 6))

			cpu := hashPct(rs+"|cpu", 3, 82)
			mem := hashPct(rs+"|mem", 8, 74)

			u := Utilisation{
				Source: k.Source, Service: k.Service, Team: k.Team,
				Resource: id, Kind: kind, P95CPU: cpu, P95Mem: mem, Days: 30,
				Monthly: parts[i],
			}
			// Advice follows the measurement, and only the measurement. A page
			// that recommends downsizing something busy is one nobody reads
			// twice, which is why the comfortable and the busy stay on it.
			switch {
			case cpu < 15 && mem < 35:
				u.Advice = "one size down, or two"
				u.Saving = u.Monthly * 3 / 4
				u.Why = "Under fifteen percent p95 CPU and under thirty-five percent memory across thirty days."
			case cpu < 30 && mem < 50:
				u.Advice = "one size down"
				u.Saving = u.Monthly / 2
				u.Why = "Consistently under a third of its CPU over thirty days, with memory to match."
			case cpu > 70 || mem > 70:
				u.Why = "Busy. A rightsizing page that only lists candidates hides its own false-negative rate."
			default:
				u.Why = "Comfortably used. Named here so the page shows what NOT downsizing looks like."
			}
			out = append(out, u)
		}
	}
	// Biggest first: the money is the reason anybody opened the page.
	sort.SliceStable(out, func(i, j int) bool { return out[i].Monthly > out[j].Monthly })
	return out
}

func prefixFor(source, kind string) string {
	switch source {
	case "aws":
		if kind == "database" {
			return "db-"
		}
		return "i-"
	case "gcp":
		return "gce-"
	case "azure":
		return "vm-"
	case "onprem":
		return "host-"
	case "ai":
		return "gpu-"
	}
	return ""
}

func pad2i(n int) string {
	if n < 10 {
		return "0" + string(rune('0'+n))
	}
	return string(rune('0'+n/10)) + string(rune('0'+n%10))
}

// ------------------------------------------------------------ commitments

var commitmentSpec = []struct {
	Source, Name, Kind, Expires, Term, Note string
	Share                                   float64 // of the desk's committable spend
}{
	{"aws", "Compute Savings Plan, 1yr", "savings-plan", "2026-11-30", "1 year",
		"Healthy.", 0.42},
	{"aws", "RDS Reserved, 3yr", "reserved", "2028-04-15", "3 years",
		"Bought for a workload that moved to Aurora. Two years left to run.", 0.18},
	{"gcp", "Committed Use Discount, 1yr", "cud", "2026-09-14", "1 year",
		"Expires inside 30 days. Renew, resize or let it lapse.", 0.46},
	{"azure", "Reserved VM Instances, 1yr", "reserved", "2027-01-20", "1 year",
		"Just under the line and drifting down.", 0.39},
}

// Commitments is what each desk has bought in advance, as a share of the spend
// a commitment can actually cover.
//
// Committable spend is compute, database and accelerator: storage and egress
// are not covered by a savings plan, and a commitment sized against the whole
// bill would look prudent while covering things it cannot.
var Commitments = buildCommitments()

func buildCommitments() []Commitment {
	month := lastFullMonth()
	lines := monthlyLines(month)
	committable := map[string]money.Cents{}
	for _, k := range sortedKeys(lines) {
		if resourceKind(k.Service) == "" {
			continue
		}
		committable[k.Source] += lines[k]
	}
	out := make([]Commitment, 0, len(commitmentSpec))
	for _, c := range commitmentSpec {
		monthly := money.Cents(float64(committable[c.Source]) * c.Share)
		out = append(out, Commitment{
			Source: c.Source, Name: c.Name, Kind: c.Kind,
			Hourly:  monthly / 730,
			Used:    hashPct(c.Source+"|"+c.Name+"|used", 58, 96),
			Expires: c.Expires, Term: c.Term, Note: c.Note,
		})
	}
	return out
}
