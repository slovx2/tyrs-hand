import assert from "node:assert/strict";
import isNumber from "is-number";

assert.equal(process.versions.node, "24.14.0");
assert.equal(isNumber("42"), true);
