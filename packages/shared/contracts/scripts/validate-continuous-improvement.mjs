import { readFile } from "node:fs/promises";
import { fileURLToPath } from "node:url";
import path from "node:path";
import Ajv2020 from "ajv/dist/2020.js";

// Usage: validate-continuous-improvement.mjs <reportPath...> <spansPath>
// One or more report fixtures followed by the canonical span fixture they were
// built from. Every report is validated; an empty span fixture always fails.
const packageRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const args = process.argv.slice(2);
if (args.length < 2) throw new Error("at least one report path and the canonical span fixture path are required");
const spansPath = args[args.length - 1];
const reportPaths = args.slice(0, -1);

const [reportSchema, spanSchema, spans] = await Promise.all([
  readFile(path.join(packageRoot, "schemas", "continuous-improvement-report.schema.json"), "utf8").then(JSON.parse),
  readFile(path.join(packageRoot, "schemas", "canonical-span.schema.json"), "utf8").then(JSON.parse),
  readFile(spansPath, "utf8").then(JSON.parse),
]);
const reports = await Promise.all(reportPaths.map((reportPath) => readFile(reportPath, "utf8").then(JSON.parse)));

const ajv = new Ajv2020({ allErrors: true, strict: true });
const validateReport = ajv.compile(reportSchema);
const validateSpan = ajv.compile(spanSchema);
const contiguouslyContains = (haystack, needle) => {
  for (let start = 0; start + needle.length <= haystack.length; start += 1) {
    if (needle.every((operation, offset) => haystack[start + offset] === operation)) return true;
  }
  return false;
};
let relationshipCount = 0;
let motifCount = 0;
for (const [index, report] of reports.entries()) {
  if (!validateReport(report)) throw new Error(`report fixture ${reportPaths[index]}: ${ajv.errorsText(validateReport.errors)}`);
  for (const theme of report.themes) {
    // A durable registry id and a predecessor are the same claim: a theme that
    // continues an earlier one must carry both, and a new theme neither.
    const isNew = theme.lineage.transition === "new";
    if (isNew !== (theme.registry_id === "")) {
      throw new Error(`report fixture ${reportPaths[index]}: theme ${theme.id} transition ${theme.lineage.transition} disagrees with registry_id "${theme.registry_id}"`);
    }
    if (isNew !== (theme.lineage.predecessor_ids.length === 0)) {
      throw new Error(`report fixture ${reportPaths[index]}: theme ${theme.id} transition ${theme.lineage.transition} disagrees with its predecessor list`);
    }
    if (!isNew && !theme.lineage.predecessor_ids.includes(theme.registry_id)) {
      throw new Error(`report fixture ${reportPaths[index]}: theme ${theme.id} inherited a registry id that is not one of its predecessors`);
    }
  }

  // An opportunity names the exact pair of workflow variants it was emitted
  // from, and a safety finding carries no borrowed dollar figure: copying the
  // efficiency finding's alternative metrics and expected value would let the
  // same money be counted twice under two detectors.
  const variantIds = new Set(report.workflow_variants.map((variant) => variant.id));
  for (const opportunity of report.opportunities) {
    const where = `report fixture ${reportPaths[index]}: opportunity ${opportunity.id}`;
    for (const key of ["current_variant_id", "alternative_variant_id"]) {
      if (opportunity[key] && !variantIds.has(opportunity[key])) {
        throw new Error(`${where} ${key} ${opportunity[key]} is not a workflow variant of this report`);
      }
    }
    if (opportunity.detector_id === "dominated-workflow") {
      if (!opportunity.current_variant_id || !opportunity.alternative_variant_id) {
        throw new Error(`${where} compares two workflows without naming both variants`);
      }
    }
    if (opportunity.type === "safety") {
      if (opportunity.alternative_variant_id) throw new Error(`${where} is a safety finding with an alternative variant`);
      if (opportunity.expected_value !== 0) throw new Error(`${where} is a safety finding carrying expected value ${opportunity.expected_value}`);
      if (opportunity.alternative_metrics.cost_per_outcome_usd !== null || opportunity.alternative_metrics.eligible_runs !== 0) {
        throw new Error(`${where} is a safety finding carrying another workflow's metrics`);
      }
    }
  }

  // A relationship is a count, so it must be recomputable from the counts it
  // carries. Anything a reader cannot re-derive is a claim, not evidence.
  const themeIds = new Set(report.themes.map((theme) => theme.id));
  for (const relationship of report.relationships) {
    const where = `report fixture ${reportPaths[index]}: relationship ${relationship.id}`;
    if (!themeIds.has(relationship.theme_a_id) || !themeIds.has(relationship.theme_b_id)) {
      throw new Error(`${where} references a theme that is not in this report`);
    }
    if (!(relationship.theme_a_id < relationship.theme_b_id)) {
      throw new Error(`${where} theme ids are not in canonical order`);
    }
    if (relationship.shared_unit_count > relationship.theme_a_unit_count || relationship.shared_unit_count > relationship.theme_b_unit_count) {
      throw new Error(`${where} shares more units than either theme has`);
    }
    if (relationship.shared_unit_ids.length > relationship.shared_unit_count) {
      throw new Error(`${where} carries more shared unit ids than its shared unit count`);
    }
    const expectedBGivenA = relationship.shared_unit_count / relationship.theme_a_unit_count;
    const expectedAGivenB = relationship.shared_unit_count / relationship.theme_b_unit_count;
    if (Math.abs(relationship.probability_b_given_a - expectedBGivenA) > 1e-9) {
      throw new Error(`${where} probability_b_given_a ${relationship.probability_b_given_a} != ${expectedBGivenA}`);
    }
    if (Math.abs(relationship.probability_a_given_b - expectedAGivenB) > 1e-9) {
      throw new Error(`${where} probability_a_given_b ${relationship.probability_a_given_b} != ${expectedAGivenB}`);
    }
  }
  relationshipCount += report.relationships.length;

  // A motif is a structural count over variants this report carries, so every
  // part of it must be re-derivable from those variants.
  const variantsByID = new Map(report.workflow_variants.map((variant) => [variant.id, variant]));
  const familyIDs = new Set(report.task_families.map((family) => family.id));
  for (const motif of report.motifs) {
    const where = `report fixture ${reportPaths[index]}: motif ${motif.id}`;
    if (!familyIDs.has(motif.task_family_id)) throw new Error(`${where} references a task family that is not in this report`);
    if (motif.support_variant_count !== motif.variant_ids.length) throw new Error(`${where} support variant count disagrees with its variant ids`);
    let runs = 0;
    let weighted = 0;
    for (const variantID of motif.variant_ids) {
      const variant = variantsByID.get(variantID);
      if (!variant) throw new Error(`${where} references workflow variant ${variantID} that is not in this report`);
      if (variant.task_family_id !== motif.task_family_id) throw new Error(`${where} supporting variant ${variantID} belongs to another task family`);
      if (!contiguouslyContains(variant.signature, motif.operations)) {
        throw new Error(`${where} operations are not a contiguous subsequence of variant ${variantID}'s signature`);
      }
      runs += variant.eligible_runs;
      weighted += (motif.operations.length / variant.signature.length) * variant.eligible_runs;
    }
    if (motif.support_run_count !== runs) throw new Error(`${where} support run count ${motif.support_run_count} != ${runs}`);
    const expectedShare = runs > 0 ? weighted / runs : 0;
    if (Math.abs(motif.structural_cost_share - expectedShare) > 1e-6) {
      throw new Error(`${where} structural_cost_share ${motif.structural_cost_share} != ${expectedShare}`);
    }
  }
  motifCount += report.motifs.length;

  // The causal investigation of every case: the cohort's arms, the traces it
  // selected from them, and the backward hard-dependency slice.
  const unitsByID = new Map(report.analysis_units.map((unit) => [unit.id, unit]));
  const familiesByID = new Map(report.task_families.map((family) => [family.id, family]));
  for (const item of report.cases) {
    const where = `report fixture ${reportPaths[index]}: case ${item.id}`;
    const family = familiesByID.get(item.cohort.task_family_id);
    if (!family) throw new Error(`${where} cohort references a task family that is not in this report`);
    const familyUnits = new Set(family.analysis_unit_ids);
    if (item.cohort.family_unit_count !== familyUnits.size) throw new Error(`${where} cohort family unit count disagrees with the task family`);
    const roles = item.cohort.arms.map((arm) => arm.role);
    if (roles.join(",") !== "baseline,alternative") throw new Error(`${where} cohort arms are not baseline then alternative`);
    const armVariants = new Map();
    let comparedUnits = 0;
    for (const arm of item.cohort.arms) {
      const variant = variantsByID.get(arm.variant_id);
      if (!variant) throw new Error(`${where} cohort arm ${arm.role} references a workflow variant that is not in this report`);
      if (variant.task_family_id !== family.id) throw new Error(`${where} cohort arm ${arm.role} uses a variant of another task family`);
      armVariants.set(arm.role, variant);
      comparedUnits += arm.unit_count;
    }
    const excluded = item.cohort.excluded_units.reduce((total, exclusion) => total + exclusion.unit_count, 0);
    if (comparedUnits + excluded !== item.cohort.family_unit_count) {
      throw new Error(`${where} cohort arms (${comparedUnits}) plus exclusions (${excluded}) do not account for its ${item.cohort.family_unit_count} family units`);
    }

    const selectedPerArm = new Map();
    for (const trace of item.representative_traces) {
      const variant = armVariants.get(trace.arm);
      if (!variant || variant.id !== trace.variant_id) throw new Error(`${where} representative trace names a variant that is not that arm's variant`);
      if (!unitsByID.has(trace.analysis_unit_id)) throw new Error(`${where} representative trace ${trace.analysis_unit_id} is not an analysis unit of this report`);
      if (!familyUnits.has(trace.analysis_unit_id)) throw new Error(`${where} representative trace ${trace.analysis_unit_id} is not a member of the cohort's task family`);
      if (!variant.analysis_unit_ids.includes(trace.analysis_unit_id)) throw new Error(`${where} representative trace ${trace.analysis_unit_id} did not run that arm's workflow variant`);
      selectedPerArm.set(trace.arm, (selectedPerArm.get(trace.arm) ?? 0) + 1);
    }
    for (const [arm, count] of selectedPerArm) {
      if (count > 3) throw new Error(`${where} arm ${arm} carries ${count} representative traces, more than three`);
    }

    const slice = item.causal_slice;
    const sliceVariant = variantsByID.get(slice.variant_id);
    if (!sliceVariant) throw new Error(`${where} causal slice references a workflow variant that is not in this report`);
    if (sliceVariant.id !== armVariants.get("baseline").id) throw new Error(`${where} causal slice is not taken from the baseline arm's variant`);
    const sliceNodes = new Set(sliceVariant.nodes.map((node) => node.id));
    if (slice.manifestation_node_id !== "" && !sliceNodes.has(slice.manifestation_node_id)) {
      throw new Error(`${where} manifestation node ${slice.manifestation_node_id} is not a node of variant ${sliceVariant.id}`);
    }
    if (item.diagnosis.manifestation.node_id !== slice.manifestation_node_id) {
      throw new Error(`${where} diagnosis manifestation node disagrees with its causal slice`);
    }
    for (const nodeID of slice.node_chain) {
      if (!sliceNodes.has(nodeID)) throw new Error(`${where} causal slice node ${nodeID} is not a node of variant ${sliceVariant.id}`);
    }
    if (slice.manifestation_node_id !== "" && !slice.node_chain.includes(slice.manifestation_node_id)) {
      throw new Error(`${where} causal slice omits its own manifestation node`);
    }
    const provenDependency = (edge) => edge.strength === "hard" && (edge.type === "data" || edge.type === "control") && edge.evidence_refs.length > 0;
    const variantEdges = new Map(sliceVariant.edges.map((edge) => [`${edge.from} ${edge.to} ${edge.type}`, edge]));
    for (const edge of slice.hard_edges) {
      if (!provenDependency(edge)) throw new Error(`${where} hard chain carries an unproven edge ${edge.from}->${edge.to}`);
      if (!sliceNodes.has(edge.from) || !sliceNodes.has(edge.to)) throw new Error(`${where} hard chain edge ${edge.from}->${edge.to} names a node outside variant ${sliceVariant.id}`);
      if (!slice.node_chain.includes(edge.from) || !slice.node_chain.includes(edge.to)) {
        throw new Error(`${where} hard chain edge ${edge.from}->${edge.to} is outside the slice's node chain`);
      }
      const source = variantEdges.get(`${edge.from} ${edge.to} ${edge.type}`);
      if (!source || source.strength !== "hard") throw new Error(`${where} hard chain edge ${edge.from}->${edge.to} is not a hard edge of variant ${sliceVariant.id}`);
    }
    for (const edge of slice.unproven_adjacencies) {
      // The whole point of the separate list: nothing in it may read as proven.
      if (provenDependency(edge)) throw new Error(`${where} unproven adjacency ${edge.from}->${edge.to} is in fact a proven hard dependency`);
      if (slice.hard_edges.some((hard) => hard.from === edge.from && hard.to === edge.to && hard.type === edge.type)) {
        throw new Error(`${where} adjacency ${edge.from}->${edge.to} appears in both the hard chain and the unproven list`);
      }
    }
    const upstream = new Set(item.diagnosis.root_cause_hypothesis.upstream_node_ids);
    for (const nodeID of upstream) {
      if (!slice.node_chain.includes(nodeID) || nodeID === slice.manifestation_node_id) {
        throw new Error(`${where} diagnosis upstream node ${nodeID} is not an upstream node of its causal slice`);
      }
    }

    // A case that proposes nothing must carry nothing: no operator, no eval
    // pack, no replay. Anything else would present an abstention as a proposal.
    if (item.status === "diagnosis_only") {
      if (item.change_set.operator_id !== "" || item.eval_pack.id !== "" || item.replay.status !== "not_run") {
        throw new Error(`${where} is diagnosis-only but carries a change set, eval pack or replay`);
      }
      continue;
    }
    if (item.change_set.case_id !== item.id || item.eval_pack.case_id !== item.id) {
      throw new Error(`${where} change set or eval pack is bound to another case`);
    }

    // The case must investigate the pair its own opportunity named. A cohort
    // built from a differently-derived pair would describe one comparison and
    // measure another.
    const source = report.opportunities.find((opportunity) => opportunity.id === item.opportunity_id);
    if (!source) throw new Error(`${where} names an opportunity that is not in this report`);
    if (armVariants.get("baseline").id !== source.current_variant_id || armVariants.get("alternative").id !== source.alternative_variant_id) {
      throw new Error(`${where} cohort arms (${armVariants.get("baseline").id}/${armVariants.get("alternative").id}) are not the variants opportunity ${source.id} named`);
    }

    // Every required grader is one the recorded interpreter actually computes.
    // `json_schema` is deliberately absent: over a typed interpreter result it
    // could only assert that a hash is non-empty, which is a grader that cannot
    // fail and therefore inflates how much a replay was checked.
    const knownGraders = new Set(["exact_match", "tool_order", "tool_count", "guard_respected"]);
    for (const grader of item.eval_pack.graders) {
      if (!knownGraders.has(grader)) throw new Error(`${where} requires grader ${grader}, which the recorded interpreter does not compute`);
    }

    // Every dataset case — recorded or generated — must name a real analysis
    // unit of this case's own task family, and its generator and perturbation
    // must agree. A generated case whose source unit cannot be resolved would be
    // invented evidence wearing an adversarial label.
    const dataset = item.eval_pack.dataset;
    const generatorPerturbation = new Map([
      ["recorded_case.v1", "none"],
      ["boundary_guard_threshold.v1", "guard_threshold_boundary"],
      ["adversarial_misleading_tool_output.v1", "misleading_tool_output"],
      ["adversarial_stale_input.v1", "stale_input"],
    ]);
    const roleCounts = { target_failure: 0, prior_success: 0, boundary: 0, adversarial: 0 };
    const recordedSources = new Set();
    const datasetCaseIDs = new Set();
    for (const entry of dataset.cases) {
      const at = `${where} dataset case ${entry.id}`;
      if (datasetCaseIDs.has(entry.id)) throw new Error(`${at} appears twice in the dataset`);
      datasetCaseIDs.add(entry.id);
      if (!unitsByID.has(entry.source_unit_id)) throw new Error(`${at} names source unit ${entry.source_unit_id} that is not an analysis unit of this report`);
      if (!familyUnits.has(entry.source_unit_id)) throw new Error(`${at} names source unit ${entry.source_unit_id} that is not a member of task family ${family.id}`);
      if (generatorPerturbation.get(entry.generator) !== entry.perturbation) {
        throw new Error(`${at} generator ${entry.generator} disagrees with perturbation ${entry.perturbation}`);
      }
      if (entry.perturbation === "none" && entry.id !== entry.source_unit_id) {
        throw new Error(`${at} replays a recorded flow but is not identified by its source unit`);
      }
      if (entry.perturbation !== "none" && entry.id !== `${entry.generator}:${entry.source_unit_id}`) {
        throw new Error(`${at} is a generated case whose id does not name its generator and source unit`);
      }
      roleCounts[entry.role] += 1;
      if (entry.role === "target_failure" || entry.role === "prior_success") recordedSources.add(entry.source_unit_id);
    }
    for (const entry of dataset.cases) {
      // Leakage: a generated case must not perturb a unit the manifest already
      // replays unperturbed.
      if (entry.perturbation !== "none" && recordedSources.has(entry.source_unit_id)) {
        throw new Error(`${where} generated case ${entry.id} perturbs a unit the dataset already replays unperturbed`);
      }
    }

    // Manifest composition arithmetic (spec 18.2): target-failure + prior-success
    // + one boundary + up to two adversarial, each capped and each recomputable.
    //
    // The boundary and stale-input generators perturb the localization evidence
    // a confidence guard reads. A ChangeSet whose applicability never reads that
    // evidence has no threshold to sit beside, so those two cases must NOT be
    // generated for it — a "boundary" case against a guard that does not exist
    // tests nothing while counting as adversarial coverage.
    const confidenceGuard = item.change_set.applicability.all.some((condition) =>
      condition === "failure_location_confidence >= 0.90" ||
      condition === "symbol_resolution == unique" ||
      condition === "targeted_test_reproduces == true");
    const perturbations = new Set(dataset.cases.map((entry) => entry.perturbation));
    for (const [perturbation, label] of [["guard_threshold_boundary", "boundary"], ["stale_input", "stale-input"]]) {
      if (perturbations.has(perturbation) !== confidenceGuard) {
        throw new Error(`${where} ${confidenceGuard ? "omits" : "generated"} a ${label} case ${confidenceGuard ? "for" : "against"} a change set that ${confidenceGuard ? "declares" : "declares no"} confidence guard`);
      }
    }
    const expectedTargets = Math.min(4, dataset.target_failure_cases.length);
    const expectedPriors = Math.min(2, dataset.prior_success_cases.length);
    if (roleCounts.target_failure !== expectedTargets) throw new Error(`${where} dataset carries ${roleCounts.target_failure} target-failure cases, want ${expectedTargets}`);
    if (roleCounts.prior_success !== expectedPriors) throw new Error(`${where} dataset carries ${roleCounts.prior_success} prior-success cases, want ${expectedPriors}`);
    if (roleCounts.boundary > 1) throw new Error(`${where} dataset carries ${roleCounts.boundary} boundary cases, want at most one`);
    if (roleCounts.adversarial > (confidenceGuard ? 2 : 1)) throw new Error(`${where} dataset carries ${roleCounts.adversarial} adversarial cases`);
    if (roleCounts.boundary !== dataset.boundary_cases.length) throw new Error(`${where} boundary case list disagrees with the composed dataset`);
    if (roleCounts.adversarial !== dataset.generated_cases.length) throw new Error(`${where} generated case list disagrees with the composed dataset`);
    const composed = roleCounts.target_failure + roleCounts.prior_success + roleCounts.boundary + roleCounts.adversarial;
    if (composed !== dataset.cases.length) throw new Error(`${where} dataset roles (${composed}) do not account for its ${dataset.cases.length} cases`);
    if (dataset.replay_case_ids.length !== datasetCaseIDs.size) throw new Error(`${where} replay manifest carries ${dataset.replay_case_ids.length} ids for ${datasetCaseIDs.size} dataset cases`);
    for (const datasetCaseID of dataset.replay_case_ids) {
      if (!datasetCaseIDs.has(datasetCaseID)) throw new Error(`${where} replay manifest case ${datasetCaseID} is not a composed dataset case`);
    }

    // The guard grader exists exactly when there is a perturbed case to catch a
    // candidate on; a required grader with no case behind it is decoration.
    const perturbed = dataset.cases.some((entry) => entry.perturbation !== "none");
    if (perturbed !== item.eval_pack.graders.includes("guard_respected")) {
      throw new Error(`${where} guard_respected grader ${perturbed ? "missing for" : "declared without"} perturbed dataset cases`);
    }
    for (const proof of item.replay.trial_proofs) {
      if (!datasetCaseIDs.has(proof.dataset_case_id)) throw new Error(`${where} replay trial ${proof.id} replays a case outside the composed dataset`);
      for (const arm of [proof.baseline, proof.candidate]) {
        const graders = arm.grader_results.map((grader) => grader.grader).sort();
        const required = [...item.eval_pack.graders].sort();
        if (graders.join(",") !== required.join(",")) {
          throw new Error(`${where} replay trial ${proof.id} graded ${graders.join(",")}, want exactly ${required.join(",")}`);
        }
      }
    }
  }
}
if (!Array.isArray(spans) || spans.length === 0) throw new Error("canonical span fixture is empty");
for (const [index, span] of spans.entries()) {
  if (!validateSpan(span)) throw new Error(`canonical span ${index}: ${ajv.errorsText(validateSpan.errors)}`);
}
if (relationshipCount === 0) throw new Error("no theme relationship reached the shared-analysis-unit threshold in any fixture");
if (motifCount === 0) throw new Error("no workflow motif recurred across two variants of one task family in any fixture");
console.log(`validated ${reports.length} generated improvement report(s), ${relationshipCount} theme relationship(s), ${motifCount} workflow motif(s) and ${spans.length} canonical spans`);
