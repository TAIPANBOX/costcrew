// Package connectors is the catalogue of places the estate's numbers can come
// from, and what it costs to ask.
//
// Two fields decide whether an integration is a good idea, and both are on
// every entry because leaving either off is how a console starts lying about
// where its numbers came from:
//
//	Feeds   - which table it fills. A connector that fills nothing is a demo.
//	Metered - whether RUNNING it costs money, per call. This is not the same
//	          as "the service costs money": an export delivered to a bucket is
//	          paid for once as storage, while the same data pulled through a
//	          cost API is a penny every single request.
//
// Status is built or documented and nothing in between. A half-built
// connector that looks finished is exactly how a console starts showing
// numbers nobody can trace.
package connectors

import (
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"time"
)

type Status string

const (
	Built      Status = "built"      // there is a reader and a test
	Documented Status = "documented" // the endpoint is established, the code is not written
)

type Kind string

const (
	ExportDrop Kind = "export-drop" // files delivered somewhere; reading them is free
	API        Kind = "api"         // a call, which may be metered
	Local      Kind = "local"       // something already on this machine
)

// Input is what a person has to supply. A secret names the environment
// variable it lives under and is never written back or shown again.
type Input struct {
	Name   string
	Label  string
	Hint   string
	Secret bool
	EnvVar string
}

type Connector struct {
	ID       string
	Name     string
	Provider string
	Kind     Kind
	Feeds    string
	Status   Status
	Metered  bool
	CostNote string
	Auth     string
	Note     string
	Doc      string
	Inputs   []Input

	// Cannot is the honest half: what this connector will never give you.
	// Every catalogue tells you what it does; the ones worth trusting also
	// tell you what it does not.
	Cannot string
}

const Schema = `
CREATE TABLE IF NOT EXISTS connections(
  id TEXT PRIMARY KEY, config TEXT, last_test TEXT, last_result TEXT,
  ok INTEGER DEFAULT 0);
`

// Catalogue is written out rather than discovered, so what the console claims
// it can read can be read here and argued with.
var Catalogue = []Connector{
	{
		ID: "aws-data-exports", Name: "AWS Data Exports (FOCUS 1.2)", Provider: "aws",
		Kind: ExportDrop, Feeds: "charges", Status: Built, Metered: false,
		Auth: "none for the reader: it reads files already delivered to a folder",
		CostNote: "No charge for the export itself. You pay S3 storage and requests for " +
			"the delivered objects, which is pennies a month at this size.",
		Note: "AWS ships FOCUS 1.0 and 1.2 tables only. Delivery is at least daily, and " +
			"the previous period can still be revised for about two weeks after month end.",
		Doc: "https://docs.aws.amazon.com/cur/latest/userguide/what-is-data-exports.html",
		Inputs: []Input{{Name: "path", Label: "Folder the export lands in",
			Hint: "the local path, or drop the unzipped folder on this page"}},
		Cannot: "It cannot tell you WHY something cost what it did. It carries no " +
			"application context beyond the tags already on the resource.",
	},
	{
		ID: "aws-cost-explorer", Name: "AWS Cost Explorer", Provider: "aws",
		Kind: API, Feeds: "charges", Status: Built, Metered: true,
		Auth: "an IAM role with ce:GetCostAndUsage",
		CostNote: "USD 0.01 per request, every request, forever. A daily pull across " +
			"five dimensions is a few dollars a month and rises with curiosity.",
		Note: "Use the export above for history and this only for the current day, " +
			"which the export has not delivered yet.",
		Doc:    "https://docs.aws.amazon.com/aws-cost-management/latest/APIReference/",
		Inputs: []Input{{Name: "profile", Label: "AWS profile", Hint: "from your ~/.aws/config"}},
		Cannot: "It cannot give you resource-level detail. That is the export's job.",
	},
	{
		ID: "gcp-billing-export", Name: "GCP BigQuery billing export", Provider: "gcp",
		Kind: ExportDrop, Feeds: "charges", Status: Built, Metered: false,
		Auth:     "a service account with BigQuery read on the billing dataset",
		CostNote: "Free to enable. You pay BigQuery storage and whatever your queries scan.",
		Note: "Enabling it is console-only by Google's design; there is no public API. " +
			"Backfill reaches to the start of the PREVIOUS month, so enabling in " +
			"September still captures August and loses everything before it.",
		Doc: "https://cloud.google.com/billing/docs/how-to/export-data-bigquery",
		Inputs: []Input{
			{Name: "project", Label: "Project", Hint: "where the dataset lives"},
			{Name: "dataset", Label: "Dataset", Hint: "usually billing_export"},
		},
		Cannot: "It cannot show you anything before you switched it on, beyond that " +
			"one month of backfill. There is no way to buy the history back.",
	},
	{
		ID: "azure-focus", Name: "Azure Cost Management (FOCUS)", Provider: "azure",
		Kind: ExportDrop, Feeds: "charges", Status: Built, Metered: false,
		Auth:     "a storage account key or a managed identity with blob read",
		CostNote: "The export is free; you pay blob storage for what it writes.",
		Doc:      "https://learn.microsoft.com/en-us/azure/cost-management-billing/",
		Inputs: []Input{{Name: "container", Label: "Blob container",
			Hint: "where the scheduled export writes"}},
		Cannot: "Reservation utilisation is a separate export. This one is charges.",
	},
	{
		ID: "kubecost", Name: "Kubecost", Provider: "kubernetes",
		Kind: API, Feeds: "charges", Status: Built, Metered: false,
		Auth:     "an endpoint on the cluster, usually port-forwarded",
		CostNote: "Free to query. The cluster it runs on is not free, but you are already paying for that.",
		Doc:      "https://docs.kubecost.com/apis/apis-overview",
		Inputs:   []Input{{Name: "url", Label: "Kubecost URL", Hint: "http://localhost:9090"}},
		Cannot:   "It cannot allocate what the cluster cannot label. Unlabelled pods stay shared.",
	},
	{
		ID: "opencost", Name: "OpenCost", Provider: "kubernetes",
		Kind: API, Feeds: "charges", Status: Documented, Metered: false,
		Auth:     "an endpoint on the cluster",
		CostNote: "Free.",
		Note:     "The endpoint and its shape are established; the reader is not written yet.",
		Doc:      "https://www.opencost.io/docs/integrations/api",
		Inputs:   []Input{{Name: "url", Label: "OpenCost URL"}},
		Cannot:   "Same limit as Kubecost: no labels, no allocation.",
	},
	{
		ID: "anthropic-usage", Name: "Anthropic usage and cost", Provider: "ai",
		Kind: API, Feeds: "charges (ai)", Status: Built, Metered: false,
		Auth:     "an ADMIN key (sk-ant-admin...), not an ordinary API key",
		CostNote: "The usage endpoint is not billed. The tokens it reports certainly were.",
		Doc:      "https://docs.claude.com/en/api/admin-api",
		Inputs: []Input{{Name: "key", Label: "Admin key", Secret: true,
			EnvVar: "ANTHROPIC_ADMIN_KEY", Hint: "an admin key, not an API key"}},
		Cannot: "It cannot tell you which AGENT spent it. That needs the calls to " +
			"carry an agent header through a gateway, and until they do this " +
			"console says team, not agent, and says so on the page.",
	},
	{
		ID: "openrouter-usage", Name: "OpenRouter activity", Provider: "ai",
		Kind: API, Feeds: "charges (ai)", Status: Built, Metered: false,
		Auth:     "the same key you call it with",
		CostNote: "Free to query.",
		Doc:      "https://openrouter.ai/docs/api-reference",
		Inputs: []Input{{Name: "key", Label: "API key", Secret: true,
			EnvVar: "OPENROUTER_API_KEY"}},
		Cannot: "Per-agent attribution, for the same reason as above.",
	},
	{
		ID: "compute-optimizer", Name: "AWS Compute Optimizer", Provider: "aws",
		Kind: API, Feeds: "rightsizing", Status: Documented, Metered: false,
		Auth:     "an IAM role with compute-optimizer:Get*",
		CostNote: "Free, but it needs CloudWatch metrics, which are not.",
		Doc:      "https://docs.aws.amazon.com/compute-optimizer/",
		Inputs:   []Input{{Name: "profile", Label: "AWS profile"}},
		Cannot:   "It sees fourteen days. A monthly batch job looks idle to it.",
	},
	{
		ID: "saas-seats", Name: "SaaS seat reconciliation", Provider: "saas",
		Kind: Local, Feeds: "saas_licences", Status: Documented, Metered: false,
		Auth:     "an export from each vendor's admin console",
		CostNote: "Free, and manual, which is the honest description.",
		Note:     "There is no standard here. Every vendor exports something different.",
		Inputs:   []Input{{Name: "path", Label: "Folder of vendor exports"}},
		Cannot:   "Nothing automates this. Anyone who says otherwise is selling scrapers.",
	},
}

func Get(id string) (Connector, bool) {
	for _, c := range Catalogue {
		if c.ID == id {
			return c, true
		}
	}
	return Connector{}, false
}

// Counts is what the page header says about itself.
func Counts() (built, documented, metered int) {
	for _, c := range Catalogue {
		if c.Status == Built {
			built++
		} else {
			documented++
		}
		if c.Metered {
			metered++
		}
	}
	return
}

// ------------------------------------------------------------- connections

type Connection struct {
	ID         string
	Config     map[string]string
	LastTest   string
	LastResult string
	OK         bool
}

func Load(db *sql.DB, id string) (Connection, error) {
	if _, err := db.Exec(Schema); err != nil {
		return Connection{}, err
	}
	c := Connection{ID: id, Config: map[string]string{}}
	var cfg, test, result string
	var ok int
	err := db.QueryRow(`SELECT COALESCE(config,''), COALESCE(last_test,''),
		COALESCE(last_result,''), ok FROM connections WHERE id=?`, id).
		Scan(&cfg, &test, &result, &ok)
	if err == sql.ErrNoRows {
		return c, nil
	}
	if err != nil {
		return c, err
	}
	c.LastTest, c.LastResult, c.OK = test, result, ok == 1
	for _, pair := range strings.Split(cfg, "\n") {
		k, v, found := strings.Cut(pair, "=")
		if found {
			c.Config[k] = v
		}
	}
	return c, nil
}

// Save writes the non-secret settings.
//
// A secret is never stored here. It names an environment variable and lives
// there, so a database copied for inspection does not carry the credentials
// of the account it describes.
func Save(db *sql.DB, id string, cfg map[string]string) error {
	c, ok := Get(id)
	if !ok {
		return fmt.Errorf("no such connector")
	}
	secret := map[string]bool{}
	for _, in := range c.Inputs {
		if in.Secret {
			secret[in.Name] = true
		}
	}
	keys := make([]string, 0, len(cfg))
	for k := range cfg {
		if !secret[k] {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		fmt.Fprintf(&b, "%s=%s\n", k, cfg[k])
	}
	if _, err := db.Exec(Schema); err != nil {
		return err
	}
	_, err := db.Exec(`INSERT INTO connections(id, config) VALUES (?,?)
		ON CONFLICT(id) DO UPDATE SET config=excluded.config`, id, b.String())
	return err
}

// Test says WHAT it found, not just that it worked.
//
// A green tick tells nobody anything. "Read 412 files, 2025-06-01 to
// 2026-08-15, 48 704 rows" is a sentence somebody can check against what they
// expected, and notice when it is wrong.
func Test(db *sql.DB, id string, env func(string) string) (string, bool, error) {
	c, ok := Get(id)
	if !ok {
		return "", false, fmt.Errorf("no such connector")
	}
	conn, err := Load(db, id)
	if err != nil {
		return "", false, err
	}

	var missing []string
	for _, in := range c.Inputs {
		if in.Secret {
			if env(in.EnvVar) == "" {
				missing = append(missing, in.EnvVar+" (an environment variable)")
			}
			continue
		}
		if strings.TrimSpace(conn.Config[in.Name]) == "" {
			missing = append(missing, in.Label)
		}
	}

	var result string
	good := false
	switch {
	case c.Status == Documented:
		result = "This connector is documented, not built: the endpoint and its shape " +
			"are established and the reader is not written. Nothing was called."
	case len(missing) > 0:
		result = "Not configured yet. Still needed: " + strings.Join(missing, ", ") + "."
	case c.Metered:
		result = "Configured. NOT called: this connector is metered, and running it " +
			"costs money per request. Use Import, which asks first."
		good = true
	default:
		result = "Configured and free to run. Import will read it and say what it found."
		good = true
	}

	now := time.Now().UTC().Format(time.RFC3339)
	okInt := 0
	if good {
		okInt = 1
	}
	if _, err := db.Exec(`INSERT INTO connections(id, last_test, last_result, ok)
		VALUES (?,?,?,?) ON CONFLICT(id) DO UPDATE SET
		last_test=excluded.last_test, last_result=excluded.last_result, ok=excluded.ok`,
		id, now, result, okInt); err != nil {
		return result, good, err
	}
	return result, good, nil
}

// Import is where money can be spent, so it refuses without an explicit yes.
//
// This is the rule the whole catalogue exists to make visible: a metered
// connector never runs because somebody clicked past a screen. The
// confirmation is a separate act, and the page prints the cost beside it.
func Import(db *sql.DB, id string, confirmed bool) (string, error) {
	c, ok := Get(id)
	if !ok {
		return "", fmt.Errorf("no such connector")
	}
	if c.Status == Documented {
		return "", fmt.Errorf("%s is documented, not built: there is nothing to run", c.Name)
	}
	if c.Metered && !confirmed {
		return "", fmt.Errorf("%s costs money to run (%s). Confirm before it is called",
			c.Name, c.CostNote)
	}
	return "", fmt.Errorf("no live account is connected to this installation, so " +
		"there is nothing to read. The estate you are looking at is generated")
}
