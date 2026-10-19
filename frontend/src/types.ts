export interface Interval { estimate: number | null; low: number | null; high: number | null; method: string; n: number }
export interface Dist { n: number; mean: number | null; min: number | null; p50: number | null; p90: number | null; p95: number | null; p99: number | null; max: number | null }

export interface Provider {
  name: string; label: string; kind: string; base_url: string | null; configured: boolean; requires_key: boolean;
  rpm: number | null; reports_cost: boolean;
}

export interface CatalogModel {
  provider: string; id: string; name: string; description: string; modalities: string[]; context_length: number | null;
  price_in: number | null; price_out: number | null; price_source: string; structured_output: boolean | null;
  seed: boolean | null; free?: boolean; fake?: boolean; reasoning?: boolean; created?: number | null;
}

export interface Dataset {
  id: string; name: string; version: number; digest: string; modality: "text" | "image"; task_type: string;
  description: string; instructions: string; card: Record<string, any>; sample_count: number; builtin: boolean;
  synthetic: boolean; created_at: number; used_by_campaigns?: number; predictions?: { import_name: string; n: number }[];
  meta_keys?: string[];
}

export interface Sample {
  id: string; key: string; ordinal: number; split: string; group: string | null;
  input: { text: string; images: string[] }; expected?: any; meta: Record<string, any>; content_hash: string;
}

export interface ScorerInfo { key: string; label: string; version: string; description: string; params: Record<string, any>; kind: string }

export interface GenParams { temperature?: number | null; top_p?: number | null; max_tokens: number; seed?: number | null; json_mode?: boolean; reasoning_effort?: string | null }
export interface TargetSpec { provider: string; model: string; label?: string | null; params: GenParams }
export interface ScorerSpec { id: string; key: string; params: Record<string, any> }
export interface PromptVariant { id: string; system: string; user: string }
export interface Plan {
  schema_version?: string; dataset_id: string; targets: TargetSpec[]; prompts: PromptVariant[];
  params_grid: Record<string, any[]>; scorers: ScorerSpec[]; primary_scorer: string;
  protocol: { repetitions: number; order: "interleaved" | "blocked"; seed: number; concurrency: number; timeout_s: number; max_retries: number; splits: string[]; sample_limit: number | null; stream: boolean };
  budget: { max_usd: number | null; paid_run_confirmed: boolean };
  acceptance: { min_pass_rate: number | null; baseline_arm: string | null; non_inferiority_margin: number | null; min_assessed_per_arm: number; max_p95_latency_ms: number | null; max_cost_per_pass_usd: number | null; max_error_rate: number };
}

export interface Arm { key: string; label: string; provider: string; model: string; params: Record<string, any>; prompt_id: string; system: string; user: string; target_index: number; applicable?: boolean; trials?: number; cost_low_usd?: number | null; cost_high_usd?: number | null; price_known?: boolean }

export interface Preflight {
  ok: boolean; errors: string[]; warnings: string[]; arms: Arm[]; samples: number; trials: number; calls_estimate: number;
  judge_calls_estimate: number; cost_low_usd: number | null; cost_high_usd: number | null; unknown_price_arms: string[];
  disclosure: { recipient: string; kind: string; data: string; models?: string[] }[]; plan_digest: string; labels: string[];
}

export interface CampaignRow {
  id: string; experiment_id: string; status: string; created_at: number; started_at: number | null; finished_at: number | null;
  spent_usd: number; reserved_usd: number; unknown_cost_attempts: number; planned_trials: number; stop_reason: string | null;
  labels: string[]; budget_cap_usd: number | null; name: string; dataset_id: string; done: number; passes: number; assessed: number; live: boolean;
}

export interface LiveState {
  status: string; counts: Record<string, number>; outcomes: Record<string, number>; spent_usd: number; reserved_usd: number;
  unknown_cost_attempts: number; cap_usd: number | null; throughput_per_min: number | null; elapsed_s: number;
}

export interface Campaign extends Omit<CampaignRow, "done" | "passes" | "assessed"> {
  hypothesis: string; plan: Plan; plan_digest: string; seed: number; scoring: { scorers: ScorerSpec[]; primary: string; revision: number };
  arms: Arm[]; counts: { status: string; outcome: string; n: number }[]; state: LiveState | null; mode: string; parent_campaign_id: string | null;
}

export interface GateCheck { check: string; result: string; detail: string }
export interface Route { model: string; n: number; share: number; pass_rate: Interval; cost_usd: number | null; cost_per_call_usd: number | null; latency_p50_ms: number | null; providers: string[] }
export interface Routing {
  is_router: boolean; routes: Route[]; distinct_models: number; calls: number; stability: number | null; stability_samples: number;
  by_slice: { key: string; rows: { value: string; routes: Record<string, { n: number; passes: number; assessed: number }> }[] }[];
}
export interface ArmSummary {
  routing: Routing | null;
  arm: Arm; planned: number; not_applicable: number; completed: number; errors: number; error_categories: Record<string, number>;
  cancelled: number; pending: number; error_rate: number | null; attempts: number; retries: number;
  scorers: Record<string, { assessed: number; passes: number; fails: number; inconclusive: number; not_applicable: number; missing: number; pass_rate: Interval; mean_score: Interval; versions: string[]; cost_usd: number | null }>;
  primary: { assessed: number; passes: number; pass_rate: Interval; all_admitted_rate: Interval; task_balanced: Interval; pass_k: null | { k: number; pass_at_k: number | null; all_k: number | null; pass_at_1: number | null; samples_complete: number } };
  latency_ms: Dist; ttft_ms: Dist; latency_hist: { edges: number[]; counts: number[] }; latency_values: number[];
  output_tokens_per_s: Dist; tokens_in_mean: number | null; tokens_out_mean: number | null;
  cost: { target_known_usd: number; judge_known_usd: number; total_known_usd: number; unknown_cost_attempts: number; complete: boolean; per_trial_usd: number | null; per_pass_usd: number | null };
}
export interface Comparison { baseline: string; candidate: string; paired_samples: number; missing_pairs: number; wins: number; losses: number; ties: number; diff: Interval | null; mcnemar_p: number | null; test: string }
export interface Results {
  campaign_id: string; status: string; primary_scorer: string; scorers: ScorerSpec[]; scoring_revision: number;
  dataset: { id: string; name: string; version: number; digest: string; modality: string };
  labels: string[]; arms: Arm[]; baseline: string; summaries: Record<string, ArmSummary>; comparisons: Comparison[];
  matrix: { samples: { id: string; key: string; meta: Record<string, any> }[]; arms: string[]; values: (number | null)[][] };
  slices: { key: string; exploratory: boolean; rows: { value: any; arms: Record<string, Interval> }[] }[];
  pareto: { cost: string[]; latency: string[] };
  gate: { arms: Record<string, { verdict: string; checks: GateCheck[]; is_baseline: boolean }>; execution_status: string; execution_complete: boolean; note: string | null; policy: any };
  suspect_labels: { sample_id: string; key: string; arms: number; trials: number; consensus_answer: string; agreement: number; expected: any }[];
  ranking: string[]; best: string | null; statistically_tied_with_best: string[]; methods: Record<string, string>;
}

export interface TrialRow {
  id: string; arm_key: string; repetition: number; order_index: number; status: string; outcome: string; error_category: string | null;
  latency_ms: number | null; ttft_ms: number | null; tokens_in: number | null; tokens_out: number | null; cost_usd: number | null;
  attempt_count: number; resolved_model: string | null; output_preview: string | null; sample_key: string; sample_id: string;
}

export interface Page<T> { items: T[]; total: number; offset: number; limit: number }
