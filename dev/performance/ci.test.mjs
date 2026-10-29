import assert from "node:assert/strict";
import test from "node:test";
import { mkdtempSync, mkdirSync, writeFileSync, rmSync } from "node:fs";
import { join } from "node:path";
import { tmpdir } from "node:os";
import { readPlannerChanged, revisions } from "./ci.mjs";

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

test("read planning profiles use the same explicit immutable source selection", () => {
  for (const tier of ["read-fast", "read", "read-resources"]) {
    const event = { inputs: { candidate: "b".repeat(40), tier } };
    assert.equal(revisions("workflow_dispatch", event, "a".repeat(40)).tier, tier);
    assert.throws(() => revisions("workflow_dispatch", event), /explicit review/);
  }
});

test("Restore PR scope compares checked-out files including additions and deletions", (t) => {
  const root = mkdtempSync(join(tmpdir(), "yatm-read-ci-"));
  t.after(() => rmSync(root, { recursive: true, force: true }));
  const before = join(root, "before"), after = join(root, "after");
  for (const source of [before, after]) {
    mkdirSync(join(source, "internal/media"), { recursive: true });
    writeFileSync(join(source, "internal/media/read_sequence.go"), "same");
  }
  assert.equal(readPlannerChanged(before, after), false);
  writeFileSync(join(after, "README.md"), "unrelated");
  assert.equal(readPlannerChanged(before, after), false);
  writeFileSync(join(after, "internal/media/read_sequence.go"), "changed");
  assert.equal(readPlannerChanged(before, after), true);
  writeFileSync(join(after, "internal/media/read_sequence.go"), "same");
  writeFileSync(join(after, "internal/media/read_fixture_test.go"), "new");
  assert.equal(readPlannerChanged(before, after), true);
  rmSync(join(after, "internal/media/read_fixture_test.go"));
  rmSync(join(after, "internal/media/read_sequence.go"));
  assert.equal(readPlannerChanged(before, after), true);
});
