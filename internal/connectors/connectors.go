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
// Status is built or documented and nothing in between, and it is DERIVED
// from the readers registry below rather than written on each entry: seven
// entries once said Built by hand while no reader existed anywhere in the
// module for any of them, which is exactly the kind of half-built connector
// that looks finished and is not. Built now holds only when readers[id]
// actually exists.
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

// Reader actually reads a connector's source and returns the sentence Test()
// promises: files read, first and last day, rows, total. It takes the saved
// connection config, the options for this call, and the store to write into.
//
// One function does both jobs Test and Import need, DryRun told apart by
// ImportOptions rather than by a second registry: a describe-only pass and a
// real one drift the moment they are two functions, because nothing forces
// the second edit when the first one changes what a file means.
type Reader func(db *sql.DB, cfg map[string]string, opt ImportOptions) (string, error)

// ImportOptions is what a Reader needs beyond the saved, non-secret config.
type ImportOptions struct {
	// DryRun is Test(): describe what Import would do and write nothing.
	DryRun bool
	// ReplaceGenerated is the operator's explicit yes to wipe a generated
	// estate before real rows are written. A Reader that never mixes
	// generated and real money ignores this unless it finds generated rows.
	ReplaceGenerated bool
	// Actor is who to credit in the journal for anything a Reader records
	// there. Empty when nothing is known, such as inside Test().
	Actor string
	// Rec is where a Reader journals a decision it made, such as replacing
	// the generated estate. Nil is valid and means nothing is journaled.
	Rec Recorder
}

// Recorder is store.Recorder, restated here so this package does not import
// the one that imports it, the same reason store.go gives for restating
// anomaly.Recorder in its own package.
type Recorder interface {
	Emit(kind, actor, severity string, data map[string]any, onBehalfOf []string) error
}

// readers is the whole truth about what this console can actually read.
//
// A map LITERAL, not an init()-time assignment: deriveStatus runs from this
// package's own init(), and Go does not promise which file's init() a
// compiler visits first. A second init() registering a reader could run
// after deriveStatus already decided Documented, and the entry would stay
// wrong until the next process restart. Package-level variables are
// initialised before any init() runs, in dependency order, so a literal here
// is resolved before deriveStatus can read it, regardless of which file
// tokenFuseFocusReader is defined in.
var readers = map[string]Reader{
	"aws-data-exports":            cloudFocusReader(awsDataExportsSpec),
	"gcp-billing-export":          cloudFocusReader(gcpBillingExportSpec),
	"tokenfuse-focus":             tokenFuseFocusReader,
	"aws-rightsizing":             awsRightsizingReader,
	"gcp-recommender":             gcpRecommenderReader,
	"azure-advisor":               azureAdvisorReader,
	"saas-seats":                  saasSeatsReader,
	"aws-budgets-recommended":     awsBudgetsRecommendedReader,
	"gcp-cost-recommender-budget": gcpCostRecommenderBudgetReader,
	"azure-advisor-budget":        azureAdvisorBudgetReader,
	"anthropic-usage":             providerUsageReader(anthropicUsageSpec),
	"openai-usage":                providerUsageReader(openaiUsageSpec),
}

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
	// Optional means Test does not demand it before it will run the reader:
	// a setting with a sensible default, such as a limit or a tag key.
	Optional bool
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

// cloudFocusInputs is the form the two plain-FOCUS folder readers share: a
// required folder and four optional settings, each with a default the reader
// documents in cloudfocus.go.
func cloudFocusInputs(pathLabel, pathHint string) []Input {
	return []Input{
		{Name: "path", Label: pathLabel, Hint: pathHint},
		{Name: "team_tag", Label: "Tag key that names the team", Optional: true,
			Hint: "default team; several keys, comma separated, are tried in order"},
		{Name: "provider_names", Label: "ProviderName values to accept", Optional: true,
			Hint: "default per provider; set it if the export names its provider differently"},
		{Name: "max_file_mb", Label: "Largest file on disk, in MB", Optional: true,
			Hint: "default 4096; a larger file is refused by name"},
		{Name: "max_unpacked_mb", Label: "Largest a gzip may inflate to, in MB", Optional: true,
			Hint: "default 20480; a gzip that inflates past it is refused by name"},
	}
}

// Catalogue is written out rather than discovered, so what the console claims
// it can read can be read here and argued with.
var Catalogue = []Connector{
	{
		ID: "aws-data-exports", Name: "AWS Data Exports (FOCUS 1.0 and 1.2)", Provider: "aws",
		Kind: ExportDrop, Feeds: "charges", Metered: false,
		Auth: "none for the reader: it reads files already delivered to a folder",
		CostNote: "No charge for the export itself, or for reading it. You pay S3 storage and requests for " +
			"the delivered objects, which is pennies a month at this size.",
		Note: "Reads the FOCUS 1.0 or 1.2 with AWS columns tables (FOCUS_1_0_AWS, FOCUS_1_2_AWS) as " +
			"CSV or CSV.gz from a folder you synced the export to; there is no S3 client in this " +
			"binary, and the folder is walked recursively. Create the export as text/csv with gzip, " +
			"time granularity DAILY or HOURLY, file versioning \"Overwrite existing data export " +
			"file\", and sync with deletion: a folder holding two versions of one period counts it " +
			"twice. USD only. All five charge categories land under their own name, so a Savings " +
			"Plan fee, tax or credit never counts as usage and credits stay on their own line; a " +
			"negative BilledCost is kept. A row longer than a day (AWS writes the month's Tax as " +
			"one) lands whole on the day it starts. The team comes from the Tags column, key " +
			"team_tag (default team). Delivery is at least daily, and the previous period can " +
			"still be revised for about two weeks after month end; re-sync and import again and " +
			"the revised file replaces its earlier version. Measured on a real FOCUS 1.0 file; " +
			"1.2 is read from the spec and AWS's dictionary only.",
		Doc:    "https://docs.aws.amazon.com/cur/latest/userguide/table-dictionary-focus-1-2-aws.html",
		Inputs: cloudFocusInputs("Folder the export was synced to", "the local path of the synced export"),
		Cannot: "It cannot tell you WHY something cost what it did. It carries no " +
			"application context beyond the tags already on the resource. It does not fetch from " +
			"S3, read Parquet or zip, convert other currencies, or feed the commitments table.",
	},
	{
		ID: "aws-cost-explorer", Name: "AWS Cost Explorer", Provider: "aws",
		Kind: API, Feeds: "charges", Metered: true,
		Auth: "an IAM role with ce:GetCostAndUsage",
		CostNote: "USD 0.01 per request, every request, forever. A daily pull across " +
			"five dimensions is a few dollars a month and rises with curiosity.",
		Note: "Use the export above for history and this only for the current day, " +
			"which the export has not delivered yet. The reader is not written; the " +
			"export's shape and cost are documented so the decision can be made before it is.",
		Doc:    "https://docs.aws.amazon.com/aws-cost-management/latest/APIReference/",
		Inputs: []Input{{Name: "profile", Label: "AWS profile", Hint: "from your ~/.aws/config"}},
		Cannot: "It cannot give you resource-level detail. That is the export's job.",
	},
	{
		ID: "gcp-billing-export", Name: "GCP billing export (FOCUS CSV folder)", Provider: "gcp",
		Kind: ExportDrop, Feeds: "charges", Metered: false,
		Auth: "none for the reader: it reads a CSV already exported from the BigQuery FOCUS view",
		CostNote: "No charge for reading the folder. Producing the CSV is yours: BigQuery storage, " +
			"the bytes your query scans, and wherever the file sits meanwhile.",
		Note: "This entry is a folder reader, not a BigQuery client. Google's FOCUS export is a " +
			"BigQuery view (Preview when this was written); run a query over it that writes " +
			"ChargePeriodStart and ChargePeriodEnd as RFC 3339 text and its x_Tags as JSON text, " +
			"export the result to CSV or CSV.gz, and point this at the folder. The same engine as " +
			"the AWS reader: USD only, every charge category kept under its own name, a negative " +
			"BilledCost kept, a row longer than a day landing whole on the day it starts, the " +
			"team from the tag named by team_tag. " +
			"Google does not state its ProviderName value on the page this was written from, so " +
			"Google Cloud is assumed, not measured: provider_names overrides it.",
		Doc:    "https://docs.cloud.google.com/billing/docs/how-to/export-data-bigquery-focus-setup",
		Inputs: cloudFocusInputs("Folder the exported CSV files are in", "the local path of the exported CSV files"),
		Cannot: "It cannot query BigQuery, and it cannot show you anything before the FOCUS export " +
			"was switched on. It does not feed the commitments table.",
	},
	{
		ID: "azure-focus", Name: "Azure Cost Management (FOCUS)", Provider: "azure",
		Kind: ExportDrop, Feeds: "charges", Metered: false,
		Auth:     "a storage account key or a managed identity with blob read",
		CostNote: "The export is free; you pay blob storage for what it writes.",
		Note: "The reader is not written; the export's shape and cost are documented " +
			"so the decision can be made before it is.",
		Doc: "https://learn.microsoft.com/en-us/azure/cost-management-billing/",
		Inputs: []Input{{Name: "container", Label: "Blob container",
			Hint: "where the scheduled export writes"}},
		Cannot: "Reservation utilisation is a separate export. This one is charges.",
	},
	{
		ID: "kubecost", Name: "Kubecost", Provider: "kubernetes",
		Kind: API, Feeds: "charges", Metered: false,
		Auth:     "an endpoint on the cluster, usually port-forwarded",
		CostNote: "Free to query. The cluster it runs on is not free, but you are already paying for that.",
		Note: "The reader is not written; the export's shape and cost are documented " +
			"so the decision can be made before it is.",
		Doc:    "https://docs.kubecost.com/apis/apis-overview",
		Inputs: []Input{{Name: "url", Label: "Kubecost URL", Hint: "http://localhost:9090"}},
		Cannot: "It cannot allocate what the cluster cannot label. Unlabelled pods stay shared.",
	},
	{
		ID: "opencost", Name: "OpenCost", Provider: "kubernetes",
		Kind: API, Feeds: "charges", Metered: false,
		Auth:     "an endpoint on the cluster",
		CostNote: "Free.",
		Note:     "The endpoint and its shape are established; the reader is not written yet.",
		Doc:      "https://www.opencost.io/docs/integrations/api",
		Inputs:   []Input{{Name: "url", Label: "OpenCost URL"}},
		Cannot:   "Same limit as Kubecost: no labels, no allocation.",
	},
	{
		ID: "tokenfuse-focus", Name: "TokenFuse FOCUS export", Provider: "ai",
		Kind: ExportDrop, Feeds: "charges (ai)", Metered: false,
		Auth: "none for the reader: it reads files already written to a folder",
		CostNote: "No charge for reading the export. TokenFuse itself, and whatever it " +
			"gateways to, are separate bills.",
		Note: "A FOCUS 1.2 CSV (or .csv.gz) with the gateway's own extension columns: an " +
			"agent id and a run id on every row, so this is the first connector that can " +
			"attribute AI spend to an agent rather than only a team. From TokenFuse 1.7.0 a call " +
			"refused for identity is filed under the credential that sent it (key: and the key's " +
			"name), never under the agent it claimed, and every row names its credential and, " +
			"when blocked, why. USD only in this step.",
		Doc: "https://focus.finops.org/",
		Inputs: []Input{{Name: "path", Label: "Folder tokenfuse focus-export wrote to",
			Hint: "the local path, or drop the folder on this page"}},
		Cannot: "It cannot tell you what the call was for: an outcome is what the calling " +
			"agent tagged, or nothing.",
	},
	{
		ID: "anthropic-usage", Name: "Anthropic usage and cost", Provider: "ai",
		Kind: API, Feeds: "provider_usage", Metered: false,
		Auth: "none for the console: it reads a folder. costcrew-usage fetches the folder with an " +
			"Admin API key read from ANTHROPIC_ADMIN_KEY (sk-ant-admin..., not an ordinary API key), " +
			"never stored and never printed",
		CostNote: "Neither the Usage and Cost API guide nor its reference names a charge for " +
			"calling these endpoints (read 2026-10-07). The tokens it reports certainly were billed.",
		Note: "The provider's own cost per model per day (GET /v1/organizations/cost_report) and " +
			"tokens per model, key and day (GET /v1/organizations/usage_report/messages), saved as " +
			"JSON by costcrew-usage or by hand into usage-*.json and cost-*.json files. Read into " +
			"provider_usage and set beside the gateway's own rows on the reconciliation page. " +
			"It never writes charges: it is a check on the gateway's figure, not a second copy of it.",
		Doc: "https://platform.claude.com/docs/en/build-with-claude/usage-cost-api",
		Inputs: []Input{
			{Name: "path", Label: "Folder costcrew-usage writes to",
				Hint: "the local path holding usage-*.json and cost-*.json"},
			{Name: "api_key_ids", Label: "API key ids the gateway uses", Optional: true,
				Hint: "comma-separated apikey_... ids; empty keeps every key"},
			{Name: "workspace_ids", Label: "Workspace ids", Optional: true,
				Hint: "comma-separated wrkspc_... ids; empty keeps every workspace"},
			{Name: "tolerance_cents", Label: "Tolerance, cents", Optional: true,
				Hint: "a day and model within this many cents is matched; default 1"},
			{Name: "tolerance_bp", Label: "Tolerance, basis points of the provider's figure", Optional: true,
				Hint: "or within this share of it, whichever is larger; default 50 (0.5%)"},
		},
		Cannot: "It cannot tell you which AGENT spent it: one key is shared, and only the " +
			"gateway's rows name the agent. Its cost report carries no API key, so money is " +
			"scoped by workspace, not by key. Priority Tier costs are not in the cost report at all.",
	},
	{
		ID: "openai-usage", Name: "OpenAI organization usage and costs", Provider: "ai",
		Kind: API, Feeds: "provider_usage", Metered: false,
		Auth: "none for the console: it reads a folder. costcrew-usage fetches the folder with an " +
			"Admin key read from OPENAI_ADMIN_KEY, never stored and never printed",
		CostNote: "OpenAI's API reference for these endpoints names no charge for calling them " +
			"(read 2026-10-07). The tokens they report certainly were billed.",
		Note: "The organization's own cost per line item, project and key per day " +
			"(GET /v1/organization/costs) and completions tokens per model, key and day " +
			"(GET /v1/organization/usage/completions), saved as JSON into usage-*.json and " +
			"cost-*.json files. Read into provider_usage and set beside the gateway's own rows " +
			"on the reconciliation page. It never writes charges.",
		Doc: "https://platform.openai.com/docs/api-reference/usage",
		Inputs: []Input{
			{Name: "path", Label: "Folder costcrew-usage writes to",
				Hint: "the local path holding usage-*.json and cost-*.json"},
			{Name: "api_key_ids", Label: "API key ids the gateway uses", Optional: true,
				Hint: "comma-separated key_... ids; empty keeps every key"},
			{Name: "project_ids", Label: "Project ids", Optional: true,
				Hint: "comma-separated proj_... ids; empty keeps every project"},
			{Name: "tolerance_cents", Label: "Tolerance, cents", Optional: true,
				Hint: "a day and model within this many cents is matched; default 1"},
			{Name: "tolerance_bp", Label: "Tolerance, basis points of the provider's figure", Optional: true,
				Hint: "or within this share of it, whichever is larger; default 50 (0.5%)"},
		},
		Cannot: "It cannot tell you which AGENT spent it, for the same reason as the Anthropic " +
			"entry. A cost line item is read as \"model, token type\"; one in any other shape is " +
			"kept whole as its own row rather than guessed into a model.",
	},
	{
		ID: "openrouter-usage", Name: "OpenRouter activity", Provider: "ai",
		Kind: API, Feeds: "charges (ai)", Metered: false,
		Auth:     "the same key you call it with",
		CostNote: "Free to query.",
		Note: "The reader is not written; the export's shape and cost are documented " +
			"so the decision can be made before it is.",
		Doc: "https://openrouter.ai/docs/api-reference",
		Inputs: []Input{{Name: "key", Label: "API key", Secret: true,
			EnvVar: "OPENROUTER_API_KEY"}},
		Cannot: "Per-agent attribution, for the same reason as above.",
	},
	{
		ID: "compute-optimizer", Name: "AWS Compute Optimizer", Provider: "aws",
		Kind: API, Feeds: "rightsizing", Metered: false,
		Auth:     "an IAM role with compute-optimizer:Get*",
		CostNote: "Free, but it needs CloudWatch metrics, which are not.",
		Doc:      "https://docs.aws.amazon.com/compute-optimizer/",
		Inputs:   []Input{{Name: "profile", Label: "AWS profile"}},
		Cannot:   "It sees fourteen days. A monthly batch job looks idle to it.",
	},
	{
		ID: "aws-rightsizing", Name: "AWS Cost Explorer rightsizing recommendations", Provider: "aws",
		Kind: ExportDrop, Feeds: "recommendations", Metered: false,
		Auth: "none for the reader: it reads a CSV already exported from Cost Explorer's " +
			"Rightsizing Recommendations report",
		CostNote: "No charge for the export itself, or for reading it.",
		Note: "Cost Explorer's own Rightsizing Recommendations report, downloaded as CSV " +
			"(Cost Explorer console, Recommendations, Rightsizing recommendations, Download " +
			"CSV). Distinct from the Compute Optimizer connector above: that one is the " +
			"live API, this one is an export somebody already generated. The lookback is " +
			"whatever the report was generated with, carried as its own column, not a " +
			"fixed default this reader assumes.",
		Doc: "https://docs.aws.amazon.com/cost-management/latest/userguide/ce-rightsizing.html",
		Inputs: []Input{{Name: "path", Label: "Folder the rightsizing CSV export lands in",
			Hint: "the local path, or drop the file on this page"}},
		Cannot: "It only ever sees what the report was generated with. A monthly job " +
			"looks idle to a fourteen-day lookback, and this reader has no way to tell " +
			"the two apart from the file alone.",
	},
	{
		ID: "gcp-recommender", Name: "GCP Recommender cost recommendations", Provider: "gcp",
		Kind: ExportDrop, Feeds: "recommendations", Metered: false,
		Auth:     "none for the reader: it reads a CSV already exported from the Recommender API",
		CostNote: "No charge for the export itself, or for reading it.",
		Note: "google.compute.instance.MachineTypeRecommender and its neighbours " +
			"(an idle-VM recommender among them), exported to CSV rather than read live " +
			"through the API.",
		Doc: "https://cloud.google.com/recommender/docs/machine-type-recommendations",
		Inputs: []Input{{Name: "path", Label: "Folder the recommender CSV export lands in",
			Hint: "the local path, or drop the file on this page"}},
		Cannot: "Same limit as the AWS reader: it only ever sees the observation period " +
			"the export was generated with.",
	},
	{
		ID: "azure-advisor", Name: "Azure Advisor cost recommendations", Provider: "azure",
		Kind: ExportDrop, Feeds: "recommendations", Metered: false,
		Auth:     "none for the reader: it reads a CSV already exported from Advisor",
		CostNote: "No charge for the export itself, or for reading it.",
		Note: "Advisor's own CSV export reports a recommendation's saving as POTENTIAL " +
			"ANNUAL cost, never monthly; this reader divides by twelve, rounded half " +
			"away from zero, because every other row in this console's recommendations " +
			"table is a monthly figure.",
		Doc: "https://learn.microsoft.com/en-us/azure/advisor/advisor-cost-recommendations",
		Inputs: []Input{{Name: "path", Label: "Folder the Advisor CSV export lands in",
			Hint: "the local path, or drop the file on this page"}},
		Cannot: "Advisor does not publish its own analysis window as a documented figure; " +
			"this reader reads whatever the export's own column says and nothing more.",
	},
	{
		ID: "aws-budgets-recommended", Name: "AWS Budgets recommended threshold", Provider: "aws",
		Kind: ExportDrop, Feeds: "budget_recommendations", Metered: false,
		Auth:     "none for the reader: it reads a CSV already exported from AWS Budgets",
		CostNote: "No charge for the export itself, or for reading it.",
		Note: "AWS Budgets' own recommended threshold, exported to CSV with the team this " +
			"console already tracks named alongside it. Cited beside the team's real budget " +
			"on a finops-partner's own packet section, never applied as this console's own " +
			"budget figure. `@claude`, not measured against a real export: AWS does not " +
			"publish a literal per-team recommended-budget report, and this reader's own " +
			"column shape approximates one the way an operator's own budget naming or " +
			"tagging convention would carry a team into an export.",
		Doc: "https://docs.aws.amazon.com/cost-management/latest/userguide/budgets-managing-costs.html",
		Inputs: []Input{{Name: "path", Label: "Folder the recommended-threshold CSV export lands in",
			Hint: "the local path, or drop the file on this page"}},
		Cannot: "It cannot invent a team boundary the export does not carry: a row whose own " +
			"Team column is empty is refused, not guessed. It is never read by a budget or " +
			"guard check anywhere in this console.",
	},
	{
		ID: "gcp-cost-recommender-budget", Name: "GCP Cost Recommender budget recommendation", Provider: "gcp",
		Kind: ExportDrop, Feeds: "budget_recommendations", Metered: false,
		Auth:     "none for the reader: it reads a CSV already exported from the Recommender API",
		CostNote: "No charge for the export itself, or for reading it.",
		Note: "Cost Recommender's own budget-shaped recommendation, exported to CSV. Cited " +
			"beside the team's real budget on a finops-partner's own packet section, never " +
			"applied as this console's own budget figure. `@claude`, not measured against a " +
			"real export: the same honest caveat as the AWS reader above.",
		Doc: "https://cloud.google.com/recommender/docs/recommenders-overview",
		Inputs: []Input{{Name: "path", Label: "Folder the Recommender CSV export lands in",
			Hint: "the local path, or drop the file on this page"}},
		Cannot: "Same limit as the AWS reader: a row with no team column is refused, not " +
			"guessed, and it is never read by a budget or guard check anywhere in this console.",
	},
	{
		ID: "azure-advisor-budget", Name: "Azure Advisor budget-shaped cost recommendation", Provider: "azure",
		Kind: ExportDrop, Feeds: "budget_recommendations", Metered: false,
		Auth:     "none for the reader: it reads a CSV already exported from Advisor",
		CostNote: "No charge for the export itself, or for reading it.",
		Note: "Advisor's own budget-shaped cost recommendation, exported to CSV, already " +
			"monthly (unlike this package's rightsizing-style Advisor reader, this one does " +
			"not divide an annual figure by twelve). Cited beside the team's real budget on " +
			"a finops-partner's own packet section, never applied as this console's own " +
			"budget figure. `@claude`, not measured against a real export.",
		Doc: "https://learn.microsoft.com/en-us/azure/advisor/advisor-cost-recommendations",
		Inputs: []Input{{Name: "path", Label: "Folder the Advisor CSV export lands in",
			Hint: "the local path, or drop the file on this page"}},
		Cannot: "Same limit as the other two: a row with no team column is refused, not " +
			"guessed, and it is never read by a budget or guard check anywhere in this console.",
	},
	{
		ID: "saas-seats", Name: "SaaS seat reconciliation", Provider: "saas",
		// Feeds is the real table now that a reader exists: "licences", not
		// the "saas_licences" this entry guessed while still Documented.
		Kind: Local, Feeds: "licences", Metered: false,
		Auth:     "an export from each vendor's admin console",
		CostNote: "Free, and manual, which is the honest description.",
		Note:     "There is no standard here. Every vendor exports something different.",
		Inputs:   []Input{{Name: "path", Label: "Folder of vendor exports"}},
		Cannot:   "Nothing automates this. Anyone who says otherwise is selling scrapers.",
	},
}

func init() {
	deriveStatus()
}

// deriveStatus sets Status on every catalogue entry from the readers
// registry. This is the ONLY place anything assigns Connector.Status: no
// entry above names it, so there is nowhere left for the catalogue to claim
// a reader that does not exist.
func deriveStatus() {
	for i := range Catalogue {
		if _, ok := readers[Catalogue[i].ID]; ok {
			Catalogue[i].Status = Built
		} else {
			Catalogue[i].Status = Documented
		}
	}
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
	// LastImport and LastImportResult are the last import that ran and the
	// sentence its reader wrote. Until invariant 89 the web console threw
	// that sentence away and said only that the import worked, so a refused
	// row, a call counted once from two exports, or a gateway refusal with
	// nobody to file it under was invisible to the person who clicked.
	LastImport       string
	LastImportResult string
}

// ensureImportColumns adds the two columns an installation from before them
// lacks; running it again changes nothing (invariant 11).
func ensureImportColumns(db *sql.DB) error {
	if _, err := db.Exec(Schema); err != nil {
		return err
	}
	for _, c := range []string{"last_import", "last_import_result"} {
		if _, err := db.Exec(`ALTER TABLE connections ADD COLUMN ` + c + ` TEXT`); err != nil &&
			!strings.Contains(err.Error(), "duplicate column name") {
			return fmt.Errorf("adding connections.%s: %w", c, err)
		}
	}
	return nil
}

func Load(db *sql.DB, id string) (Connection, error) {
	if err := ensureImportColumns(db); err != nil {
		return Connection{}, err
	}
	c := Connection{ID: id, Config: map[string]string{}}
	var cfg, test, result string
	var ok int
	err := db.QueryRow(`SELECT COALESCE(config,''), COALESCE(last_test,''),
		COALESCE(last_result,''), ok, COALESCE(last_import,''), COALESCE(last_import_result,'')
		FROM connections WHERE id=?`, id).
		Scan(&cfg, &test, &result, &ok, &c.LastImport, &c.LastImportResult)
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
		if !in.Optional && strings.TrimSpace(conn.Config[in.Name]) == "" {
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
		// Built and free: actually ask the reader what it would do, rather than
		// print a generic sentence true of every non-metered connector. DryRun
		// is the whole of what makes this safe to call here: the same function
		// Import uses, told to look and not write.
		reader, hasReader := readers[id]
		if !hasReader {
			// Status Built is derived from this exact map, so this cannot
			// happen; kept as the same generic sentence rather than a panic,
			// because a reader that vanishes between the check and the call
			// is a bug this should describe, not crash on.
			result = "Configured and free to run. Import will read it and say what it found."
			good = true
			break
		}
		desc, rerr := reader(db, conn.Config, ImportOptions{DryRun: true})
		if rerr != nil {
			result = rerr.Error()
			good = false
		} else {
			result = desc
			good = true
		}
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
// That gate is checked FIRST, and independent of Status: Metered is a fact
// about the external service, true whether or not this console has a reader
// for it yet, so it must not become reachable only once a reader exists.
//
// Once past that gate, Import looks up the reader. When none is registered
// (which is every connector but tokenfuse-focus today) it returns the same
// refusal it always has: there is nothing to read. When one is registered,
// it is handed the saved, non-secret config and opt, and it runs.
//
// opt.DryRun is always cleared: a caller cannot turn a real Import into a
// describe-only pass by handing it a DryRun option built for Test.
func Import(db *sql.DB, id string, confirmed bool, opt ImportOptions) (string, error) {
	c, ok := Get(id)
	if !ok {
		return "", fmt.Errorf("no such connector")
	}
	if c.Metered && !confirmed {
		return "", fmt.Errorf("%s costs money to run (%s). Confirm before it is called",
			c.Name, c.CostNote)
	}
	reader, hasReader := readers[id]
	if !hasReader {
		return "", fmt.Errorf("no live account is connected to this installation, so " +
			"there is nothing to read. The estate you are looking at is generated")
	}
	conn, err := Load(db, id)
	if err != nil {
		return "", err
	}
	opt.DryRun = false
	msg, err := reader(db, conn.Config, opt)
	if err != nil {
		return msg, err
	}
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := db.Exec(`INSERT INTO connections(id, last_import, last_import_result)
		VALUES (?,?,?) ON CONFLICT(id) DO UPDATE SET
		last_import=excluded.last_import, last_import_result=excluded.last_import_result`,
		id, now, msg); err != nil {
		return msg, fmt.Errorf("the import ran, but recording what it read failed: %w", err)
	}
	return msg, nil
}
