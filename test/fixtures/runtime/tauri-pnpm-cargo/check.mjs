import assert from "node:assert/strict";
import { stat } from "node:fs/promises";

assert.equal(process.versions.node, "24.14.0");
await stat("dist/index.html");
