import assert from "node:assert/strict";
import test from "node:test";
import { revisions } from "./ci.mjs";

test("PR gate uses the exact event base and head, never the synthetic merge or latest branch", () => {
  const event = { pull_request: { base: { sha: "a".repeat(40) }, head: { sha: "b".repeat(40) } } };
  assert.deepEqual(revisions("pull_request", event), { baseline: "a".repeat(40), candidate: "b".repeat(40), tier: "fast" });
});

test("release gate requires an explicitly accepted immutable source baseline", () => {
  const event = { inputs: { candidate: "b".repeat(40), tier: "release" } };
  assert.throws(() => revisions("workflow_dispatch", event), /explicit review/);
  assert.throws(() => revisions("workflow_dispatch", event, "v1"), /immutable/);
  assert.equal(revisions("workflow_dispatch", event, "a".repeat(40)).tier, "release");
  assert.throws(() => revisions("workflow_dispatch", { inputs: { ...event.inputs, tier: "fast" } }, "a".repeat(40)), /requires critical/);
  assert.throws(() => revisions("workflow_dispatch", { inputs: { ...event.inputs, candidate: "$(echo injected)" } }, "a".repeat(40)), /immutable/);
});
